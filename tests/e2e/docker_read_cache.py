#!/usr/bin/env python3
"""Disposable real Curvine/FUSE test against a read-only S3 protocol fixture.
Requires Linux Docker /dev/fuse, the native target volume and a Linux Go helper.
No kubeconfig, production credentials or existing volumes are used.
"""
import argparse,json,os,pathlib,subprocess,tempfile,time,urllib.request,uuid
p=argparse.ArgumentParser();p.add_argument('--fixtures',required=True);p.add_argument('--toolchain-image',default='curvine-readcache-toolchain:local');p.add_argument('--native-volume',default='curvine-readcache-target');p.add_argument('--node-binary',required=True);p.add_argument('--output',required=True);a=p.parse_args()
root=pathlib.Path(a.fixtures).resolve();tests=pathlib.Path(__file__).resolve().parent
pathlib.Path(a.output).parent.mkdir(parents=True,exist_ok=True)
prefix='cv-cache-'+uuid.uuid4().hex[:8];network=prefix;containers=[];results={}
def run(args,check=True):
    r=subprocess.run(args,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,timeout=90)
    if check and r.returncode:raise RuntimeError('command failed: '+args[0]+' '+(' '.join(args[1:3]))+'; '+r.stderr[-1200:])
    return r

def docker(*args,check=True):return run(['docker',*args],check)
def launch(role,command,aliases=(),extra=()):
    name=prefix+'-'+role;args=['run','-d','--name',name,'--network',network]
    for alias in aliases:args+=['--network-alias',alias]
    args+=['--mount',f'type=volume,src={a.native_volume},dst=/build,readonly','--mount',f'type=bind,src={root},dst=/etc/curvine,readonly','--mount',f'type=bind,src={root}/credentials,dst=/etc/curvine-origin,readonly']
    if role in ('master','worker'):
        command=['sh','-c','mkdir -p /var/log/curvine /var/lib/curvine; exec "$@"','sh',*command]
    args+=list(extra)+[a.toolchain_image]+command
    docker(*args);containers.append(name);return name

def get_origin(path,post=False):
    return docker('exec',origin,'python3','-c',"import urllib.request; print(urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:8080/"+path+"',method='"+('POST' if post else 'GET')+"')).read().decode())").stdout

def wait(fn,seconds=90):
    end=time.monotonic()+seconds
    while time.monotonic()<end:
        try:
            v=fn()
            if v:return v
        except (RuntimeError,ValueError,json.JSONDecodeError):pass
        time.sleep(2)
    raise RuntimeError('condition timeout')

def node_report():
    return json.loads(docker('exec',fuse,'python3','-c',"import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:9101/report').read().decode())").stdout)
def read():return docker('exec',fuse,'/app/curvine/lib/curvine-cache-node','--read-probe',check=False).returncode==0
def metrics():
    text=docker('exec',fuse,'python3','-c',"import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:9000/metrics').read().decode())").stdout
    return {name:sum(float(l.split()[-1]) for l in text.splitlines() if l.startswith(name+'{') or l.startswith(name+'_total{') or l.startswith(name+' ') or l.startswith(name+'_total ')) for name in ['client_mount_cache_hits','client_mount_cache_misses','client_read_bytes']}
