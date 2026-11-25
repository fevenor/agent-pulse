"""验证完整 Hub 发布命名和远端平台边界；不登录、不构建、不推送。"""
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("hub_publish", Path(__file__).resolve().parents[1] / "release/publish-hub-release.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class PublishTests(unittest.TestCase):
    def test_only_matching_complete_hub_candidate(self):
        value = {"commit": "a" * 40, "tag": "v0.5.0-rc.2", "hub": "ghcr.io/example/agent-pulse-hub:v0.5.0-rc.2", "hub_build": {"publisher_only": True}}
        self.assertEqual(module.release_target(value)[1], "ghcr.io/example/agent-pulse-hub:sha-" + "a" * 40)
        for changes in ({"commit": "main"}, {"tag": "latest"}, {"hub": "ghcr.io/example/agent-pulse/hub:v0.5.0-rc.2"}, {"hub": "ghcr.io/example/agent-pulse-hub:v0.5.0-rc.1"}, {"hub_build": {}}):
            with self.assertRaises(ValueError): module.release_target({**value, **changes})

    def test_stable_version_uses_same_signed_image_path(self):
        value = {"commit": "a" * 40, "tag": "v0.6.1", "hub": "ghcr.io/example/agent-pulse-hub:v0.6.1", "hub_build": {"publisher_only": True}}
        self.assertEqual(module.release_target(value)[0], value["hub"])

    def test_two_actual_platforms_required(self):
        platforms = [{"platform": {"os": "linux", "architecture": arch}} for arch in ["amd64", "arm64"]]
        module.verify_platforms({"manifests": platforms})
        module.verify_platforms({"manifests": platforms + [{"platform": {"os": "unknown", "architecture": "unknown"}}]})
        for raw in ({}, {"manifests": platforms[:1]}, {"manifests": platforms + [{"platform": {"os": "linux", "architecture": "arm"}}]}):
            with self.assertRaises(ValueError): module.verify_platforms(raw)


if __name__ == "__main__": unittest.main()
