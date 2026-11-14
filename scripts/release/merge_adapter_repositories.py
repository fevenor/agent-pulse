"""合并各平台本地构建的适配包仓库，生成未签名索引；不读取发布私钥。"""

import argparse
import hashlib
import io
import json
import os
import re
import tempfile
import zipfile
from pathlib import Path

MAX_PACKAGE_BYTES = 32 << 20


def plugin_files(data):
    """通用 JS 包仅比较文件内容，忽略不同构建平台产生的 ZIP 时间与权限。"""
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        entries = archive.infolist()
        if sum(entry.file_size for entry in entries) > MAX_PACKAGE_BYTES:
            raise ValueError("通用插件解压后超过 32 MiB")
        if len({entry.filename for entry in entries}) != len(entries):
            raise ValueError("通用插件含重复文件名")
        return {entry.filename: archive.read(entry) for entry in entries if not entry.is_dir()}


def merge(repositories, output, sequence):
    if not 0 < sequence < 2**64:
        raise ValueError("发布序号必须为有效正整数")
    selected = {}
    for repository in repositories:
        index = json.loads((repository / "index-input.json").read_text(encoding="utf-8-sig"))
        for package in index["packages"]:
            digest = package["sha256"]
            if not isinstance(digest, str) or not re.fullmatch(r"[a-f0-9]{64}", digest):
                raise ValueError("适配包摘要无效")
            path = repository / "packages" / (digest + ".zip")
            if not 0 < path.stat().st_size <= MAX_PACKAGE_BYTES:
                raise ValueError("适配包大小无效")
            data = path.read_bytes()
            if len(data) != package["size"] or hashlib.sha256(data).hexdigest() != digest:
                raise ValueError("适配包大小或摘要不匹配")
            key = (package["id"], package["version"], package["platform"])
            if key in selected:
                previous, previous_data = selected[key]
                if previous == package:
                    continue
                manifest = lambda p: {k: v for k, v in p.items() if k not in ("sha256", "size")}
                if (package["kind"] == "opencode-plugin" and package["platform"] == "any"
                        and manifest(previous) == manifest(package)
                        and plugin_files(previous_data) == plugin_files(data)):
                    # 等价通用包复用第一个仓库的既有不可变 ZIP，不重新打包。
                    continue
                raise ValueError(f"同版本适配包冲突：{'/'.join(key)}；内容变更须提升版本")
            selected[key] = (package, data)
    if not selected or len(selected) > 128:
        raise ValueError("索引必须包含 1 至 128 个适配包")
    output.mkdir(parents=True, exist_ok=True)
    (output / "packages").mkdir(exist_ok=True)

    def atomic_write(path, data):
        with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as handle:
            temporary = Path(handle.name)
            try:
                handle.write(data)
                handle.flush()
                os.fsync(handle.fileno())
            except BaseException:
                handle.close()
                temporary.unlink(missing_ok=True)
                raise
        try:
            os.replace(temporary, path)
        finally:
            temporary.unlink(missing_ok=True)

    packages = []
    for key in sorted(selected):
        package, data = selected[key]
        destination = output / "packages" / (package["sha256"] + ".zip")
        if destination.exists():
            if destination.read_bytes() != data:
                raise ValueError("输出仓库的既有不可变包已损坏")
        else:
            atomic_write(destination, data)
        packages.append(package)
    result = {"sequence": sequence, "packages": packages}
    atomic_write(output / "index-input.json", (json.dumps(result, ensure_ascii=False, indent=2) + "\n").encode())
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("repositories", nargs="+", type=Path, help="含 index-input.json 的输入仓库，通用包优先保留第一个")
    parser.add_argument("--output", required=True, type=Path, help="合并后的本地仓库")
    parser.add_argument("--sequence", required=True, type=int, help="后续签名使用的递增发布序号")
    args = parser.parse_args()
    try:
        result = merge(args.repositories, args.output, args.sequence)
    except (OSError, ValueError, KeyError, TypeError, zipfile.BadZipFile) as error:
        parser.exit(1, f"合并失败：{error}\n")
    print(f"已生成未签名索引，共 {len(result['packages'])} 个适配包；发布前使用已有签名工具校验并签名。")
