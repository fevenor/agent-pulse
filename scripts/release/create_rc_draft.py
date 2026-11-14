"""合并同一工作流的候选产物并创建 GitHub RC 草稿；不签名、不转正。"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tomllib
import zipfile

ROOT = Path(__file__).resolve().parents[2]
EXPECTED_SCOPE = {("codex", "linux/amd64"), ("antigravity", "linux/amd64"),
                  ("qoderclicn", "linux/amd64"), ("opencode", "any"), ("zcode", "windows/amd64")}


def command(*args, allow_failure=False):
    result = subprocess.run(list(map(str, args)), capture_output=True, text=True, encoding="utf-8")
    if result.returncode and not allow_failure:
        raise RuntimeError(f"命令执行失败：{args[0]}\n{result.stderr.strip()}")
    return result


def validate_tag(tag, version):
    if not re.fullmatch(r"v" + re.escape(version) + r"-rc\.[1-9][0-9]*", tag):
        raise ValueError("候选标签必须与工程版本一致，形如 v0.5.0-rc.1")


def validate_scope(packages):
    actual = {(p["id"], p["platform"]) for p in packages}
    if actual != EXPECTED_SCOPE or len(packages) != len(EXPECTED_SCOPE):
        raise ValueError("候选适配包范围不符：Windows 仅 ZCode，Linux 保留四类来源")


def require_draft(release, tag, commit):
    if not release.get("isDraft") or not release.get("isPrerelease"):
        raise ValueError("已有 Release 已被人工发布或修改，不自动覆盖")
    if release.get("tagName") != tag or release.get("targetCommitish") != commit:
        raise ValueError("已有草稿不是当前候选提交，不自动覆盖")


def unique_file(directory, name):
    paths = list(directory.rglob(name))
    if len(paths) != 1:
        raise ValueError(f"候选产物 {name} 必须唯一，实际 {len(paths)} 个")
    return paths[0]


def bridge_artifacts(directory):
    """核验同一 CI 的两架构 Bridge，不执行二进制、不在用户安装时编译。"""
    expected = {"agent-pulse-bridge-linux-amd64": ("linux/amd64", 62),
                "agent-pulse-bridge-linux-arm64": ("linux/arm64", 183)}
    checksum_file = unique_file(directory, "SHA256SUMS.txt")
    checksums = {}
    for line in checksum_file.read_text(encoding="utf-8").splitlines():
        fields = line.split()
        if len(fields) != 2 or not re.fullmatch(r"[a-f0-9]{64}", fields[0]):
            raise ValueError("Bridge 校验清单格式无效")
        name = fields[1].lstrip("*")
        if name in checksums or name not in expected:
            raise ValueError("Bridge 校验清单包含重复或范围外文件")
        checksums[name] = fields[0]
    if set(checksums) != set(expected):
        raise ValueError("Bridge 发布物缺少支持架构")
    artifacts = []
    for name, (platform, machine) in expected.items():
        path = unique_file(directory, name)
        raw = path.read_bytes()
        if hashlib.sha256(raw).hexdigest() != checksums[name]:
            raise ValueError("Bridge 预构建摘要不匹配")
        if len(raw) < 20 or raw[:6] != b"\x7fELF\x02\x01" or int.from_bytes(raw[18:20], "little") != machine:
            raise ValueError("Bridge 预构建格式或架构不符")
        # 读取原始构建信息，不能仅相信文件名或工作流声称的版本。
        build_info = command("go", "version", "-m", path).stdout
        if not re.search(r"(?m)^.*: go1\.27\.4$", build_info) or not re.search(r"(?m)^\s*path\s+agent-pulse/cmd/agent-pulse-bridge$", build_info) or "golang.org/x/sys\tv0.44.0" not in build_info or "CGO_ENABLED=0" not in build_info:
            raise ValueError("Bridge 编译器、依赖或静态构建基线不符")
        artifacts.append({"name": name, "platform": platform, "sha256": checksums[name],
                          "size": len(raw), "compiler": "go1.27.4", "path": path})
    return artifacts


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    version = tomllib.loads((ROOT / "tray/Cargo.toml").read_text(encoding="utf-8"))["package"]["version"]
    validate_tag(args.tag, version)
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", args.repository) or not re.fullmatch(r"[0-9a-f]{40}", args.commit):
        raise ValueError("仓库或提交标识无效")
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    linux, windows, bridges, assets = output / "linux", output / "windows", output / "bridges", output / "assets"
    assets.mkdir()
    for name, directory in (("linux-adapters", linux), ("windows-amd64", windows), ("linux-bridge", bridges)):
        command("gh", "run", "download", args.run_id, "--repo", args.repository,
                "--name", name, "--dir", directory)
    bridge_files = bridge_artifacts(bridges)
    for artifact in bridge_files:
        shutil.copyfile(artifact["path"], assets / artifact["name"])
    shutil.copyfile(ROOT / "scripts/install/bridge.sh", assets / "install-bridge.sh")
    windows_zip = unique_file(windows, "windows-amd64.zip")
    with zipfile.ZipFile(windows_zip) as archive:
        if archive.testzip() is not None or sorted(archive.namelist()) != sorted([
            "agent-pulse-tray.exe", "bridge.example.yaml", "tray.example.yaml", "使用说明.md"]):
            raise ValueError("Windows 便携包不符合单入口清单")
    shutil.copyfile(windows_zip, assets / "windows-amd64.zip")
    linux_index, windows_index = unique_file(linux, "index-input.json"), unique_file(windows, "index-input.json")
    combined = output / "repository"
    command(sys.executable, ROOT / "scripts/release/merge_adapter_repositories.py",
            linux_index.parent, windows_index.parent, "--output", combined, "--sequence", "1")
    index = json.loads((combined / "index-input.json").read_text(encoding="utf-8"))
    validate_scope(index["packages"])
    for package in index["packages"]:
        expected = json.loads((ROOT / "adapters" / package["id"] / "manifest.json").read_text(encoding="utf-8"))["version"]
        if package["version"] != expected:
            raise ValueError("工作流产物的版本与当前提交不一致")
    with tarfile.open(assets / "adapters-unsigned.tar.gz", "w:gz") as archive:
        archive.add(combined / "index-input.json", arcname="index-input.json")
        for package in index["packages"]:
            name = package["sha256"] + ".zip"
            archive.add(combined / "packages" / name, arcname="packages/" + name)
    publisher = assets / "agent-pulse-adapters-linux-amd64"
    result = subprocess.run(["go", "build", "-trimpath", "-buildvcs=false", "-o", str(publisher),
                             "./cmd/agent-pulse-adapters"], cwd=ROOT / "go", capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError("发布工具构建失败：" + result.stderr)
    image_root = "ghcr.io/" + args.repository.lower()
    manifest = {"tag": args.tag, "commit": args.commit, "workflow_run_id": args.run_id,
                "draft": True, "prerelease": True, "packages": index["packages"],
                "hub_base": image_root + "/hub-base:" + args.tag,
                "relay": image_root + "/relay:" + args.tag,
                "signed_repository": False, "index_sequence_is_placeholder": True,
                "bridge_assets": [{key: value for key, value in artifact.items() if key != "path"} for artifact in bridge_files]}
    (assets / "候选构建清单.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    notes = f"""# AgentPulse {args.tag} 试运行草稿

