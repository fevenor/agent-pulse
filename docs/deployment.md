# 部署指南

本文面向首次部署的使用者和执行部署任务的 Agent。按顺序完成 Hub、适配包、Bridge；需要跨公网访问时再配置 Relay。所有地址、目录、主机标识、镜像标签和公钥示例都需要替换，不使用开发者的私人环境作为默认值。

## 组件与准备

| 组件 | 部署位置 | 所需环境 | 配置与持久数据 |
| --- | --- | --- | --- |
| Hub（会话中心） | 一台可信内网服务器 | Docker Engine 和 Compose 插件 | `hub.runtime.yaml`；独立数据卷保存 SQLite；`repository/` 分发适配包 |
| Bridge（本机接入） | 每台运行 Agent 的 Linux 主机或 Windows 环境 | Linux 用户 systemd；Go 1.25 用于源码构建 | `bridge.yaml`；适配包与安装状态目录；事件队列只在内存 |
| Relay（公网中继，可选） | 公网服务器 | Docker/Compose、域名及支持 WebSocket 的 HTTPS 代理 | `relay.runtime.yaml`；独立数据卷保存待转交事件 |
| Tray（桌面通知） | Windows 桌面 | 已构建的 Windows 便携包 | 程序旁 `tray.yaml` 和 `data/`；可托管本机 Bridge |

开始前确定：Hub 内网地址、各主机唯一 `source_id`、目标架构、运行的 Agent 及版本、是否需要 Relay、手机渠道请求格式。已有服务先检查并备份配置和数据，避免重复创建 Hub、重新生成签名私钥或覆盖 Agent 配置。获取源码、提交或推送等 Git 操作按使用者授权范围执行。

部署 Hub/Relay 已发布镜像不需要 Go、Node.js 或 Rust。构建 Linux Bridge 只需要 Go；构建适配包需要 Go 和 Volta 管理的 Node.js/pnpm。`scripts/package-adapters.sh` 当前产出 Linux amd64 适配包，不能直接用于 arm64；其他架构的构建边界见 [适配器文档](adapters.md)。

## 1. 部署 Hub

在已有源码目录中执行以下命令，创建独立部署目录；不要覆盖已有目录中的配置：

```sh
export PULSE_HUB_DIR="$HOME/agent-pulse-hub"
install -d -m 700 "$PULSE_HUB_DIR"
install -d -m 755 "$PULSE_HUB_DIR/repository"
cp deploy/compose/hub.image.yaml "$PULSE_HUB_DIR/compose.yaml"
cp deploy/compose/hub.runtime.yaml "$PULSE_HUB_DIR/hub.runtime.yaml"
```

在该目录创建权限为 600 的 `hub.env`：

```dotenv
AGENT_PULSE_HUB_IMAGE=ghcr.io/fevenor/agent-pulse/hub:sha-替换为已发布完整提交哈希
HUB_BIND_IP=替换为服务器可信内网IP
```

