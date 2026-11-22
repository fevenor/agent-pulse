"""准备签名适配仓库离线包；显式 --publish 时发布独立GitHub附件，不修改产品latest。"""
import argparse
import json
from pathlib import Path
import re
import subprocess
import zipfile


def command(*args):
    result = subprocess.run(list(map(str, args)), capture_output=True, text=True, encoding="utf-8")
    if result.returncode:
        raise RuntimeError(f"命令执行失败：{args[0]}\n{result.stderr.strip()}")
    return result.stdout


def prepare(repository, public, publisher, output):
    """复用Go验签器，私钥不进入产物；包内容不执行。"""
    index = json.loads(command(publisher, "verify", "--repository", repository, "--public", public))
    paths = [repository / "index.json"]
    for package in index["packages"]:
        path = repository / "packages" / (package["sha256"] + ".zip")
        if path not in paths:
            paths.append(path)
    if output.exists():
        raise ValueError("离线输出已存在，不覆盖")
    output.parent.mkdir(parents=True, exist_ok=True)
    try:
        with zipfile.ZipFile(output, "x", compression=zipfile.ZIP_STORED) as archive:
            for path in paths:
                archive.write(path, str(path.relative_to(repository)).replace("\\", "/"))
    except Exception:
        output.unlink(missing_ok=True)
        raise
    return index, paths


def publish(github, commit, index, paths, bundle, draft=False):
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", github):
        raise ValueError("GitHub仓库必须为owner/repository")
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("必须提供明确的40位发布构建提交")
    tag = "adapters-" + str(index["sequence"])
    # 不覆盖既有发布或附件；新内容只能使用更高序号和新包版本。
    existing = subprocess.run(["gh", "release", "view", tag, "--repo", github], capture_output=True)
    if existing.returncode == 0:
        raise ValueError("适配仓库发布已存在，不覆盖；请使用更高序号")
    notes = ("完整签名适配仓库。Hub可手工或按每日配置时刻获取；Bridge仅在启动、setup或显式同步时应用。\n"
             "离线ZIP导入与在线附件共用验签和序号规则，不能用归档公钥替换已信任公钥。\n"
             "本发布不推广产品latest，宿主真实采集与提醒实收需单独验收。")
    flags = ["--draft"] if draft else []
    command("gh", "release", "create", tag, *paths, bundle, *flags, "--repo", github,
            "--target", commit, "--prerelease", "--latest=false", "--title", "AgentPulse 适配仓库 " + str(index["sequence"]),
            "--notes", notes)
    result = json.loads(command("gh", "release", "view", tag, "--repo", github,
                                "--json", "isDraft,tagName,assets,url"))
    expected = {path.name: path.stat().st_size for path in [*paths, bundle]}
    actual = {asset["name"]: asset["size"] for asset in result["assets"]}
    if result["isDraft"] != draft or result["tagName"] != tag or actual != expected or len(actual) != len(result["assets"]):
        raise ValueError("GitHub公开附件清单或大小核验失败；不要报告已完成分发")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True, type=Path)
    parser.add_argument("--public", required=True, type=Path)
    parser.add_argument("--publisher", required=True, type=Path, help="已构建的agent-pulse-adapters")
    parser.add_argument("--output", required=True, type=Path)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--publish", action="store_true", help="明确授权创建公开独立发布及标签")
    mode.add_argument("--draft", action="store_true", help="创建独立发布草稿，不公开、不推广latest")
    parser.add_argument("--github", default="fevenor/agent-pulse")
    parser.add_argument("--commit", default="")
    args = parser.parse_args()
    index, paths = prepare(args.repository.resolve(), args.public.resolve(), args.publisher.resolve(), args.output.resolve())
    result = {"sequence": index["sequence"], "offline_bundle": str(args.output.resolve()), "published": False}
    if args.publish or args.draft:
        result.update(publish(args.github, args.commit, index, paths, args.output.resolve(), draft=args.draft))
        result["published"] = args.publish
        result["draft"] = args.draft
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
