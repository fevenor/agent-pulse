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
                  ("qoderclicn", "linux/amd64"), ("opencode", "any"), ("zcode", "windows/amd64"),
                  ("codex", "windows/amd64"), ("antigravity", "windows/amd64")}


def command(*args, allow_failure=False):
    result = subprocess.run(list(map(str, args)), capture_output=True, text=True, encoding="utf-8")
    if result.returncode and not allow_failure:
        raise RuntimeError(f"命令执行失败：{args[0]}\n{result.stderr.strip()}")
    return result


def validate_tag(tag, version):
    if not re.fullmatch(r"v" + re.escape(version) + r"-rc\.[1-9][0-9]*", tag):
        raise ValueError("候选标签必须与工程版本一致，形如 v0.6.0-rc.1")


def validate_scope(packages):
    actual = {(p["id"], p["platform"]) for p in packages}
    if actual != EXPECTED_SCOPE or len(packages) != len(EXPECTED_SCOPE):
        raise ValueError("候选适配包范围不符：Windows 为 ZCode、Codex/ChatGPT和Antigravity，Linux 保留四类来源")


def require_draft(release, tag, commit):
    if not release.get("isDraft") or not release.get("isPrerelease"):
        raise ValueError("已有 Release 已被人工发布或修改，不自动覆盖")
    if release.get("tagName") != tag or release.get("targetCommitish") != commit:
        raise ValueError("已有草稿不是当前候选提交，不自动覆盖")


