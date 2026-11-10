"""验证 Windows 与 Debian Bridge 向 Debian Docker Hub 提交时的来源隔离及已读。"""
from datetime import datetime, timezone
import json
import re
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request


ROOT = Path(__file__).resolve().parents[2]
sys.stdout.reconfigure(encoding="utf-8")


def wsl(*args, **kwargs):
    return subprocess.check_output(["wsl", "-d", "Debian", "--", *map(str, args)], **kwargs).decode().strip()


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait(check):
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        try:
            if check():
                return
        except (OSError, ValueError):
            pass
        time.sleep(0.2)
    raise RuntimeError("跨环境联调等待超时")


def main():
    name = "agent-pulse-wsl-" + secrets.token_hex(5)
    linux_root = wsl("mktemp", "-d", "/tmp/agent-pulse-wsl.XXXXXX")
    if not linux_root.startswith("/tmp/agent-pulse-wsl."):
        raise RuntimeError("临时目录不在预期位置")
    hp, wp, lp = port(), port(), port()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    children = []

    def request(p, path, body=None):
        req = urllib.request.Request(f"http://127.0.0.1:{p}{path}",
                                     data=json.dumps(body).encode() if body is not None else None,
                                     headers={"Content-Type": "application/json"})
        with opener.open(req, timeout=3) as response:
            return json.load(response)

    def linux_file(name, content):
        subprocess.run(["wsl", "-d", "Debian", "--", "tee", linux_root + "/" + name], input=content, stdout=subprocess.DEVNULL, check=True)

    try:
        with tempfile.TemporaryDirectory(prefix=name) as directory:
            root = Path(directory)
            linux_file("hub.yaml", json.dumps({"listen": "0.0.0.0:18700", "database": "/data/hub.db", "push": []}).encode())
            wsl("chmod", "644", linux_root + "/hub.yaml")
            wsl("docker", "run", "-d", "--rm", "--name", name,
                "-p", f"127.0.0.1:{hp}:18700", "--tmpfs", "/data:uid=65532,gid=65532,mode=700",
                "-v", f"{linux_root}/hub.yaml:/etc/agent-pulse/hub.yaml:ro", "agent-pulse-hub:windows-dev")
            wait(lambda: request(hp, "/healthz"))
            common = {"hub_url": f"http://127.0.0.1:{hp}"}
            config = root / "bridge.local.yaml"
            config.write_text(json.dumps(common | {"listen": f"127.0.0.1:{wp}", "source_id": "windows-isolated"}), encoding="utf-8")
            children.append(subprocess.Popen([str(ROOT / "bin/dev/agent-pulse-bridge.exe"), "serve", "--config", str(config)],
                                             creationflags=subprocess.CREATE_NO_WINDOW, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
            linux_file("bridge", (ROOT / ".cache/windows-check/bridge-linux").read_bytes())
            wsl("chmod", "700", linux_root + "/bridge")
            linux_file("bridge.yaml", json.dumps(common | {"listen": f"127.0.0.1:{lp}", "source_id": "debian-isolated"}).encode())
            children.append(subprocess.Popen(["wsl", "-d", "Debian", "--", linux_root + "/bridge", "serve", "--config", linux_root + "/bridge.yaml"],
                                             creationflags=subprocess.CREATE_NO_WINDOW, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
            wait(lambda: request(wp, "/healthz"))
            wait(lambda: request(lp, "/healthz"))
            ids = []
            for label, p in [("windows", wp), ("debian", lp)]:
                event_id = name + "-" + label
                ids.append(event_id)
                request(p, "/api/v1/events", {"event_id": event_id, "agent": "test", "session_id": "same-native-session",
                    "kind": "turn.completed", "scope": "main", "title": "跨环境隔离测试", "body": "测试数据",
                    "occurred_at": datetime.now(timezone.utc).isoformat()})
            wait(lambda: len(request(hp, "/api/v1/snapshot")["unread"]) == 2)
            snapshot = request(hp, "/api/v1/snapshot")
            assert {s["source_id"] for s in snapshot["sessions"]} == {"windows-isolated", "debian-isolated"}
            assert len(snapshot["sessions"]) == 2
            request(hp, "/api/v1/read", {"event_ids": ids})
            assert request(hp, "/api/v1/snapshot")["unread"] == []
            print(json.dumps({"通过": True, "平台": "Windows amd64 与 Debian WSL2 linux/amd64",
                "检查": ["Windows 原生 Bridge 连接 Debian Docker Hub", "Debian Bridge 独立接入", "相同会话 ID 由来源标识隔离", "两来源提醒与已读正常"]}, ensure_ascii=False, indent=2))
    finally:
        # 只结束本脚本创建的命令和具有本轮随机路径的 Linux 进程。
        subprocess.run(["wsl", "-d", "Debian", "--", "pkill", "-f", "^" + re.escape(linux_root) + "/bridge serve"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for child in children:
            child.terminate()
            child.wait(timeout=10)
        subprocess.run(["wsl", "-d", "Debian", "--", "docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        wsl("rm", "-rf", "--", linux_root)


if __name__ == "__main__":
    main()
