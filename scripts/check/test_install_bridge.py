"""隔离验证用户安装器：只下载/复制预构建，不要求编译器或覆盖私人配置。"""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
INSTALLER = ROOT / "scripts/install/bridge.sh"


class InstallTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.home = self.root / "用户 家目录"
        self.home.mkdir()
        self.tools = self.root / "tools"
        self.tools.mkdir()
        self.artifact = self.root / "预构建 Bridge"
        header = bytearray(20)
        header[:6] = b"\x7fELF\x02\x01"
        header[18] = 62
        self.artifact.write_bytes(header + b"isolated-fixture")
        self.checksum = hashlib.sha256(self.artifact.read_bytes()).hexdigest()
        self.env = {**os.environ, "HOME": str(self.home), "PATH": str(self.tools) + os.pathsep + os.environ["PATH"],
                    "FIXTURE_ROOT": str(self.root), "FIXTURE_FILE": str(self.artifact), "FIXTURE_HASH": self.checksum}
        self.tool("uname", '#!/bin/sh\nif [ "$1" = -s ]; then printf "%s\\n" "${FIXTURE_OS:-Linux}"; else printf "%s\\n" "${FIXTURE_ARCH:-x86_64}"; fi\n')
        for name in ("go", "pnpm", "cargo", "systemctl"):
            self.tool(name, '#!/bin/sh\nprintf "%s\\n" "$0" >> "$FIXTURE_ROOT/unexpected-tools"\nexit 3\n')
        self.tool("curl", f"#!{sys.executable}\n" + '''import os,pathlib,sys
args=sys.argv[1:];url=args[-1];root=pathlib.Path(os.environ['FIXTURE_ROOT'])
with (root/'requests').open('a') as stream:stream.write(url+'\\n')
mode=os.environ.get('FIXTURE_MODE','')
if mode=='unavailable':sys.exit(22)
if url.endswith('/releases/latest'):
 print('https://github.com/fevenor/agent-pulse/releases/tag/'+os.environ.get('FIXTURE_LATEST','v0.5.0'),end='');sys.exit(0)
out=pathlib.Path(args[args.index('--output')+1])
if url.endswith('/SHA256SUMS.txt'):
 name=os.environ.get('FIXTURE_ASSET','agent-pulse-bridge-linux-amd64')
 checksum=os.environ.get('FIXTURE_HASH','0'*64)
 if mode=='bad-hash':checksum='0'*64
 text=checksum+'  '+name+'\\n'
 if mode=='duplicate':text+=text
 out.write_text(text)
else:out.write_bytes(pathlib.Path(os.environ['FIXTURE_FILE']).read_bytes())
''')

    def tool(self, name, content):
        path = self.tools / name
        path.write_text(content)
        path.chmod(0o755)

    def run_install(self, *args, success=True, env=None):
        result = subprocess.run(["bash", str(INSTALLER), *args], env={**self.env, **(env or {})}, capture_output=True, text=True)
        if success:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout)
        return result

    def assert_untouched(self):
        self.assertFalse((self.home / ".local").exists())
        self.assertFalse((self.home / ".config").exists())

    def test_download_selects_amd64_and_keeps_config_and_unit(self):
        config = self.home / ".config/agent-pulse/bridge.yaml"
        config.parent.mkdir(parents=True)
        config.write_text("私人配置保持不变")
        unit = self.home / ".config/systemd/user/agent-pulse-bridge.service"
        unit.parent.mkdir(parents=True)
        unit.write_text("已有服务保持不变")
        self.run_install("--version", "0.5.0-rc.1")
        self.assertEqual((self.home / ".local/bin/agent-pulse-bridge").read_bytes(), self.artifact.read_bytes())
        self.assertEqual(config.read_text(), "私人配置保持不变")
        self.assertEqual(unit.read_text(), "已有服务保持不变")
        self.assertIn("v0.5.0-rc.1/agent-pulse-bridge-linux-amd64", (self.root / "requests").read_text())
        self.assertFalse((self.root / "unexpected-tools").exists())

    def test_download_selects_arm64(self):
        raw = bytearray(self.artifact.read_bytes())
        raw[18] = 183
        self.artifact.write_bytes(raw)
        self.run_install("--version", "v0.5.0", env={"FIXTURE_ARCH": "aarch64", "FIXTURE_ASSET": "agent-pulse-bridge-linux-arm64", "FIXTURE_HASH": hashlib.sha256(raw).hexdigest()})
        self.assertIn("agent-pulse-bridge-linux-arm64", (self.root / "requests").read_text())

    def test_default_follows_latest_stable_release(self):
        self.run_install()
        self.assertIn("/releases/latest", (self.root / "requests").read_text())
        self.assertIn("/releases/download/v0.5.0/", (self.root / "requests").read_text())

    def test_missing_release_never_builds_or_writes(self):
        result = self.run_install("--version", "v0.5.0-rc.1", success=False, env={"FIXTURE_MODE": "unavailable"})
        self.assertIn("草稿", result.stderr)
        self.assert_untouched()
        self.assertFalse((self.root / "unexpected-tools").exists())

    def test_bad_digest_and_duplicate_manifest_fail_before_install(self):
        for mode in ("bad-hash", "duplicate"):
            self.run_install("--version", "v0.5.0", success=False, env={"FIXTURE_MODE": mode})
            self.assert_untouched()

    def test_wrong_architecture_fails_before_install(self):
        self.run_install("--version", "v0.5.0", success=False, env={"FIXTURE_ARCH": "aarch64", "FIXTURE_ASSET": "agent-pulse-bridge-linux-arm64"})
        self.assert_untouched()

    def test_local_prebuilt_does_not_access_network_or_compilers(self):
        self.run_install("--version", "v0.5.0-rc.1", "--file", str(self.artifact), "--sha256", self.checksum)
        self.assertFalse((self.root / "requests").exists())
        self.assertFalse((self.root / "unexpected-tools").exists())
        unit = (self.home / ".config/systemd/user/agent-pulse-bridge.service").read_text()
        self.assertIn("EnvironmentFile=", unit)

    def test_local_wrong_digest_preserves_existing_binary(self):
        binary = self.home / ".local/bin/agent-pulse-bridge"
        binary.parent.mkdir(parents=True)
        binary.write_bytes("原二进制".encode())
        self.run_install("--version", "v0.5.0", "--file", str(self.artifact), "--sha256", "0" * 64, success=False)
        self.assertEqual(binary.read_bytes(), "原二进制".encode())

    def test_dry_run_and_unsupported_platform_do_not_download(self):
        self.run_install("--dry-run")
        self.assert_untouched()
        for env in ({"FIXTURE_OS": "Darwin"}, {"FIXTURE_ARCH": "riscv64"}):
            self.run_install("--version", "v0.5.0", success=False, env=env)
            self.assert_untouched()
        self.assertFalse((self.root / "requests").exists())

    def test_running_bridge_is_not_replaced(self):
        binary = self.home / ".local/bin/agent-pulse-bridge"
        binary.parent.mkdir(parents=True)
        binary.write_bytes("运行中的旧程序".encode())
        self.tool("systemctl", "#!/bin/sh\nexit 0\n")
        result = self.run_install("--version", "v0.5.0", "--file", str(self.artifact), "--sha256", self.checksum, success=False)
        self.assertIn("正在运行", result.stderr)
        self.assertEqual(binary.read_bytes(), "运行中的旧程序".encode())
        self.assertEqual(len(list(binary.parent.iterdir())), 1)

    def test_stopped_upgrade_preserves_backup(self):
        binary = self.home / ".local/bin/agent-pulse-bridge"
        binary.parent.mkdir(parents=True)
        binary.write_bytes("停止的旧程序".encode())
        self.tool("systemctl", "#!/bin/sh\nexit 3\n")
        self.run_install("--version", "v0.5.0", "--file", str(self.artifact), "--sha256", self.checksum)
        self.assertEqual(binary.read_bytes(), self.artifact.read_bytes())
        backups = list(binary.parent.glob("agent-pulse-bridge.backup.*"))
        self.assertEqual(len(backups), 1)
        self.assertEqual(backups[0].read_bytes(), "停止的旧程序".encode())

    def test_latest_stable_does_not_silently_select_prerelease(self):
        self.run_install(success=False, env={"FIXTURE_LATEST": "v0.5.0-rc.1"})
        self.assert_untouched()

    def test_unsafe_version_and_local_missing_checksum_are_rejected(self):
        self.run_install("--version", "../../other", success=False)
        self.run_install("--version", "v0.5.0", "--file", str(self.artifact), success=False)
        self.assert_untouched()


if __name__ == "__main__":
    unittest.main()
