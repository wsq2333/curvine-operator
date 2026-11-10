import datetime
import importlib.util
import json
import pathlib
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

source = pathlib.Path(__file__).with_name('existing_scale_out.py')
spec = importlib.util.spec_from_file_location('existing_scale_out', source)
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class ExistingAcceptanceTest(unittest.TestCase):
    def test_scoped_scale_out_and_no_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            item = {'path': 'probe', 'sha256': 'a' * 64, 'sizeBytes': 25}
            (root / 'probe.json').write_text(json.dumps([item]))
            (root / 'cold.json').write_text(json.dumps([{**item, 'path': 'cold'}]))
            current = {'metadata': {'uid': 'cr', 'generation': 1, 'resourceVersion': '10'},
                       'spec': {'replicas': 1}, 'status': {'phase': 'PreflightReady', 'observedGeneration': 1, 'sampleTime': datetime.datetime.now(datetime.timezone.utc).isoformat()}}
            commands = []
            counters = 0

            def kube(command, **kwargs):
                nonlocal counters
                commands.append(command)
                self.assertEqual(command[1:7], ['--kubeconfig', 'explicit-file', '--context', 'explicit-context', '-n', 'cache'])
                op = command[8]
                if op == 'patch':
                    patch_value = json.loads(command[command.index('-p') + 1])
                    self.assertEqual(patch_value[-1]['value'], 2)
                    self.assertEqual(patch_value[0]['op'], 'test')
                    current['metadata']['generation'] = 2
                    current['status'] = {'phase': 'Registered', 'observedGeneration': 2, 'workerName': 'expand-worker-0', 'sampleTime': datetime.datetime.now(datetime.timezone.utc).isoformat()}
                if '--raw' in command:
                    self.assertIn(':19002/proxy/metrics', command[-1])
                    counters += 25
                    output = f'read_bytes {counters}\nwrite_bytes {counters}\n'
                elif op == 'exec':
                    request = json.loads(kwargs['input'])
                    if request['items'][0]['path'] == 'cold':
                        time.sleep(1.1)  # Let the independent continuous reader run again.
                    output = ''
                else:
                    output = json.dumps(current)
                return subprocess.CompletedProcess(command, 0, stdout=output, stderr='')

            argv = ['acceptance', '--kubeconfig', 'explicit-file', '--context', 'explicit-context', '--namespace', 'cache',
                    '--expansion', 'expand', '--consumer', 'consumer', '--mount-path', '/data',
                    '--probe', str(root / 'probe.json'), '--manifest', str(root / 'cold.json'), '--output', str(root / 'out.json'),
                    '--restart-operator', 'operator', '--timeout', '5']
            with patch.object(sys, 'argv', argv), patch.object(runner.subprocess, 'run', side_effect=kube), patch('builtins.print'):
                self.assertEqual(runner.main(), 0)
            result = json.loads((root / 'out.json').read_text())
            self.assertTrue(result['passed'])
            self.assertTrue(result['operatorRolloutComplete'])
            self.assertEqual(result['readFailures'], 0)
            self.assertFalse(any('delete' in command or 'scale' in command for command in commands))

    def test_stale_registration_is_not_ready(self):
        resource = {'metadata': {'generation': 2}, 'status': {'observedGeneration': 2, 'phase': 'Registered', 'sampleTime': '2020-01-01T00:00:00Z'}}
        self.assertIsNone(runner.current_phase(resource))
