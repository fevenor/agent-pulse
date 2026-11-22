"""验证Linux原生验收的钩子撤销边界，不调用模型或真实宿主。"""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("zcode_linux", ROOT / "tests/e2e/zcode_linux.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class CleanupTests(unittest.TestCase):
    def fixture(self, root):
        config = (root / "bridge.json").resolve()
        hook = {"type": "process", "command": "/test/agent-pulse-bridge",
                "args": ["dispatch", "--config", str(config), "--adapter", "zcode", "--hook", "Stop"]}
        return config, {"hooks": {"enabled": True, "events": {"Stop": [{"hooks": [hook]}]}}}

    def test_originally_absent_config_is_removed_after_test_only_edit(self):
        with tempfile.TemporaryDirectory() as value:
            root = Path(value)
            target = root / "zcode.json"
            config, installed = self.fixture(root)
            module.write(target, installed)
            module.restore_hooks(target, config, None, installed)
            self.assertFalse(target.exists())

    def test_existing_config_is_restored_byte_for_byte(self):
        with tempfile.TemporaryDirectory() as value:
            root = Path(value)
            target = root / "zcode.json"
            original = b'{ "theme": "original", "hooks": { "enabled": true } }\r\n'
            config, installed = self.fixture(root)
            module.write(target, installed)
            module.restore_hooks(target, config, original, installed)
            self.assertEqual(target.read_bytes(), original)

    def test_concurrent_user_hook_and_setting_are_preserved(self):
        with tempfile.TemporaryDirectory() as value:
            root = Path(value)
            target = root / "zcode.json"
            config, installed = self.fixture(root)
            current = json.loads(json.dumps(installed))
            other = {"type": "process", "command": "/user/keep", "args": ["自己的钩子"]}
            current["theme"] = "user-changed"
            current["hooks"]["events"]["Stop"][0]["hooks"].append(other)
            module.write(target, current)
            module.restore_hooks(target, config, None, installed)
            saved = module.read(target)
            self.assertEqual(saved["theme"], "user-changed")
            self.assertEqual(saved["hooks"]["events"]["Stop"][0]["hooks"], [other])

    def test_other_bridge_config_is_not_owned(self):
        with tempfile.TemporaryDirectory() as value:
            root = Path(value)
            config, installed = self.fixture(root)
            hook = installed["hooks"]["events"]["Stop"][0]["hooks"][0]
            self.assertTrue(module.owns_hook(hook, config))
            hook["args"][2] = str(root / "other.json")
            self.assertFalse(module.owns_hook(hook, config))


if __name__ == "__main__":
    unittest.main()
