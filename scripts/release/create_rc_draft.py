"""合并同一工作流的候选产物并创建 GitHub 版本草稿；不签名、不转正。"""
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
                  ("qoderclicn", "linux/amd64"), ("opencode", "linux/amd64"), ("opencode", "linux/arm64"), ("zcode", "windows/amd64"),
                  ("zcode", "linux/amd64"), ("dsh", "linux/amd64"), ("dsh", "linux/arm64"),
                  ("codex", "windows/amd64"), ("antigravity", "windows/amd64")}


def command(*args, allow_failure=False):
    result = subprocess.run(list(map(str, args)), capture_output=True, text=True, encoding="utf-8")
    if result.returncode and not allow_failure:
        raise RuntimeError(f"命令执行失败：{args[0]}\n{result.stderr.strip()}")
    return result


def validate_tag(tag, version):
    if not re.fullmatch(r"v" + re.escape(version) + r"(?:-rc\.[1-9][0-9]*)?", tag):
        raise ValueError("标签必须与工程版本一致，使用 v版本 或 v版本-rc.序号")


def validate_scope(packages):
    actual = {(p["id"], p["platform"]) for p in packages}
    if actual != EXPECTED_SCOPE or len(packages) != len(EXPECTED_SCOPE):
        raise ValueError("候选适配包范围不符：Windows 为三类来源，Linux 为含 ZCode Server、DSH 的六类来源，OpenCode 与 DSH 控制入口按两架构分包")


def require_draft(release, tag, commit):
    if not release.get("isDraft") or release.get("isPrerelease") != ("-rc." in tag):
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
                "draft": True, "prerelease": "-rc." in args.tag, "packages": index["packages"],
                "hub": image_root + "-hub:" + args.tag,
                "hub_build": {"asset": "hub-build.tar", "sha256": hub_hash, "publisher_only": True},
                "relay": image_root + "-relay:" + (args.relay_version or args.tag),
                "signed_repository": False, "index_sequence_is_placeholder": True,
                "bridge_assets": [{key: value for key, value in artifact.items() if key != "path"} for artifact in bridge_files]}
    (assets / "candidate-manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    notes = f"""# AgentPulse {args.tag}

本版本包含 Hub、Relay、Linux Bridge 和 Windows Tray；用户附件由同一次 Workflow 检查和构建。草稿仅在签名仓库与完整 Hub 镜像准备并核验后公开。

## 本次改进

- DSH 0.1.3 修复并行工具误清等待、递交乱序、重复归档/心跳身份及空最终正文问题；有界后台重试保持事件身份。
- DSH 探测与 profile 配置由随包控制入口负责，宿主直接引用不可变包，消除安装窗口缓存旧版本的问题。
- Hub 只校验适配仓库的分发结构；Bridge 允许控制入口接管未知插件形态。保留原签名公钥、摘要与安装恢复。
- 包含 Codex 工具活动后的最终模型错误采集修复及 Windows 中文会话显示修复。
- Hub 独立更新适配仓库；Bridge 在启动、setup 或显式同步时应用，运行和重连不检查。保留现有数据库与配置，不重放历史提醒。

## 安装和升级

完整 Hub：`{manifest['hub']}`；Relay：`{manifest['relay']}`。

用户附件为两架构 Linux Bridge、Windows Tray 便携 ZIP 和 SHA256SUMS.txt。安装入口为 https://raw.githubusercontent.com/fevenor/agent-pulse/main/scripts/install/bridge.sh ，使用 `--version {args.tag}` 显式选择本版本，不在目标机器编译。

已有实例先核对 Bridge 队列为零、备份配置和数据，再替换程序并同步签名适配包；保留主机身份、公钥、手机模板和用户数据。插件写入配置后仍须按宿主规则重新加载，installed 不能代替 running 和实际提醒验收。发布维护资料保存在 Workflow Artifact，不混入用户附件。

## 验证范围

Workflow 执行 Linux/Windows 检查、Go race/vet、前端和插件测试、两架构容器验证及产物摘要核对。ARM 模拟/交叉构建不代替真机运行。DSH ACP 暂未纳入支持验收；手机和 Tray 实收仍按实际目标确认。

构建提交：{args.commit}
[Workflow 检查与构建](https://github.com/{args.repository}/actions/runs/{args.run_id})
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
                "--target", args.commit, "--draft", *(["--prerelease"] if manifest["prerelease"] else []),
                "--title", "AgentPulse " + args.tag + "（待发布草稿）", "--notes-file", maintenance / "trial-guide.md")
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
            summary.write(f"## 版本草稿已创建\n\n[{args.tag}]({release['url']})\n\n草稿已准备；完成本机签名与完整镜像核验后，使用发布工作流公开。\n\n提交：{args.commit}\n")
    print(json.dumps(result, ensure_ascii=True))


if __name__ == "__main__":
    main()
