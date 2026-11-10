import importlib.util
import pathlib
import tempfile
import unittest

source = pathlib.Path(__file__).parents[2] / 'internal/existing/diskguard.py'
spec = importlib.util.spec_from_file_location('diskguard', source)
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)


class DiskIdentityTest(unittest.TestCase):
    def test_restart_and_foreign_owner(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory).resolve()
            identity = {'expansionUID': 'one', 'clusterID': 'cache'}
            guard.claim([str(root)], identity)
            (root / 'worker-data').write_text('retained')
            guard.claim([str(root)], identity)
            with self.assertRaises(ValueError):
                guard.claim([str(root)], {'expansionUID': 'replacement', 'clusterID': 'cache'})
            self.assertEqual((root / 'worker-data').read_text(), 'retained')

    def test_validate_all_disks_before_claiming(self):
        with tempfile.TemporaryDirectory() as directory:
            first = pathlib.Path(directory).resolve() / 'first'
            second = first.parent / 'second'
            first.mkdir()
            second.mkdir()
            (second / 'foreign-data').write_text('keep')
            with self.assertRaises(ValueError):
                guard.claim([str(first), str(second)], {'expansionUID': 'one'})
            self.assertFalse((first / guard.MARKER).exists())
            self.assertTrue((second / 'foreign-data').exists())

    def test_reject_symlink_marker(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory).resolve()
            (root / guard.MARKER).symlink_to(root / 'missing')
            with self.assertRaises(ValueError):
                guard.claim([str(root)], {'expansionUID': 'one'})
