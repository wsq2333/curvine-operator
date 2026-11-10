#!/usr/bin/env python3
"""Explicit acceptance for CurvineWorkerExpansion. Never runs from reconcile.
Requires an already-reviewed preflight CR, a read-only FUSE consumer with Python,
a warm-probe manifest (one object), and previously cold immutable objects.
"""
import argparse
import datetime
import json
import pathlib
import subprocess
import threading
import time
from kubernetes_scale_out import validate_manifest

READ = '''import hashlib,json,pathlib,sys
request=json.load(sys.stdin)
root=pathlib.Path(request['root'])
mounted=False
for line in pathlib.Path('/proc/self/mountinfo').read_text().splitlines():
 a,sep,b=line.partition(' - ')
 left,right=a.split(),b.split()
 if sep and len(left)>=6 and right and left[4]==str(root) and right[0].startswith('fuse') and 'ro' in left[5].split(','): mounted=True
if not mounted: raise RuntimeError('read-only FUSE mount unavailable')
for item in request['items']:
 p=root
 for part in pathlib.PurePosixPath(item['path']).parts:
  p=p/part
  if p.is_symlink(): raise RuntimeError('symlink rejected')
 with p.open('rb') as f: data=f.read(item['sizeBytes']+1)
 if len(data)!=item['sizeBytes'] or hashlib.sha256(data).hexdigest()!=item['sha256']: raise RuntimeError('content mismatch')
'''


def current_phase(resource):
    status = resource.get('status', {})
    if status.get('observedGeneration') != resource['metadata']['generation']:
        return None
    try:
        sample = datetime.datetime.fromisoformat(status['sampleTime'].replace('Z', '+00:00'))
        age = (datetime.datetime.now(datetime.timezone.utc) - sample).total_seconds()
    except (KeyError, ValueError, TypeError):
        return None
    return status.get('phase') if -5 <= age <= 90 else None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('kubeconfig', 'context', 'namespace', 'expansion', 'consumer', 'mount-path', 'probe', 'manifest', 'output'):
        parser.add_argument('--' + name, required=True)
    parser.add_argument('--restart-operator')
    parser.add_argument('--timeout', type=int, default=600)
    args = parser.parse_args()
    if args.timeout <= 0 or not args.mount_path.startswith('/') or str(pathlib.PurePosixPath(args.mount_path)) != args.mount_path or '..' in pathlib.PurePosixPath(args.mount_path).parts:
        parser.error('invalid timeout or mount path')
    warm = json.loads(pathlib.Path(args.probe).read_text())
    cold = json.loads(pathlib.Path(args.manifest).read_text())
    validate_manifest(warm)
    validate_manifest(cold)
    if len(warm) != 1 or any(x['path'] == warm[0]['path'] for x in cold):
        parser.error('use one warm probe and separate cold objects')
    base = ['kubectl', '--kubeconfig', args.kubeconfig, '--context', args.context, '-n', args.namespace, '--request-timeout=30s']

    def kube(*command, stdin=None):
        result = subprocess.run(base + list(command), input=stdin, text=True, capture_output=True, timeout=60)
        if result.returncode:
            raise RuntimeError('Kubernetes operation failed: ' + command[0])
        return result.stdout

    def get():
        return json.loads(kube('get', 'curvineworkerexpansion', args.expansion, '-o', 'json'))

    def read(items):
        kube('exec', '-i', args.consumer, '--', 'python3', '-c', READ,
             stdin=json.dumps({'root': args.mount_path, 'items': items}))

    original = get()
    if original['spec']['replicas'] != 1 or current_phase(original) != 'PreflightReady':
        raise RuntimeError('requires current preflight-only expansion CR')
    uid = original['metadata']['uid']
    report = {'expansionUID': uid, 'from': 1, 'to': 2, 'passed': False, 'readRounds': 0, 'readFailures': 0}
    stop = threading.Event()
    started = threading.Event()

    def continuous():
        while not stop.is_set():
            try:
                read(warm)
                report['readRounds'] += 1
            except Exception:
                report['readFailures'] += 1
            started.set()
            stop.wait(1)

    thread = threading.Thread(target=continuous)
    try:
        read(warm)
        thread.start()
        if not started.wait(65) or report['readFailures']:
            raise RuntimeError('baseline reader failed')
        latest = get()
        if latest['metadata']['uid'] != uid or latest['metadata']['generation'] != original['metadata']['generation']:
            raise RuntimeError('intent changed before expansion')
        patch = [{'op': 'test', 'path': '/metadata/uid', 'value': uid},
                 {'op': 'test', 'path': '/metadata/resourceVersion', 'value': latest['metadata']['resourceVersion']},
                 {'op': 'replace', 'path': '/spec/replicas', 'value': 2}]
        current = json.loads(kube('patch', 'curvineworkerexpansion', args.expansion, '--type=json', '-p', json.dumps(patch), '-o', 'json'))
        generation = current['metadata']['generation']
        report['generation'] = generation
        if args.restart_operator:
            kube('rollout', 'restart', 'deployment/' + args.restart_operator)

        def check_intent():
            current = get()
            if current['metadata']['uid'] != uid or current['metadata']['generation'] != generation:
                raise RuntimeError('intent changed concurrently')
            return current

        deadline = time.monotonic() + args.timeout
        while time.monotonic() < deadline:
            current = check_intent()
            if current_phase(current) == 'Registered':
                break
            time.sleep(2)
        else:
            raise RuntimeError('worker allocation/registration timeout')
        name = current['status']['workerName']

        def metrics():
            text = kube('get', '--raw', f'/api/v1/namespaces/{args.namespace}/pods/{name}:19002/proxy/metrics')
            counters = {}
            for line in text.splitlines():
                parts = line.split()
                if len(parts) == 2 and parts[0] in ('read_bytes', 'write_bytes', 'read_bytes_total', 'write_bytes_total'):
                    counters[parts[0].removesuffix('_total')] = float(parts[1])
            if set(counters) != {'read_bytes', 'write_bytes'}:
                raise RuntimeError('new Worker metrics unavailable')
            return counters

        before = metrics()
        deadline = time.monotonic() + args.timeout
        while time.monotonic() < deadline:
            check_intent()
            read(cold)
            after = metrics()
            report['newWorkerBytes'] = {key: after[key] - before[key] for key in before}
            if all(value > 0 for value in report['newWorkerBytes'].values()):
                break
            time.sleep(2)
        else:
            raise RuntimeError('new Worker IO evidence incomplete')
        if args.restart_operator:
            kube('rollout', 'status', 'deployment/' + args.restart_operator, '--timeout=45s')
            report['operatorRolloutComplete'] = True
        if report['readRounds'] < 2 or report['readFailures']:
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
        report['finishedAt'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        path = pathlib.Path(args.output)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report, indent=2))
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
