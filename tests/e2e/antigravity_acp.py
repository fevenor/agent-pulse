"""验证真实 agy 直接启动与 ACP 过滤；默认不调用模型，显式授权模型后仅执行一次简短普通对话。"""
import json
import argparse
import queue
import shutil
import socket
import subprocess
import tempfile
import threading
import time
from datetime import datetime, timezone
from pathlib import Path
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.request import Request, urlopen

project = Path(__file__).resolve().parents[2]
binary = project / "bin"
root = Path(tempfile.mkdtemp(prefix="agent-pulse-agy-direct-"))
processes, handles = [], []
proxy = None
ingress = []
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--model", help="仅在用户授权后指定普通 CLI 测试模型；会发起一条短请求")
parser.add_argument("--workspace", type=Path, help="已由宿主正常确认可信的专用测试工作区")
args = parser.parse_args()
workspace = args.workspace.resolve() if args.workspace else root
hook_path = workspace / ".agents/hooks.json"
hook_before = hook_path.read_bytes() if hook_path.exists() else None
hook_installed = None


def port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def start(name, args, **kwargs):
    log = (root / (name + ".log")).open("w")
    handles.append(log)
    workdir = kwargs.pop("workdir", root)
    p = subprocess.Popen(list(map(str, args)), cwd=workdir, stdout=log, stderr=log, **kwargs)
    processes.append(p)
    return p


def wait(check, seconds=30):
    until = time.monotonic() + seconds
    while time.monotonic() < until:
        try:
            result = check()
            if result:
                return result
        except (OSError, ValueError):
            pass
        time.sleep(.2)
    raise RuntimeError("隔离测试未取得所需证据；诊断目录：" + str(root))


hub_url = "http://127.0.0.1:" + str(port())
bridge_port = port()
get = lambda path: json.load(urlopen(hub_url + path, timeout=3))


class ReceiptProxy(BaseHTTPRequestHandler):
    # 仅为隔离测试旁观本机接收回执；不生成事件，不改变 PID、载荷或响应。
    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        with urlopen(Request(f"http://127.0.0.1:{bridge_port}" + self.path,
                             data=raw, headers={"Content-Type": "application/json"}), timeout=5) as response:
            reply = response.read()
            status = response.status
        payload, receipt = json.loads(raw), json.loads(reply)
        ingress.append({"session_id": payload.get("session_id"), "kind": payload.get("kind"),
                        "process_id": payload.get("process_id"), "status": status,
                        "accepted": receipt.get("accepted")})
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(reply)

    def log_message(self, *_args):
        pass


