import importlib.util
import io
from pathlib import Path
import tarfile
import unittest

spec = importlib.util.spec_from_file_location('core_build', Path(__file__).resolve().parents[2] / 'openwrt/singbox.py')
core = importlib.util.module_from_spec(spec)
spec.loader.exec_module(core)


class SingBoxBuildTests(unittest.TestCase):
    def test_archive_is_reproducible_and_keeps_binary_mode(self):
        entries = [('LICENSE', b'license', 0o644), ('./usr/bin/sing-box', b'binary', 0o755)]
        first = core.archive_bytes(entries)
        self.assertEqual(first, core.archive_bytes(list(reversed(entries))))
        with tarfile.open(fileobj=io.BytesIO(first)) as tf:
            self.assertEqual(tf.getmember('./usr/bin/sing-box').mode, 0o755)
            self.assertTrue(all(m.uid == 0 and m.mtime == 0 for m in tf))

    def test_patch_refuses_unknown_implementation(self):
        with self.assertRaises(ValueError):
            core.patch_reality('package tls\n')


if __name__ == '__main__':
    unittest.main()