本草稿由同一提交的 Linux 与 Windows 工作流成功后生成，等待发布者人工运行数日后再决定发布；不会自动转正，也不更新稳定 latest 镜像别名。

## 本轮范围

- Windows 本机接入只支持 ZCode 图形界面；Windows Codex CLI 和 ChatGPT Windows 桌面端仅规划。
- Linux 保留 Codex、Qoder、Antigravity CLI 与 OpenCode。
- 本候选修复时间线折叠/空白、同版本缺失钩子与配置向导假成功；同步原生会话名称，正常 Agent 直接启动不要求包装器。
- Windows 本地已验证 ZCode 3.18.2 的两轮短对话和改名后下一次真实事件同步；ACP 完整宿主任务、真实子代理及长期桌面场景仍按开发记录保留边界。
- 主机快捷偏好、唤醒退出、通知分流及悬浮入口的剩余检查留到试运行中观察。
- 编译器 Go 1.27.4、x/sys 0.44.0；ARM 镜像运行为模拟验证，不代表真机。

## 试运行文件与镜像

- windows-amd64.zip：单入口便携包，私人 tray.yaml 与 data/ 不包含在附件中；升级前自行备份并保留现有配置。
- adapters-unsigned.tar.gz：五包未签名仓库。序号 1 是占位值；在更新现有设备之前，发布者须使用既有长期私钥、递增正式索引序号签名，不能生成替代公钥。
- agent-pulse-adapters-linux-amd64：Linux 发布工具，下载后赋予可执行权限；私钥保持在发布者机器，不进入 CI 或附件。
- agent-pulse-bridge-linux-amd64 / agent-pulse-bridge-linux-arm64：已完成构建的 Bridge；用户无需安装编译器。Bridge arm64 不代表当前命令适配包已支持 arm64。
- install-bridge.sh：自动识别 Linux 架构、下载并校验 Bridge 的用户安装入口；Release 草稿不能匿名下载，公开后才可使用 --version 指向本候选版本。
- Hub 基础镜像：{manifest['hub_base']}。它不含正式签名仓库，不能冒充普通用户完整 RC。
- Relay 镜像：{manifest['relay']}。