try:
    repo = root / "repository"
    shutil.copytree(project / "dist/repository", repo)
    for publisher_args in [["keygen", "--private", root / "publisher.key", "--public", root / "publisher.pub"],
                 ["publish", "--repository", repo, "--private", root / "publisher.key", "--index", repo / "index-input.json"]]:
        subprocess.run([str(binary / "agent-pulse-adapters"), *map(str, publisher_args)], check=True, stdout=subprocess.DEVNULL)
    shutil.copy2(root / "publisher.pub", repo / "publisher.pub")
    (root / "hub.yaml").write_text(f'listen: "127.0.0.1:{hub_url.rsplit(":",1)[1]}"\ndatabase: "{root}/hub.db"\nadapter_directory: "{repo}"\n')
    (root / "bridge.yaml").write_text(f'''listen: "127.0.0.1:{bridge_port}"
source_id: "isolated-agy-direct"
hub_url: "{hub_url}"
adapters:
  directory: "{root}/adapters"
  public_key: "{(root / 'publisher.pub').read_text().strip()}"
  poll_seconds: 3600
  managed:
    antigravity: "{hook_path}"
''')
    start("hub", [binary / "agent-pulse-hub", "--config", root / "hub.yaml"])
    wait(lambda: get("/healthz"))
    start("bridge", [binary / "agent-pulse-bridge", "serve", "--config", root / "bridge.yaml"])
    wait(lambda: hook_path.exists() and str(root / "bridge.yaml") in hook_path.read_text() and "dispatch" in hook_path.read_text())
    hook_installed = hook_path.read_bytes()
    wait(lambda: json.load(urlopen(f"http://127.0.0.1:{bridge_port}/healthz", timeout=3)))
    # 服务已加载真实监听地址；仅将随后 hook 读取的临时配置指向回执旁观器。
    # 配置和 hook 命令仍由真实 Bridge 生成，不增加 Agent 的启动包装或主会话绑定。
    proxy = ThreadingHTTPServer(("127.0.0.1", 0), ReceiptProxy)
    threading.Thread(target=proxy.serve_forever, daemon=True).start()
    bridge_config = root / "bridge.yaml"
    bridge_config.write_text(bridge_config.read_text().replace(
        f'listen: "127.0.0.1:{bridge_port}"', f'listen: "127.0.0.1:{proxy.server_port}"', 1))
    runtime = root / "adapters/runtime/antigravity.json"
    ordinary_events = []
    ordinary = "本次未指定 --model，未执行普通 CLI 模型任务"
    if args.model:
        # 只有显式指定已授权模型才执行一次真实普通任务；保持原生 agy 启动，不包装。
        task = start("direct-task", ["agy", "--model", args.model, "--print", "只回复：测试完成。不要调用工具。"], stdin=subprocess.DEVNULL, workdir=workspace)
        task.wait(timeout=60)
        if task.returncode != 0:
            raise RuntimeError("普通 CLI 任务失败，诊断目录：" + str(root))
        wait(lambda: any(e["agent"] == "antigravity" and e["kind"] == "turn.completed" for e in get("/api/v1/events")))
        ordinary_events = [e for e in get("/api/v1/events") if e["agent"] == "antigravity"]
        expected_version = json.loads((project / "adapters/antigravity/manifest.json").read_text())["version"]
        wait(lambda: runtime.exists() and json.loads(runtime.read_text())["version"] == expected_version)
        ordinary = "真实普通 CLI 任务及完成 hook 通过"
    runtime_before_acp = runtime.read_bytes() if runtime.exists() else None
    acp_ingress_start = len(ingress)
    # ACP 创建专用空会话，只验证元数据和过滤，不发送 session/prompt。
    acp_log = (root / "acp.log").open("w"); handles.append(acp_log)
    acp = subprocess.Popen(["agy", "acp", "--stdio", "--process-per-session", "--disable-telemetry"], cwd=workspace, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=acp_log, text=True)
    processes.append(acp)
    replies = queue.Queue()
    def read_rpc():
        for line in acp.stdout:
            try: replies.put(json.loads(line))
            except ValueError: pass
    threading.Thread(target=read_rpc, daemon=True).start()
    def rpc(ident, method, params):
        acp.stdin.write(json.dumps({"jsonrpc":"2.0","id":ident,"method":method,"params":params}) + "\n"); acp.stdin.flush()
        until = time.monotonic() + 30
        while time.monotonic() < until:
            response = replies.get(timeout=max(.1, until-time.monotonic()))
            if response.get("id") == ident:
                if "error" in response: raise RuntimeError("ACP 元数据调用失败，错误码：" + str(response["error"].get("code")))
                return response.get("result", {})
        raise RuntimeError("ACP 元数据超时")
    rpc(1, "initialize", {"protocolVersion":1,"clientCapabilities":{}})
    created = rpc(2, "session/new", {"cwd":str(workspace),"mcpServers":[]})
    acp_id = created["sessionId"]
    # 空会话可能尚未触发原生 hook。独立的合成探针只核验实际 ACP 进程的本机识别，
    # 不把它当作原生事件采集，也不以它替代真实模型测试。
    probe = {"event_id": "isolated-acp-process-probe", "source_id": "isolated-agy-direct",
             "agent": "antigravity", "session_id": "isolated-acp-process-probe",
             "kind": "session.started", "occurred_at": datetime.now(timezone.utc).isoformat(),
             "scope": "main", "title": "仅本机进程识别探针", "process_id": acp.pid}
    with urlopen(Request(f"http://127.0.0.1:{proxy.server_port}/api/v1/events", data=json.dumps(probe).encode(),
                         headers={"Content-Type": "application/json"}), timeout=5) as response:
        boundary_filtered = response.status == 202 and json.load(response).get("accepted") == "filtered"
    if not boundary_filtered:
        raise RuntimeError("真实 ACP 进程的本机识别失败；合成探针不应进入 Hub")
    time.sleep(2)
    assert all(e["session_id"] != acp_id for e in get("/api/v1/events")), "ACP 事件不应进入 Hub"
    acp_receipts = [r for r in ingress[acp_ingress_start:] if r["session_id"] == acp_id]
    filtered = any(r["accepted"] == "filtered" and r["status"] == 202 for r in acp_receipts)
    if any(r["accepted"] != "filtered" for r in acp_receipts):
        raise RuntimeError("ACP 原生事件未在 Bridge 共用入口过滤")
    hook_observed = runtime.exists() and runtime.read_bytes() != runtime_before_acp
    acp_check = "ACP 原生 hook 获得 Bridge filtered 回执，且未进入 Hub" if filtered else (
        "ACP 新会话未进入 Hub；" + ("hook 运行已确认，但没有本机过滤回执，不能宣称共用入口过滤已实测"
                                      if hook_observed else "未取得 ACP hook 调用证据，不能宣称过滤已验收"))
    (root / "ordinary-events.json").write_text(json.dumps(ordinary_events, ensure_ascii=False, indent=2))
    (root / "ingress-receipts.json").write_text(json.dumps(ingress, ensure_ascii=False, indent=2))
    result = {"result": "通过" if args.model and filtered else "部分验证", "ordinary_cli":ordinary,
              "acp":acp_check,"acp_filtered_receipt":filtered,"acp_hook_observed":hook_observed,
              "acp_process_boundary_probe":boundary_filtered,
              "model_requests":1 if args.model else 0,"directory":str(root)}
    (root / "result.json").write_text(json.dumps(result, ensure_ascii=False, indent=2))
    print(json.dumps(result, ensure_ascii=False))
finally:
    for p in reversed(processes):
        if p.poll() is not None: continue
        p.terminate()
        try: p.wait(timeout=10)
        except subprocess.TimeoutExpired: p.kill(); p.wait()
    if proxy is not None:
        proxy.shutdown(); proxy.server_close()
    for h in handles: h.close()
    # 仅还原测试确实写入的专用项目钩子，保留任何外部并发修改。
    if hook_installed is not None and hook_path.exists() and hook_path.read_bytes() == hook_installed:
        if hook_before is None: hook_path.unlink()
        else: hook_path.write_bytes(hook_before)
