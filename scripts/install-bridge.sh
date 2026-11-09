#!/usr/bin/env bash
# 显式运行此脚本才安装到当前用户目录，不修改 Agent 配置或自动启用服务。
set -euo pipefail
project_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
install -d -m 700 "$HOME/.config/agent-pulse" "$HOME/.config/systemd/user" "$HOME/.local/bin"
install -m 755 "$project_root/bin/agent-pulse-bridge" "$HOME/.local/bin/agent-pulse-bridge"
install -m 644 "$project_root/deploy/systemd/agent-pulse-bridge.service" "$HOME/.config/systemd/user/agent-pulse-bridge.service"
if [[ ! -e "$HOME/.config/agent-pulse/bridge.yaml" ]]; then
  install -m 600 "$project_root/config/bridge.example.yaml" "$HOME/.config/agent-pulse/bridge.yaml"
fi
printf '%s\n' '安装完成。请填写 source_id、hub_url 及适配器配置，然后执行：' 'systemctl --user daemon-reload' 'systemctl --user enable --now agent-pulse-bridge'
