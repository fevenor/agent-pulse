# 部署指南

本文面向首次部署的使用者和执行部署任务的 Agent。按顺序完成 Hub 和 Bridge；需要跨公网访问时再配置 Relay。所有地址、目录、主机标识、镜像标签和公钥示例都需要替换，不使用开发者的私人环境作为默认值。

## 组件与准备

| 组件 | 部署位置 | 所需环境 | 配置与持久数据 |
| --- | --- | --- | --- |
| Hub（会话中心） | 一台可信内网服务器 | Docker Engine 和 Compose 插件 | Hub 镜像自带默认配置与该 RC 的签名适配仓库；独立数据卷保存 SQLite |
| Bridge（本机接入） | 每台运行 Agent 的 Linux 主机或 Windows 环境 | Linux 用户 systemd、Bash、curl；安装预构建无需编译器 | `bridge.yaml`；适配包与安装状态目录；事件队列只在内存 |
| Relay（公网中继，可选） | 公网服务器 | Docker/Compose、域名及支持 WebSocket 的 HTTPS 代理 | `relay.runtime.yaml`；独立数据卷保存待转交事件 |
| Tray（桌面通知） | Windows 桌面 | 已构建的 Windows 便携包 | 程序旁 `tray.yaml` 和 `data/`；可托管本机 Bridge |

开始前确定：Hub 内网地址、目标架构、运行的 Agent 及版本、是否需要 Relay、手机渠道请求格式。Bridge setup 会为每个接入环境生成稳定主机指纹，默认采用系统主机名显示；需要跨主机统一命名时在 Hub 管理页修改。已有服务先检查并备份配置和数据，避免重复创建 Hub、重新生成签名私钥或覆盖 Agent 配置。获取源码、提交或推送等 Git 操作按使用者授权范围执行。

部署 Hub/Relay 已发布镜像不需要 Go、Node.js 或 Rust。用户安装 Bridge 只需要 Bash、curl 和常用系统工具，不需要编译器。构建维护者使用 Go；构建适配包使用 Go 和 Volta 管理的 Node.js/pnpm。`scripts/build/package-adapters.sh` 当前产出 Linux amd64 适配包，不能直接用于 arm64；其他架构的构建边界见 [适配器文档](adapters.md)。

## 1. 部署 Hub

将镜像标签替换为发布页提供的完整 RC 版本。Docker 容器自带默认配置、管理网页、适配包公钥与签名仓库；普通使用者不需要准备 Hub YAML 或适配仓库。

```sh
docker volume create agent-pulse-hub-data
docker run -d --name agent-pulse-hub --restart unless-stopped \
  -p 127.0.0.1:18700:18700 \
  -v agent-pulse-hub-data:/data \
  ghcr.io/fevenor/agent-pulse/hub:替换为已发布RC版本
```

访问 `http://服务器本机:18700/healthz` 应返回 `status: ok`，根路径应显示管理页面。默认只开放服务器本机访问；若同一可信内网其他设备需要访问，可将端口绑定地址从 `127.0.0.1` 改为服务器的可信内网 IP。可信个人内网可不设置共享令牌；公网入口必须使用访问控制与 TLS，不要直接暴露免鉴权服务。

如 Hub 需要鉴权，先创建权限为 600 的环境文件 `hub.env` 并在其中设置 `AGENT_PULSE_TOKEN`；重建容器时用 `docker run --env-file hub.env ...` 传入，在 Bridge/Tray 配置同一令牌。若需 Relay，也在该文件中设置 `AGENT_PULSE_RELAY_URL` 和 `AGENT_PULSE_RELAY_TOKEN`，Bridge/Tray 会从 Hub 自动获取。手机推送与自定义 Push 渠道属于额外的 Hub 高级配置。也可使用 [默认 Compose 文件](../deploy/compose/hub.image.yaml)，填写镜像版本和端口绑定即可。不要在命令参数中直接输入真实令牌或将其提交到版本控制。
## 发布维护者：自定义适配包或准备 RC 镜像

