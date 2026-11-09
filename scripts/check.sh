#!/usr/bin/env bash
# 需 Go、Volta 管理的 Node.js、pnpm；网络集成测试需要允许本地回环监听。
set -euo pipefail
project_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$project_root/go"
GOTOOLCHAIN=local go test -race ./...
GOTOOLCHAIN=local go vet ./...
cd "$project_root/adapters/opencode"
pnpm check
pnpm test

cd "$project_root/web/hub"
pnpm build