def validate_uploaded_assets(release, paths):
    """上传后核对 GitHub 实际保存的名称和大小，不把本地文件清单冒充分发结果。"""
    expected = {p.name: p.stat().st_size for p in paths}
    actual = {a["name"]: a["size"] for a in release.get("assets", [])}
    if len(actual) != len(release.get("assets", [])) or actual != expected:
        raise ValueError("GitHub 实际附件名称或大小与校验清单不符")


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
    parser.add_argument("--replace-draft", action="store_true")
    parser.add_argument("--relay-version", default="")
    args = parser.parse_args()
    version = tomllib.loads((ROOT / "tray/Cargo.toml").read_text(encoding="utf-8"))["package"]["version"]
    validate_tag(args.tag, version)
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", args.repository) or not re.fullmatch(r"[0-9a-f]{40}", args.commit):
        raise ValueError("仓库或提交标识无效")
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    linux, windows, bridges, assets = output / "linux", output / "windows", output / "bridges", output / "assets"
    assets.mkdir()
    for name, directory in (("linux-adapters", linux), ("windows-amd64", windows), ("linux-bridge", bridges), ("hub-build", output / "hub-build")):
        command("gh", "run", "download", args.run_id, "--repo", args.repository,
                "--name", name, "--dir", directory)
    hub_archive = unique_file(output / "hub-build", "hub-build.tar")
    hub_sums = unique_file(output / "hub-build", "SHA256SUMS.txt").read_text(encoding="utf-8").split()
    hub_hash = hashlib.sha256(hub_archive.read_bytes()).hexdigest()
    if hub_sums != [hub_hash, "hub-build.tar"]:
        raise ValueError("Hub 内部构建摘要不符")
    shutil.copyfile(hub_archive, assets / "hub-build.tar")
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
                "hub": image_root + "-hub:" + args.tag,
                "hub_build": {"asset": "hub-build.tar", "sha256": hub_hash, "publisher_only": True},
                "relay": image_root + "-relay:" + (args.relay_version or args.tag),
                "signed_repository": False, "index_sequence_is_placeholder": True,
                "bridge_assets": [{key: value for key, value in artifact.items() if key != "path"} for artifact in bridge_files]}
    (assets / "candidate-manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    notes = f"""# AgentPulse {args.tag} 试运行草稿

**保持草稿与预发布状态，尚未公开。** 本候选更新Hub、Bridge、Windows Tray及有变化的命令适配包，Relay使用 `{manifest['relay']}`。原公开RC4及其附件保持不可变，不推广稳定latest。

## 本次行为

- Tray展开事件标题在前台完整可见连续1.5秒后标读；切后台、折叠、滚出或离开后重新计时。
- 打开详情优先定位最近未读，支持下一条未读及回到最新；默认关键事件在Hub筛选后分页，普通运行记录不占页额，完整历史按需查看。
- Antigravity App首次hook早于子会话身份落盘时，Bridge暂存并调用签名适配器核对身份；主会话按序转发，子会话不进入Hub。身份游标只在本机流转，不采集历史正文。
- 正常Stop后台尚未空闲保持运行，明确错误/取消按原生原因处理；主会话后台收尾按同轮原生状态观察，不用超时猜完成。原生与观察终态按本机轮次去重。
- Agent内已读同步不实施；保留AgentPulse已有跨客户端和离线已读。

## 附件与安装

用户附件仅两架构Linux Bridge、Windows便携ZIP和SHA256SUMS；源码由标签归档提供。构建清单、签名准备仓库及Hub内部构建保留在CI Artifact。草稿附件需有权限的账号下载，不能作为匿名默认安装目标。

Linux安装脚本入口为 `https://raw.githubusercontent.com/fevenor/agent-pulse/main/scripts/install/bridge.sh`。下载并核验预构建后，使用该raw安装入口显式指定 `--version {args.tag} --file /已下载文件 --sha256 对应摘要`；安装器不编译。停止Bridge前检查health的pending为0，pending包含等待身份确认的事件。Windows正常退出Tray，备份后替换EXE，保留tray.yaml、data、原来源标识和发布公钥。

Hub须使用由发布者附加同候选官方签名仓库后核验的成品 `{manifest['hub']}`；草稿创建不代表此镜像已发布。先更新匹配Hub/Bridge/Tray和适配包，再验收新查询与本机身份暂存能力。本次不更改Hub/Relay数据库结构或Tray缓存格式，不迁移、不清空旧数据、不合并/删除历史子会话。

身份等待复用Bridge有界内存队列：进程退出会失去尚未交付的事件，与原内存暂存边界一致。长期无法确认身份时保留等待并报告pending_identity，不猜主身份。正式切换前备份配置、数据和旧产物；新适配包应在匹配Bridge已更新后启用。

## 验证边界

Antigravity App 2.32.1 / WSL Debian已取得原生浏览器子任务首次缺行、后续父子关系出现的证据；修复后的新子任务未进入隔离Hub，主会话完成。原生网页点击曾因宿主访问授权超时未完成，不将该步骤写为通过。App最终回答字段仍受宿主能力限制，不从历史补正文。

自动测试、模拟数据和隔离服务分别记录，不替代手机实收、所有主机或全部Windows交互验收。草稿范围不包含新手机渠道、后台历史重放或宿主Agent自动升级。

构建提交：{args.commit}
[候选构建与检查](https://github.com/{args.repository}/actions/runs/{args.run_id})
"""
    (assets / "trial-guide.md").write_text(notes, encoding="utf-8")
    maintenance = output / "maintenance"
    maintenance.mkdir()
    for path in list(assets.iterdir()):
        if path.name not in {"agent-pulse-bridge-linux-amd64","agent-pulse-bridge-linux-arm64","windows-amd64.zip"}:
            shutil.move(path, maintenance / path.name)
    checksums = [hashlib.sha256(p.read_bytes()).hexdigest() + "  " + p.name for p in sorted(assets.iterdir())]
    (assets / "SHA256SUMS.txt").write_text("\n".join(checksums) + "\n", encoding="utf-8")
    fields = "isDraft,isPrerelease,tagName,targetCommitish,url,assets"
    existing = command("gh", "release", "view", args.tag, "--repo", args.repository, "--json", fields, allow_failure=True)
    if existing.returncode == 0:
        previous = json.loads(existing.stdout)
        if args.replace_draft:
            require_draft(previous, args.tag, previous["targetCommitish"])
            command("gh", "release", "edit", args.tag, "--repo", args.repository, "--target", args.commit)
        else:
            require_draft(previous, args.tag, args.commit)
    else:
        command("gh", "release", "create", args.tag, "--repo", args.repository,
                "--target", args.commit, "--draft", "--prerelease",
                "--title", "AgentPulse " + args.tag + "（试运行草稿）", "--notes-file", maintenance / "trial-guide.md")
    command("gh", "release", "edit", args.tag, "--repo", args.repository,
            "--notes-file", maintenance / "trial-guide.md")
    # 仅已确认仍为草稿时清理旧草稿多余附件；公开版本始终由require_draft拒绝。
    current=json.loads(command("gh","release","view",args.tag,"--repo",args.repository,"--json",fields).stdout)
    require_draft(current,args.tag,args.commit)
    for asset in current.get("assets",[]):
        if asset["name"] not in {p.name for p in assets.iterdir()}:
            command("gh","release","delete-asset",args.tag,asset["name"],"--repo",args.repository,"--yes")
    command("gh", "release", "upload", args.tag, "--repo", args.repository, "--clobber", *sorted(assets.iterdir()))
    release = json.loads(command("gh", "release", "view", args.tag, "--repo", args.repository, "--json", fields).stdout)
    require_draft(release, args.tag, args.commit)
    validate_uploaded_assets(release, list(assets.iterdir()))
    result = {**release, "commit": args.commit, "workflow_run_id": args.run_id,
              "assets": [p.name for p in sorted(assets.iterdir())], "signed_repository": False}
    (output / "release-result.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    if os.getenv("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as summary:
            summary.write(f"## RC 草稿已创建\n\n[{args.tag}]({release['url']})\n\n保持草稿及预发布状态；等待人工试运行后决定转正。\n\n提交：{args.commit}\n")
    print(json.dumps(result, ensure_ascii=True))


if __name__ == "__main__":
    main()
