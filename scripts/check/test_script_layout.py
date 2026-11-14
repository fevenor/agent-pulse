"""验证脚本分类及迁移后的入口，避免工作流或 Windows 路径仍引用旧目录。"""
from pathlib import Path
import re
import os
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class LayoutTests(unittest.TestCase):
    def test_root_has_only_index_and_categorized_scripts(self):
        self.assertEqual({p.name for p in (ROOT / "scripts").iterdir() if p.is_file()}, {"README.md"})
        self.assertTrue(all((ROOT / "scripts" / folder).is_dir() for folder in ("build", "install", "check", "release")))

    def test_referenced_script_paths_exist(self):
        files = [ROOT / "README.md", ROOT / "AGENTS.md"]
        for folder in ("docs", ".github/workflows", "scripts", "tests/e2e", "go/cmd", "tray/tests", "adapters/opencode/test"):
            files.extend(path for path in (ROOT / folder).rglob("*") if path.is_file() and path.suffix in (".md", ".yml", ".py", ".ps1", ".sh", ".go", ".rs", ".ts"))
        for path in files:
            text = path.read_text(encoding="utf-8")
            for reference in re.findall(r"scripts/(?:build|install|check|release)/[\w.-]+\.(?:sh|ps1|py)", text):
                self.assertTrue((ROOT / reference).is_file(), f"{path.relative_to(ROOT)} 引用不存在的 {reference}")
            self.assertNotRegex(text, r"scripts/(?:build|check|install-bridge|package-adapters|package-windows|test_rc_draft|test_prepare_hub_release|test_merge_adapter_repositories)\.(?:sh|ps1|py)")

    @unittest.skipUnless(os.name == "posix", "构建路径替身使用 POSIX 可执行脚本，Windows 仅检查真实入口文本")
    def test_bridge_build_resolves_relative_output_before_changing_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tool = root / "go"
            tool.write_text(f"#!{sys.executable}\n" + """import sys,pathlib
args=sys.argv[1:]
if args[:2]==['env','GOVERSION']:print('go1.27.4')
elif args[0]=='build':
 p=pathlib.Path(args[args.index('-o')+1]);assert p.is_absolute();p.write_bytes(b'build-path-fixture')
elif args[:2]==['version','-m']:
 assert pathlib.Path(args[2]).is_file();print('fixture build info')
else:sys.exit(1)
""")
            tool.chmod(0o755)
            env = {**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"]}
            result = subprocess.run(["bash", str(ROOT / "scripts/build/package-bridge.sh"), "相对发布输出"], cwd=root, env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((root / "相对发布输出/agent-pulse-bridge-linux-amd64").is_file())
            self.assertTrue((root / "相对发布输出/agent-pulse-bridge-linux-arm64").is_file())
            self.assertTrue((root / "相对发布输出/SHA256SUMS.txt").is_file())

    def test_install_does_not_call_compilers_and_roots_are_adjusted(self):
        text = (ROOT / "scripts/install/bridge.sh").read_text()
        self.assertNotRegex(text, r"\b(?:go|cargo|pnpm|npm|yarn)\s+(?:build|install|run)\b")
        self.assertNotIn("scripts/build/", text)
        for path in (ROOT / "scripts/build").glob("*.sh"):
            self.assertIn('/../.."', path.read_text(), str(path))
        for folder in ("build", "check"):
            for path in (ROOT / "scripts" / folder).glob("*.ps1"):
                if path.name != "windows-tools.ps1":
                    self.assertIn("Split-Path (Split-Path $PSScriptRoot -Parent) -Parent", path.read_text(), str(path))


if __name__ == "__main__":
    unittest.main()
