"""经 Hub 向显式配置的渠道发送一次测试；密钥只驻留内存。"""
import argparse
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--send", action="store_true", help="明确执行一次真实手机推送")
    parser.add_argument("--script", type=Path, help="可选：从指定通知脚本读取已有渠道字面量")
    args = parser.parse_args()
    if not args.send:
        parser.error("真实发送需要 --send；常规 CI 不执行此脚本")
    # 只识别已知字面量，不 source 或执行外部脚本，也不输出包含密钥的 URL。
    url = os.environ.get("CODEX_NOTIFY_API_URL")
    if not url and args.script is not None:
        match = re.search(r'API_URL="\$\{CODEX_NOTIFY_API_URL:-([^}\s]+)\}"', args.script.read_text())
        url = match.group(1) if match else None
    if not url or not url.startswith("https://"):
        parser.error("请通过 CODEX_NOTIFY_API_URL 或显式 --script 提供 HTTPS 推送渠道")
    evidence = {"external_requests": 0, "transport_status": None, "business_fields": {}}

    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args, **kwargs):
            return None

    class ForwardOnce(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_POST(self):
            if evidence["external_requests"]:
                self.send_error(400)
                return
            payload = self.rfile.read(int(self.headers.get("Content-Length", "0")))
            fields = json.loads(payload)
            assert set(fields) == {"title", "msg"}, "渠道参数不匹配"
            evidence["external_requests"] += 1
            req = urllib.request.Request(url, data=payload, headers={"Content-Type": "application/json"})
            try:
                try:
                    response = urllib.request.build_opener(NoRedirect).open(req, timeout=15)
                except urllib.error.HTTPError as error:
                    response = error
                with response:
                    evidence["transport_status"] = response.status
                    raw = response.read(65536)
                try:
                    result = json.loads(raw)
                    if isinstance(result, dict):
                        # 只输出数值/布尔业务码，不保留原始响应或任意字符串。
                        evidence["business_fields"] = {k: v for k, v in result.items()
                            if k in ("code", "errcode", "success", "status") and isinstance(v, (int, bool))}
                        evidence["response_mentions_success"] = any(
                            isinstance(v, str) and ("成功" in v or v.lower() == "success") for v in result.values())
                except ValueError:
                    evidence["response_format"] = "非 JSON"
                self.send_response(evidence["transport_status"])
                self.end_headers()
            except (OSError, ValueError):
                # 未知网络结果不重复调用真实渠道。
                self.close_connection = True

    server = ThreadingHTTPServer(("127.0.0.1", 0), ForwardOnce)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        hub_port = sock.getsockname()[1]
    local = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def api(path, data=None):
        req = urllib.request.Request(f"http://127.0.0.1:{hub_port}" + path,
            data=json.dumps(data).encode() if data is not None else None, headers={"Content-Type": "application/json"})
        with local.open(req, timeout=3) as response:
            return json.load(response)

    with tempfile.TemporaryDirectory(prefix="agent-pulse-phone-") as directory:
        root = Path(directory)
        configuration = {"listen": f"127.0.0.1:{hub_port}", "database": str(root / "hub.db"), "push": [{
            "name": "existing-phone", "method": "POST", "url": "${env:AGENT_PULSE_PHONE_TEST_URL}",
            "format": "json", "fields": {"title": "${title}", "msg": "${body}"}, "timeout_seconds": 20}]}
        (root / "hub.yaml").write_text(json.dumps(configuration))
        env = dict(os.environ, AGENT_PULSE_PHONE_TEST_URL=f"http://127.0.0.1:{server.server_port}/push")
        with (root / "hub.log").open("w") as log:
            proc = subprocess.Popen([str(Path(__file__).resolve().parents[2] / "bin/agent-pulse-hub"),
                "--config", str(root / "hub.yaml")], env=env, stdout=log, stderr=log)
            try:
                for _ in range(50):
                    try:
                        api("/healthz")
                        break
                    except OSError:
                        time.sleep(.1)
                stamp = datetime.now(timezone.utc)
                ident = "phone-test-" + stamp.strftime("%Y%m%dT%H%M%SZ")
                event = {"event_id": ident, "source_id": "phone-test", "agent": "test", "session_id": ident,
                    "scope": "main", "kind": "turn.completed", "occurred_at": stamp.isoformat(),
                    "title": "AgentPulse 手机推送测试", "body": "项目：agent-pulse\nHub 真实渠道测试完成。"}
                api("/api/v1/events", event)
                deadline = time.monotonic() + 65
                while time.monotonic() < deadline:
                    jobs = api("/api/v1/push-jobs")
                    if jobs and jobs[0]["status"] in ("sent", "failed", "unknown"):
                        evidence["hub_job"] = jobs[0]
                        break
                    time.sleep(.5)
                evidence["tested_at"] = stamp.isoformat()
                print(json.dumps(evidence, ensure_ascii=False, indent=2))
                assert evidence.get("hub_job", {}).get("status") == "sent", "真实渠道未返回 HTTP 成功"
                assert evidence["external_requests"] == 1
            finally:
                proc.terminate()
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()
                server.shutdown()
                server.server_close()


if __name__ == "__main__":
    main()
