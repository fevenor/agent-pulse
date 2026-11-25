"""用本地 Responses 模拟服务验证 Codex 原生 hook，不调用在线模型。通过 uv run 执行。"""
import http.server
import json
import os
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
binary = project / ("bin/dev" if os.name == "nt" else "bin")

def tool(name):
    return binary / (name + (".exe" if os.name == "nt" else ""))
(project / "tmp").mkdir(exist_ok=True)
root = Path(tempfile.mkdtemp(prefix="codex-native-", dir=project / "tmp"))
capture = root / "hooks.jsonl"
(root / "capture.py").write_text('''import sys,json,pathlib
data=json.load(sys.stdin)
keep={k:data[k] for k in ["session_id","hook_event_name","agent_id","agent_type","turn_id","source","tool_name"] if k in data}
with pathlib.Path(sys.argv[1]).open("a") as f:f.write(json.dumps(keep)+"\\n")
print("{}")
''')


requests_received = []
scenario = "success"
scenario_attempts = 0


class Mock(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        global scenario_attempts
        scenario_attempts += 1
        requests_received.append(self.path)
        request_body = json.loads(self.rfile.read(int(self.headers.get("content-length", "0"))))
        tool_first = scenario.startswith("tool_") and scenario_attempts == 1
        if scenario in ["fatal", "rate_limit", "tool_fatal", "tool_rate_limit"] and not tool_first:
            self.send_response(429 if "rate_limit" in scenario else 400)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"error":{"message":"隔离模型最终报错", "type":"rate_limit_error" if "rate_limit" in scenario else "invalid_request_error"}}).encode())
            return
        if scenario in ["retry", "tool_retry"] and scenario_attempts == (2 if scenario.startswith("tool_") else 1):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(b'event: response.created\ndata: {"type":"response.created","response":{"id":"failed-attempt"}}\n\n')
            self.wfile.flush()
            return
        # 隔离名称索引替身：真实 Stop hook 读取新名称，不声称执行了交互 /rename。
        hooks = [json.loads(line) for line in capture.read_text().splitlines()] if capture.exists() else []
        if hooks:
            session_id = hooks[0]["session_id"]
            rows = [{"id": session_id, "thread_name": title} for title in ["本地原名称", "本地更新后的会话名称"]]
            (root / ".codex/session_index.jsonl").write_text("".join(json.dumps(row, ensure_ascii=False) + "\n" for row in rows))
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
        if tool_first:
            # 让真实执行器执行无副作用的本地命令，再由下一次模型请求触发错误或重试。
            names = {tool.get("name") for tool in request_body.get("tools", [])}
            if "exec_command" in names:
                name, arguments = "exec_command", {"cmd":"printf agent-pulse-tool-probe", "max_output_tokens":64}
            elif "shell_command" in names:
                name, arguments = "shell_command", {"command":"printf agent-pulse-tool-probe", "timeout_ms":1000}
            else:
                raise RuntimeError("隔离模型请求未提供受支持的命令工具：" + str(sorted(name for name in names if name)))
            if os.name == "nt":
                arguments["cmd" if name == "exec_command" else "command"] = "Write-Output agent-pulse-tool-probe"
            item = {"type":"function_call", "id":"fc_probe", "call_id":"call_probe", "name":name, "arguments":json.dumps(arguments)}
            response["output"] = [item]
            events = [
                {"type":"response.created", "response":{**response,"status":"in_progress","output":[]}},
                {"type":"response.output_item.added", "output_index":0, "item":{**item,"arguments":""}},
                {"type":"response.function_call_arguments.delta", "output_index":0, "item_id":item["id"], "delta":item["arguments"]},
                {"type":"response.output_item.done", "output_index":0, "item":item},
                {"type":"response.completed", "response":response},
            ]
        for event in events:
            self.wfile.write(("event: " + event["type"] + "\ndata: " + json.dumps(event) + "\n\n").encode())
        self.wfile.flush()


