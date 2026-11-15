"""验证现有签名仓库，准备附带适配包的 Hub 镜像构建目录；不签名、不构建、不发布。"""

import argparse
import hashlib
import tarfile
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import time


def rename_prepared_directory(staging: Path, output: Path):
    """同一输出父目录内原子改名；Windows 短时占用最多等待 1.5 秒，不覆盖已有目标。"""
    staging, output = staging.resolve(), output.resolve()
    if staging.parent != output.parent:
        raise ValueError("发布暂存目录与输出必须位于同一父目录")
    for attempt in range(5):
        if output.exists():
            raise ValueError("输出目录已存在，不覆盖已有发布产物")
        try:
            os.rename(staging, output)
            return
        except PermissionError as error:
            # 只对 Windows 的访问/共享占用错误短时重试；其余错误及耗尽后保留原异常。
            if getattr(error, "winerror", None) not in (5, 32, 33) or attempt == 4:
                raise
            time.sleep(0.1 * (2 ** attempt))


def prepare(repository: Path, public_key: Path, base_image: str, output: Path, tool: Path, base_archive: Path | None = None, base_sha256: str = ""):
    """只复制签名索引、公钥和引用包，拒绝覆盖已有输出；复用 Go 发布工具验签。"""
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._/:@-]*", base_image):
        raise ValueError("基准镜像名称无效，不能包含空格、换行或 Dockerfile 表达式")
    repository, public_key, output, tool = (path.resolve() for path in (repository, public_key, output, tool))
    if output.exists():
        raise ValueError("输出目录已存在，请选择新的目录，不覆盖已有发布产物")
    if output == repository or repository in output.parents:
        raise ValueError("输出目录不能位于签名仓库内")
    if not repository.is_dir() or not public_key.is_file() or not tool.is_file():
        raise ValueError("签名仓库、公钥文件和已构建的发布工具必须存在")

    def verify(directory, public):
        result = subprocess.run([str(tool), "verify", "--repository", str(directory), "--public", str(public)], capture_output=True, text=True, encoding="utf-8")
        if result.returncode:
            raise ValueError("签名仓库验证失败：" + result.stderr.strip())
        index = json.loads(result.stdout)
        if not index["packages"]:
            raise ValueError("发布仓库没有适配包，不能准备正式 Hub 镜像")
        return index

    if base_image == "hub-build" and base_archive is None:
        raise ValueError("Hub 内部构建需提供 OCI 归档及校验清单摘要，不从同名远端镜像隐式回退")
    if base_archive is not None:
        if base_image != "hub-build" or not re.fullmatch(r"[a-f0-9]{64}", base_sha256):
            raise ValueError("OCI 构建必须使用 hub-build 上下文和发布清单中的 SHA256")
        base_archive = base_archive.resolve()
        if not base_archive.is_file() or hashlib.sha256(base_archive.read_bytes()).hexdigest() != base_sha256:
            raise ValueError("Hub 内部构建归档摘要不符")
    index = verify(repository, public_key)
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".hub-release-", dir=output.parent) as temporary:
        staging = Path(temporary)
        target = staging / "repository"
        (target / "packages").mkdir(parents=True)
        shutil.copyfile(repository / "index.json", target / "index.json")
        shutil.copyfile(public_key, target / "publisher.pub")
        for package in index["packages"]:
            filename = package["sha256"] + ".zip"
            shutil.copyfile(repository / "packages" / filename, target / "packages" / filename)
        if base_archive is not None:
            # OCI 文件仅作为 Buildx 命名上下文，不能包含路径越界、设备或符号链接。
            build = staging / "hub-build"
            build.mkdir()
            with tarfile.open(base_archive) as archive:
                for member in archive.getmembers():
                    relative = Path(member.name)
                    if relative.is_absolute() or ".." in relative.parts or not (member.isfile() or member.isdir()):
                        raise ValueError("Hub 内部归档包含非法路径或文件类型")
                archive.extractall(build, filter="data")
            if not (build / "oci-layout").is_file() or not (build / "index.json").is_file():
                raise ValueError("Hub 内部归档不是 OCI 镜像布局")
        # 对复制结果再次验签和核对摘要，避免源目录在准备过程中变化。
        copied = verify(target, target / "publisher.pub")
        if copied != index:
            raise ValueError("准备期间发布索引发生变化，请使用不可变的签名仓库重试")
        (staging / "Dockerfile").write_text(
            "# 基准 Hub 镜像须包含默认配置；仅附加已验证的公开发布仓库。\n"
            + "FROM " + base_image + "\n"
            + "COPY --chown=65532:65532 repository/ /repository/\n", encoding="utf-8")
        # 容器以非 root 身份运行；构建上下文不保留发布者私有文件权限。
        for path in staging.rglob("*"):
            path.chmod(0o755 if path.is_dir() else 0o644)
        rename_prepared_directory(staging, output)
    return index


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True, type=Path, help="包含 index.json 和 packages/ 的完整签名仓库")
    parser.add_argument("--public", required=True, type=Path, help="发布者公钥 publisher.pub；不读取私钥")
    parser.add_argument("--base-image", default="hub-build", help="发布者已有基准镜像或默认 OCI 命名上下文 hub-build")
    parser.add_argument("--base-archive", type=Path, help="同一候选的 hub-build.tar，仅发布者使用")
    parser.add_argument("--base-sha256", default="", help="候选构建清单中的 Hub 构建摘要")
    parser.add_argument("--output", required=True, type=Path, help="新的独立 Docker 构建目录")
    parser.add_argument("--tool", required=True, type=Path, help="已构建的 agent-pulse-adapters 发布工具")
    args = parser.parse_args()
    try:
        index = prepare(args.repository, args.public, args.base_image, args.output, args.tool, args.base_archive, args.base_sha256)
    except (OSError, ValueError, KeyError, TypeError) as error:
        parser.exit(1, f"准备失败：{error}\n")
    print(f"已准备序号 {index['sequence']} 的完整签名仓库，共 {len(index['packages'])} 个包。")
    if args.base_archive:
        print("Buildx 命名上下文：hub-build=oci-layout://" + str((args.output / "hub-build").resolve()))
    print("输出目录可作为 Docker 构建上下文；尚未构建、推送镜像或发布任何产物。")


if __name__ == "__main__":
    main()
