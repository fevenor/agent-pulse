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
        raise ValueError("候选标签必须与工程版本一致，形如 v0.5.0-rc.1")


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
                "relay": image_root + "-relay:" + args.tag,
                "signed_repository": False, "index_sequence_is_placeholder": True,
                "bridge_assets": [{key: value for key, value in artifact.items() if key != "path"} for artifact in bridge_files]}
    (assets / "candidate-manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    notes = f"""# AgentPulse {args.tag} 试运行草稿

本草稿绑定同一提交的Linux与Windows流水线产物，不自动公开，不修改RC2，也不更新稳定latest。

## 候选范围

- Linux：Codex、Qoder中国CLI、Antigravity CLI/WSL桌面执行器、OpenCode；Windows：ZCode、Codex/ChatGPT本地任务、Antigravity App。共七类来源/平台包，平台与版本以candidate-manifest.json为准。
- 补齐原生完成正文、授权动作/目标/原因、提问及处理结果、可见失败摘要；局部工具错误不伪装成主任务最终失败。内部/子代理按明确身份过滤，不读取历史正文补内容。
- 手机标题使用原生会话名，正文包含友好主机/环境、中文事件类型和本事件内容；最终模板文本按渠道实测上限截短，避免重复状态标题。私人渠道模板变更和实收验收另按部署授权执行。
- OpenCode合法JSONC保留注释、尾逗号和原始数值，拒绝重复键；有效Bridge配置的部分接入失败不阻断其他接入及服务启动。
- Windows新增来源可单独Go构建，不要求Rust/MSVC；Tray与完整便携包仍使用原有工具链。Windows和WSL各自运行Bridge，不由Tray代管WSL服务，不重复安装同家族App/CLI hook。
- Tray默认10分钟无可信真人输入视为离开，可调或关闭；可识别的软件注入不保持在场，未知/离开不累计自动已读。显示变化保留隐藏窗口状态，不自动显示或还原。

## 产物及签名

- windows-amd64.zip：单入口便携包，不含私人tray.yaml与data/；升级保留原配置和数据。
- adapters-unsigned.tar.gz：七包未签名仓库，序号1仅为占位；发布者必须沿用原长期私钥及公钥，以递增正式索引序号签名，不生成替代生产公钥。
- agent-pulse-adapters-linux-amd64：发布维护工具，私钥不进入CI或附件。
- 两架构Linux Bridge与install-bridge.sh：安装只使用预构建；草稿不能匿名下载，无参数默认仍指向脚本标注的公开版本，不冒充已安装RC3。
- hub-build.tar：发布者专用的同提交双架构OCI基准构建，未附加正式签名仓库，不发布第三个公开镜像。
- Hub成品目标：{manifest['hub']}。需要发布者核验同提交构建并附加正式签名仓库后发布；草稿生成不代表该镜像已经存在。
- Relay目标：{manifest['relay']}。实际发布状态须单独核对，不以草稿清单代替。

## 验证边界

Go1.27.4、Rust1.99.1、Node24.14.1/pnpm10.33.2按锁定依赖构建。ARM运行采用模拟，不代表ARM真机。自动回归、版本探测、消息探针与真实宿主任务、权限恢复、记忆/子代理过滤、RDP/多屏和手机实收分别记录；最新可复现证据见docs/development-status.md，未验事项不得写成通过。草稿不是实际手机送达证明。

试运行使用匹配的现行组件，保留配置和数据库备份，不自动迁移或清空正式数据，不批量重发历史失败消息。完整Hub组装见docs/images.md；发布者只在本机保管签名私钥。

来源提交：{args.commit}
工作流：https://github.com/{args.repository}/actions/runs/{args.run_id}
"""
    (assets / "trial-guide.md").write_text(notes, encoding="utf-8")
    checksums = [hashlib.sha256(p.read_bytes()).hexdigest() + "  " + p.name for p in sorted(assets.iterdir())]
    (assets / "SHA256SUMS.txt").write_text("\n".join(checksums) + "\n", encoding="utf-8")
    fields = "isDraft,isPrerelease,tagName,targetCommitish,url,assets"
    existing = command("gh", "release", "view", args.tag, "--repo", args.repository, "--json", fields, allow_failure=True)
    if existing.returncode == 0:
        require_draft(json.loads(existing.stdout), args.tag, args.commit)
    else:
        command("gh", "release", "create", args.tag, "--repo", args.repository,
                "--target", args.commit, "--draft", "--prerelease",
                "--title", "AgentPulse " + args.tag + "（试运行草稿）", "--notes-file", assets / "trial-guide.md")
    command("gh", "release", "edit", args.tag, "--repo", args.repository,
            "--notes-file", assets / "trial-guide.md")
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
