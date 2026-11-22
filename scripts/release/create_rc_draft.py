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
                  ("qoderclicn", "linux/amd64"), ("opencode", "linux/amd64"), ("opencode", "linux/arm64"), ("zcode", "windows/amd64"),
                  ("zcode", "linux/amd64"),
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
        raise ValueError("候选适配包范围不符：Windows 为三类来源，Linux 为含 ZCode Server 的五类来源，OpenCode 控制入口按两架构分包")


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

**保持草稿与预发布状态，不公开、不推广latest。** 本候选更新Hub、Linux Bridge和Windows Tray内置Bridge；Relay沿用已验证标签，与本候选使用相同网络协议。公开推荐仍为RC4。

## 本次能力

- Hub默认每日02:00（Asia/Shanghai）获取独立签名adapter发布，时间、时区可配置或关闭；支持管理员手工检查和离线目录/ZIP导入。所有平台包就绪后才推广索引，旧包供在途下载，失败保持旧仓库。
- 镜像仓库只作首次种子，活动仓库存入持久数据目录；固定公钥及不可变包规则保持，回滚必须使用更高签名序号。
- Bridge每次启动检查一次；setup/手工检查默认最小间隔300秒，--force-sync绕过时间节流。运行中与重连不检查；离线使用可信包副本，缺包或实际接入失败明确报告未完成。
- Adapter自带名称、探测及hook配置生成的固定本机控制接口，普通新增来源、映射和配置格式无需更新Hub/Bridge。包与hook联动提交，保留用户其他配置、关闭开关及恢复日志，短提交不暴露半完成选版。
- 新增ZCode Server Linux接入，WSL和SSH共用相同Server用户路径、原生process/argv和事件转换，不由Tray代理远程hook。OpenCode按amd64/arm64携带Go控制入口，不要求目标新增运行时。
- 延续会话继续运行时清除此前未读、对象增量、正文缓存、原生身份与既有通知规则。不迁移或清空数据库，不自动终止Agent。

## 升级与产物

用户草稿附件为两架构Linux Bridge、Windows便携ZIP和SHA256SUMS。按本提交校验后使用；完整平台adapter仓库由发布者本机签名，并另行提供独立adapter草稿及离线ZIP，草稿不是匿名在线安装来源。

Hub成品为`{manifest['hub']}`，由发布者附加同提交签名仓库；Relay使用`{manifest['relay']}`。首次上线先升级核心与Bridge配置，将adapters.poll_seconds改为min_check_seconds，再导入较高序号adapter仓库。保留原公钥、来源身份、私人配置、数据卷和现行已读；停止Bridge前确认队列为空。共享Codex hook仍由WSL管理，Windows不能争抢写入。

用户安装入口仍为https://raw.githubusercontent.com/fevenor/agent-pulse/main/scripts/install/bridge.sh，默认公开版本不变；草稿使用核验后的本地附件，不要求目标编译。

后续普通adapter升级不需要重建Hub镜像或部署Bridge。实际安装和最近运行回执分别验收；hook写入不代表宿主热加载，按其规则重新加载。

## 验证边界

全模块Go竞态/静态检查、签名仓库安全、未知Agent动态接入及离线修复、平台构建和样例控制入口有回归证据。ZCode的WSL/SSH真实流程曾验证开始、继续、完成、提问和子身份过滤；新控制包在目标上的真实事件、Tray弹卡和手机实收需部署后分别确认。既有Windows PowerShell命令字符串hook在本机返回空stdout的用例未被宣称通过，不关闭防护或跳过断言。ARM模拟构建不代替ARM宿主实测，长期网络流量验收仍独立保留。

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
