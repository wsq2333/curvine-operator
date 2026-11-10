#!/usr/bin/env python3
"""Explicit test-cluster acceptance: grow a pool, keep reading, verify new Worker IO.
Requires an existing healthy read-cache CR, a FUSE consumer with Python 3, and a non-sensitive immutable object manifest. Never shrinks,
deletes data, creates source objects, or edits the user's default kubeconfig.
"""
import argparse
import datetime
import json
import pathlib
import re
import subprocess
import threading
import time


def validate_manifest(items):
    if not isinstance(items, list) or not 1 <= len(items) <= 256:
        raise ValueError('manifest must contain 1..256 objects')
    total = 0
    keys = set()
    for item in items:
        key = item['path']
        if not isinstance(key, str) or not re.fullmatch(r'[A-Za-z0-9_./-]{1,512}', key) or key == '.' or key in keys or key.startswith('/') or str(pathlib.PurePosixPath(key)) != key or '..' in pathlib.PurePosixPath(key).parts:
            raise ValueError('invalid relative object path')
        keys.add(key)
        size = item['sizeBytes']
        if type(size) is not int or not 0 < size <= 16 * 1024 * 1024:
            raise ValueError('invalid object size')
        digest = item['sha256']
        if not isinstance(digest, str) or len(digest) != 64 or any(c not in '0123456789abcdef' for c in digest):
            raise ValueError('invalid SHA256')
        total += size
    if total > 64 * 1024 * 1024:
        raise ValueError('manifest exceeds 64 MiB per round')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for flag in ('kubeconfig', 'context', 'namespace', 'cluster', 'pool', 'consumer', 'manifest', 'output'):
        parser.add_argument('--' + flag, required=True)
    parser.add_argument('--replicas', type=int, required=True)
    parser.add_argument('--restart-operator', help='Explicitly restart this operator Deployment once after expanding')
    parser.add_argument('--timeout', type=int, default=600)
    args = parser.parse_args()
    if args.timeout <= 0:
        parser.error("timeout must be positive")
    items = json.loads(pathlib.Path(args.manifest).read_text())
    validate_manifest(items)
    base = ['kubectl', '--kubeconfig', args.kubeconfig, '--context', args.context, '-n', args.namespace, '--request-timeout=30s']
    def kubectl(*command, stdin=None):
        result = subprocess.run(base + list(command), input=stdin, text=True, capture_output=True, timeout=60)
        if result.returncode:
            # Do not echo stderr, Pod logs, kubeconfigs or source configuration.
            raise RuntimeError('Kubernetes operation failed: ' + command[0])
        return result.stdout
    def cluster():
        return json.loads(kubectl('get', 'curvinecluster', args.cluster, '-o', 'json'))
    def worker_metrics(ordinal):
        name = f'{args.cluster}-worker-{args.pool}-{ordinal}'
        text = kubectl('get', '--raw', f'/api/v1/namespaces/{args.namespace}/pods/{name}:9000/proxy/metrics')
        result = {}
        for line in text.splitlines():
            parts = line.split()
            if len(parts) == 2 and parts[0] in ('write_bytes', 'read_bytes', 'write_bytes_total', 'read_bytes_total'):
                result[parts[0].removesuffix('_total')] = float(parts[1])
        if set(result) != {'read_bytes', 'write_bytes'}:
            raise RuntimeError('Worker IO metrics unavailable')
        return result
    original = cluster()
    if not original['spec'].get('readCache'):
        raise RuntimeError('requires immutable read-cache purpose')
    pools = original['spec']['workerPools']
    index = next(i for i, p in enumerate(pools) if p['name'] == args.pool)
    before = pools[index]['replicas']
    if not before < args.replicas <= 10000:
        raise RuntimeError('acceptance permits scale-out only')
    uid = original['metadata']['uid']
    # A successful starting read and healthy generation are required before mutation.
    conditions = {c['type']: c for c in original['status'].get('conditions', [])}
    if any(conditions.get(k, {}).get('status') != 'True' or conditions[k].get('observedGeneration') != original['metadata']['generation'] for k in ('ResourcesReady', 'WorkersHealthy', 'ReadPathVerified')):
        raise RuntimeError('cluster is not currently healthy')
    read_code = """import hashlib,json,os,pathlib,sys
root='/var/lib/curvine-export/data'
mounted=False
for line in pathlib.Path('/proc/self/mountinfo').read_text().splitlines():
 a,sep,b=line.partition(' - ')
 left,right=a.split(),b.split()
 if sep and len(left)>=6 and right and left[4]==root and right[0].startswith('fuse') and 'ro' in left[5].split(','): mounted=True
if not mounted: raise RuntimeError('read-only FUSE mount unavailable')
for item in json.load(sys.stdin):
 p=pathlib.Path('/var/lib/curvine-export/data')
 for part in pathlib.PurePosixPath(item['path']).parts:
  p=p/part
  if p.is_symlink(): raise RuntimeError('symlink rejected')
 with p.open('rb') as f: data=f.read(item['sizeBytes']+1)
 if len(data)!=item['sizeBytes'] or hashlib.sha256(data).hexdigest()!=item['sha256']: raise RuntimeError('content mismatch')
"""
    probe = original['spec']['readCache']['probe']
    warm = [{'path': probe['object'], 'sizeBytes': probe['sizeBytes'], 'sha256': probe['sha256']}]
    validate_manifest(warm)
    if any(item['path'] == probe['object'] for item in items):
        raise RuntimeError('manifest must exclude the warm probe')
    def read_objects(objects):
        kubectl('exec', '-i', args.consumer, '--', 'python3', '-c', read_code, stdin=json.dumps(objects))
    read_objects(warm)
    report = {'clusterUID': uid, 'pool': args.pool, 'from': before, 'to': args.replicas,
              'passed': False, 'readRounds': 0, 'readFailures': 0, 'phases': [], 'newWorkers': {}}
    stop = threading.Event()
    started = threading.Event()
    def read_probe_loop():
        # Probe is already warm; do not warm acceptance objects before new Workers exist.
        while not stop.is_set():
            try:
                read_objects(warm)
                report['readRounds'] += 1
            except Exception:
                report['readFailures'] += 1
            started.set()
            stop.wait(1)
    thread = threading.Thread(target=read_probe_loop)
    try:
        thread.start()
        if not started.wait(65) or report['readFailures']:
            raise RuntimeError('baseline reader failed')
        latest = cluster()
        if latest['metadata']['uid']!=uid or latest['metadata']['generation']!=original['metadata']['generation']:
            raise RuntimeError('cluster intent changed before expansion')
        patch = [{'op': 'test', 'path': '/metadata/uid', 'value': uid},
                 {'op': 'test', 'path': '/metadata/resourceVersion', 'value': latest['metadata']['resourceVersion']},
                 {'op': 'replace', 'path': f'/spec/workerPools/{index}/replicas', 'value': args.replicas}]
        updated = json.loads(kubectl('patch', 'curvinecluster', args.cluster, '--type=json', '-p', json.dumps(patch), '-o', 'json'))
        generation = updated['metadata']['generation']
        report['generation'] = generation
        if args.restart_operator:
            kubectl('rollout', 'restart', 'deployment/' + args.restart_operator)
        deadline = time.monotonic() + args.timeout
        while time.monotonic() < deadline:
            current = cluster()
            if current['metadata']['uid'] != uid or current['metadata']['generation'] != generation:
                raise RuntimeError('cluster intent changed concurrently')
            p = next((p for p in current.get('status', {}).get('workerPools', []) if p['name'] == args.pool), {})
            if p.get('observedGeneration') == generation:
                observed = {'phase': p['phase'], 'reason': p['reason']}
                if not report['phases'] or report['phases'][-1] != observed:
                    report['phases'].append(observed)
                if p['phase'] == 'Registered':
                    break
            time.sleep(2)
        else:
            raise RuntimeError('allocation and registration timeout')
        baseline = {i: worker_metrics(i) for i in range(before, args.replicas)}
        # Files must be previously cold in this cluster, so growth measures new placement.
        deadline = time.monotonic() + args.timeout
        while time.monotonic() < deadline:
            current=cluster()
            if current['metadata']['uid']!=uid or current['metadata']['generation']!=generation:
                raise RuntimeError('cluster intent changed during IO verification')
            read_objects(items)
            values = {i: worker_metrics(i) for i in baseline}
            report['newWorkers'] = {str(i): {k: values[i][k] - baseline[i][k] for k in ('read_bytes', 'write_bytes')} for i in baseline}
            if all(v['read_bytes'] > 0 and v['write_bytes'] > 0 for v in report['newWorkers'].values()):
                break
            time.sleep(2)
        else:
            raise RuntimeError('new Worker read/write evidence incomplete; use more cold objects')
        if args.restart_operator:
            kubectl('rollout', 'status', 'deployment/' + args.restart_operator, '--timeout=45s')
            report['operatorRolloutComplete'] = True
        if report['readFailures'] or report['readRounds'] < 2:
            raise RuntimeError('continuous read verification failed')
        report['passed'] = True
    except Exception as exc:
        report['failure'] = str(exc)
    finally:
        stop.set()
        if thread.ident is not None:
            thread.join(timeout=65)
        if thread.is_alive() or report['readFailures']:
            report['passed'] = False
            report['failure'] = 'continuous reader did not finish successfully'
        report['finishedAt'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        output = pathlib.Path(args.output)
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report, indent=2))
    return 0 if report['passed'] else 1

if __name__ == '__main__':
    raise SystemExit(main())
