# Linux 运行说明

提供 Hub、Relay、Bridge、Vue 管理页面、命令行测试客户端、HTTP 推送和独立适配包更新；桌面弹窗由 Windows Tray 承担。完整规划见 [实现设计](agent-pulse-design.md)，验证进度见 [开发记录](development-status.md)。

## 准备和构建

需要 Go 1.25 或兼容版本。SQLite 使用纯 Go 驱动，无需额外数据库进程或 C 编译器；执行竞态检测需要本机 Go 支持的 C 工具链。适配器开发/检查使用 Volta 管理 Node.js 24.14.1、pnpm 10.33.2；插件运行由 OpenCode 提供宿主，不要求 Bridge 所在主机额外运行 Node 服务。

从仓库根目录执行：

```sh
pnpm --dir web/hub install --frozen-lockfile --ignore-scripts
pnpm --dir web/hub build
bash scripts/build/build.sh
```

产物在 `bin/`。普通构建使用现有 Go 缓存；受限环境可显式设置 `GOMODCACHE` 和 `GOCACHE` 为可写目录。下载依赖需要网络，HTTP/WebSocket 集成测试需要允许本地回环监听。任何命令都不暂存或提交代码。

## 启动与接入

将 `config/hub.example.yaml`、`config/bridge.example.yaml` 分别复制成 `config/hub.local.yaml`、`config/bridge.local.yaml`。每个 Bridge 配置固定且不同的 `source_id`。示例监听回环地址；多主机使用时，Hub 绑定指定可信内网 IP，并相应调整 Bridge 的 `hub_url`。

Hub 只服务个人可信内网时，`token` 可省略；若另行开放到公网，必须配置访问控制。Relay 的公网 token 独立配置，Hub 通过主动建立的连接使用 Relay，不要求给 Hub 开放公网入站。

数据库相对路径相对于启动时工作目录，固定工作目录或改用绝对路径。不要在多个进程中同时打开同一个 Hub 数据库。分别在两个终端运行：

```sh
./bin/agent-pulse-hub --config config/hub.local.yaml
```

```sh
./bin/agent-pulse-bridge serve --config config/bridge.local.yaml
```

查看实时快照：

```sh
./bin/agent-pulse-client --hub http://127.0.0.1:18700 --watch
```

在 Hub YAML 设置 `web_directory` 后，访问 Hub 根地址管理会话、已读、设备和适配器。四种来源的安装与签名更新见 [来源接入说明](adapters.md)；OpenCode 也可按 [插件说明](../adapters/opencode/README.md) 直接加载源码目录。只验证通用入口时，可从 stdin 发送测试事件，`emit` 自动补事件 ID、时间和本地来源：

```sh
./bin/agent-pulse-bridge emit <<'JSON'
{"agent":"manual","session_id":"demo","kind":"input.required","scope":"main","title":"请选择下一步","body":"这是一条手工测试事件"}
JSON
```

复制快照内的事件 ID 后明确标记已读：

```sh
./bin/agent-pulse-client --read "替换为实际事件ID"
```

`--watch` 不自动标记已读，不显示桌面弹窗，也不保存离线缓存。Hub 配置 token 时，通过环境变量 `AGENT_PULSE_TOKEN` 给测试客户端传值，并在 Bridge 配置相同令牌。

## 手机 HTTP 渠道

Hub 的 `push` 默认为空。配置已有渠道的请求方法、URL、headers、query，以及 POST 的 `format: json|form` 和 `fields`。GET 只用 query；标题/正文分别用 `${title}`、`${body}`，秘密配置可用 `${env:PUSH_TOKEN}`。字段模板经过 JSON、URL 或表单编码，不进行脚本执行。

```yaml
push:
  - name: "phone"
    method: "POST"
    url: "https://push.example.invalid/send"
    format: "json"
    headers:
      Authorization: "Bearer ${env:PUSH_TOKEN}"
    fields:
      title: "${title}"
      content: "${body}"
    timeout_seconds: 5
```

实际 URL 和参数名按已有渠道修改。通用渠道以 HTTP 2xx 为接收成功；部分渠道需另检查业务状态。新增需要业务码或特殊签名的渠道应局部适配，不能仅凭 HTTP 200 宣称手机收到。

每个渠道可独立设置 `max_title_chars`、`max_body_chars`，解决同一事件在不同渠道可接受长度不同的问题。0 或省略表示不限制，负数拒绝启动。上限按 Unicode 码点计数：普通中英文、换行和单码点 emoji 各计 1；组合 emoji 可能占多个码点，不按 UTF-8 字节数计算。超过上限时保留前面的字符，并以 `…` 结尾，省略号计入上限；上限为 1 时结果只有省略号。