server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Mock)
threading.Thread(target=server.serve_forever, daemon=True).start()
command = "uv run --offline --no-project python " + (subprocess.list2cmdline([str(root / "capture.py"),str(capture)]) if os.name == "nt" else shlex.quote(str(root / "capture.py")) + " " + shlex.quote(str(capture)))
config = {
    "model_provider": '"pulse_mock"', "model": '"pulse-test"',
    "model_providers.pulse_mock": '{name="本地测试", base_url="http://127.0.0.1:' + str(server.server_port) + '/v1", wire_api="responses", requires_openai_auth=false, request_max_retries=0, stream_max_retries=1}',
    "check_for_update_on_startup": "false", "history.persistence": '"none"',
    "sqlite_home": json.dumps(str(root / "db")),
}
(root / ".codex").mkdir()
(root / ".codex/config.toml").write_text("check_for_update_on_startup = false\n")
(root / ".codex/hooks.json").write_text(json.dumps({"hooks":{event:[{"hooks":[{"type":"command","command":command,"timeout":2}]}] for event in ["SessionStart","UserPromptSubmit","PostToolUse","Stop","SessionEnd"]}}))
config['projects'] = '{' + json.dumps(str(root)) + '={trust_level="trusted"}}'
config['project_root_markers'] = '[".codex"]'
args = [os.environ.get("AGENT_PULSE_CODEX_BINARY", "codex")]
for key, value in config.items():
    args += ["-c", key + "=" + value]
args += ["exec", "--dangerously-bypass-hook-trust", "--skip-git-repo-check", "--ignore-user-config", "--json", "返回测试文本，不使用工具。"]
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
    processes.append(subprocess.Popen([str(a) for a in args], cwd=root, env={**os.environ, "CODEX_HOME":str(root / ".codex"), "CODEX_SQLITE_HOME":str(root / "db")}, stdout=handle, stderr=handle))


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


