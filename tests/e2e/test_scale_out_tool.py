import importlib.util
import pathlib
import unittest

spec=importlib.util.spec_from_file_location('scale_out',pathlib.Path(__file__).with_name('kubernetes_scale_out.py'))
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class ManifestTest(unittest.TestCase):
    def test_bounds_and_paths(self):
        good={'path':'version/object','sha256':'a'*64,'sizeBytes':1}
        module.validate_manifest([good])
        for replacement in ({'path':'.'},{'path':'a\x00b'},{'path':'../object'},{'path':'/object'},{'path':'a//b'},{'sizeBytes':True},{'sizeBytes':17000000},{'sha256':'bad'}):
            with self.subTest(replacement=replacement), self.assertRaises(ValueError):
                module.validate_manifest([{**good,**replacement}])
        with self.assertRaises(ValueError):module.validate_manifest([good, good])
        with self.assertRaises(ValueError):module.validate_manifest([])
        with self.assertRaises(ValueError):module.validate_manifest([good]*257)

if __name__=='__main__':unittest.main()
