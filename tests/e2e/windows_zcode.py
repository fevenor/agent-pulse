"""隔离 Hub + 发布 Tray 内置 Bridge；真实模型请求由 ZCode 桌面完成。"""
import argparse
import json
import shutil
import socket
import subprocess
import time
import urllib.parse
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def read(path):
    return json.loads(path.read_text(encoding="utf-8-sig"))


def write(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def get(base, path):
    with OPENER.open(base + path, timeout=2) as response:
        return json.load(response)


def wait(check, seconds=20):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            if check():
                return
        except (OSError, ValueError, KeyError):
            pass
        time.sleep(0.1)
    raise AssertionError("验证条件未在限定时间内满足")


def is_test_hook(handler, config):
    args = handler.get("args", [])
    if (handler.get("type") != "process" or len(args) != 7 or
            args[:2] != ["dispatch", "--config"] or
            args[3:6] != ["--adapter", "zcode", "--hook"]):
        return False
    try:
        # 内置 Bridge 收到的是带扩展前缀的规范 Windows 路径，不能直接比较文本。
        return Path(args[2]).samefile(config)
    except (OSError, TypeError, ValueError):
        return False


def serve(directory, target):
    """测试期间管理已明确指定的 ZCode 配置，退出时恢复原来的 hooks。"""
    assert not directory.exists(), "请使用新的测试目录"
    assert target.exists(), "请先安装并启动 ZCode"
    directory.mkdir(parents=True)
    original = read(target)
    shutil.copy2(target, directory / "zcode-before.json")
    repository = directory / "repository"
    repository.mkdir()
    package = read(ROOT / "dist/repository-windows/zcode.json")
    (repository / "packages").mkdir()
    name = package["sha256"] + ".zip"
    shutil.copy2(ROOT / "dist/repository-windows/packages" / name, repository / "packages" / name)
    write(repository / "input.json", {"sequence": 1, "packages": [package]})
    tool = ROOT / "bin/dev/agent-pulse-adapters.exe"
    subprocess.run([str(tool), "keygen", "--private", str(directory / "publisher.key"),
                    "--public", str(directory / "publisher.pub")], check=True)
    subprocess.run([str(tool), "publish", "--repository", str(repository), "--private",
                    str(directory / "publisher.key"), "--index", str(repository / "input.json")], check=True)
    hub_port, bridge_port = port(), port()
    base = f"http://127.0.0.1:{hub_port}"
    write(directory / "hub.json", {"listen": f"127.0.0.1:{hub_port}",
          "database": str(directory / "hub.db"), "adapter_directory": str(repository), "push": []})
    write(directory / "bridge.json", {"listen": f"127.0.0.1:{bridge_port}",
          "source_id": "isolated-windows-zcode", "hub_url": base,
          "adapters": {"directory": str(directory / "adapters"),
                       "public_key": (directory / "publisher.pub").read_text().strip(),
                       "poll_seconds": 30, "managed": {"zcode": str(target)}}})
    write(directory / "tray.yaml", {"hub_url": base, "bridge_enabled": True, "bridge_config": "bridge.json"})
    shutil.copy2(ROOT / "bin/agent-pulse-tray.exe", directory / "agent-pulse-tray.exe")
    children = []
    try:
        for exe, args, log in [(ROOT / "bin/dev/agent-pulse-hub.exe", ["--config", str(directory / "hub.json")], "hub"),
                               (directory / "agent-pulse-tray.exe", ["--settings"], "tray")]:
            with (directory / (log + ".log")).open("wb") as output:
                children.append(subprocess.Popen([str(exe), *args], cwd=directory,
                    creationflags=subprocess.CREATE_NO_WINDOW, stdout=output, stderr=output))
        state = directory / "adapters/state.json"
        wait(lambda: state.exists() and read(state).get("installed", {}).get("zcode", {}).get("package", {}).get("version") == package["version"])
        assert children[-1].poll() is None, "Tray 未持续运行"
        write(directory / "ready.json", {"hub": base, "bridge_port": bridge_port})
        print("隔离测试已就绪：请在 ZCode 新建无项目任务，使用 GLM-5.3-Flash 完成简短文本请求。", flush=True)
        deadline = time.monotonic() + 3600
        while not (directory / "stop").exists() and time.monotonic() < deadline:
            assert all(p.poll() is None for p in children), "测试服务提前退出"
            time.sleep(0.5)
    finally:
        for child in reversed(children):
            if child.poll() is None:
                child.kill()
                child.wait(timeout=8)
        # 仅移除测试安装的命令；测试期间其他配置修改照常保留。
        current = read(target)
        events = current.get("hooks", {}).get("events", {})
        for event, groups in list(events.items()):
            kept = []
            for group in groups:
                commands = [h for h in group["hooks"] if not is_test_hook(h, directory / "bridge.json")]
                if len(commands) == len(group["hooks"]):
                    kept.append(group)
                elif commands:
                    kept.append({**group, "hooks": commands})
            if kept:
                events[event] = kept
            else:
                events.pop(event)
        if not events and "hooks" not in original:
            current.pop("hooks", None)
        elif "hooks" in original:
            old_enabled = original["hooks"].get("enabled")
            if old_enabled is None:
                current["hooks"].pop("enabled", None)
            else:
                current["hooks"]["enabled"] = old_enabled
        write(target, current)
        print("隔离服务已停止，测试钩子已移除。", flush=True)


def verify(directory):
    base = read(directory / "ready.json")["hub"]
    snapshot = get(base, "/api/v1/snapshot")
    sessions = [s for s in snapshot["sessions"] if s["agent"] == "zcode"]
    assert sessions and all(s["state"] == "idle" for s in sessions), "ZCode 未确认完成"
    kinds = set()
    for session in sessions:
        query = urllib.parse.urlencode({k: session[k] for k in ("source_id", "agent", "session_id")})
        history = get(base, "/api/v1/events?" + query)
        events = history["events"] if isinstance(history, dict) else history
        kinds.update(e["kind"] for e in events)
    assert {"session.started", "session.resumed", "turn.completed"} <= kinds, kinds
    runtime = read(directory / "adapters/runtime/zcode.json")
    assert runtime["version"] == "0.1.0"
    tray = read(directory / "data/state.json")
    assert any(e["agent"] == "zcode" for e in tray["snapshot"]["unread"]), "Tray 未接收到事件"
    result = {"source": "ZCode 原生钩子", "model": "GLM-5.3-Flash（由桌面确认）",
              "embedded_bridge": True, "lifecycle_kinds": sorted(kinds), "tray_recorded": True,
              "phone_push": False, "sessions": len(sessions)}
    write(directory / "result.json", result)
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["serve", "verify", "stop"])
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--zcode-config", type=Path, default=Path.home() / ".zcode/cli/config.json")
    args = parser.parse_args()
    directory = args.directory.resolve()
    if args.mode == "serve":
        serve(directory, args.zcode_config.resolve())
    elif args.mode == "verify":
        verify(directory)
    else:
        (directory / "stop").touch()
