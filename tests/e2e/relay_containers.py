"""验证真实 Hub/Relay 镜像的鉴权、持久队列、恢复与已读；仅创建临时资源。"""
import argparse
import hashlib
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import platform
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def docker(*args):
    return subprocess.check_output(["docker", *map(str, args)], text=True).strip()


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait(check, seconds=45):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                return value
        except (OSError, ValueError):
            pass
        time.sleep(.25)
    raise RuntimeError("等待容器服务状态超时")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--arch", choices=["amd64", "arm64"], default="amd64")
    parser.add_argument("--hub-image", default="agent-pulse-hub:test-amd64")
    parser.add_argument("--relay-image", default="agent-pulse-relay:test-amd64")
    parser.add_argument("--emulator-file", type=Path, help="显式只读挂载用户态模拟器，不注册主机内核处理器")
    args = parser.parse_args()
    emulator = args.emulator_file.expanduser().resolve() if args.emulator_file else None
    if emulator is not None and (not emulator.is_file() or not os.access(emulator, os.X_OK)):
        parser.error("模拟器必须是现有可执行文件")
    name = "agent-pulse-e2e-" + secrets.token_hex(5)
    token = secrets.token_urlsafe(24)
    hub_port, relay_port = port(), port()
    hub_url, relay_url = f"http://127.0.0.1:{hub_port}", f"http://127.0.0.1:{relay_port}"
    containers, volumes = [], []
    image_commands = {}
    evidence = {"architecture": args.arch, "host_architecture": platform.machine(), "checks": []}
    if emulator is not None:
        evidence["emulation"] = "显式用户态模拟，不修改主机配置"
        evidence["emulator_sha256"] = hashlib.sha256(emulator.read_bytes()).hexdigest()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def request(base, path, body=None, auth=True):
        headers = {"Content-Type": "application/json"}
        if auth:
            headers["Authorization"] = "Bearer " + token
        req = urllib.request.Request(base + path, data=json.dumps(body).encode() if body is not None else None, headers=headers)
        try:
            response = opener.open(req, timeout=5)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read()
            try:
                data = json.loads(raw)
            except ValueError:
                data = raw.decode()
            return response.status, data

    with tempfile.TemporaryDirectory(prefix=name) as directory:
        root = Path(directory)
        # 配置只含测试令牌的环境变量占位符，容器内非 root 用户可读。
        (root / "relay.yaml").write_text(f'listen: "127.0.0.1:{relay_port}"\ndatabase: /data/relay.db\ntoken: "${{env:AGENT_PULSE_TEST_TOKEN}}"\n')
        (root / "hub.yaml").write_text(f'listen: "127.0.0.1:{hub_port}"\ndatabase: /data/hub.db\nweb_directory: /web\nrelay_url: "{relay_url}"\nrelay_token: "${{env:AGENT_PULSE_TEST_TOKEN}}"\n')
        previous = os.environ.get("AGENT_PULSE_TEST_TOKEN")
        os.environ["AGENT_PULSE_TEST_TOKEN"] = token
        try:
            for component, image in [("relay", args.relay_image), ("hub", args.hub_image)]:
                info = json.loads(docker("image", "inspect", image))[0]
                assert info["Architecture"] == args.arch, "镜像架构不正确"
                assert info["Config"]["User"] == "65532:65532", "镜像必须以非 root 用户运行"
                evidence[component + "_image_id"] = info["Id"]
                if emulator is not None:
                    # 保留镜像原始程序及参数，只在进程启动边界加用户态模拟器。
                    entrypoint = info["Config"].get("Entrypoint") or []
                    assert entrypoint, "模拟运行需要镜像具有原始启动程序"
                    image_commands[component] = entrypoint + (info["Config"].get("Cmd") or [])
                volume = name + "-" + component
                docker("volume", "create", volume)
                volumes.append(volume)

            def start(component, image):
                container = name + "-" + component
                containers.append(container)
                emulator_options, command = [], []
                if emulator is not None:
                    emulator_options = ["--mount", f"type=bind,source={emulator},target=/agent-pulse-qemu,readonly",
                                        "--entrypoint", "/agent-pulse-qemu"]
                    command = image_commands[component]
                docker("run", "-d", "--name", container, "--platform", "linux/" + args.arch,
                       "--network", "host", "--env", "AGENT_PULSE_TEST_TOKEN",
                       "--mount", f"type=volume,source={container},target=/data",
                       "--mount", f"type=bind,source={root / (component + '.yaml')},target=/etc/agent-pulse/{component}.yaml,readonly",
                       *emulator_options, image, *command)

            start("relay", args.relay_image)
            wait(lambda: request(relay_url, "/healthz")[0] == 200)
            assert request(relay_url, "/api/v1/snapshot", auth=False)[0] == 401
            assert request(relay_url, "/api/v1/snapshot")[0] == 503
            evidence["checks"].append("公网入口拒绝未鉴权请求，Hub 离线查询返回 503")
            event = {"event_id": name, "source_id": "container-test", "agent": "test", "session_id": name,
                     "kind": "turn.completed", "scope": "main", "title": "容器恢复测试",
                     "body": "测试数据", "occurred_at": datetime.now(timezone.utc).isoformat()}
            code, receipt = request(relay_url, "/api/v1/events", event)
            assert code == 202 and receipt["accepted"] == "durable"
            assert request(relay_url, "/api/v1/events", event)[1]["duplicate"]
            # 强制终止模拟崩溃：验证 202 确认的事件已真正落盘。
            docker("kill", name + "-relay")
            docker("start", name + "-relay")
            wait(lambda: request(relay_url, "/healthz")[1].get("pending") == 1)
            evidence["checks"].append("离线事件去重，Relay 崩溃重启后持久队列恢复")
            start("hub", args.hub_image)
            wait(lambda: request(hub_url, "/healthz")[0] == 200)
            wait(lambda: (lambda s: s.get("hub_connected") and s.get("pending") == 0)(request(relay_url, "/healthz")[1]))
            history = request(relay_url, "/api/v1/events")[1]
            assert len(history) == 1 and history[0]["event_id"] == name
            assert len(request(relay_url, "/api/v1/snapshot")[1]["unread"]) == 1
            assert "<html" in request(hub_url, "/")[1]
            evidence["checks"].append("Hub 主动重连，Relay 补发、查询转发及 Hub 页面正常")
            assert request(relay_url, "/api/v1/read", {"event_ids": [name]})[0] == 200
            docker("kill", name + "-hub")
            docker("start", name + "-hub")
            wait(lambda: request(hub_url, "/healthz")[0] == 200)
            wait(lambda: request(relay_url, "/api/v1/snapshot")[0] == 200)
            assert request(relay_url, "/api/v1/snapshot")[1]["unread"] == []
            assert len(request(hub_url, "/api/v1/events")[1]) == 1
            evidence["checks"].append("经 Relay 标记已读，Hub 崩溃重启后历史和已读保持")
        finally:
            for container in reversed(containers):
                subprocess.run(["docker", "rm", "-f", container], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            for volume in volumes:
                subprocess.run(["docker", "volume", "rm", volume], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if previous is None:
                os.environ.pop("AGENT_PULSE_TEST_TOKEN", None)
            else:
                os.environ["AGENT_PULSE_TEST_TOKEN"] = previous
    evidence["passed"] = True
    print(json.dumps(evidence, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
