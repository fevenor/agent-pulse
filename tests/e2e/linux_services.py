"""串行验证 qoderclicn/DeepSeek-Flash → 独立适配包 → Bridge → Hub → 本地推送接收器。"""
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.request

project = Path(__file__).resolve().parents[2]
root = Path(tempfile.mkdtemp(prefix="agent-pulse-services-"))
workspace = Path(os.environ.get("AGENT_PULSE_QODER_WORKSPACE", str(root)))
settings = workspace / ".qoder/settings.json"
settings_before = settings.read_bytes() if settings.exists() else None
settings_test = None
binary = project / "bin"
processes = []
logs = []


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def request(base, path):
    with urllib.request.urlopen(base + path, timeout=2) as response:
        return json.load(response)


def wait(check, seconds=10):
    until = time.monotonic() + seconds
    while time.monotonic() < until:
        try:
            value = check()
            if value:
                return value
        except (OSError, ValueError, KeyError):
            pass
        time.sleep(.2)
    raise RuntimeError("等待超时，日志目录：" + str(root))


def run(args, **kwargs):
    return subprocess.run([str(a) for a in args], check=True, **kwargs)


def start(name, args):
    log = (root / (name + ".log")).open("w")
    logs.append(log)
    proc = subprocess.Popen([str(a) for a in args], stdout=log, stderr=log, cwd=root)
    processes.append(proc)
    return proc


hub_port, bridge_port, push_port = port(), port(), port()
hub_url = "http://127.0.0.1:" + str(hub_port)
push_url = "http://127.0.0.1:" + str(push_port)
try:
    repo = root / "repository"
    shutil.copytree(project / "dist/repository", repo)
    run([binary / "agent-pulse-adapters", "keygen", "--private", root / "publisher.key", "--public", root / "publisher.pub"])
    run([binary / "agent-pulse-adapters", "publish", "--repository", repo, "--private", root / "publisher.key", "--index", repo / "index-input.json"])
    # 公钥随测试仓库提供，不降低 Hub 的启动校验。
    shutil.copy2(root / "publisher.pub", repo / "publisher.pub")
    (root / "hub.yaml").write_text(f'''listen: "127.0.0.1:{hub_port}"
database: "{root}/hub.db"
adapter_directory: "{repo}"
push:
  - name: "local-phone-test"
    method: "POST"
    url: "{push_url}/push"
    format: "json"
    fields:
      title: "${{title}}"
      body: "${{body}}"
''')
    (root / "bridge.yaml").write_text(f'''listen: "127.0.0.1:{bridge_port}"
source_id: "native-qoder-test"
hub_url: "{hub_url}"
adapters:
  directory: "{root}/adapters"
  public_key: "{(root / 'publisher.pub').read_text().strip()}"
  poll_seconds: 5
  managed:
    qoderclicn: "{root}/qoder-settings.json"
''')
    start("phone", [binary / "agent-pulse-push-test", "--listen", f"127.0.0.1:{push_port}"])
    start("hub", [binary / "agent-pulse-hub", "--config", root / "hub.yaml"])
    wait(lambda: request(hub_url, "/healthz"))
    start("bridge", [binary / "agent-pulse-bridge", "serve", "--config", root / "bridge.yaml"])
    wait(lambda: (root / "qoder-settings.json").exists())
    # qoderclicn 的用户目录为 ~/.qoder-cn；此处只写临时工作区的项目配置。
    (workspace / ".qoder").mkdir(exist_ok=True)
    settings_test = (root / "qoder-settings.json").read_bytes()
    settings.write_bytes(settings_test)
    qoder_log = (root / "qoder.log").open("w")
    logs.append(qoder_log)
    run(["qoderclicn", "--model", "dfmodel", "--cwd", workspace, "--setting-sources", "project", "--strict-mcp-config", "--mcp-config", '{"mcpServers":{}}', "--no-session-persistence", "--tools", "", "--max-output-tokens", "64", "--max-model-request-retries", "0", "--print", "这是通知集成测试。请仅回复：测试完成。不要调用工具。"], stdout=qoder_log, stderr=qoder_log, timeout=55)
    qoder_log.flush()
    if "using \"auto\" instead" in (root / "qoder.log").read_text():
        raise RuntimeError("宿主回退到其他模型，本次不能记为 DeepSeek-Flash 验证")
    history = wait(lambda: (lambda events: events if any(e["kind"] == "turn.completed" for e in events) else None)(request(hub_url, "/api/v1/events")))
    pushed = wait(lambda: request(push_url, "/messages"), seconds=45)
    statuses = request(hub_url, "/api/v1/adapters/status")
    result = {"model": "DeepSeek-Flash", "source": "真实 qoderclicn hook", "phone": "本地 HTTP 接收器，未验证手机实收", "history": history, "pushes": pushed, "adapters": statuses, "directory": str(root)}
    (root / "evidence.json").write_text(json.dumps(result, ensure_ascii=False, indent=2))
    print(json.dumps(result, ensure_ascii=False, indent=2))
finally:
    for proc in reversed(processes):
        proc.terminate()
    for proc in reversed(processes):
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
    for log in logs:
        log.close()
    # 只撤回本测试的写入；测试期间外部修改过的内容必须保留。
    if settings_test is not None and settings.exists() and settings.read_bytes() == settings_test:
        if settings_before is None:
            settings.unlink()
        else:
            settings.write_bytes(settings_before)
    print("测试目录：", root)