普通使用者可跳过本节。Hub 正式 RC 镜像由发布者使用一套长期保管的私钥，随 RC 一并构建签名仓库；私钥不进入 Git、镜像或客户端。自建包与正式发布步骤见[适配包发布说明](adapters.md#发布者构建与签名)。

单独维护 Hub 的部署者可仍使用 `deploy/compose/hub.runtime.yaml` 覆盖内置默认配置，或将自己的签名仓库挂载到 `/repository`；该操作仅适用于高级自建流程。Bridge 始终只需配置 Hub 地址，并从可访问的 Hub 获取签名公钥与 Relay 信息。

## 2. 在 Linux 主机安装 Bridge

用户安装始终下载已完成构建的 Linux Bridge，不在目标主机编译。安装器识别 `uname` 返回的系统与架构，下载 `agent-pulse-bridge-linux-amd64` 或 `agent-pulse-bridge-linux-arm64`，读取同一 Release 的 `SHA256SUMS.txt` 并核对二进制格式。默认选择最新稳定发布；指定 `--version` 可安装已公开的预发布版本。

公开 Release 附件齐备后，可在交互终端一条命令下载、安装、逐项设置并启动用户服务：

```sh
set -o pipefail
curl --fail --location --proto '=https' --proto-redir '=https' \
  https://github.com/fevenor/agent-pulse/releases/latest/download/install-bridge.sh \
  | bash -s -- --setup
```

若已取得安装脚本，也可执行 `bash scripts/install/bridge.sh --version "v已公开版本" --setup`。不带 `--setup` 只安装二进制和用户服务定义，不自动启用服务或修改 Agent 配置；已有 Bridge YAML、服务自定义和固定公钥均保留。正在运行的旧 Bridge 需先确认队列清空并停止，安装器才允许替换，旧二进制会备份。

当前 RC 若仍为草稿或附件缺失，安装明确失败，不回退到源码编译。仅本次测试或离线部署可由维护者提供已构建文件与核验摘要，显式使用：

```sh
bash scripts/install/bridge.sh --version "v已构建版本" \
  --file "/维护者提供的二进制路径" --sha256 "对应64位摘要"
```

该入口只复制预构建，仍校验摘要和架构；不要求目标用户安装 Go。Linux arm64 Bridge 的存在不表示当前 amd64 命令适配包可直接用于 arm64，受支持的适配包范围仍以清单为准。

一键安装时使用 `--setup` 会自动进入配置；若选择只安装，则手动执行 `agent-pulse-bridge setup`。按提示填写 Hub 地址，再逐项选择启用、禁用或跳过的受支持 Agent。setup 会创建稳定随机主机指纹、默认采用系统主机名作为显示名，并列出本次修改的 Agent 配置文件。允许配置页面上的 Hub 地址为可信内网 HTTP；公网部署应使用 HTTPS 和访问控制。Relay 和适配包发布公钥均由 Bridge 直连 Hub 获取，公钥首次固定后若发生变化会停止同步并要求核验。

配置开始前先说明接下来的操作；逐项显示 Agent 名称、版本、自动识别的配置路径与当前状态，结束后给出结果清单，再提示以后可用 setup 修改。OpenCode 优先沿用已有 opencode.json，其次采用 opencode.jsonc，已有 Bridge 中的明确路径始终优先；不要求用户额外手填路径。仅本次启用且配置完成的 Codex CLI 需要在 `/hooks` 审核并信任钩子，不给其他正常接入增加额外授权提示。Antigravity 与其他 CLI 一样直接启动；ACP 调用和子代理运行不发送给用户。配置文件无法解析或含不支持的 JSONC 时，Bridge 仍拒绝覆盖，错误会说明接入未完成。

对于需要脚本化部署的高级使用者，可手工配置 `~/.config/agent-pulse/bridge.yaml`，其中 `source_id` 应沿用 setup 已生成的值：

```yaml
listen: "127.0.0.1:18702"
source_id: "setup 生成的稳定主机指纹"
host_name: "" # 留空时使用系统主机名
hub_url: "http://服务器内网地址:18700"
adapters:
  directory: "/home/your-user/.local/share/agent-pulse/adapters"
  poll_seconds: 300
  managed:
    codex: "/home/your-user/.codex/hooks.json"
    qoderclicn: "/home/your-user/.qoder-cn/settings.json"
    antigravity: "/home/your-user/.gemini/config/hooks.json"
    opencode: "/home/your-user/.config/opencode/opencode.json"
```

`managed` 决定安装器可改写的文件范围，不应列入未安装的宿主。各宿主受支持版本见适配器 `manifest.json`；超出范围时查看同步结果，不绕过 Agent 主版本政策和包完整性校验。
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

手工同步使用当前 shell 的 PATH；服务启动后还应在管理页检查适配状态，确认 service 的 PATH 同样可找到宿主。用户服务若要在退出登录后继续运行，需要系统管理员启用该用户的 linger。Bridge 必须保留回环监听；同机 Windows 与 WSL 作为不同观察环境分别运行 setup，生成不同的主机指纹。

若配置 token 或 Relay token，可以使用权限为 600、版本控制外的 Bridge YAML 保存。若使用 `${env:变量名}`，这些变量必须同时在 systemd 服务及各 Agent 的 hook 进程环境中存在；仅放在 systemd 的 EnvironmentFile 会导致交互式 Agent hook 加载配置失败。

配置完成后可以正常启动所选 Agent。Codex CLI 需先在 `/hooks` 审核钩子，重启 daemon 本身不代表已信任；Qoder 原有的项目/命令信任流程保持不变，不是安装脚本新增的授权步骤。若旧进程仍未加载新配置，按下文故障排查处理，不将所有 Agent 的手工重启写成安装必选操作。Antigravity 保持原来的 `agy` 启动命令；`agy acp` 的运行事件会被过滤。

Windows Tray 的便携包、`tray.yaml` 和内置 Bridge 管理见 [Windows 客户端](windows-client.md)，不要把上述 Linux systemd 步骤照搬到 Windows。

## 3. 可选：部署公网 Relay

在公网服务器建立独立目录，复制 `deploy/compose/relay.image.yaml` 为 `compose.yaml`，复制 `relay.runtime.yaml` 到同一目录。创建权限为 600 的 `relay.env`：

```dotenv
AGENT_PULSE_RELAY_IMAGE=ghcr.io/fevenor/agent-pulse/relay:sha-替换为已发布完整提交哈希
AGENT_PULSE_RELAY_TOKEN=替换为生成的随机令牌
```

在该目录执行 `docker compose --env-file relay.env -f compose.yaml config --quiet`、`pull`、`up -d`。默认仅绑定 `127.0.0.1:18701`；用 [Nginx 示例](../deploy/proxy/relay.nginx.conf) 配置域名和有效证书，保留 WebSocket Upgrade 及 `X-Forwarded-Proto` 转发。代理在容器中时，需按实际容器网络调整 `proxy_pass`，不能将其回环地址当作宿主地址。

在 Hub 的环境变量中加入相同的 `AGENT_PULSE_RELAY_TOKEN` 和 Relay 外部 HTTPS 地址 `AGENT_PULSE_RELAY_URL`。例如 Compose 环境文件中设置：

```dotenv
AGENT_PULSE_RELAY_URL=https://relay.example.invalid
AGENT_PULSE_RELAY_TOKEN=与Relay部署一致的令牌
```

使用 Compose 时编辑 `hub.env` 后执行 `docker compose up -d --force-recreate`。若原先使用 `docker run`，需停止并删除原容器，再用相同数据卷重建容器并添加 `-e` 变量；SQLite 保留在原命名卷中。Hub 主动连接 Relay，不必开放 Hub 公网入站。新版 Bridge 与 Tray 直连 Hub 发现并缓存 Relay 配置，手工配置仍优先。缓存含中继凭据，应和便携配置一并保管。发布包含 ZCode 的适配包索引前，须先将所有消费该索引的 Bridge 升级到 0.5.0，详见适配器说明。

验收公网 `/healthz` 返回 `hub_connected: true`；业务接口无凭据应返回 401；带凭据访问 `/api/v1/snapshot` 应与 Hub 的 `run_id` 一致。健康接口免 token 不代表业务接口免鉴权。公网必须 HTTPS/WSS，仅字面回环 IP 允许 HTTP。

## 4. 验收、手机推送与升级

1. Hub 根地址显示管理页，设备列表出现当前 Bridge 和 Tray。
2. 适配器页面中 `installed` 与预期版本一致；真实启动一次宿主后 `running` 也应更新。
3. 用专用测试会话执行一轮短任务，核对来源、会话状态和桌面实收；合成 `emit` 只能验证网络入口，不能代替原生 hook 验证。
4. 配置手机渠道后，分别验证接口接受、App 收件和系统通知；Tray 在线且未锁屏时手机任务会被跳过，这是当前通知规则。
5. 需要 Relay 时，用隔离测试 Bridge 配置不可达的 Hub 地址，验证中继投递及 `relay` 设备路径；不要为测试停掉正式 Hub。

手机请求方法、参数、通用字符上限及本地测试接收器见 [Linux 运行说明](linux-quickstart.md#手机-http-渠道)。无手机渠道时保持 `push: []` 即可，不影响桌面通知。

| 现象 | 优先检查 |
| --- | --- |
| Hub 健康但无管理页 | 非容器部署是否配置 `web_directory`、是否已构建页面 |
| 适配索引 404 | 确认部署的是完整 RC 镜像，而非仅用于发布者的 `hub-base`；自建仓库时检查 `/repository/index.json` 与容器读取权限 |
| 服务在线但没有原生会话 | 宿主是否重新加载、hook 是否已信任、managed 路径和版本是否匹配 |
| 手工同步成功，服务同步失败 | systemd PATH、配置所引用环境变量及文件权限 |
| Relay 健康但 `hub_connected: false` | Hub 中继地址和 token、HTTPS/WebSocket 代理、出站网络 |
| 只有 App 收件、无系统通知 | 渠道/设备长度限制、App 前后台和系统权限，不能只看 HTTP 状态 |

查看 `journalctl --user -u agent-pulse-bridge` 或 Compose 日志时不要公开密钥、完整渠道 URL 和原始事件正文。升级前记录镜像版本并备份配置、适配包和 SQLite；数据库使用在线备份能力或停服务复制数据库及 WAL，不只复制正在写入的主文件。当前开发阶段不保证不同结构间的数据互用；结构变化时经明确授权重建，备份只用于对应结构的恢复；不要运行 `down -v` 删除数据卷。Bridge 停止会丢失内存中尚未发送的事件，应先确认队列清空。
