"""验证跨平台包合并、通用插件去重和错误输入对既有仓库的保护。"""

import hashlib
import io
import json
import tempfile
import unittest
import zipfile
from pathlib import Path

from merge_adapter_repositories import merge


class RepositoryTests(unittest.TestCase):
    def test_merge_and_reject_conflicts_without_replacing_index(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)

            def package(repo, platform, content, timestamp, plugin=False):
                buffer = io.BytesIO()
                with zipfile.ZipFile(buffer, "w") as archive:
                    archive.writestr(zipfile.ZipInfo("index.js" if plugin else "adapter", timestamp), content)
                data = buffer.getvalue()
                item = {"id": "opencode" if plugin else "sample", "version": "1.0.0", "platform": platform,
                        "kind": "opencode-plugin" if plugin else "executable",
                        "sha256": hashlib.sha256(data).hexdigest(), "size": len(data)}
                (repo / "packages").mkdir(parents=True, exist_ok=True)
                (repo / "packages" / (item["sha256"] + ".zip")).write_bytes(data)
                return item

            linux, windows, output = root / "linux", root / "windows", root / "output"
            old, new = (2025, 1, 1, 0, 0, 0), (2026, 1, 1, 0, 0, 0)
            common = package(linux, "any", "通用插件", old, True)
            common_windows = package(windows, "any", "通用插件", new, True)
            self.assertNotEqual(common["sha256"], common_windows["sha256"])
            first = [package(linux, "linux/amd64", "Linux 程序", old), common]
            second = [package(windows, "windows/amd64", "Windows 程序", new), common_windows]

            def index(repo, packages):
                (repo / "index-input.json").write_text(json.dumps({"sequence": 1, "packages": packages}), encoding="utf-8")

            index(linux, first)
            index(windows, second)
            result = merge([linux, windows], output, 2)
            self.assertEqual(result["sequence"], 2)
            self.assertEqual(len(result["packages"]), 3)
            self.assertIn(common, result["packages"])
            for item in result["packages"]:
                data = (output / "packages" / (item["sha256"] + ".zip")).read_bytes()
                self.assertEqual(hashlib.sha256(data).hexdigest(), item["sha256"])
            saved = (output / "index-input.json").read_bytes()
            index(windows, [package(windows, "any", "实际内容不同", new, True)])
            with self.assertRaisesRegex(ValueError, "同版本适配包冲突"):
                merge([linux, windows], output, 3)
            self.assertEqual(saved, (output / "index-input.json").read_bytes())
            index(windows, second)
            (windows / "packages" / (common_windows["sha256"] + ".zip")).write_bytes("损坏".encode())
            with self.assertRaisesRegex(ValueError, "大小或摘要不匹配"):
                merge([linux, windows], output, 3)
            self.assertEqual(saved, (output / "index-input.json").read_bytes())
            for sequence in (0, -1, 2**64):
                with self.assertRaises(ValueError):
                    merge([linux], output, sequence)


if __name__ == "__main__":
    unittest.main()
