#!/usr/bin/env bash
# 显式运行此脚本才安装到当前用户目录，不修改 Agent 配置或自动启用服务。
set -euo pipefail
project_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
install -d -m 700 "$HOME/.config/agent-pulse" "$HOME/.config/systemd/user" "$HOME/.local/bin"
install -m 755 "$project_root/bin/agent-pulse-bridge" "$HOME/.local/bin/agent-pulse-bridge"
if [[ ! -e "$HOME/.config/systemd/user/agent-pulse-bridge.service" ]]; then
  install -m 644 "$project_root/deploy/systemd/agent-pulse-bridge.service" "$HOME/.config/systemd/user/agent-pulse-bridge.service"
fi
# 首份配置由 setup 生成，避免示例文件使安装流程跳过 Hub 地址输入。
printf '%s\n' '安装完成。先执行 agent-pulse-bridge setup --config ~/.config/agent-pulse/bridge.yaml 选择 Agent 并确认 Hub 地址，然后执行：' 'systemctl --user daemon-reload' 'systemctl --user enable --now agent-pulse-bridge'
