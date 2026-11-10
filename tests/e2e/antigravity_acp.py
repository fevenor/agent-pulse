"""用已登录 agy 的 ACP 验证原生 hook、独立包和 Hub；不读取认证文件。"""
import json
import argparse
import os
from pathlib import Path
import queue
import shutil
import signal
import socket
import subprocess
import tempfile
import threading
import time
import urllib.request

project = Path(__file__).resolve().parents[2]
binary = project / "bin"
root = Path(tempfile.mkdtemp(prefix="agent-pulse-agy-native-"))
processes, logs = [], []
messages = queue.Queue()
updates = []
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--resume", action="store_true", help="重启 ACP 包装器并加载原会话，追加一条真实模型请求")
options = parser.parse_args()


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait(check, seconds=20):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                return value
        except (OSError, ValueError):
            pass
        time.sleep(.2)
    raise RuntimeError("等待本地服务超时")


def get(path):
    with urllib.request.urlopen(hub_url + path, timeout=2) as response:
        return json.load(response)


def run(args):
    return subprocess.run(list(map(str, args)), check=True, stdout=subprocess.DEVNULL)


def start(name, args, rpc=False):
    handle = (root / (name + ".log")).open("w")
    logs.append(handle)
    proc = subprocess.Popen(list(map(str, args)), cwd=root, start_new_session=True,
                            stdin=subprocess.PIPE if rpc else subprocess.DEVNULL,
                            stdout=subprocess.PIPE if rpc else handle, stderr=handle,
                            text=True, bufsize=1)
    processes.append(proc)
    return proc


def reader(proc):
    for line in proc.stdout:
        try:
            messages.put(json.loads(line))
        except ValueError:
            pass
    messages.put(None)


def request(method, params, ident):
    acp.stdin.write(json.dumps({"jsonrpc": "2.0", "id": ident, "method": method, "params": params}) + "\n")
    acp.stdin.flush()
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        try:
            message = messages.get(timeout=max(.1, deadline - time.monotonic()))
        except queue.Empty:
            break
        if message is None:
            raise RuntimeError("ACP 进程提前退出")
        if message.get("id") == ident and "method" not in message:
            if "error" in message:
                # 只汇报标准错误码，避免宿主错误中夹带认证数据或完整对话。
                raise RuntimeError(f"ACP {method} 失败，错误码 {message['error'].get('code')}")
            return message.get("result", {})
        if message.get("method") == "session/update":
            updates.append(message.get("params", {}).get("update", {}).get("sessionUpdate"))
        if "method" in message and "id" in message:
            if message["method"] == "session/request_permission":
                response = {"result": {"outcome": {"outcome": "cancelled"}}}
            else:
                response = {"error": {"code": -32601, "message": "测试客户端不提供该能力"}}
            acp.stdin.write(json.dumps({"jsonrpc": "2.0", "id": message["id"], **response}) + "\n")
            acp.stdin.flush()
    raise RuntimeError(f"ACP {method} 超时")


