#!/usr/bin/env python3
"""Render, never apply, a consumer Pod from live CR and managed ConfigMap JSON.
The init probe requires a propagated read-only FUSE mount and a matching file.
No credentials are copied to the application Pod.
"""
import argparse
import json
import pathlib
import re

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--cluster', required=True)
parser.add_argument('--configmap', required=True)
parser.add_argument('--image', required=True, help='Application image pinned by sha256 digest')
parser.add_argument('--command-json', required=True, help='JSON array containing command and arguments')
args = parser.parse_args()
cluster = json.loads(pathlib.Path(args.cluster).read_text())
cm = json.loads(pathlib.Path(args.configmap).read_text())
uid = cluster['metadata']['uid']
namespace = cluster['metadata']['namespace']
name = cluster['metadata']['name']
config = json.loads(cm['data']['cache.json'])
if not re.fullmatch(r'[A-Za-z0-9-]{1,64}', uid):
    parser.error('invalid live cluster UID')
if (cm['metadata']['namespace'] != namespace or config['clusterUID'] != uid
        or config['readCache'] != cluster['spec']['readCache']
        or not cm.get('immutable') or config['mountPath'] != '/read-cache'
        or not any(o.get('controller') and o['uid'] == uid
                   and o['kind'] == 'CurvineCluster' and o['name'] == name
                   for o in cm['metadata'].get('ownerReferences', []))):
    parser.error('ConfigMap does not belong to the live read-cache cluster')
if not re.fullmatch(r'[^\s@]+@sha256:[a-f0-9]{64}', args.image):
    parser.error('application image must be pinned by digest')
command = json.loads(args.command_json)
if not isinstance(command, list) or not command or not all(isinstance(x, str) and x for x in command):
    parser.error('command must be a nonempty string array')
mount = {'name': 'cache', 'mountPath': '/var/lib/curvine-export', 'readOnly': True,
         'mountPropagation': 'HostToContainer'}
security = {'allowPrivilegeEscalation': False, 'capabilities': {'drop': ['ALL']}}
pod = {'apiVersion': 'v1', 'kind': 'Pod',
       'metadata': {'generateName': name + '-consumer-', 'namespace': namespace},
       'spec': {'automountServiceAccountToken': False, 'restartPolicy': 'Never',
                'nodeSelector': {'kubernetes.io/os': 'linux', 'storage.curvine.io/fuse': 'enabled'},
                'initContainers': [{'name': 'verify-cache', 'image': cluster['spec']['image'],
                    'command': ['/app/curvine/lib/curvine-cache-node', '--read-probe'],
                    'securityContext': security,
                    'volumeMounts': [mount, {'name': 'config', 'mountPath': '/etc/curvine', 'readOnly': True}]}],
                'containers': [{'name': 'application', 'image': args.image, 'command': command,
                    'securityContext': security, 'volumeMounts': [mount]}],
                'volumes': [{'name': 'cache', 'hostPath': {'path': '/var/lib/curvine-fuse/' + uid, 'type': 'Directory'}},
                            {'name': 'config', 'configMap': {'name': cm['metadata']['name']}}]}}
print(json.dumps(pod, indent=2))
