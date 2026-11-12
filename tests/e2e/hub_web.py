"""使用真实 Hub 和独立浏览器验证会话管理后的网页显示；只使用合成数据。"""

import json
import socket
import subprocess
import tempfile
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from urllib.request import Request, urlopen

root = Path(__file__).resolve().parents[2]
temp = Path(tempfile.mkdtemp(prefix="agent-pulse-web-"))
browser_session = "pulse-web-" + uuid.uuid4().hex[:10]
with socket.socket() as listener:
    listener.bind(("127.0.0.1", 0))
    port = listener.getsockname()[1]
base = f"http://127.0.0.1:{port}"
config = {
    "listen": f"127.0.0.1:{port}", "database": str(temp / "hub.db"), "push": [],
    "web_directory": str(root / "web/hub/dist"),
    "sources": {"linux-test": {"name": "开发主机", "environment": "Linux 测试环境"},
                "windows-test": {"name": "桌面主机", "environment": "Windows 测试环境"}},
}
(temp / "hub.yaml").write_text(json.dumps(config), encoding="utf-8")


def api(path, data=None, method=None):
    request = Request(base + "/api/v1/" + path, data=None if data is None else json.dumps(data).encode(),
                      headers={"Content-Type": "application/json"}, method=method)
    with urlopen(request, timeout=5) as response:
        return json.load(response)


def browser(*args):
    result = subprocess.run(["agent-browser", "--session", browser_session, *args],
                            capture_output=True, text=True, timeout=45)
    with (temp / "browser.log").open("a", encoding="utf-8") as log:
        log.write(result.stdout + result.stderr)
    if result.returncode:
        raise RuntimeError(f"浏览器命令失败：{args[0]}；诊断目录：{temp}")
    return result.stdout


def wait_for(expression):
    browser("wait", "--fn", expression)


def row(title):
    return f"Array.from(document.querySelectorAll('tbody tr')).find(r=>r.querySelector('strong')?.textContent==={json.dumps(title)})"


def counts(running, waiting):
    wait_for(f"parseInt(document.querySelectorAll('.stats strong')[0].textContent)==={running} && "
             f"parseInt(document.querySelectorAll('.stats strong')[1].textContent)==={waiting}")


def event(source, session, kind, sequence, title):
    api("events", {"event_id": f"{source}-{session}-{sequence}", "source_id": source,
                   "agent": "test", "session_id": session, "kind": kind, "scope": "main",
                   "title": "测试提醒", "session_title": title, "sequence": sequence,
                   "occurred_at": datetime.now(timezone.utc).isoformat()})


def manage(source, session, **fields):
    current = next(s for s in api("snapshot")["sessions"] if s["source_id"] == source and s["session_id"] == session)
    api("sessions/manage", {"source_id": source, "agent": "test", "session_id": session,
                           "last_event_id": current["last_event_id"],
                           "management_version": current["management_version"], **fields})


