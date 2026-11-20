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

**保持草稿与预发布状态，尚未公开。** 本候选为完整通讯优化，Hub、Relay、Bridge、Windows Tray与Web需要一起更新；旧公开RC4和首候选保持，不推广stable/latest。

## 本次行为

- 首次建立状态基准，之后直接推送会话、未读、来源及设备变化，不再周期下载完整快照。已读、提醒失效、归档和删除明确同步。
- 新提醒完整正文自动补齐并缓存，已读/状态变化不重传正文；隐藏详情暂停额外历史下载。离线已读与已缓存正文保持。
- 断线在有界近期窗口内续接；Hub重启、窗口不足或游标缺口时重建元数据基准。权威状态确认回显客户端探测，保持35秒失联保护。
- Bridge通过常驻连接发送小批事件并逐条等待持久确认，Relay在Hub离线时仍暂存事件和已读；原有去重与确认规则保持。
- 大JSON压缩，Relay隧道传原始载荷，不再Base64包裹正文或ZIP；健康中继连接不定时断开。适配报告按变化发送，索引条件读取，包下载支持核验后的分块续传。
- 增加应用载荷分类统计，不记录正文或凭据，不把其等同移动网络计费。普通运行历史不删除、不减少关键提醒。

## 产物与升级

四个用户附件为两架构Linux Bridge、Windows便携ZIP及SHA256SUMS。使用有权限账号取得草稿附件并校验摘要；源码由标签归档提供，维护清单保留在CI Artifact。

Hub成品为`{manifest['hub']}`，须由发布者附加同候选官方签名仓库后核验；Relay为`{manifest['relay']}`。旧Relay不支持本候选新隧道，须协调更新全部组件。先备份并排空待发，再切换预构建程序；保留原数据库、已读、来源、公钥、配置和手机模板，不重新setup或自动清空数据。原共享签名索引只递增。

Linux raw安装脚本入口为`https://raw.githubusercontent.com/fevenor/agent-pulse/main/scripts/install/bridge.sh`，仍以已公开版本为默认，试用草稿显式传入`--version {args.tag} --file /已核验文件 --sha256 对应摘要 --install-only`。Windows原路径替换EXE，保留tray.yaml和data。正文缓存可重建，不能以清缓存为由覆盖state.json。回滚保留升级期间现行事件及已读。

## 验证边界

自动回归、隔离服务和短时字节测量不等于所有目标日常使用或运营商流量账单。3小时活动和8小时后台需按实际测量记录；手机实收、自然模型最终失败和特殊桌面组合保留已有未验边界。首候选的阅读、历史定位和Antigravity身份修复继续保留。

构建提交：{args.commit}
[完整候选构建与检查](https://github.com/{args.repository}/actions/runs/{args.run_id})
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
