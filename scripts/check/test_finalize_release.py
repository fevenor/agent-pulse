"""发布前附件损坏、缺失与镜像提交不匹配必须阻止公开；不联网。"""
import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest
import zipfile

spec = importlib.util.spec_from_file_location("finalize", Path(__file__).resolve().parents[1] / "release/finalize_release.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class FinalizeTests(unittest.TestCase):
    def test_complete_assets_and_tamper_detection(self):
        with tempfile.TemporaryDirectory() as value:
            root = Path(value)
            for arch in ("amd64", "arm64"):
                (root / ("agent-pulse-bridge-linux-" + arch)).write_bytes(arch.encode())
            with zipfile.ZipFile(root / "windows-amd64.zip", "w") as archive:
                for name in ("agent-pulse-tray.exe", "bridge.example.yaml", "tray.example.yaml", "使用说明.md"):
                    archive.writestr(name, "fixture")
            sums = [hashlib.sha256(p.read_bytes()).hexdigest() + "  " + p.name for p in root.iterdir()]
            (root / "SHA256SUMS.txt").write_text("\n".join(sums) + "\n")
            self.assertEqual(len(module.verify_assets(root)), 3)
            (root / "agent-pulse-bridge-linux-amd64").write_bytes(b"changed")
            with self.assertRaisesRegex(ValueError, "摘要"):
                module.verify_assets(root)
            (root / "private.key").write_text("must not publish")
            with self.assertRaisesRegex(ValueError, "维护材料"):
                module.verify_assets(root)

    def test_image_revision_must_match_workflow(self):
        value = [{"Config": {"Labels": {"org.opencontainers.image.revision": "a" * 40}}}]
        module.verify_image_metadata(value, "a" * 40)
        with self.assertRaisesRegex(ValueError, "提交"):
            module.verify_image_metadata(value, "b" * 40)


if __name__ == "__main__":
    unittest.main()
