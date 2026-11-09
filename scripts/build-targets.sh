#!/usr/bin/env bash
# 交叉编译只能证明编译通过，不能代替目标机器运行验收。
set -euo pipefail
project_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$project_root/go"
for target in linux/amd64 linux/arm64 windows/amd64; do
  target_os="${target%/*}"
  target_arch="${target#*/}"
  directory="$project_root/dist/$target_os-$target_arch"
  mkdir -p "$directory"
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" GOTOOLCHAIN=local go build -trimpath -buildvcs=false -ldflags '-s -w' -o "$directory/" ./cmd/agent-pulse-hub ./cmd/agent-pulse-relay ./cmd/agent-pulse-bridge ./cmd/agent-pulse-client
done
# Windows ARM64 只交付本地 Bridge/测试客户端；Hub 和 Relay 运行在 Linux。
mkdir -p "$project_root/dist/windows-arm64"
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 GOTOOLCHAIN=local go build -trimpath -buildvcs=false -ldflags '-s -w' -o "$project_root/dist/windows-arm64/" ./cmd/agent-pulse-bridge ./cmd/agent-pulse-client
