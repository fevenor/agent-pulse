#!/usr/bin/env bash
# 用户安装入口：只下载或复制已构建二进制，不调用编译器或源码构建脚本。
set -euo pipefail
umask 077
usage() {
  printf '%s\n' \
    '用法：bash bridge.sh [--version v版本] [--setup] [--dry-run]' \
    '默认安装 GitHub 最新稳定发布，自动选择 Linux amd64/arm64 并校验 SHA-256。' \
    '--version 指定已发布版本，包括已公开的预发布候选版本；草稿不能匿名下载。' \
    '--setup 安装后交互选择 Agent 并启用当前用户的 systemd 服务。' \
    '测试/离线：--version v版本 --file 预构建二进制 --sha256 已核验摘要。' \
    '缺少发布物时明确失败，不回退到用户本机编译；不覆盖已有 Bridge 配置。'
}
version=latest local_file='' checksum='' setup=0 dry_run=0
while (($#)); do
  case "$1" in
    --version|--file|--sha256)
      (($# >= 2)) || { usage >&2; exit 2; }
      case "$1" in --version) version="$2" ;; --file) local_file="$2" ;; --sha256) checksum="$2" ;; esac
      shift 2 ;;
    --setup) setup=1; shift ;;
    --dry-run) dry_run=1; shift ;;
    --help|-h) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done
fail() { printf '%s\n' "$*" >&2; exit 1; }
[[ "$(uname -s)" == Linux ]] || fail '此安装入口仅支持 Linux；Windows 请下载 Tray 便携包。'
case "$(uname -m)" in
  x86_64|amd64) arch=amd64; machine=62 ;;
  aarch64|arm64) arch=arm64; machine=183 ;;
  *) fail '当前 CPU 架构尚无已发布 Bridge 二进制。' ;;
