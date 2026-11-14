#!/usr/bin/env bash
# 需 Go、uv、Volta 管理的 Node.js、pnpm；网络集成测试需要允许本地回环监听。
set -euo pipefail
project_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
uv run --offline --no-project python "$project_root/scripts/check/test_script_layout.py"
uv run --offline --no-project python "$project_root/scripts/check/test_install_bridge.py"
cd "$project_root/go"
GOTOOLCHAIN=local go test -race ./...
GOTOOLCHAIN=local go vet ./...
cd "$project_root/adapters/opencode"
pnpm check
pnpm test

cd "$project_root/web/hub"
pnpm test
pnpm build
