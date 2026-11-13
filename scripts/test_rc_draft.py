"""验证 RC 草稿边界，不联网、不提交或发布。"""
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("rc_draft", Path(__file__).with_name("create_rc_draft.py"))
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
