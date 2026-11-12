"""使用一次性签名密钥验证 Hub 镜像适配仓库准备流程。"""
import argparse
import importlib.util
import json
import shutil
import subprocess
import sys
import tempfile
import time
from urllib.error import URLError
from pathlib import Path
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "prepare-hub-release.py"
spec = importlib.util.spec_from_file_location("prepare_hub_release", SCRIPT)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def run(tool, *args):
    result = subprocess.run([str(tool), *map(str, args)], capture_output=True, text=True, encoding="utf-8")
    if result.returncode:
        raise AssertionError(f"命令失败：{args[0]}\n{result.stderr}")
    return result.stdout


def docker_smoke(image, expected_key):
    container = run("docker", "run", "--detach", "--publish", "127.0.0.1::18700", image).strip()
    try:
        address = run("docker", "port", container, "18700/tcp").strip().splitlines()[0]
        base = "http://" + address
        deadline = time.monotonic() + 20
        while True:
            try:
                with urlopen(base + "/healthz", timeout=1) as response:
                    health = json.load(response)
                if health.get("status") != "ok":
                    raise AssertionError(f"Hub 健康接口返回错误：{health}")
                with urlopen(base + "/api/v1/client-config", timeout=2) as response:
                    client = json.load(response)
                if client.get("public_key") != expected_key:
                    raise AssertionError("完整 RC Hub 未通过 client-config 下发随仓库公钥")
                if client.get("relay_url") or client.get("relay_token"):
                    raise AssertionError("默认 Hub 不应虚构 Relay 配置")
                return
            except (URLError, OSError):
                if time.monotonic() >= deadline:
                    status = subprocess.run(["docker", "inspect", "--format", "{{.State.Status}} {{.State.ExitCode}}", container],
                                             capture_output=True, text=True, check=False).stdout.strip()
                    logs = subprocess.run(["docker", "logs", container], capture_output=True, text=True,
                                          encoding="utf-8", check=False).stdout
                    diagnostic = ROOT / "tmp" / "hub-release-container.log"
                    diagnostic.parent.mkdir(parents=True, exist_ok=True)
                    diagnostic.write_text(f"状态：{status}\n{logs}", encoding="utf-8")
                    raise AssertionError(f"签名仓库覆盖 Hub 容器未能健康启动；诊断已写入忽略目录：{diagnostic}")
                time.sleep(0.25)
    finally:
        subprocess.run(["docker", "rm", "--force", container], capture_output=True, check=False)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tool", type=Path, help="已构建的 agent-pulse-adapters 工具")
    parser.add_argument("--docker-base", help="可选：构建生成的 Dockerfile 所用基础镜像")
    parser.add_argument("--docker-tag", default="agent-pulse-hub:release-overlay-test", help="临时覆盖镜像标签")
    parser.add_argument("--docker-smoke", action="store_true", help="启动临时容器并验证健康接口与发布公钥发现")
    args = parser.parse_args()
    tool = args.tool.resolve()
    if not tool.is_file():
        raise SystemExit(f"找不到适配包发布工具：{tool}")
    if args.docker_smoke and not args.docker_base:
        parser.error("--docker-smoke 需要同时提供 --docker-base")
    temp_root = ROOT / "tmp"
    temp_root.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="hub-release-test-", dir=temp_root) as value:
        root = Path(value)
        private, public = root / "publisher.key", root / "publisher.pub"
        source, repository = root / "source", root / "repository"
        source.mkdir()
        repository.mkdir()
        (source / "adapter").write_text("synthetic executable entry\n", encoding="utf-8")
        manifest = {
            "id": "fixture", "version": "1.0.0", "format": 1, "protocol": 1,
            "min_bridge": "0.5.0", "platform": "linux/amd64", "kind": "executable",
            "entry": "adapter", "command": "fixture", "agent_min": "1.0.0",
            "agent_max": "1.0.0", "config_style": "hooks", "hooks": {"SessionStart": ""},
        }
        manifest_path = root / "manifest.json"
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        package_path = root / "package.json"
        run(tool, "keygen", "--private", private, "--public", public)
        run(tool, "pack", "--source", source, "--manifest", manifest_path,
            "--repository", repository, "--out", package_path)
        package = json.loads(package_path.read_text(encoding="utf-8"))
        (repository / "index-input.json").write_text(
            json.dumps({"sequence": 7, "packages": [package]}), encoding="utf-8")
        run(tool, "publish", "--repository", repository, "--private", private,
            "--index", repository / "index-input.json")

        output = root / "image-context"
        base_image = args.docker_base or "example/hub:base"
        module.prepare(repository, public, base_image, output, tool)
        verified = json.loads(run(tool, "verify", "--repository", output / "repository",
                                  "--public", output / "repository" / "publisher.pub"))
        assert verified["sequence"] == 7 and len(verified["packages"]) == 1
        digest = verified["packages"][0]["sha256"]
        assert (output / "repository" / "packages" / f"{digest}.zip").read_bytes() == (
            repository / "packages" / f"{digest}.zip").read_bytes()
        dockerfile = (output / "Dockerfile").read_text(encoding="utf-8")
        assert f"FROM {base_image}\n" in dockerfile
        assert "COPY --chown=65532:65532 repository/ /repository/" in dockerfile
        assert not (output / "repository" / "publisher.key").exists()
        assert not (output / "repository" / "index-input.json").exists()
        try:
            module.prepare(repository, public, "example/hub:base", output, tool)
        except ValueError as error:
            assert "输出目录已存在" in str(error)
        else:
            raise AssertionError("不应覆盖已有镜像构建目录")
        try:
            module.prepare(repository, public, "example/hub:base", repository / "image-context", tool)
        except ValueError as error:
            assert "签名仓库内" in str(error)
        else:
            raise AssertionError("镜像输出目录不得写入签名仓库")
        if args.docker_base:
            result = subprocess.run(["docker", "build", "--tag", args.docker_tag, str(output)],
                                    capture_output=True, text=True, encoding="utf-8")
            if result.returncode:
                raise AssertionError(f"签名仓库覆盖镜像构建失败：{result.stderr}")
            if args.docker_smoke:
                docker_smoke(args.docker_tag, public.read_text(encoding="utf-8").strip())

        damaged = root / "damaged"
        shutil.copytree(repository, damaged)
        package_file = damaged / "packages" / f"{digest}.zip"
        package_file.write_bytes(package_file.read_bytes() + "损坏".encode("utf-8"))
        rejected_output = root / "rejected"
        try:
            module.prepare(damaged, public, "example/hub:base", rejected_output, tool)
        except ValueError as error:
            assert "验证失败" in str(error)
        else:
            raise AssertionError("损坏的签名仓库必须被拒绝")
        assert not rejected_output.exists(), "验签失败不应留下部分发布目录"
    print("Hub 发布镜像准备检查通过：仓库验签、不可变包复制、公开密钥随包、私钥排除、损坏拒绝及不覆盖输出。")


if __name__ == "__main__":
    main()