try:
    repo = root / "repository"
    repo.mkdir()
    package_source = root / "adapter-source"
    package_source.mkdir()
    entry = "adapter.exe" if os.name == "nt" else "adapter"
    shutil.copy2(tool("agent-pulse-adapter-codex"), package_source / entry)
    manifest = json.loads((project / "adapters/codex/manifest.json").read_text(encoding="utf-8"))
    manifest.update(platform="windows/amd64" if os.name == "nt" else "linux/amd64", entry=entry, control=entry)
    (root / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    subprocess.run([str(tool("agent-pulse-adapters")), "pack", "--source", str(package_source), "--manifest", str(root / "manifest.json"), "--repository", str(repo), "--out", str(repo / "codex.json")], check=True)
    (repo / "index-input.json").write_text(json.dumps({"sequence":1,"packages":[json.loads((repo / "codex.json").read_text())]}))
    for command_args in [
        ["keygen", "--private", root / "publisher.key", "--public", root / "publisher.pub"],
        ["publish", "--repository", repo, "--private", root / "publisher.key", "--index", repo / "index-input.json"],
    ]:
        subprocess.run([str(tool("agent-pulse-adapters")), *map(str, command_args)], check=True)
    # 新 Hub 从发布仓库读取公钥；测试仓库也必须携带同一次签名的公钥。
    shutil.copy2(root / "publisher.pub", repo / "publisher.pub")
    hub_port, bridge_port = port(), port()
    hub_url = "http://127.0.0.1:" + str(hub_port)
    (root / "hub.yaml").write_text(f'listen: "127.0.0.1:{hub_port}"\ndatabase: "{root.as_posix()}/hub.db"\nadapter_directory: "{repo.as_posix()}"\n')
    ipc = "" if os.name == "nt" else "unix:" + root.as_posix() + "/bridge.sock"
    (root / "bridge.yaml").write_text(f'''listen: "{ipc}"
source_id: "native-codex-test-{root.name}"
hub_url: "{hub_url}"
adapters:
  directory: "{root.as_posix()}/adapters"
  public_key: "{(root / 'publisher.pub').read_text().strip()}"
  managed:
    codex: "{root.as_posix()}/.codex/hooks.json"
''')
    start("hub", [tool("agent-pulse-hub"), "--config", root / "hub.yaml"])
    wait(lambda: request(hub_url + "/healthz"))
    start("bridge", [tool("agent-pulse-bridge"), "serve", "--config", root / "bridge.yaml"])
    wait(lambda: (lambda state: state.get("installed", {}).get("codex", {}).get("package", {}).get("version") == json.loads((project / "adapters/codex/manifest.json").read_text(encoding="utf-8"))["version"])(json.loads((root / "adapters/state.json").read_text())))
    # --ignore-user-config 不等于项目可信；projects 使用 TOML 内联表避免带引号的点分键被误解析。
    result = subprocess.run(args,cwd=root,env={**os.environ,"CODEX_HOME":str(root / ".codex"),"CODEX_SQLITE_HOME":str(root / "db")},stdin=subprocess.DEVNULL,stdout=output,stderr=log,timeout=35)
    output.flush()
    messages = [json.loads(line) for line in (root / "exec.jsonl").read_text().splitlines() if line.startswith("{")]
    events = [json.loads(line) for line in capture.read_text().splitlines()] if capture.exists() else []
    finished = result.returncode==0 and any(message.get("type")=="turn.completed" for message in messages)
    history = wait(lambda: (lambda items: items if {"session.started", "session.resumed", "turn.completed", "session.closed"}.issubset({item["kind"] for item in items}) else None)(request(hub_url + "/api/v1/events")))
    runtime = json.loads((root / "adapters/runtime/codex.json").read_text())
    completed = next(item for item in history if item["kind"] == "turn.completed")
    if completed.get("session_title") != "本地更新后的会话名称":
        raise RuntimeError("原生最终结束事件没有读取测试索引中的最新名称")
    snapshot = request(hub_url + "/api/v1/snapshot")
    if len(snapshot["sessions"]) != 1 or snapshot["sessions"][0]["title"] != completed["session_title"]:
        raise RuntimeError("名称更新必须定位同一后台会话")
    final_cases = []
    for mode, expected in [("fatal", "run.failed"), ("rate_limit", "run.failed"), ("tool_rate_limit", "run.failed"), ("tool_fatal", "run.failed"), ("retry", "turn.completed"), ("tool_retry", "turn.completed")]:
        scenario, scenario_attempts = mode, 0
        before_ids = {item["event_id"] for item in request(hub_url + "/api/v1/events")}
        case_log = (root / (mode + ".log")).open("w")
        result = subprocess.run(args, cwd=root, env={**os.environ,"CODEX_HOME":str(root / ".codex"),"CODEX_SQLITE_HOME":str(root / "db")}, stdin=subprocess.DEVNULL, stdout=case_log, stderr=case_log, timeout=40)
        case_log.close()
        current = wait(lambda: (lambda items: items if any(item["kind"] == expected and item["event_id"] not in before_ids for item in items) else None)(request(hub_url + "/api/v1/events")))
        endings = [item for item in current if item["event_id"] not in before_ids and item["kind"] in ["run.failed", "turn.completed"]]
        if len(endings) != 1 or endings[0]["kind"] != expected:
            raise RuntimeError("模型请求失败或重复终态被采集：" + mode)
        if not endings[0].get("valid") or endings[0].get("read"):
            raise RuntimeError("最终结果未形成有效未读提醒：" + mode)
        if expected == "run.failed" and result.returncode == 0:
            raise RuntimeError("没有取得宿主真实失败结束")
        if mode.endswith("retry") and (result.returncode != 0 or scenario_attempts < (3 if mode.startswith("tool_") else 2)):
            raise RuntimeError("没有取得失败后重试成功")
        current_hooks = [json.loads(line) for line in capture.read_text().splitlines()]
        current_hooks = [hook for hook in current_hooks if hook["session_id"] == endings[0]["session_id"]]
        if mode.startswith("tool_") and not any(hook["hook_event_name"] == "PostToolUse" and hook.get("turn_id") for hook in current_hooks):
            raise RuntimeError("未取得带轮次的原生工具活动：" + mode)
        if expected == "run.failed" and "rate_limit" in mode and "429" not in endings[0].get("body", ""):
            raise RuntimeError("最终错误未携带429可见原因")
        final_cases.append({"case":mode,"final_kind":expected,"final_events":len(endings),"requests":scenario_attempts})
    snapshot = request(hub_url + "/api/v1/snapshot")
    if any(session["state"] == "running" for session in snapshot["sessions"]):
        raise RuntimeError("整轮结束后仍有运行任务")
    history = request(hub_url + "/api/v1/events")
    evidence = {"finished":finished,"final_snapshot":snapshot,"model":"本地 Responses 模拟，不调用在线服务", "title_source":"隔离索引替身，真实 hook 读取；不是交互 /rename 验收", "final_cases":final_cases,"local_requests":requests_received,"hooks":events,"history":history,"adapter_runtime":runtime,"directory":str(root)}
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