从 [镜像工作流](https://github.com/fevenor/agent-pulse/actions/workflows/images.yml) 选择已成功发布的提交，Hub/Relay 尽量使用同一提交标签；标签规则见 [镜像说明](images.md)。`latest` 可用于初次试用，长期部署宜固定版本。不要把文档示例中的占位标签当成可拉取版本。私有 GHCR 镜像需预先具备读取授权。

`hub.runtime.yaml` 默认配置 `/data/hub.db`、内置网页 `/web` 和适配包路径 `/repository`，手机推送关闭。初次尚无签名索引时，适配包接口返回 404，完成下一节后恢复。启动：

```sh
chmod 600 "$PULSE_HUB_DIR/hub.env"
docker compose --env-file "$PULSE_HUB_DIR/hub.env" -f "$PULSE_HUB_DIR/compose.yaml" config --quiet
docker compose --env-file "$PULSE_HUB_DIR/hub.env" -f "$PULSE_HUB_DIR/compose.yaml" pull
docker compose --env-file "$PULSE_HUB_DIR/hub.env" -f "$PULSE_HUB_DIR/compose.yaml" up -d
```

访问 `http://服务器内网IP:18700/healthz` 应返回 `status: ok`，根路径应显示管理页面。可信个人内网可不设 Hub token；公网入口必须另有访问控制，不应把免 token 的 Hub 直接暴露到公网。

若需要 Hub token，在 `hub.env` 加入 `AGENT_PULSE_TOKEN`，运行配置加 `token: "${env:AGENT_PULSE_TOKEN}"`，客户端配置相同 token。Compose 运行配置需要容器用户 65532 可读；秘密优先放在宿主 600 权限的环境文件中。`--env-file` 不会自动将所有变量传进容器，使用新增变量时必须同步配置 Compose 的 `environment`。

## 2. 发布适配包

Hub 负责分发，不负责自动生成签名包。在一台发布者主机上构建并签名，然后复制公开产物到 Hub；发布私钥只留在发布者机器，后续更新沿用该私钥并递增序号。

在源码根目录执行（首次创建密钥，已有密钥不能重复生成）：

```sh
pnpm --dir adapters/opencode install --frozen-lockfile --ignore-scripts
bash scripts/package-adapters.sh ./dist/repository 1
install -d -m 700 "$HOME/.config/agent-pulse-publisher"
./bin/agent-pulse-adapters keygen \
  --private "$HOME/.config/agent-pulse-publisher/publisher.key" \
  --public "$HOME/.config/agent-pulse-publisher/publisher.pub"
./bin/agent-pulse-adapters publish --repository ./dist/repository \
  --private "$HOME/.config/agent-pulse-publisher/publisher.key" \
  --index ./dist/repository/index-input.json
```

将 `dist/repository/packages/` 和 `index.json` 复制到 Hub 部署目录的 `repository/`，文件及目录需容器用户可读。若两者在同一主机：

```sh
cp -R dist/repository/packages "$PULSE_HUB_DIR/repository/"
cp dist/repository/index.json "$PULSE_HUB_DIR/repository/index.json"
chmod -R a+rX "$PULSE_HUB_DIR/repository"
```

远程部署使用自己的安全传输方式；先复制不可变包，最后原子替换 `index.json`，避免更新过程中客户端读到不完整索引。首次部署完成后，Hub 的 `/api/v1/adapters/index` 应返回 `payload` 和 `signature`。把 `publisher.pub` 的一行 Base64 公钥交给每台 Bridge，无需向 Hub 或 Bridge 分发私钥。

也可使用 GitHub Actions 的 `linux-adapters` 和 `windows-amd64` 产物，合并后再用自己的私钥签名；产物不是已信任的发布索引。具体合并命令、平台限制及更新规则见 [适配包发布](adapters.md#发布者构建与签名)。普通适配器更新只需发布更高序号的兼容包，不用逐台重装 Bridge；最低 Bridge 版本发生变化时仍需升级 Bridge。

## 3. 在 Linux 主机安装 Bridge

在该主机的源码根目录构建并安装：

```sh
bash scripts/build.sh
bash scripts/install-bridge.sh
```

安装脚本不会启用服务或自动修改 Agent。编辑 `~/.config/agent-pulse/bridge.yaml`，替换下面所有占位值；路径必须是本机绝对路径，仅列出需要授权安装的 Agent：

```yaml
listen: "127.0.0.1:18702"
source_id: "my-linux"
hub_url: "http://服务器内网IP:18700"
adapters:
  directory: "/home/your-user/.local/share/agent-pulse/adapters"
  public_key: "替换为 publisher.pub 的完整一行"
  poll_seconds: 300
  managed:
    codex: "/home/your-user/.codex/hooks.json"
    qoderclicn: "/home/your-user/.qoder-cn/settings.json"
    antigravity: "/home/your-user/.gemini/config/hooks.json"
    opencode: "/home/your-user/.config/opencode/opencode.json"
```

已有 OpenCode 若使用 `.jsonc`，应指向实际文件，先备份并整理为标准 JSON；安装器不覆盖无法解析的配置。其他 Agent 配置同样先备份。`managed` 决定安装器可改写的文件范围，不应列入未安装的宿主。各宿主受支持版本见适配器 `manifest.json`；超出范围时查看同步结果，不绕过兼容检查。

若 Agent 在用户目录安装，创建 `~/.config/agent-pulse/environment`，写入实际 PATH，不能依赖 systemd 读取 shell 启动配置：

```dotenv
PATH=/home/your-user/.local/bin:/home/your-user/.opencode/bin:/home/your-user/.volta/bin:/usr/local/bin:/usr/bin:/bin
```

然后执行：

```sh
chmod 600 "$HOME/.config/agent-pulse/bridge.yaml" "$HOME/.config/agent-pulse/environment"
"$HOME/.local/bin/agent-pulse-bridge" sync-adapters --config "$HOME/.config/agent-pulse/bridge.yaml"
systemctl --user daemon-reload
systemctl --user enable --now agent-pulse-bridge
systemctl --user is-active agent-pulse-bridge
curl --fail http://127.0.0.1:18702/healthz
```

手工同步使用当前 shell 的 PATH；服务启动后还应在管理页检查适配状态，确认 service 的 PATH 同样可找到宿主。用户服务若要在退出登录后继续运行，需要系统管理员启用该用户的 linger。Bridge 必须保留回环监听；同机 Windows 与 WSL 使用不同来源标识。

若配置 token 或 Relay token，可以使用权限为 600、版本控制外的 Bridge YAML 保存。若使用 `${env:变量名}`，这些变量必须同时在 systemd 服务及各 Agent 的 hook 进程环境中存在；仅放在 systemd 的 EnvironmentFile 会导致交互式 Agent hook 加载配置失败。

安装后仍需宿主加载：OpenCode 重启或重新加载；Codex 在 `/hooks` 或桌面设置的 Hooks 页面审核新 hook，重启 daemon 本身不代表已信任；Qoder 保持正常项目/命令信任流程。Antigravity 必须通过主会话包装入口启动，例如 ACP：

```sh
"$HOME/.local/bin/agent-pulse-bridge" run-adapter \
  --config "$HOME/.config/agent-pulse/bridge.yaml" --adapter antigravity -- \
  acp --stdio --process-per-session
```

Windows Tray 的便携包、`tray.yaml` 和内置 Bridge 管理见 [Windows 客户端](windows-client.md)，不要把上述 Linux systemd 步骤照搬到 Windows。

## 4. 可选：部署公网 Relay

在公网服务器建立独立目录，复制 `deploy/compose/relay.image.yaml` 为 `compose.yaml`，复制 `relay.runtime.yaml` 到同一目录。创建权限为 600 的 `relay.env`：

```dotenv
AGENT_PULSE_RELAY_IMAGE=ghcr.io/fevenor/agent-pulse/relay:sha-替换为已发布完整提交哈希
AGENT_PULSE_RELAY_TOKEN=替换为生成的随机令牌
```

在该目录执行 `docker compose --env-file relay.env -f compose.yaml config --quiet`、`pull`、`up -d`。默认仅绑定 `127.0.0.1:18701`；用 [Nginx 示例](../deploy/proxy/relay.nginx.conf) 配置域名和有效证书，保留 WebSocket Upgrade 及 `X-Forwarded-Proto` 转发。代理在容器中时，需按实际容器网络调整 `proxy_pass`，不能将其回环地址当作宿主地址。

在 Hub 的 `hub.env` 中加入相同的 `AGENT_PULSE_RELAY_TOKEN`，在 `hub.runtime.yaml` 中加入：

```yaml
relay_url: "https://relay.example.invalid"
relay_token: "${env:AGENT_PULSE_RELAY_TOKEN}"
```

替换域名后重新执行 Hub 的 Compose `up -d --force-recreate`，让环境和配置同时生效。Hub 主动连接 Relay；不必开放 Hub 公网入站。需要备用通路的 Bridge 和 Tray 同样配置 `relay_url` 和 `relay_token`，并重新加载。

验收公网 `/healthz` 返回 `hub_connected: true`；业务接口无凭据应返回 401；带凭据访问 `/api/v1/snapshot` 应与 Hub 的 `run_id` 一致。健康接口免 token 不代表业务接口免鉴权。公网必须 HTTPS/WSS，仅字面回环 IP 允许 HTTP。

## 5. 验收、手机推送与升级

1. Hub 根地址显示管理页，设备列表出现当前 Bridge 和 Tray。
2. 适配器页面中 `installed` 与预期版本一致；真实启动一次宿主后 `running` 也应更新。
3. 用专用测试会话执行一轮短任务，核对来源、会话状态和桌面实收；合成 `emit` 只能验证网络入口，不能代替原生 hook 验证。
4. 配置手机渠道后，分别验证接口接受、App 收件和系统通知；Tray 在线且未锁屏时手机任务会被跳过，这是当前通知规则。
5. 需要 Relay 时，用隔离测试 Bridge 配置不可达的 Hub 地址，验证中继投递及 `relay` 设备路径；不要为测试停掉正式 Hub。

手机请求方法、参数、通用字符上限及本地测试接收器见 [Linux 运行说明](linux-quickstart.md#手机-http-渠道)。无手机渠道时保持 `push: []` 即可，不影响桌面通知。

| 现象 | 优先检查 |
| --- | --- |
| Hub 健康但无管理页 | 非容器部署是否配置 `web_directory`、是否已构建页面 |
| 适配索引 404 | `repository/index.json` 是否存在、挂载及容器读取权限是否正确 |
| 服务在线但没有原生会话 | 宿主是否重新加载、hook 是否已信任、managed 路径和版本是否匹配 |
| 手工同步成功，服务同步失败 | systemd PATH、配置所引用环境变量及文件权限 |
| Relay 健康但 `hub_connected: false` | Hub 中继地址和 token、HTTPS/WebSocket 代理、出站网络 |
| 只有 App 收件、无系统通知 | 渠道/设备长度限制、App 前后台和系统权限，不能只看 HTTP 状态 |

查看 `journalctl --user -u agent-pulse-bridge` 或 Compose 日志时不要公开密钥、完整渠道 URL 和原始事件正文。升级前记录镜像版本并备份配置、适配包和 SQLite；数据库使用在线备份能力或停服务复制数据库及 WAL，不只复制正在写入的主文件。回退镜像前核对数据库兼容性，必要时恢复匹配备份；不要运行 `down -v` 删除数据卷。Bridge 停止会丢失内存中尚未发送的事件，应先确认队列清空。
