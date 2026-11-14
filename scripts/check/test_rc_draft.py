"""验证 RC 草稿边界，不联网、不提交或发布。"""
import importlib.util
import hashlib
from types import SimpleNamespace
from unittest.mock import patch
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("rc_draft", Path(__file__).resolve().parents[1] / "release" / "create_rc_draft.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class DraftTests(unittest.TestCase):
    def test_only_matching_rc_tag(self):
        module.validate_tag("v0.5.0-rc.1", "0.5.0")
        for tag in ("v0.5.0", "v0.5.0-rc.0", "v0.6.0-rc.1", "main"):
            with self.assertRaises(ValueError):
                module.validate_tag(tag, "0.5.0")

    def test_published_release_is_never_overwritten(self):
        release = {"isDraft": True, "isPrerelease": True, "tagName": "v0.5.0-rc.1", "targetCommitish": "a" * 40}
        module.require_draft(release, "v0.5.0-rc.1", "a" * 40)
        for key in ("isDraft", "isPrerelease"):
            with self.assertRaises(ValueError):
                module.require_draft({**release, key: False}, "v0.5.0-rc.1", "a" * 40)
        with self.assertRaises(ValueError):
            module.require_draft(release, "v0.5.0-rc.1", "b" * 40)

    def test_windows_only_zcode(self):
        packages = [{"id": i, "platform": p} for i, p in module.EXPECTED_SCOPE]
        module.validate_scope(packages)
        with self.assertRaises(ValueError):
            module.validate_scope(packages + [{"id": "codex", "platform": "windows/amd64"}])

    def test_bridge_assets_are_complete_and_bound_to_hash_and_architecture(self):
        with tempfile.TemporaryDirectory() as value:
            root = Path(value)
            lines = []
            for arch, machine in (("amd64", 62), ("arm64", 183)):
                raw = bytearray(20)
                raw[:6] = b"\x7fELF\x02\x01"
                raw[18] = machine
                name = "agent-pulse-bridge-linux-" + arch
                (root / name).write_bytes(raw)
                lines.append(hashlib.sha256(raw).hexdigest() + "  " + name)
            (root / "SHA256SUMS.txt").write_text("\n".join(lines) + "\n")
            good = "fixture: go1.27.4\npath\tagent-pulse/cmd/agent-pulse-bridge\ndep\tgolang.org/x/sys\tv0.44.0\nbuild\tCGO_ENABLED=0\n"
            with patch.object(module, "command", return_value=SimpleNamespace(stdout=good)):
                assets = module.bridge_artifacts(root)
                self.assertEqual({item["platform"] for item in assets}, {"linux/amd64", "linux/arm64"})
                (root / "agent-pulse-bridge-linux-arm64").write_bytes(b"bad")
                with self.assertRaisesRegex(ValueError, "摘要"):
                    module.bridge_artifacts(root)

    def test_missing_bridge_architecture_and_old_compiler_are_rejected(self):
        with tempfile.TemporaryDirectory() as value:
            root = Path(value)
            (root / "SHA256SUMS.txt").write_text("0" * 64 + "  agent-pulse-bridge-linux-amd64\n")
            with self.assertRaisesRegex(ValueError, "缺少"):
                module.bridge_artifacts(root)
            lines = []
            for arch, machine in (("amd64", 62), ("arm64", 183)):
                raw = bytearray(20)
                raw[:6] = b"\x7fELF\x02\x01"
                raw[18] = machine
                name = "agent-pulse-bridge-linux-" + arch
                (root / name).write_bytes(raw)
                lines.append(hashlib.sha256(raw).hexdigest() + "  " + name)
            (root / "SHA256SUMS.txt").write_text("\n".join(lines))
            with patch.object(module, "command", return_value=SimpleNamespace(stdout="go1.25.11")):
                with self.assertRaisesRegex(ValueError, "基线"):
                    module.bridge_artifacts(root)

    def test_ambiguous_artifact_is_rejected(self):
        with tempfile.TemporaryDirectory() as value:
            root = Path(value)
            (root / "windows-amd64.zip").touch()
            self.assertEqual(module.unique_file(root, "windows-amd64.zip"), root / "windows-amd64.zip")
            (root / "other").mkdir()
            (root / "other/windows-amd64.zip").touch()
            with self.assertRaises(ValueError):
                module.unique_file(root, "windows-amd64.zip")


if __name__ == "__main__":
    unittest.main()