try:
    (root/'credentials').mkdir(exist_ok=True)
    for key,value in [('accessKey','test-access'),('secretKey','test-secret')]:
        f=root/'credentials'/key;f.write_text(value);f.chmod(0o644) # isolated fixture only, never production keys
    for file in root.glob('*'):
        if file.is_file():file.chmod(0o644)
    docker('network','create',network)
    origin=launch('origin',['python3','/tests/s3_fixture.py'],['origin'],['--mount',f'type=bind,src={tests},dst=/tests,readonly'])
    host='read-cache-master-0.read-cache-master.curvine-system.svc.cluster.local'
    master=launch('master',['/build/debug/curvine-server','--service','master','--conf','/etc/curvine/curvine-cluster.toml'],[host],['-e','CURVINE_MASTER_HOSTNAME='+host])
    workerhost=(root/'worker-host').read_text()
    worker=launch('worker',['/build/debug/curvine-server','--service','worker','--conf','/etc/curvine/curvine-cluster.toml'],[workerhost],['-e','CURVINE_WORKER_HOSTNAME='+workerhost,'--tmpfs','/var/lib/curvine:rw,size=512m'])
    wait(lambda:docker('exec',master,'python3','-c',"import urllib.request,json; d=json.load(urllib.request.urlopen('http://read-cache-master-0.read-cache-master.curvine-system.svc.cluster.local:9000/api/overview')); assert d['master_state']=='Active' and d['live_workers']==1").returncode==0)
    docker('exec',master,'/build/debug/curvine-cache-mount','configure')
    docker('exec',master,'/build/debug/curvine-cache-mount','configure')
    results['mount_idempotent']=True
    (root/'credentials'/'accessKey').write_text('invalid-fixture-access')
    results['bad_credentials_rejected']=docker('exec',master,'/build/debug/curvine-cache-mount','origin',check=False).returncode!=0
    results['credential_drift_blocked']=docker('exec',master,'/build/debug/curvine-cache-mount','check',check=False).returncode!=0
    (root/'credentials'/'accessKey').write_text('test-access')
    docker('exec',master,'/build/debug/curvine-cache-mount','check')
    cold_origin_gets=json.loads(get_origin('__stats'))['gets']
    setup='mkdir -p /app/curvine/lib /var/lib/curvine-export /var/lib/curvine-state /var/log/curvine; test -c /dev/fuse || mknod /dev/fuse c 10 229; ln -s /build/debug/curvine-fuse /app/curvine/lib/curvine-fuse; ln -s /build/debug/curvine-cache-mount /app/curvine/lib/curvine-cache-mount; ln -s /node-binary /app/curvine/lib/curvine-cache-node; exec /app/curvine/lib/curvine-cache-node'
    fuse=launch('fuse',['sh','-c',setup],extra=['--privileged','--mount',f'type=bind,src={pathlib.Path(a.node_binary).resolve()},dst=/node-binary,readonly'])
    wait(lambda:node_report().get('readVerified'))
    results['cold_read_sha256']=True
    results['cold_miss_counter']=metrics()['client_mount_cache_misses']>0
    results['cold_origin_get']=json.loads(get_origin('__stats'))['gets']>cold_origin_gets
    def warm():
        before=metrics();ok=read();after=metrics()
        return ok and after['client_mount_cache_hits']>before['client_mount_cache_hits'] and after['client_read_bytes']-before['client_read_bytes']>=25
    wait(warm);results['warm_hit_counter']=True
    # Kernel-enforced ro: all mutations target a disposable fixture object.
    code="import os,errno; p='/var/lib/curvine-export/data/probe.txt'; checks=[lambda:open(p+'.new','wb'),lambda:open(p,'wb'),lambda:os.unlink(p),lambda:os.rename(p,p+'.moved'),lambda:os.mkdir('/var/lib/curvine-export/data/write-test')];\nfor f in checks:\n try:f();raise AssertionError('write allowed')\n except OSError as e:assert e.errno in (errno.EROFS,errno.EACCES,errno.EPERM)"
    docker('exec',fuse,'python3','-c',code);results['create_overwrite_delete_rename_rejected']=True
    get_origin('__control/down',True)
    results['warm_read_origin_down']=read()
    get_origin('__control/up',True)
    docker('stop','-t','5',worker)
    stopped=time.time()
    before=json.loads(get_origin('__stats'))['gets']
    results['worker_down_read_sha256']=wait(read)
    after=json.loads(get_origin('__stats'))['gets']
    if after<=before:raise RuntimeError('fallback had no UFS GET evidence')
    results['worker_down_ufs_get']=True
    def degraded():
        from datetime import datetime
        report=node_report()
        sample=datetime.fromisoformat(report['sampleTime'].replace('Z','+00:00')).timestamp()
        return sample>=stopped and report['readVerified'] and not report['cacheHitObserved']
    wait(degraded);results['worker_down_not_cache_ready']=True
    get_origin('__control/change',True)
    results['changed_origin_rejected']=docker('exec',master,'/build/debug/curvine-cache-mount','origin',check=False).returncode!=0
    get_origin('__control/restore',True)
    stats=json.loads(get_origin('__stats'));results['no_origin_writes']=stats['writes']==0
    if not all(results.values()):raise RuntimeError('scenario failed')
    results['passed']=True
except Exception as exc:
    results['passed']=False;results['failure']=str(exc)
    # Logs can contain source details; keep them in the private test output dir.
    out=pathlib.Path(a.output);out.parent.mkdir(parents=True,exist_ok=True)
    for name in containers:
        log=docker('logs','--tail','100',name,check=False)
        f=out.parent/(name+'.log');f.write_text(log.stdout+log.stderr);f.chmod(0o600)
        native=docker('exec',name,'sh','-c','tail -n 100 /var/log/curvine/*.log',check=False)
        f=out.parent/(name+'-native.log');f.write_text(native.stdout+native.stderr);f.chmod(0o600)
finally:
    pathlib.Path(a.output).write_text(json.dumps(results,indent=2)+'\n')
    for name in reversed(containers):docker('rm','-f',name,check=False)
    docker('network','rm',network,check=False)
print(json.dumps(results,indent=2))
raise SystemExit(0 if results.get('passed') else 1)