with (temp / "hub.log").open("w", encoding="utf-8") as log:
    hub = subprocess.Popen([str(root / "bin/agent-pulse-hub"), "--config", str(temp / "hub.yaml")],
                           stdout=log, stderr=log)
    try:
        deadline = time.monotonic() + 15
        while True:
            try:
                api("snapshot")
                break
            except OSError:
                if time.monotonic() >= deadline or hub.poll() is not None:
                    raise RuntimeError(f"Hub 启动失败：{temp}")
                time.sleep(0.1)
        event("linux-test", "main", "session.started", 1, "来源原始标题")
        event("windows-test", "main", "session.started", 1, "归档测试会话")
        event("linux-test", "waiting", "input.required", 1, "待输入会话")
        browser("open", base)
        wait_for("document.body.textContent.includes('实时连接正常')")
        counts(2, 1)
        manage("linux-test", "main", alias="便于识别的别名", override_state="closed")
        manage("windows-test", "main", archived=True)
        counts(0, 1)
        wait_for(f"{row('便于识别的别名')}?.textContent.includes('人工修正 · 来源：运行中')")
        wait_for(f"{row('便于识别的别名')}?.textContent.includes('原标题：来源原始标题')")
        wait_for(f"{row('归档测试会话')}?.textContent.includes('已归档')")
        wait_for(f"{row('便于识别的别名')}?.textContent.includes('开发主机') && {row('归档测试会话')}?.textContent.includes('桌面主机')")

        browser("select", '[aria-label="筛选会话状态"]', "closed")
        wait_for("document.querySelectorAll('tbody tr').length===1")
        browser("select", '[aria-label="筛选会话状态"]', "running")
        wait_for(f"document.querySelectorAll('tbody tr').length===1 && Boolean({row('归档测试会话')})")
        browser("select", '[aria-label="筛选归档状态"]', "active")
        wait_for("document.body.textContent.includes('没有匹配的会话')")
        browser("select", '[aria-label="筛选会话状态"]', "")
        browser("select", '[aria-label="筛选归档状态"]', "archived")
        wait_for(f"document.querySelectorAll('tbody tr').length===1 && Boolean({row('归档测试会话')})")
        browser("select", '[aria-label="筛选归档状态"]', "")
        for query, expected in [("便于识别", 1), ("来源原始标题", 1), ("开发主机", 2), ("Linux 测试环境", 2), ("windows-test", 1)]:
            browser("fill", '[aria-label="搜索会话或主机"]', query)
            wait_for(f"document.querySelectorAll('tbody tr').length==={expected}")
        browser("fill", '[aria-label="搜索会话或主机"]', "")

        manage("linux-test", "main", override_state="")
        counts(1, 1)
        wait_for(f"!{row('便于识别的别名')}?.textContent.includes('人工修正')")
        manage("linux-test", "main", override_state="interrupted")
        counts(0, 1)
        event("linux-test", "main", "activity.observed", 2, "来源更新标题")
        counts(1, 1)
        wait_for(f"{row('便于识别的别名')}?.textContent.includes('原标题：来源更新标题')")

        # 归档不清除未读，只从活跃计数排除；明确已读仍须 Hub 确认。
        manage("linux-test", "waiting", archived=True)
        counts(1, 0)
        wait_for("parseInt(document.querySelectorAll('.stats strong')[2].textContent)===1")
        browser("click", "nav button:nth-child(2)")
        browser("click", ".reminders article button")
        wait_for("document.body.textContent.includes('没有未读提醒')")
        assert api("snapshot")["unread"] == []
        browser("click", "nav button:first-child")
        event("unlabelled-test", "fallback", "session.started", 1, "未命名来源会话")
        wait_for(f"{row('未命名来源会话')}?.textContent.includes('unlabelled-test')")
        # 中央改名应更新普通会话展示；主机归档仅对桌面历史隐藏，管理页面仍可追溯。
        api("hosts/linux-test", {"display_name": "统一维护的主机名"}, "PATCH")
        wait_for(f"{row('便于识别的别名')}?.textContent.includes('统一维护的主机名')")
        api("hosts/linux-test", {"archived": True}, "PATCH")
        counts(1, 0)
        assert all(e["source_id"] != "linux-test" for e in api("events"))
        assert any(e["source_id"] == "linux-test" for e in api("events?include_archived=true"))
        browser("click", "nav button:nth-child(4)")
        wait_for(f"{row('统一维护的主机名')}?.textContent.includes('已归档')")
        browser("click", "nav button:nth-child(7)")
        wait_for("document.querySelector('tbody')?.textContent.includes('统一维护的主机名')")
        api("hosts/linux-test", {"archived": False}, "PATCH")
        counts(2, 0)
        # 删除仅限本测试生成的数据，验证页面和历史不会重新出现旧主机。
        api("hosts/windows-test", method="DELETE")
        browser("click", "nav button:nth-child(4)")
        wait_for("!document.querySelector('tbody')?.textContent.includes('windows-test')")
        assert all(s["source_id"] != "windows-test" for s in api("snapshot")["sessions"])
        assert all(e["source_id"] != "windows-test" for e in api("events?include_archived=true"))
        browser("click", "nav button:first-child")
        browser("set", "viewport", "390", "844")
        wait_for("document.documentElement.scrollWidth<=window.innerWidth")
        browser("screenshot", str(temp / "mobile.png"))
        browser("set", "viewport", "1440", "1000")
        browser("screenshot", str(temp / "desktop.png"))
        errors = browser("errors").strip()
        if errors and errors != "✓ No errors":
            raise RuntimeError(f"浏览器存在错误：{temp}")
        result = {"result": "通过", "checks": ["别名与来源事实", "人工状态与计数", "归档筛选", "主机与环境搜索",
                  "恢复自动与新活动", "归档未读保留", "明确已读", "未命名来源回退", "主机集中改名", "主机归档与管理页历史", "永久删除清理", "移动布局", "无页面错误"]}
        (temp / "result.json").write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
        print(json.dumps(result, ensure_ascii=False))
        print(f"诊断目录：{temp}")
    finally:
        try:
            browser("close")
        finally:
            hub.terminate()
            try:
                hub.wait(timeout=10)
            except subprocess.TimeoutExpired:
                hub.kill()
                hub.wait(timeout=5)
