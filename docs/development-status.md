# 开发记录

Linux 服务端、管理页面、中继和适配包独立更新已实现。OpenCode、Qoder、agy ACP 原生链路及已有手机渠道请求已通过，双架构容器恢复验证通过；各来源的验证程度及剩余工作列在下方。Windows Tray 按约定留到其他环境，当前不能宣称整个项目已经交付。

## 实施状态

| 项目 | 状态与边界 |
| --- | --- |
| Hub | SQLite 事务、去重与冲突、会话归约、提前已读、快照、历史分页、实时订阅和手机任务 |
| Bridge | 回环接入、有界内存队列、重试、直连/中继、设备心跳、适配包同步及稳定 dispatch 入口 |
| Relay | 单表持久队列、重启补发、Hub 反向连接、双向设备与查询转发、分块包下载；无管理 UI |
| Hub Web | 已确认并采用 Vue 3 + TypeScript + Vite；会话、设备路径、适配器版本、推送记录、历史及标记已读 |
| 手机 HTTP 推送 | 通用 HTTP 发送器及抑制/重试已实现；已有真实渠道返回 HTTP 200、业务 status 200 和成功提示，用户已确认手机收到测试正文；多渠道当前分别发送，优先级后置 |
| 适配器发布 | Ed25519 签名索引、不可变包、兼容版本选择、两主机同步、配置保留、安装中断恢复、命令入口失败回滚及坏包隔离 |
| OpenCode | 2.0.20 原生 TypeScript 插件；独立包经 loader 实际加载，主会话、表单等待、已读及 Hub 强制重启恢复通过 |
| Qoder 中国版 CLI | qoderclicn 1.1.85；使用 DeepSeek-Flash 完成真实轮次，经独立包、Bridge、Hub 到本地 HTTP 推送接收器 |
| Codex | 0.167.0 命令 hook 适配已实现；原生 hook 与本地模拟模型测试见下文；等待输入/批准的主子归属仍有能力缺口 |
| Antigravity CLI | agy 1.5.3 ACP 真实模型经签名包、原生 hook、Bridge 到 Hub 完成和未读通过；修正 NO_TOOL_CALL 停止原因；真实子代理与续接待验证 |
| zcode | 按用户要求挂起，不阻塞本阶段 |
| 部署 | Hub/Relay Docker、Compose、Nginx、用户 systemd；GitHub 双架构检查发布 Workflow 和 Relay 镜像部署配置已提供；远端检查与 GHCR 双架构发布已通过 |
| 目标构建 | Linux amd64/arm64、Windows amd64 Go 程序及 Windows arm64 Bridge 可交叉编译；容器在 amd64 原生及 arm64 模拟运行通过；Windows 与 arm64 真机未验收 |
| Tray | 尚未实现；Windows 环境先做 Rust + GPUI 原型，不可行时才采用 Go + Walk |

## 2026-11-09 验证记录

实际平台为 Linux amd64。工具版本：Go 1.25.11、Volta Node.js 24.14.1、pnpm 10.33.2、Docker 29.5.3。Go 基线为 1.25，直接依赖 modernc.org/sqlite 1.48.0、gorilla/websocket 1.5.3、yaml.v3 3.0.1。Web 固定 Vue 3.5.45、Vite 8.3.6；依赖版本由各锁文件维护。