hub_port, bridge_port = port(), port()
hub_url = f"http://127.0.0.1:{hub_port}"
try:
    repo = root / "repository"
    shutil.copytree(project / "dist/repository", repo)
    run([binary / "agent-pulse-adapters", "keygen", "--private", root / "publisher.key", "--public", root / "publisher.pub"])
    run([binary / "agent-pulse-adapters", "publish", "--repository", repo, "--private", root / "publisher.key", "--index", repo / "index-input.json"])
    (root / "hub.yaml").write_text(f'listen: "127.0.0.1:{hub_port}"\ndatabase: "{root}/hub.db"\nadapter_directory: "{repo}"\n')
    (root / "bridge.yaml").write_text(f'''listen: "127.0.0.1:{bridge_port}"
source_id: "native-agy-test"
hub_url: "{hub_url}"
adapters:
  directory: "{root}/adapters"
  public_key: "{(root / 'publisher.pub').read_text().strip()}"
  poll_seconds: 5
  managed:
    antigravity: "{root}/.agents/hooks.json"
''')
    start("hub", [binary / "agent-pulse-hub", "--config", root / "hub.yaml"])
    wait(lambda: get("/healthz"))
    start("bridge", [binary / "agent-pulse-bridge", "serve", "--config", root / "bridge.yaml"])
    wait(lambda: (root / ".agents/hooks.json").exists())
    # ACP 使用自己的已登录数据目录，由 agy 自行处理认证；不复制令牌、不自动授权工具。
    acp = start("agy", [binary / "agent-pulse-bridge", "run-adapter", "--config", root / "bridge.yaml",
                        "--adapter", "antigravity", "--", "acp", "--stdio", "--process-per-session",
                        "--disable-telemetry"], rpc=True)
    reader_thread = threading.Thread(target=reader, args=(acp,), daemon=True)
    reader_thread.start()
    init = request("initialize", {"protocolVersion": 1, "clientCapabilities": {}}, 1)
    if options.resume and not init.get("agentCapabilities", {}).get("loadSession"):
        raise RuntimeError("当前 ACP 宿主未声明 loadSession 能力，不能验证跨进程续接")
    session = request("session/new", {"cwd": str(root), "mcpServers": []}, 2)
    session_id = session["sessionId"]
    result = request("session/prompt", {"sessionId": session_id, "prompt": [
        {"type": "text", "text": "这是通知集成测试。只回复：agy ACP 测试完成。不要读取文件，不要使用工具或子代理，不要修改文件。"}]}, 3)
    print(json.dumps({"agent": init.get("agentInfo"), "session_id": session_id,
                      "stop_reason": result.get("stopReason"), "updates": sorted(set(str(x) for x in updates))}, ensure_ascii=False), flush=True)
    history = wait(lambda: (lambda items: items if any(e["kind"] == "turn.completed" for e in items) else None)(get("/api/v1/events")))
    if {e["session_id"] for e in history} != {session_id}:
        raise RuntimeError("原生 hook 与 ACP 主会话 ID 不一致")
    assert result.get("stopReason") == "end_turn", "ACP 未正常结束"
    assert {e["kind"] for e in history} >= {"session.started", "activity.observed", "turn.completed"}
    assert len(get("/api/v1/snapshot")["unread"]) == 1, "完成提醒未进入未读列表"
    if options.resume:
        os.killpg(acp.pid, signal.SIGTERM)
        acp.wait(timeout=10)
        reader_thread.join(timeout=5)
        if reader_thread.is_alive():
            raise RuntimeError("旧 ACP 输出尚未关闭")
        while not messages.empty():
            messages.get_nowait()
        acp = start("agy-resumed", [binary / "agent-pulse-bridge", "run-adapter", "--config", root / "bridge.yaml",
                                  "--adapter", "antigravity", "--", "--pulse-main-session", session_id,
                                  "acp", "--stdio", "--process-per-session",
                                  "--disable-telemetry"], rpc=True)
        threading.Thread(target=reader, args=(acp,), daemon=True).start()
        request("initialize", {"protocolVersion": 1, "clientCapabilities": {}}, 4)
        request("session/load", {"sessionId": session_id, "cwd": str(root), "mcpServers": []}, 5)
        resumed = request("session/prompt", {"sessionId": session_id, "prompt": [
            {"type": "text", "text": "这是原会话的续接测试。只回复：续接测试完成。不要使用工具或子代理，不要读取或修改文件。"}]}, 6)
        assert resumed.get("stopReason") == "end_turn", "续接轮次未正常结束"
        history = wait(lambda: (lambda items: items if sum(e["kind"] == "turn.completed" for e in items) == 2 else None)(get("/api/v1/events")))
        assert {e["session_id"] for e in history} == {session_id}, "续接改变主会话归属"
        snapshot = get("/api/v1/snapshot")
        assert len(snapshot["sessions"]) == 1 and snapshot["sessions"][0]["state"] == "idle", "续接后主会话状态错误"
        assert sum(e["valid"] for e in snapshot["unread"]) == 1, "旧完成提醒未失效或新完成提醒重复"
    runtime = json.loads((root / "adapters/runtime/antigravity.json").read_text())
    assert runtime["version"] == json.loads((project / "adapters/antigravity/manifest.json").read_text())["version"]
    evidence = {"agent": init.get("agentInfo"), "stop_reason": result.get("stopReason"), "resumed": options.resume,
                "session_id": session_id, "history": history, "runtime": runtime,
                "updates": sorted(set(str(x) for x in updates)), "directory": str(root)}
    (root / "evidence.json").write_text(json.dumps(evidence, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(evidence, ensure_ascii=False, indent=2))
finally:
    for proc in reversed(processes):
        if proc.poll() is None:
            os.killpg(proc.pid, signal.SIGTERM)
    for proc in reversed(processes):
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            os.killpg(proc.pid, signal.SIGKILL)
            proc.wait()
    for handle in logs:
        handle.close()
    print("测试目录：", root)
