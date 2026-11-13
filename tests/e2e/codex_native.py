"""用本地 Responses 模拟服务验证 Codex 原生 hook，不调用在线模型。通过 uv run 执行。"""
import http.server
import json
import shutil
import socket
from pathlib import Path
import shlex
import subprocess
import tempfile
import threading
import time
import urllib.request

project = Path(__file__).resolve().parents[2]
binary = project / "bin"
root = Path(tempfile.mkdtemp(prefix="agent-pulse-codex-"))
capture = root / "hooks.jsonl"
(root / "capture.py").write_text('''import sys,json,pathlib
data=json.load(sys.stdin)
keep={k:data[k] for k in ["session_id","hook_event_name","agent_id","agent_type","turn_id","source"] if k in data}
with pathlib.Path(sys.argv[1]).open("a") as f:f.write(json.dumps(keep)+"\\n")
print("{}")
''')


requests_received = []


class Mock(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        requests_received.append(self.path)
        self.rfile.read(int(self.headers.get("content-length", "0")))
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Connection", "close")
        self.end_headers()
        part = {"type": "output_text", "text": "本地测试完成。", "annotations": []}
        item = {"type": "message", "id": "msg_test", "role": "assistant", "status": "completed", "content": [part]}
        response = {"id": "resp_test", "object": "response", "status": "completed", "model": "pulse-test", "output": [item], "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}
        events = [
            {"type": "response.created", "response": {**response, "status": "in_progress", "output": []}},
            {"type": "response.output_item.added", "output_index": 0, "item": {**item, "status": "in_progress", "content": []}},
            {"type": "response.content_part.added", "output_index": 0, "content_index": 0, "item_id": "msg_test", "part": {**part, "text": ""}},
            {"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "item_id": "msg_test", "delta": part["text"]},
            {"type": "response.output_item.done", "output_index": 0, "item": item},
            {"type": "response.completed", "response": response},
        ]
        for event in events:
            self.wfile.write(("event: " + event["type"] + "\ndata: " + json.dumps(event) + "\n\n").encode())
        self.wfile.flush()


server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Mock)
threading.Thread(target=server.serve_forever, daemon=True).start()
command = "uv run --offline --no-project python " + shlex.quote(str(root / "capture.py")) + " " + shlex.quote(str(capture))
config = {
    "model_provider": '"pulse_mock"', "model": '"pulse-test"',
    "model_providers.pulse_mock": '{name="本地测试", base_url="http://127.0.0.1:' + str(server.server_port) + '/v1", wire_api="responses", requires_openai_auth=false, request_max_retries=0, stream_max_retries=0}',
    "check_for_update_on_startup": "false", "history.persistence": '"none"',
    "sqlite_home": json.dumps(str(root / "db")),
}
(root / ".codex").mkdir()
(root / ".codex/config.toml").write_text("check_for_update_on_startup = false\n")
(root / ".codex/hooks.json").write_text(json.dumps({"hooks":{event:[{"hooks":[{"type":"command","command":command,"timeout":2}]}] for event in ["SessionStart","UserPromptSubmit","Stop","SessionEnd"]}}))
config['projects'] = '{' + json.dumps(str(root)) + '={trust_level="trusted"}}'
config['project_root_markers'] = '[".codex"]'
args = ["codex"]
for key, value in config.items():
    args += ["-c", key + "=" + value]
args += ["exec", "--dangerously-bypass-hook-trust", "--skip-git-repo-check", "--ephemeral", "--ignore-user-config", "--json", "返回测试文本，不使用工具。"]
log = (root / "stderr.log").open("w")
output = (root / "exec.jsonl").open("w")
processes = []
service_logs = []


def request(url):
    with urllib.request.urlopen(url, timeout=2) as response:
        return json.load(response)


def wait(check):
    until = time.monotonic() + 15
    while time.monotonic() < until:
        try:
            value = check()
            if value:
                return value
        except (OSError, ValueError):
            pass
        time.sleep(.2)
    raise RuntimeError("服务等待超时，日志目录：" + str(root))


def start(name, args):
    handle = (root / (name + ".log")).open("w")
    service_logs.append(handle)
    processes.append(subprocess.Popen([str(a) for a in args], cwd=root, stdout=handle, stderr=handle))


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


try:
    repo = root / "repository"
    shutil.copytree(project / "dist/repository", repo)
    for command_args in [
        ["keygen", "--private", root / "publisher.key", "--public", root / "publisher.pub"],
        ["publish", "--repository", repo, "--private", root / "publisher.key", "--index", repo / "index-input.json"],
    ]:
        subprocess.run([str(binary / "agent-pulse-adapters"), *map(str, command_args)], check=True)
    # 新 Hub 从发布仓库读取公钥；测试仓库也必须携带同一次签名的公钥。
    shutil.copy2(root / "publisher.pub", repo / "publisher.pub")
    hub_port, bridge_port = port(), port()
    hub_url = "http://127.0.0.1:" + str(hub_port)
    (root / "hub.yaml").write_text(f'listen: "127.0.0.1:{hub_port}"\ndatabase: "{root}/hub.db"\nadapter_directory: "{repo}"\n')
    (root / "bridge.yaml").write_text(f'''listen: "127.0.0.1:{bridge_port}"
source_id: "native-codex-test"
hub_url: "{hub_url}"
adapters:
  directory: "{root}/adapters"
  public_key: "{(root / 'publisher.pub').read_text().strip()}"
  poll_seconds: 5
  managed:
    codex: "{root}/.codex/hooks.json"
''')
    start("hub", [binary / "agent-pulse-hub", "--config", root / "hub.yaml"])
    wait(lambda: request(hub_url + "/healthz"))
    start("bridge", [binary / "agent-pulse-bridge", "serve", "--config", root / "bridge.yaml"])
    wait(lambda: " dispatch " in (root / ".codex/hooks.json").read_text())
    # --ignore-user-config 不等于项目可信；projects 使用 TOML 内联表避免带引号的点分键被误解析。
    result = subprocess.run(args,cwd=root,stdin=subprocess.DEVNULL,stdout=output,stderr=log,timeout=35)
    output.flush()
    messages = [json.loads(line) for line in (root / "exec.jsonl").read_text().splitlines() if line.startswith("{")]
    events = [json.loads(line) for line in capture.read_text().splitlines()] if capture.exists() else []
    finished = result.returncode==0 and any(message.get("type")=="turn.completed" for message in messages)
    history = wait(lambda: (lambda items: items if {"session.started", "session.resumed", "turn.completed", "session.closed"}.issubset({item["kind"] for item in items}) else None)(request(hub_url + "/api/v1/events")))
    runtime = json.loads((root / "adapters/runtime/codex.json").read_text())
    evidence = {"finished":finished,"model":"本地 Responses 模拟，不调用在线服务", "local_requests":requests_received,"hooks":events,"history":history,"adapter_runtime":runtime,"directory":str(root)}
    (root / "evidence.json").write_text(json.dumps(evidence,ensure_ascii=False,indent=2))
    print(json.dumps(evidence,ensure_ascii=False,indent=2))
    if not finished or not {"SessionStart","Stop"}.issubset({e["hook_event_name"] for e in events}):
        raise RuntimeError("未取得原生启动与完成 hook 证据")
finally:
    for proc in reversed(processes):
        proc.terminate()
    for proc in reversed(processes):
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
    for handle in service_logs:
        handle.close()
    server.shutdown()
    output.close()
    log.close()