| 检查 | 结果与证据 |
| --- | --- |
| Go | `go test -race ./...` 与 `go vet ./...` 通过；包含 SQLite 原子性、重复投递、连接所有权、已读和通知边界 |
| Bridge/Hub 网络 | 断网重试、提交确认丢失、重复投递、两个客户端已读同步通过 |
| Relay | 断线入队后重开数据库、恢复交付、提前已读、双向设备变化、Hub 离线查询 503 通过 |
| 更新分发 | 同一 Bridge 实现的两个隔离主机状态目录，通过直连/Relay 下载签名包；配置保留、旧索引重放拒绝、坏入口回滚和安装恢复通过；这是同机多实例模拟，不是两台物理机验收；额外覆盖新发布线不兼容时仍选旧包，以及必须升级 Bridge 时明确报错 |
| 手机发送器 | GET/query、POST JSON/表单、编码、桌面抑制、已读抑制、429/5xx 有限重试、断连结果未知通过 |
| 关闭事件 | 会话关闭保留完成/失败/中断提醒，清除等待提醒；孤立关闭只存历史，避免短命令退出漏推和多余列表项 |
| OpenCode TypeScript | 类型检查和 5 项事件映射测试通过 |
| OpenCode 真实宿主 | 插件源码加载与独立签名包加载均通过；独立包最近运行版本有宿主 setup 回执；不调用模型 |
| Qoder 真实宿主 | `--model dfmodel` 对应 DeepSeek-Flash；完成消息实际到达本地 HTTP 接收器，已安装/最近运行版本均为 0.2.0 |
| Codex 真实宿主 | 本地 Responses 模拟模型完成一轮，临时项目 hooks.json 实际触发 SessionStart、UserPromptSubmit、Stop、SessionEnd，经签名包、Bridge、Hub 入库并取得运行版本回执；不调用在线模型，测试串行运行 |
| Antigravity ACP | agy 1.5.3、适配包 0.2.1；使用默认登录目录、无工具短文本请求；SessionStart、PreInvocation、Stop 实际触发，主会话 ID 与 ACP 一致，完成提醒入未读，运行版本有回执；保留先前配置严格校验结果 |
| Web | 类型检查和生产构建通过；真实浏览器验证实时收件、单条已读、断线提示和 390×844 布局，无 JavaScript 错误 |
| Docker | Hub/Relay 双架构最终镜像构建及容器测试通过；验证非 root、鉴权、离线队列与去重、Relay/Hub 强制终止恢复、反向查询、页面、已读持久化；arm64 在 amd64 主机模拟运行；临时容器和卷清理完成 |
| GitHub Workflow | 首次运行修正 setup-uv 不存在的主版本别名后，提交 eb9875d 的检查、双架构运行与 GHCR 发布全部成功；actionlint 和本地同等测试也通过 |
| 真实手机接口 | 通过 Hub 和只转发一次的临时接收器发出 1 个 POST；HTTP 200、业务 status 200、成功提示，Hub attempts=1/sent；密钥不输出、不写文件；正式发送器增加该域名的业务判断，失败/未知分支本地测试通过 |
| 跨平台 | Linux amd64/arm64、Windows amd64 与 Windows arm64 Bridge 交叉编译通过；容器 arm64 模拟通过；不代表 arm64 真机、Windows 桌面或安装验收 |

原始 OpenCode 核心链路证据见 Linux 核心证据（原始产物不随源码保留，结果见本页摘要）。本轮补充证据见 Linux 服务验证（原始产物不随源码保留，结果见本页摘要），页面截图见 桌面（原始产物不随源码保留，结果见本页摘要） 和 手机宽度（原始产物不随源码保留，结果见本页摘要）。证据只包含测试生成的数据，不包含发布私钥、渠道令牌或日常 Agent 对话。

继续实施的 ACP、真实手机接口及双架构镜像证据见 本轮验证（原始产物不随源码保留，结果见本页摘要）。本轮完整 `scripts/check.sh` 通过，额外覆盖 agy 空闲停止判定和手机渠道的业务失败、缺失状态、畸形/超限响应。

`relay.image.yaml` 经 Compose 配置校验通过；真实发送完成后未再调用手机渠道，业务状态分支和域名选择通过本地 HTTP 服务器验证。非忽略工作区文件和已有暂存区扫描均未发现实际渠道 URL 或密钥路径。临时容器、数据卷清理确认通过，该轮未执行任何 Git 写操作。

agy 首次指定额外应用目录导致未找到登录状态；保持默认目录后真实模型正常返回。其原生 Stop 实际为大写 `NO_TOOL_CALL`，与文档的 `model_stop` 示例不同；采集字段核对后修复，最终测试恢复原始安装命令、去掉诊断拦截，并确认完成和未读。测试没有读取认证文件或自动批准工具。