限制在 `${title}`、`${body}` 替换及 HTTP 编码之前执行，适用于 GET 查询、POST JSON 和表单。模板中额外写入的固定前后缀不计入该上限，需要自行留出空间。仅裁剪该渠道的发送文本，Hub 历史、Tray 正文及其他渠道保留各自内容，不修改原始事件。例如在渠道配置中设置：

```yaml
max_title_chars: 40
max_body_chars: 1000
```

上述数值仅为示例。应分别验证渠道接口接受、App 内收件和系统通知展示，再确定适合实际设备的值；Hub 的字段上限不代表第三方渠道能接收或显示同样长度。旧版本 Hub 会拒绝未知配置字段，启用前先升级到包含该功能的版本。

Hub 重启后等待 35 秒接受设备状态和已读再处理积压。至少一台在线未锁屏 Tray 时跳过手机；没有 Tray 或全部锁屏时发送。发送前检查已读、状态失效和默认 24 小时有效期，失效只停止提醒，不删除历史。HTTP 429/5xx 最多三次尝试、间隔 10 秒；网络异常或响应结果未知记为 `unknown`，不会盲目重发；开始发送后崩溃同样记未知。Web 的“推送记录”展示最近 200 条结果；`unknown` 表示不能确认送达，不自动或人工盲目补发。会话关闭解除输入/批准等待，但保留刚发生的完成、失败和中断提醒。

## 本地手机推送测试

以下为 POST JSON 渠道配置示例，字段名按实际接口调整；完整地址可能含密钥，通过环境变量提供：

```yaml
push:
  - name: phone
    method: POST
    url: "${env:AGENT_PULSE_PHONE_URL}"
    format: json
    fields:
      title: "${title}"
      body: "${body}"
    timeout_seconds: 15
```

按所选渠道的官方文档填写字段，并通过实际设备验证长度和显示效果；接口返回成功不等于手机出现系统通知。私密配置保存在版本控制之外。Compose/systemd 可从权限为 600 的环境文件传给 Hub，Compose 还需在 Hub 的 environment 中明确转发 `AGENT_PULSE_PHONE_URL`。`.gitignore` 和 `.dockerignore` 排除本地配置、环境文件和私钥，不能据此认为已追踪文件会自动脱离版本控制。

`push` 可以配置多个渠道，目前每个渠道分别发送，不支持优先级与自动故障切换；该能力留待后续迭代。真实发送测试见 [测试说明](../tests/e2e/README.md)。

### 本地接收器

启动轻量接收器：

```sh
./bin/agent-pulse-push-test --listen 127.0.0.1:18703
```

将 Hub 的渠道改为 `url: "http://127.0.0.1:18703/push"`、`format: json`，字段使用 `title: "${title}"` 和 `body: "${body}"`。在 `http://127.0.0.1:18703/messages` 查看实际接收到的请求。Hub 首次启动等待 35 秒，测试时须留出这个时间。接收器也支持 GET 查询参数和 POST 表单，只监听回环，内存保留最近 1000 条测试消息。

## Relay 与路径恢复

使用 `config/relay.example.yaml`，设置环境变量中的共享令牌后启动：

```sh
./bin/agent-pulse-relay --config config/relay.local.yaml
```

Hub 和需要回退的 Bridge 同时配置 `relay_url`、`relay_token`。公网必须使用 HTTPS/WSS；只有字面量回环 IP 可使用 HTTP。Hub 主动连接 Relay，无需对公网开放 Hub 入站。事件直连失败后交 Relay 持久保存，收到 Hub 提交确认才删除；Hub 离线时查询返回 503，不返回假实时数据。Relay 不保存会话副本或下行日志。

事件 Relay 202 表示 Relay 已持久化；已读 Relay 202 只表示暂存，客户端仍须保留到 Hub 200 确认。Bridge 的设备连接每 30 秒重新探测直连，消息发送每次先试直连。设备快照的 `path` 区分 direct/relay。`/healthz` 可查看 pending、blocked 和 hub_connected；冲突项保留为 blocked，不让坏事件阻塞后续记录。

## Docker 和用户级服务

GitHub 自动双架构构建、容器恢复验证及拉取已发布 Relay 镜像见 [镜像构建与部署](images.md)。

