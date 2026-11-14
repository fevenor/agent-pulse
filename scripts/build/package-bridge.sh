#!/usr/bin/env bash
# 发布维护者构建 Linux Bridge；用户安装器只消费这些预构建产物。
set -euo pipefail
project_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
output="${1:-$project_root/dist/bridge-linux}"
[[ "$(go env GOVERSION)" == go1.27.4 ]] || { printf '%s\n' '发布构建需由开发方准备 Go 1.27.4，用户安装不需要 Go。' >&2; exit 1; }
[[ ! -e "$output" ]] || { printf '%s\n' '输出目录已存在，请使用新目录，避免覆盖发布物。' >&2; exit 1; }
mkdir -p "$(dirname -- "$output")"
output="$(cd -- "$(dirname -- "$output")" && pwd)/$(basename -- "$output")"
staging="$(mktemp -d "$(dirname -- "$output")/.bridge-build.XXXXXX")"
trap 'rm -rf -- "$staging"' EXIT
for arch in amd64 arm64; do
  (
    cd "$project_root/go"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags '-s -w' -o "$staging/agent-pulse-bridge-linux-$arch" ./cmd/agent-pulse-bridge
  )
  go version -m "$staging/agent-pulse-bridge-linux-$arch" > "$staging/build-info-$arch.txt"
done
(cd "$staging" && sha256sum agent-pulse-bridge-linux-amd64 agent-pulse-bridge-linux-arm64 > SHA256SUMS.txt)
mv -- "$staging" "$output"
printf '已构建两种 Linux Bridge 与校验清单：%s；尚未上传或安装。\n' "$output"
