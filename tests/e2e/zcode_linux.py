"""独立 Linux Bridge 的 ZCode Server 原生验收；模型请求只从已授权桌面发起。"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import sqlite3
import subprocess
import time
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def read(path):
    return json.loads(path.read_text(encoding="utf-8-sig"))


def write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    raw = (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode()
    temporary = path.with_name(path.name + ".temporary")
    temporary.write_bytes(raw)
    temporary.chmod(0o600)
    temporary.replace(path)


def get(base, path):
    with OPENER.open(base + path, timeout=3) as response:
        return json.load(response)


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait(check, seconds=30):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            if check():
                return
        except (OSError, ValueError, KeyError, subprocess.CalledProcessError):
            pass
        time.sleep(0.2)
    raise AssertionError("验收条件未在限定时间内满足")


def owns_hook(handler, config):
    args = handler.get("args", [])
    return (handler.get("type") == "process" and len(args) == 7 and
            args[:2] == ["dispatch", "--config"] and
            args[3:6] == ["--adapter", "zcode", "--hook"] and
            Path(args[2]).resolve() == config)


def restore_hooks(target, config, original, installed):
    """仅撤销本测试安装的条目；外部修改不被旧快照覆盖。"""
    current = read(target)
    if current == installed:
        if original is None:
            target.unlink()
        else:
            temporary = target.with_name(target.name + ".test-restore")
            temporary.write_bytes(original)
            temporary.chmod(0o600)
            temporary.replace(target)
        return "已精确恢复测试前文件"
    hooks = current.get("hooks", {})
    events = hooks.get("events", {})
    for event, groups in events.items():
        kept = []
        for group in groups:
            handlers = [h for h in group["hooks"] if not owns_hook(h, config)]
            if handlers:
                kept.append({**group, "hooks": handlers})
        events[event] = kept
    write(target, current)
    return "已移除测试钩子并保留测试期间其他修改"


def serve(args):
    directory, target, binaries = args.directory.resolve(), args.zcode_config.resolve(), args.binaries.resolve()
    assert os.name == "posix", "此入口在 ZCode Server 所在 Linux 执行"
    assert not directory.exists(), "请使用新的隔离目录，避免覆盖既有证据"
    directory.mkdir(parents=True, mode=0o700)
    original = target.read_bytes() if target.exists() else None
    if original is not None:
        (directory / "zcode-before.json").write_bytes(original)
    repository = directory / "repository"
    repository.mkdir()
    tool, bridge = binaries / "agent-pulse-adapters", binaries / "agent-pulse-bridge"
    subprocess.run([str(tool), "keygen", "--private", str(directory / "publisher.key"),
                    "--public", str(directory / "publisher.pub")], check=True)
    package_file = directory / "package.json"
    subprocess.run([str(tool), "pack", "--source", str(args.adapter_source.resolve()),
                    "--manifest", str(args.manifest.resolve()),
                    "--repository", str(repository), "--out", str(package_file)], check=True)
    package = read(package_file)
    write(directory / "index-input.json", {"sequence": 1, "packages": [package]})
    subprocess.run([str(tool), "publish", "--private", str(directory / "publisher.key"),
                    "--repository", str(repository), "--index", str(directory / "index-input.json")], check=True)
    shutil.copy2(directory / "publisher.pub", repository / "publisher.pub")
    hub_port = free_port()
    base = f"http://127.0.0.1:{hub_port}"
    config = directory / "bridge.json"
    write(directory / "hub.json", {"listen": f"127.0.0.1:{hub_port}", "database": str(directory / "hub.db"),
                                   "adapter_directory": str(repository), "push": []})
    write(config, {"listen": "", "source_id": args.source_id, "hub_url": base,
                   "adapters": {"directory": str(directory / "adapters"), "min_check_seconds": 300,
                                "public_key": (directory / "publisher.pub").read_text().strip(),
                                "managed": {"zcode": str(target)}}})
    children, installed = [], None
    try:
        for binary, command, name in [(binaries / "agent-pulse-hub", ["--config", str(directory / "hub.json")], "hub"),
                                      (bridge, ["serve", "--config", str(config)], "bridge")]:
            with (directory / (name + ".log")).open("wb") as log:
                children.append(subprocess.Popen([str(binary), *command], stdout=log, stderr=log))
            if name == "hub":
                wait(lambda: get(base, "/healthz")["status"] == "ok")
        state = directory / "adapters/state.json"
        wait(lambda: state.exists() and read(state).get("installed", {}).get("zcode", {}).get("package", {}).get("version") == package["version"])
        installed = read(target)
        assert installed.get("hooks", {}).get("enabled") is True, "钩子总开关未开启，不自动覆盖用户显式关闭"
        write(directory / "ready.json", {"hub": base, "bridge_config": str(config), "source_id": args.source_id,
                                         "package_version": package["version"],
                                         "bridge_sha256": hashlib.sha256(bridge.read_bytes()).hexdigest()})
        print("隔离 Linux 接入已就绪，请通过 ZCode 桌面在对应远程项目中执行授权测试。", flush=True)
        deadline = time.monotonic() + 7200
        while not (directory / "stop").exists() and time.monotonic() < deadline:
            assert all(child.poll() is None for child in children), "隔离服务提前退出"
            time.sleep(0.5)
    finally:
        try:
            if len(children) == 2 and children[-1].poll() is None:
                wait(lambda: json.loads(subprocess.check_output(
                    [str(bridge), "health", "--config", str(config)], stderr=subprocess.DEVNULL)).get("pending") == 0, seconds=15)
        finally:
            try:
                if installed is None and target.exists():
                    # 同步可能已写入配置却尚未发出就绪；仍仅移除测试所有条目。
                    installed = {}
                if installed is not None:
                    result = restore_hooks(target, config, original, installed)
                    write(directory / "cleanup.json", {"hook_restore": result})
            finally:
                for child in reversed(children):
                    if child.poll() is None:
                        child.terminate()
                        child.wait(timeout=10)
        print("隔离服务已停止，测试钩子已撤销。", flush=True)


def verify(args):
    directory = args.directory.resolve()
    ready = read(directory / "ready.json")
    base, source = ready["hub"], ready["source_id"]
    snapshot = get(base, "/api/v1/snapshot")
    sessions = [s for s in snapshot["sessions"] if s["source_id"] == source and s["agent"] == "zcode"]
    assert sessions and all(s["state"] == "idle" for s in sessions), "主会话尚未正常完成"
    events = []
    for session in sessions:
        query = urllib.parse.urlencode({k: session[k] for k in ("source_id", "agent", "session_id")})
        history = get(base, "/api/v1/events?" + query + "&view=all")
        events.extend(history["events"] if isinstance(history, dict) else history)
    kinds = {event["kind"] for event in events}
    assert {"session.started", "session.resumed", "turn.completed"} <= kinds, kinds
    completed = [e for e in events if e["kind"] == "turn.completed"]
    assert completed and all(e.get("body", "").strip() for e in completed), "原生完成正文未收到"
    if args.require_question:
        questions = [e for e in events if e["kind"] == "input.required"]
        assert questions and all(e.get("body", "").strip() for e in questions), "原生提问正文未收到"
    runtime = read(directory / "adapters/runtime/zcode.json")
    assert runtime["version"] == ready["package_version"], "运行回执不匹配安装包"
    children = []
    if args.require_child:
        database = args.zcode_config.resolve().parent / "db/db.sqlite"
        with sqlite3.connect(database.as_uri() + "?mode=ro", uri=True, timeout=0.2) as connection:
            connection.execute("PRAGMA query_only=ON")
            for session in sessions:
                children.extend(row[0] for row in connection.execute(
                    "SELECT id FROM session WHERE parent_id=?", (session["session_id"],)))
        assert children, "未确认宿主实际创建过子会话，不能只用Hub无子事件证明过滤"
        assert not any(s["session_id"] in children for s in snapshot["sessions"]), "原生子会话误入Hub"
        assert not any(e["session_id"] in children for e in events), "原生子会话事件误入主会话历史"
    result = {"agent": "ZCode Server", "independent_linux_bridge": True, "source_id": source,
              "kinds": sorted(kinds), "sessions": len(sessions), "events": len(events),
              "final_output_received": True, "question_checked": args.require_question,
              "native_children_filtered": len(children), "phone_push": False}
    write(directory / "result.json", result)
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=["serve", "verify", "stop"])
    parser.add_argument("--directory", required=True, type=Path)
    parser.add_argument("--binaries", type=Path, default=ROOT / "bin")
    parser.add_argument("--adapter-source", type=Path, default=ROOT / "dist/adapter-build/zcode")
    parser.add_argument("--manifest", type=Path, default=ROOT / "adapters/zcode/manifest-linux.json")
    parser.add_argument("--zcode-config", type=Path, default=Path.home() / ".zcode/cli/config.json")
    parser.add_argument("--source-id", default="isolated-zcode-linux")
    parser.add_argument("--require-question", action="store_true")
    parser.add_argument("--require-child", action="store_true", help="核对原生父子记录及Hub无对应子会话")
    args = parser.parse_args()
    if args.mode == "serve":
        serve(args)
    elif args.mode == "verify":
        verify(args)
    else:
        (args.directory.resolve() / "stop").touch()
