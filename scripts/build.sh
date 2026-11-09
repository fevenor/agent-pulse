#!/usr/bin/env bash
# 在仓库根目录执行：bash scripts/build.sh；不修改 Git 状态。
set -euo pipefail
project_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p "$project_root/bin"
cd "$project_root/go"
GOTOOLCHAIN=local go build -trimpath -buildvcs=false -o "$project_root/bin/" ./cmd/agent-pulse-hub ./cmd/agent-pulse-bridge ./cmd/agent-pulse-client ./cmd/agent-pulse-relay ./cmd/agent-pulse-adapters ./cmd/agent-pulse-push-test

for adapter_id in codex antigravity qoderclicn; do
  GOTOOLCHAIN=local go build -trimpath -buildvcs=false -ldflags "-s -w -X main.adapterID=$adapter_id" -o "$project_root/bin/agent-pulse-adapter-$adapter_id" ./cmd/agent-pulse-adapter
done