esac
if [[ "$version" != latest ]]; then
  [[ "$version" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || fail '版本格式无效。'
  version="v${version#v}"
fi
if [[ -n "$local_file" ]]; then
  [[ -f "$local_file" && "$version" != latest && "$checksum" =~ ^[[:xdigit:]]{64}$ ]] || fail '本地预构建模式必须提供已有文件、明确版本和有效 SHA-256。'
else
  [[ -z "$checksum" ]] || fail 'GitHub 下载模式自动读取发布校验清单，不接受单独传入摘要。'
fi
asset="agent-pulse-bridge-linux-$arch"
printf '平台：Linux/%s；版本：%s；资产：%s\n' "$arch" "$version" "$asset"
if ((dry_run)); then
  printf '%s\n' '预演结束：未下载、未安装、未修改配置或启动服务。'; exit 0
fi
for command in sha256sum od install mktemp mv; do
  command -v "$command" >/dev/null || fail "缺少系统工具：$command"
done
if ((setup)); then
  command -v systemctl >/dev/null || fail '交互部署需要当前用户可用的 systemd。'
  command -v curl >/dev/null || fail '健康检查需要 curl。'
  { : </dev/tty; } 2>/dev/null || fail '请在交互终端执行 --setup。'
  systemctl --user show-environment >/dev/null 2>&1 || fail '当前用户 systemd 不可用，未开始安装。'
fi
work="$(mktemp -d)"
staged=''
trap 'rm -rf -- "$work"; if [[ -n "$staged" ]]; then rm -f -- "$staged"; fi' EXIT
artifact="$work/$asset"
if [[ -n "$local_file" ]]; then
  install -m 600 "$local_file" "$artifact"
else
  command -v curl >/dev/null || fail '从 GitHub 下载需要 curl。'
  github='https://github.com/fevenor/agent-pulse'
  curl_args=(--proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 15 --max-time 180 --retry 2 --fail --location --silent --show-error)
  if [[ "$version" == latest ]]; then
    final_url="$(curl "${curl_args[@]}" --head --output /dev/null --write-out '%{url_effective}' "$github/releases/latest")" || fail '尚无可下载的稳定发布；请指定已公开的版本，不会回退到源码构建。'
    version="${final_url##*/}"
    [[ "$final_url" == "$github/releases/tag/"* && "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail '无法确认 GitHub 最新稳定版本。'
  fi
  base="$github/releases/download/$version"
  curl "${curl_args[@]}" --output "$work/SHA256SUMS.txt" "$base/SHA256SUMS.txt" || fail '发布校验清单不可下载；该版本可能仍是草稿或尚无完整附件，未安装。'
  matches=0
  while read -r hash filename extra; do
    if [[ "${filename#\*}" == "$asset" ]]; then
      [[ -z "$extra" && "$hash" =~ ^[[:xdigit:]]{64}$ ]] || fail '发布校验清单格式无效。'
      checksum="$hash"; matches=$((matches + 1))
    fi
  done < "$work/SHA256SUMS.txt"
  ((matches == 1)) || fail '发布清单缺少当前平台资产或存在重复项，未安装。'
  curl "${curl_args[@]}" --output "$artifact" "$base/$asset" || fail 'Bridge 二进制不可下载，未安装；不会调用本机编译器。'
fi
printf '%s  %s\n' "$checksum" "$artifact" | sha256sum --check --status || fail 'Bridge SHA-256 不匹配，未安装。'
header="$(od -An -tu1 -N20 -v "$artifact")"
header="${header//$'\n'/ }"
read -r -a bytes <<< "$header"
[[ ${#bytes[@]} == 20 && ${bytes[0]} == 127 && ${bytes[1]} == 69 && ${bytes[2]} == 76 && ${bytes[3]} == 70 && ${bytes[4]} == 2 && ${bytes[5]} == 1 && ${bytes[18]} == "$machine" && ${bytes[19]} == 0 ]] || fail '二进制不是当前架构的 64 位 Linux ELF，未安装。'
bin="$HOME/.local/bin"
config_dir="$HOME/.config/agent-pulse"
service_dir="$HOME/.config/systemd/user"
destination="$bin/agent-pulse-bridge"
if [[ -e "$destination" ]] && command -v systemctl >/dev/null && systemctl --user is-active --quiet agent-pulse-bridge; then
  fail 'Bridge 正在运行。请确认内存队列清空并停止用户服务后再升级。'
fi
install -d -m 700 "$bin" "$config_dir" "$service_dir"
if [[ -e "$destination" ]]; then
  backup="$(mktemp "$bin/agent-pulse-bridge.backup.XXXXXX")"
  install -m 755 "$destination" "$backup"
  printf '原二进制已备份：%s\n' "$backup"
fi
staged="$(mktemp "$bin/.agent-pulse-bridge.XXXXXX")"
install -m 755 "$artifact" "$staged"
mv -f -- "$staged" "$destination"
staged=''
service="$service_dir/agent-pulse-bridge.service"
if [[ ! -e "$service" ]]; then
  cat > "$service" <<'UNIT'
[Unit]
Description=AgentPulse 本机会话桥接
After=network-online.target

[Service]
Type=simple
ExecStart=%h/.local/bin/agent-pulse-bridge serve --config %h/.config/agent-pulse/bridge.yaml
WorkingDirectory=%h
Restart=on-failure
RestartSec=3
EnvironmentFile=-%h/.config/agent-pulse/environment
UMask=0077
NoNewPrivileges=true

[Install]
WantedBy=default.target
UNIT
  chmod 644 "$service"
fi
printf 'Bridge %s（Linux/%s）安装完成，二进制已通过校验。\n' "$version" "$arch"
if ((setup)); then
  if [[ ! -e "$config_dir/environment" ]]; then
    service_path="${PATH//\\/\\\\}"
    service_path="${service_path//\"/\\\"}"
    printf 'PATH="%s"\n' "$service_path" > "$config_dir/environment"
  fi
  "$destination" setup --config "$config_dir/bridge.yaml" </dev/tty
  systemctl --user daemon-reload
  systemctl --user enable --now agent-pulse-bridge
  for attempt in {1..10}; do
    if curl --fail --silent --show-error http://127.0.0.1:18702/healthz; then
      printf '\n%s\n' '安装与配置操作已完成，Bridge 服务已启动。请按上面的启动要求使用 Agent，并用真实事件在 Hub 验收会话与提醒。'; exit 0
    fi
    sleep 1
  done
  fail 'Bridge 健康检查未通过，请查看用户服务日志。'
fi
printf '%s\n' '本次仅安装程序，尚未配置 Agent 或启动服务。要继续配置，可运行 ~/.local/bin/agent-pulse-bridge setup。'