先构建 Web 与适配包仓库，再分别构建 Hub/Relay。两者通常在不同机器运行：

```sh
pnpm --dir web/hub build
bash scripts/build/package-adapters.sh
# 按来源接入说明签名并发布适配包。
docker compose -f deploy/compose/hub.yaml up -d --build
# 公网机器配置 AGENT_PULSE_RELAY_TOKEN 后执行：
docker compose -f deploy/compose/relay.yaml up -d --build
```

Hub 用 `HUB_BIND_IP` 指定可信内网 IP；默认只绑定回环。编辑各自的 `*.runtime.yaml` 配置渠道和中继。Hub/Relay 镜像均以非 root 用户运行，SQLite 放在独立数据卷中；适配包目录只读挂载。Relay 端口仅绑定宿主回环，由 [Nginx 示例](../deploy/proxy/relay.nginx.conf) 终结 TLS。代理应覆盖 `X-Forwarded-Proto` 并正确转发 WebSocket，不将 token 写入日志。

Bridge 用户安装入口为 `bash scripts/install/bridge.sh --setup`，自动下载并校验当前 Linux 架构的预构建二进制，不调用 Go 或构建脚本；Release 附件缺失则明确失败。只安装时不加 `--setup`，保留已有 Bridge YAML 和用户服务。开发者测试已构建文件可显式使用 `--version 版本 --file 文件 --sha256 摘要`，目标使用者无需编译。systemd 的 PATH 若不含 Agent 安装目录，在 `~/.config/agent-pulse/environment` 填写明确的 PATH，不能期待服务自动读取交互 shell 配置。

`bash scripts/build/build-targets.sh` 交叉编译 Linux amd64/arm64 和 Windows amd64 程序，以及 Windows arm64 Bridge/测试客户端；交叉编译不等于目标环境验收。Windows 安装器和 Tray 不在这个脚本的交付范围。

Dockerfile 使用构建主机上的 Go 工具链交叉编译，无需在构建阶段运行 arm64 程序。可分别用 `docker build --platform linux/arm64 --target hub -f deploy/docker/Dockerfile -t agent-pulse-hub:arm64 .` 和对应的 `--target relay` 构建 arm64 镜像；amd64 使用 `--platform linux/amd64`。镜像构建不等于 arm64 真机运行。

## 可靠性与排查

- Hub 200 表示事件和相关状态已在同一事务提交；确认丢失后用同一 ID、同一内容重试。已读先到也能持久化。
- Bridge 202 只表示进入内存；断网时有界暂存，进程退出会丢失尚未发送事件。达到 10,000 条或 64 MiB 后拒收新数据，`/healthz` 可查积压与拒收计数。
- 单条事件被 Hub 明确拒绝时，Bridge 记录失败 ID 并继续后续投递；检查日志和 `failed` 计数。普通断网使用 1 至 30 秒退避重试。
- 适配器向本地 Bridge 发送最多等待 200 毫秒，不重试公网，不把通知错误变成 Agent 失败。本地 Bridge 未运行、队列满或插件关闭时可能漏采，应查看 AgentPulse 诊断。
- WebSocket 重连后获取完整快照。`run_id` 用于识别 Hub 重启；只凭本地缓存不能宣称实时在线。
- SQLite 文件应位于本机磁盘。备份时停止 Hub 后复制数据库及尚存在的 WAL 文件，或使用 SQLite 备份能力；不要直接复制正在写入的主文件作为完整备份。

## 检查与真实宿主验证

```sh
pnpm --dir adapters/opencode install --frozen-lockfile --ignore-scripts
pnpm --dir web/hub install --frozen-lockfile --ignore-scripts
bash scripts/check/check.sh
bash scripts/build/build.sh
pnpm --dir adapters/opencode smoke
```

最后一个命令需要 OpenCode 2.0.20。脚本在临时目录使用隔离配置启动三个进程，验证原生创建/输入等待事件、已读和 Hub 强制终止恢复，不调用模型。打印证据目录后关闭进程；测试日志不等于真实模型完成、实际子代理执行或手机送达验证。

独立包 loader 验证：先运行 `bash scripts/build/package-adapters.sh`，再执行 `AGENT_PULSE_PACKAGE_SMOKE=1 pnpm --dir adapters/opencode smoke`。qoderclicn + DeepSeek-Flash + 本地推送的真实流程见 [qoderclicn 说明](../adapters/qoderclicn/README.md)。测试不自动改动日常 Agent 配置。
