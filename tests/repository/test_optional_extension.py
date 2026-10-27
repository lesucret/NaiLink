import importlib.util
import io
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("reuse_extension", ROOT / "scripts/reuse-protocol-extension.py")
reuse = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(reuse)


class ReviewedExtensionTest(unittest.TestCase):
    def test_corrupt_download_cannot_replace_existing_component(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            old = output / (reuse.PREFIX + "arm64.tar.gz")
            old.write_bytes(b"old component")
            with self.assertRaises(ValueError):
                reuse.fetch("arm64", output)
            self.assertEqual(old.read_bytes(), b"old component")

    def test_hash_checked_before_download_is_committed(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(reuse.urllib.request, "urlopen", return_value=io.BytesIO(b"wrong")):
                with self.assertRaises(ValueError):
                    reuse.fetch("arm64", Path(directory))
            self.assertEqual(list(Path(directory).iterdir()), [])

    def test_source_changes_block_stale_binary_reuse(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "openwrt").mkdir()
            archive_path = root / "source.tar.gz"
            with tarfile.open(archive_path, "w:gz") as archive:
                for member, source in reuse.SOURCES.items():
                    data = source.encode()
                    (root / "openwrt" / source).write_bytes(data)
                    item = tarfile.TarInfo(member)
                    item.size = len(data)
                    archive.addfile(item, io.BytesIO(data))
            reuse.check_source(archive_path, root)
            (root / "openwrt/extra.py").write_text("changed")
            with self.assertRaises(ValueError):
                reuse.check_source(archive_path, root)
