"""验证发布参数、跨路径单实例及内置接入进程生命周期，不操作桌面界面。"""
import json
import os
from pathlib import Path
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[2]


def binary_boundary_check():
    """检查真实产物，防止运行时分支隐藏入口却仍把演示数据打进发布包。"""
    release = (ROOT / "bin/agent-pulse-tray.exe").read_bytes()
    demo = (ROOT / "bin/dev/agent-pulse-tray-demo.exe").read_bytes()

    def subsystem(data):
        offset = struct.unpack_from("<I", data, 0x3C)[0]
        assert data[offset:offset + 4] == b"PE\0\0", "不是有效的 Windows 程序"
        return struct.unpack_from("<H", data, offset + 24 + 68)[0]

    assert subsystem(release) == 2, "发布程序不是 GUI 子系统"
    assert subsystem(demo) == 3, "演示程序不是控制台子系统"
    for text in ("检查原生托盘菜单", "切换演示浅色 / 深色", "开发演示时间",
                 "项目文档索引构建与搜索优化", "演示：管理操作已应用", "开发交互：",
                 "检查长消息滚动", "开发演示不登记或修改开机自动启动", "开发演示已就绪"):
        marker = text.encode("utf-8")
        assert marker in demo, f"演示程序缺少检查样本：{text}"
        assert marker not in release, f"发布程序包含演示样本：{text}"


def demo_console_check(directory):
    """在测试专属隐藏控制台中验证日志、演示隔离与真实 Ctrl+C 信号。"""
    import ctypes
    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    handler_type = ctypes.WINFUNCTYPE(ctypes.c_int, ctypes.c_ulong)
    handler = handler_type(lambda _: 1)
    kernel.SetConsoleCtrlHandler.argtypes = [handler_type, ctypes.c_int]
    assert kernel.SetConsoleCtrlHandler(handler, 1)
    environment = os.environ.copy()
    environment["LOCALAPPDATA"] = str(directory / "demo-appdata")
    log_path = directory / "demo.log"
    with log_path.open("wb") as output:
        process = subprocess.Popen([str(ROOT / "bin/dev/agent-pulse-tray-demo.exe"),
                                    "--demo", "--config", str(directory / "missing.yaml")],
                                   env=environment, stdout=output, stderr=output)
        try:
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                if "开发演示已就绪" in log_path.read_text(encoding="utf-8", errors="replace"):
                    break
                assert process.poll() is None, "演示提前退出，请先关闭其他 Tray 实例"
                time.sleep(0.1)
            else:
                raise AssertionError("演示未进入窗口消息循环")
            assert kernel.GenerateConsoleCtrlEvent(0, 0), "无法发送测试控制台 Ctrl+C"
            assert process.wait(timeout=8) == 0, "演示未正常退出"
        finally:
            if process.poll() is None:
                process.kill()
                process.wait(timeout=5)
    content = log_path.read_text(encoding="utf-8")
    assert "开发演示前台运行" in content and "开发交互：Quit" in content
    assert "开发交互：Settings" not in content and "开发交互：Show" not in content, "演示默认启动打开了窗口"
    assert not (directory / "demo-appdata/AgentPulse").exists(), "演示写入了生产状态目录"
    (directory / "demo-result.json").write_text(json.dumps({"console_log": True,
        "ctrl_c_exit": True, "no_production_state": True, "ignores_production_config": True,
        "default_starts_in_tray": True}))


def main():
    if "--with-demo" in sys.argv:
        binary_boundary_check()
    exe = ROOT / "bin/agent-pulse-tray.exe"
    rejected = subprocess.run([str(exe), "--demo"], capture_output=True, timeout=10)
    assert rejected.returncode == 1, "发布构建接受了演示参数"
    with tempfile.TemporaryDirectory(prefix="pulse-native-") as temp:
        directory = Path(temp)
        portable = directory / "便携 空格"
        portable.mkdir()
        portable_exe = portable / "agent-pulse-tray.exe"
        shutil.copy2(exe, portable_exe)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        (directory / "bridge.json").write_text(json.dumps({
            "listen": f"127.0.0.1:{port}", "source_id": "isolated-native-test",
            "hub_url": "http://127.0.0.1:1", "queue_events": 100, "queue_bytes": 65536,
        }), encoding="utf-8")
        (portable / "tray.yaml").write_text(json.dumps({
            "bridge_enabled": True, "bridge_config": "../bridge.json",
        }), encoding="utf-8")
        environment = os.environ.copy()
        environment["LOCALAPPDATA"] = str(directory / "appdata")
        process = subprocess.Popen([str(portable_exe)], cwd=directory,
                                   env=environment, creationflags=subprocess.CREATE_NO_WINDOW,
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

        def health():
            try:
                with opener.open(f"http://127.0.0.1:{port}/healthz", timeout=1) as response:
                    return response.status == 200
            except OSError:
                return False

        try:
            deadline = time.monotonic() + 15
            while not health() and time.monotonic() < deadline:
                time.sleep(0.1)
            assert process.poll() is None and health(), "托管接入进程未启动；请先关闭其他 Tray 实例"
            copy = directory / "other-install.exe"
            shutil.copy2(exe, copy)
            second = subprocess.run([str(copy), "--config", str(directory / "does-not-exist.yaml")],
                                    env=environment, timeout=8, capture_output=True)
            assert second.returncode == 0, "重复启动错误地读取了第二份配置"
            assert process.poll() is None and health(), "重复启动影响了首个实例"
            helpers = list((portable / "data/internal").rglob("bridge.exe"))
            assert len(helpers) == 1 and helpers[0].stat().st_size > 1_000_000
            assert (portable / "data/state.json").exists(), "状态未保存在便携目录"
            assert not (directory / "appdata/AgentPulse").exists(), "便携程序写入了用户数据目录"
        finally:
            process.kill()
            process.wait(timeout=5)
        deadline = time.monotonic() + 8
        while health() and time.monotonic() < deadline:
            time.sleep(0.1)
        assert not health(), "父进程退出后本地接入进程泄漏"
        time.sleep(0.3)
        demo_result = None
        if "--with-demo" in sys.argv:
            startup = subprocess.STARTUPINFO()
            startup.dwFlags |= subprocess.STARTF_USESHOWWINDOW
            startup.wShowWindow = 0
            worker = subprocess.run([sys.executable, str(Path(__file__).resolve()),
                                     "--demo-worker", str(directory)],
                                    creationflags=subprocess.CREATE_NEW_CONSOLE,
                                    startupinfo=startup, timeout=30, capture_output=True)
            assert worker.returncode == 0, worker.stderr.decode("utf-8", errors="replace")
            demo_result = json.loads((directory / "demo-result.json").read_text())
    print(json.dumps({"release_rejects_demo": True, "managed_bridge_starts": True,
                      "duplicate_different_path_config_exits": True, "bridge_stops_with_parent": True,
                      "portable_paths_ignore_working_directory": True,
                      "demo": demo_result,
                      "binary_demo_isolation": True if "--with-demo" in sys.argv else None}))


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--demo-worker":
        demo_console_check(Path(sys.argv[2]))
    else:
        main()
