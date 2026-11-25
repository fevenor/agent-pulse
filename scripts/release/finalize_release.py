"""核验同版本 Workflow 产物与已签名适配仓库后公开 Release；不读取发布私钥。"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import zipfile


ASSETS = {"agent-pulse-bridge-linux-amd64", "agent-pulse-bridge-linux-arm64", "windows-amd64.zip", "SHA256SUMS.txt"}


def command(*args):
    result = subprocess.run(list(map(str, args)), capture_output=True, text=True, encoding="utf-8")
    if result.returncode:
        raise RuntimeError(f"命令失败：{args[0]}\n{result.stderr.strip()}")
    return result.stdout


def verify_assets(directory):
    if {p.name for p in directory.iterdir()} != ASSETS:
        raise ValueError("用户附件清单不完整或混入维护材料")
    expected = {}
    for line in (directory / "SHA256SUMS.txt").read_text().splitlines():
        parts = line.split()
        if len(parts) != 2 or not re.fullmatch(r"[a-f0-9]{64}", parts[0]) or parts[1] in expected:
            raise ValueError("附件校验清单无效")
        expected[parts[1]] = parts[0]
    if set(expected) != ASSETS - {"SHA256SUMS.txt"}:
        raise ValueError("附件校验清单范围错误")
    for name, digest in expected.items():
        if hashlib.sha256((directory / name).read_bytes()).hexdigest() != digest:
            raise ValueError("附件摘要不符：" + name)
    with zipfile.ZipFile(directory / "windows-amd64.zip") as archive:
        if archive.testzip() or set(archive.namelist()) != {"agent-pulse-tray.exe", "bridge.example.yaml", "tray.example.yaml", "使用说明.md"}:
            raise ValueError("Windows 单入口便携包无效")
    return expected


def verify_image_metadata(metadata, commit):
    if metadata[0]["Config"].get("Labels", {}).get("org.opencontainers.image.revision") != commit:
        raise ValueError("镜像构建提交不匹配")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("repository", "tag", "commit", "adapter-release", "tool", "output"):
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", args.repository) or not re.fullmatch(r"[0-9a-f]{40}", args.commit):
        raise ValueError("仓库或提交无效")
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-rc\.[1-9][0-9]*)?", args.tag) or not re.fullmatch(r"adapters-[1-9][0-9]*", args.adapter_release):
        raise ValueError("发布标签无效")
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=False)
    fields = "isDraft,isPrerelease,tagName,targetCommitish,assets,url"
    release = json.loads(command("gh", "release", "view", args.tag, "--repo", args.repository, "--json", fields))
    if not release["isDraft"] or release["targetCommitish"] != args.commit:
        raise ValueError("只公开当前提交已完成构建的草稿，不覆盖公开版本")
    adapter = json.loads(command("gh", "release", "view", args.adapter_release, "--repo", args.repository, "--json", fields))
    if adapter["isDraft"] or adapter["targetCommitish"] != args.commit:
        raise ValueError("适配仓库必须已公开且属于同一提交")
    assets = output / "assets"
    command("gh", "release", "download", args.tag, "--repo", args.repository, "--dir", assets)
    checksums = verify_assets(assets)
    images = {}
    for component in ("hub", "relay"):
        image = "ghcr.io/" + args.repository.lower() + "-" + component + ":" + args.tag
        manifest = json.loads(command("docker", "buildx", "imagetools", "inspect", "--raw", image))
        platforms = {(p.get("platform", {}).get("os"), p.get("platform", {}).get("architecture")) for p in manifest.get("manifests", [])}
        if platforms - {("unknown", "unknown")} != {("linux", "amd64"), ("linux", "arm64")}:
            raise ValueError("镜像必须包含 Linux amd64/arm64")
        command("docker", "pull", "--platform", "linux/amd64", image)
        metadata = json.loads(command("docker", "image", "inspect", image))
        verify_image_metadata(metadata, args.commit)
        images[component] = {"tag": image, "digest": metadata[0]["RepoDigests"][0]}
    container = command("docker", "create", images["hub"]["tag"]).strip()
    repository = output / "repository"
    try:
        command("docker", "cp", container + ":/repository", repository)
    finally:
        command("docker", "rm", container)
    signed = json.loads(command(args.tool, "verify", "--repository", repository, "--public", repository / "publisher.pub"))
    if args.adapter_release != "adapters-" + str(signed["sequence"]):
        raise ValueError("Hub 镜像与适配发布序号不一致")
    adapter_index = output / "adapter-index"
    command("gh", "release", "download", args.adapter_release, "--repo", args.repository, "--pattern", "index.json", "--dir", adapter_index)
    if (adapter_index / "index.json").read_bytes() != (repository / "index.json").read_bytes():
        raise ValueError("Hub 种子与公开签名索引不一致")
    prerelease = "-rc." in args.tag
    command("gh", "release", "edit", args.tag, "--repo", args.repository, "--draft=false",
            "--prerelease=" + str(prerelease).lower(), "--latest=" + str(not prerelease).lower(), "--title", "AgentPulse " + args.tag)
    public = json.loads(command("gh", "release", "view", args.tag, "--repo", args.repository, "--json", fields))
    if public["isDraft"] or public["isPrerelease"] != prerelease:
        raise ValueError("发布状态未生效")
    result = {"tag": args.tag, "commit": args.commit, "url": public["url"], "published": True,
              "prerelease": prerelease, "checksums": checksums, "images": images, "adapter_release": args.adapter_release}
    (output / "release-result.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