Qoder 测试仅在生成的临时工作区中信任并启用项目 hook，没有接管用户日常工程。该宿主在不信任项目时会跳过 hook；仅有模型正常输出不足以判定接入成功。测试禁用工具与 MCP，只做一条短文本请求。配置使用 `.qoder/settings.json`，用户级中国版配置位置是 `~/.qoder-cn/settings.json`。

Codex 初次测试因点分参数的项目信任路径写法不正确，模型能完成但没有 hook；改用 TOML 内联表后，项目 `hooks.json` 正常触发。测试只针对临时生成并审阅的 hook 使用单次信任选项，不写用户全局 hook 信任状态；安装器不替用户批准 hook。当前不支持缺少可靠主/子归属的 Codex 工具等待通知。

## 复现

```sh
pnpm --dir adapters/opencode install --frozen-lockfile --ignore-scripts
pnpm --dir web/hub install --frozen-lockfile --ignore-scripts
bash scripts/build.sh
bash scripts/check.sh
bash scripts/build-targets.sh
AGENT_PULSE_PACKAGE_SMOKE=1 pnpm --dir adapters/opencode smoke
bash scripts/package-adapters.sh ./dist/repository 1
# 会发起一条已授权模型请求；先按 tests/e2e/README.md 准备专用 Qoder 测试工作区。
AGENT_PULSE_QODER_WORKSPACE=/专用测试工作区 uv run --offline --no-project python tests/e2e/linux_services.py
# 仅本地模拟模型。
uv run --offline --no-project python tests/e2e/codex_native.py
# 已登录 agy 1.5.3，调用一条真实模型请求。
uv run --offline --no-project python tests/e2e/antigravity_acp.py
# 按镜像说明构建后执行；arm64 需要相应真机或 QEMU。
uv run --offline --no-project python tests/e2e/relay_containers.py --arch amd64
# 明确发送一条真实手机测试，不属于常规 CI。
uv run --offline --no-project python tests/e2e/phone_channel.py --send
```

网络测试需要允许回环监听。测试使用独立可写的 Go 缓存和临时 pnpm store；下载依赖后可关闭网络重跑。具体机器路径不随源码保留。

## 首次 GitHub 发布与 Windows 接续

用户已确认手机实收，并授权向 `git@github.com:fevenor/agent-pulse.git` 首次提交、推送和验证 Workflow。目标为空的公开仓库，默认开发分支为 `main`。Windows 后续开发按 [接续说明](windows-development.md) 执行，先做 GPUI 原型；Windows 自动安装器仍需实现。[Workflow 42614959760](https://github.com/fevenor/agent-pulse/actions/runs/42614959760) 对提交 `eb9875d88b4a3a13fea4fd2bced14db4faa6bc18` 全部通过，已发布 Hub/Relay 的双架构提交标签与 latest；下载的远端容器结果及镜像摘要见 GitHub 验证证据（原始产物不随源码保留，结果见本页摘要）。初始运行因 `setup-uv@v10` 别名不存在失败，改为官方 API 确认的 v10.3.3 提交 SHA 后通过。

## 剩余验收与实现

- Windows：GPUI 原型、Tray、原生 Bridge 管理、离线缓存与已读、锁屏/全屏/焦点、多显示器、安装和 WSL2 联调。
- 宿主：Antigravity 真实子代理、续接，四种来源的异常退出和宿主重新加载。Codex 输入/批准等高级事件后续迭代；zcode 挂起。
- 发布与真实部署：GitHub Workflow 与 GHCR 权限、发布已验证，下一步在指定公网主机部署并验证 TLS/代理；systemd 登录生命周期与 arm64 真机尚待验收。两台物理主机长期更新按用户要求后置。
- 手机：用户已确认收到“Hub 真实渠道测试完成。”，真实渠道实收通过。优先级与失败切换后续再实现，当前多渠道分别发送。

需求与设计维护现行规则，变更历史只放 [CHANGELOG](../CHANGELOG.md)，本页记录实施进度与证据。
