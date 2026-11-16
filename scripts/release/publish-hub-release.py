"""发布同候选完整 Hub 成品；沿用本机签名与现有 GHCR 登录，不读取私钥。"""
import argparse
import importlib.util
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("hub_prepare", ROOT / "scripts/release/prepare-hub-release.py")
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)


def release_target(manifest):
    commit = manifest.get("commit", "")
    tag = manifest.get("tag", "")
    image = manifest.get("hub", "")
    if not re.fullmatch(r"[0-9a-f]{40}", commit) or not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+-rc\.[1-9][0-9]*", tag):
        raise ValueError("须提供已确认 RC 草稿的完整构建提交及标签")
    if not re.fullmatch(r"ghcr\.io/[a-z0-9][a-z0-9_-]*/agent-pulse-hub:" + re.escape(tag), image):
        raise ValueError("只发布横杠命名的 agent-pulse-hub 成品，不发布 hub-base 或旧路径")
    if not manifest.get("hub_build", {}).get("publisher_only"):
        raise ValueError("构建清单缺少发布者专用 Hub 构建")
    return image, image.rsplit(":", 1)[0] + ":sha-" + commit


def verify_platforms(raw):
    platforms = {(x.get("platform", {}).get("os"), x.get("platform", {}).get("architecture"))
                 for x in raw.get("manifests", [])}
    if platforms - {("unknown", "unknown")} != {("linux", "amd64"), ("linux", "arm64")}:
        raise ValueError("远端成品不是完整的 Linux amd64/arm64 双架构索引")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("manifest", "repository", "public", "archive", "tool", "output"):
        parser.add_argument("--" + name, required=True, type=Path)
    args = parser.parse_args()
    manifest = json.loads(args.manifest.read_text(encoding="utf-8"))
    image, immutable = release_target(manifest)
    index = prepare.prepare(args.repository, args.public, "hub-build", args.output, args.tool,
                            args.archive, manifest["hub_build"]["sha256"])
    if index["packages"] != manifest["packages"]:
        raise ValueError("签名仓库不是该候选的完整平台包构建，不发布混合产物")
    context = str(args.output.resolve())
    metadata = args.output.resolve() / "publish-metadata.json"
    subprocess.run(["docker", "buildx", "build", "--platform", "linux/amd64,linux/arm64",
                    "--build-context", "hub-build=oci-layout://" + str((args.output / "hub-build").resolve()),
                    "--label", "org.opencontainers.image.revision=" + manifest["commit"],
                    "--label", "org.opencontainers.image.source=https://github.com/" + image.split("/")[1] + "/agent-pulse",
                    "--tag", image, "--tag", immutable, "--metadata-file", str(metadata), "--push", context], check=True)
    digest = json.loads(metadata.read_text())["containerimage.digest"]
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
        raise ValueError("发布返回的镜像摘要无效")
    raw = subprocess.check_output(["docker", "buildx", "imagetools", "inspect", "--raw",
                                   image.rsplit(":", 1)[0] + "@" + digest], text=True, encoding="utf-8")
    verify_platforms(json.loads(raw))
    result = {"image": image, "immutable_image": immutable, "digest": digest,
              "commit": manifest["commit"], "platforms": ["linux/amd64", "linux/arm64"],
              "signed_index_sequence": index["sequence"], "remote_manifest_verified": True}
    (args.output / "publish-result.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
