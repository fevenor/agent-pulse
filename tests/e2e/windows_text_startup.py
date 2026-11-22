"""用公开中文缓存验证原生Tray启动与重启，不连接Hub或修改真实配置。"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=ROOT / "bin/agent-pulse-tray.exe")
    args = parser.parse_args()
    assert os.name == "nt", "此检查需要原生Windows"
    binary = args.binary.resolve()
    titles = ["  远程中文标题", "中文👨‍👩‍👧‍👦e\u0301结束", "长中文标题" * 40]
    with tempfile.TemporaryDirectory(prefix="agent-pulse-text-") as temporary:
        directory = Path(temporary)
        data = directory / "data"
        data.mkdir()
        config = directory / "tray.yaml"
        config.write_text('hub_url: ""\nbridge_enabled: false\n', encoding="utf-8")
        snapshot = {"version": 1, "run_id": "", "devices": [], "unread": [],
                    "sources": {"fixture": {"role": "bridge", "name": "远程中文主机",
                                           "environment": "", "archived": False}},
                    "sessions": [{"source_id": "fixture", "agent": "zcode",
                                  "session_id": f"session-{index}", "title": title,
                                  "state": "idle"} for index, title in enumerate(titles)]}
        state = data / "state.json"
        state.write_text(json.dumps({"device_id": "fixture-tray", "snapshot": snapshot,
                                     "pending_reads": ["offline-read"], "notified": []},
                                    ensure_ascii=False), encoding="utf-8")
        for _ in range(2):
            with (directory / "stderr.log").open("wb") as stderr:
                process = subprocess.Popen(
                    [str(binary), "--config", str(config)], cwd=directory,
                    creationflags=subprocess.CREATE_NO_WINDOW,
                    stdout=subprocess.DEVNULL, stderr=stderr)
                try:
                    deadline = time.monotonic() + 5
                    while process.poll() is None and time.monotonic() < deadline:
                        time.sleep(0.1)
                    assert process.poll() is None, (
                        "中文缓存启动提前退出；请先关闭其他Tray实例。\n" +
                        (directory / "stderr.log").read_text(encoding="utf-8", errors="replace"))
                    saved = json.loads(state.read_text(encoding="utf-8"))
                    assert saved["pending_reads"] == ["offline-read"]
                    assert [session["title"] for session in saved["snapshot"]["sessions"]] == titles
                finally:
                    if process.poll() is None:
                        process.terminate()
                        process.wait(timeout=5)
        print("中文、emoji和长标题原生启动/重启通过，原始标题和离线已读保持。")


if __name__ == "__main__":
    main()
