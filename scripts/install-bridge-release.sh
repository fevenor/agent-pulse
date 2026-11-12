#!/usr/bin/env bash
# 发布版用户安装器：用户显式指定下载 URL、版本和校验值，不推测尚未发布的资产地址。
set -euo pipefail

usage() {
  printf '%s\n' \
    '用法：bash install-bridge-release.sh --version 版本 --url 二进制地址 --sha256 SHA256值' \
    '安装到当前用户目录；不自动启用服务，也不覆盖现有配置。' \
    '请从项目正式发布页获取与 Linux 架构相符的二进制及其 SHA-256。'
}

version='' url='' checksum=''
while (($#)); do
  case "$1" in
    --version|--url|--sha256)
      if (($# < 2)); then usage >&2; exit 2; fi
      case "$1" in
        --version) version="$2" ;;
        --url) url="$2" ;;
        --sha256) checksum="$2" ;;
      esac
      shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done
if [[ -z "$version" || -z "$url" || ! "$checksum" =~ ^[[:xdigit:]]{64}$ || ! "$url" =~ ^https:// ]]; then
  usage >&2
  exit 2
fi
for command in curl sha256sum install mktemp; do
  command -v "$command" >/dev/null || { printf '缺少命令：%s\n' "$command" >&2; exit 1; }
done

archive="$(mktemp)"
trap 'rm -f -- "$archive"' EXIT
curl --fail --location --silent --show-error --output "$archive" "$url"
printf '%s  %s\n' "$checksum" "$archive" | sha256sum --check --status || {
  printf '%s\n' '发布包 SHA-256 不匹配，未安装。' >&2
  exit 1
}
install -d -m 700 "$HOME/.config/agent-pulse" "$HOME/.config/systemd/user" "$HOME/.local/bin"
install -m 755 "$archive" "$HOME/.local/bin/agent-pulse-bridge"
service="$HOME/.config/systemd/user/agent-pulse-bridge.service"
if [[ ! -e "$service" ]]; then
  cat >"$service" <<'UNIT'
[Unit]
Description=AgentPulse 本机会话桥接
After=network-online.target

[Service]
Type=simple
ExecStart=%h/.local/bin/agent-pulse-bridge serve --config %h/.config/agent-pulse/bridge.yaml
WorkingDirectory=%h
Restart=on-failure
RestartSec=3
UMask=0077
NoNewPrivileges=true

[Install]
WantedBy=default.target
UNIT
  chmod 644 "$service"
fi
printf '已安装 Bridge %s。执行 ~/.local/bin/agent-pulse-bridge setup 后按需执行 systemctl --user daemon-reload && systemctl --user enable --now agent-pulse-bridge。\n' "$version"