可先沿用现有 Hub 与已签名适配仓库测试 Windows 客户端。若测试新适配包，在 Linux 发布机按 docs/images.md 使用既有长期密钥签名，再用 scripts/release/prepare-hub-release.py 附加仓库到此基础镜像；无需将私钥上传 GitHub。升级服务前备份数据库，先 Hub/Relay、后 Bridge/Tray，再下发包索引。

Windows 完整仓库准备目录仍有拒绝访问的验证边界，签名与完整镜像准备沿用 Linux 流程。草稿不是新的真实宿主或手机实收证明；运行期间保留实际事件、提醒和异常证据，公开反馈不包含私人配置、令牌或会话正文。

来源提交：{args.commit}
工作流：https://github.com/{args.repository}/actions/runs/{args.run_id}
"""
    (assets / "试运行说明.md").write_text(notes, encoding="utf-8")
    checksums = [hashlib.sha256(p.read_bytes()).hexdigest() + "  " + p.name for p in sorted(assets.iterdir())]
    (assets / "SHA256SUMS.txt").write_text("\n".join(checksums) + "\n", encoding="utf-8")
    fields = "isDraft,isPrerelease,tagName,targetCommitish,url"
    existing = command("gh", "release", "view", args.tag, "--repo", args.repository, "--json", fields, allow_failure=True)
    if existing.returncode == 0:
        require_draft(json.loads(existing.stdout), args.tag, args.commit)
    else:
        command("gh", "release", "create", args.tag, "--repo", args.repository,
                "--target", args.commit, "--draft", "--prerelease",
                "--title", "AgentPulse " + args.tag + "（试运行草稿）", "--notes-file", assets / "试运行说明.md")
    command("gh", "release", "upload", args.tag, "--repo", args.repository, "--clobber", *sorted(assets.iterdir()))
    release = json.loads(command("gh", "release", "view", args.tag, "--repo", args.repository, "--json", fields).stdout)
    require_draft(release, args.tag, args.commit)
    result = {**release, "commit": args.commit, "workflow_run_id": args.run_id,
              "assets": [p.name for p in sorted(assets.iterdir())], "signed_repository": False}
    (output / "release-result.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    if os.getenv("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as summary:
            summary.write(f"## RC 草稿已创建\n\n[{args.tag}]({release['url']})\n\n保持草稿及预发布状态；等待人工试运行后决定转正。\n\n提交：{args.commit}\n")
    print(json.dumps(result, ensure_ascii=True))


if __name__ == "__main__":
    main()
