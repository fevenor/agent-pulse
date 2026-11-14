# AgentPulse

AgentPulse 是供个人使用的多主机 Agent 会话通知工具。Hub（会话中心）保存会话与已读状态，Relay（公网中继）提供备用通路，Bridge（本地接入进程）采集事件，Tray（桌面小组件）显示提醒。

Linux 组件包括 Hub、Relay、Bridge、Vue 管理页面、命令行测试客户端、手机 HTTP 推送和独立适配包更新。Windows 单入口客户端提供紧凑托盘菜单、可贴边悬浮图标、会话小窗/详情、归档与人工状态修正、展开式时间线、系统通知、按主机静默/任务唤醒和离线已读恢复，使用 Rust + GPUI。Linux 接入 OpenCode、Codex、Antigravity CLI 和 qoderclicn；本轮 Windows RC 只接入 ZCode 图形界面主会话生命周期；Windows Codex CLI 与 ChatGPT 桌面端均为后续规划；各来源及桌面行为的验证边界见开发记录。

## 普通用户：开始使用

开始使用需要一台运行 Hub 的服务器，以及至少一台安装了受支持 Agent 的主机。当前项目尚未发布 RC，公开下载包尚未提供；以下说明描述发布后的用户流程，完整部署步骤和验收标准见[部署指南](docs/deployment.md)。

1. **部署 Hub**：RC 发布后从项目发布页获取已包含默认配置、管理网页与签名适配包的 Hub 容器镜像，在可信网络中启动。普通用户无需挂载仓库或配置适配公钥。Hub 保存会话、已读状态和主机名称；新主机首次连接后默认以系统主机名显示。之后可在 Hub 管理页面统一修改名称，所有 Tray 会在同步后看到新名称。
2. **连接 Agent 主机**：从 GitHub 发布页下载安装脚本 `install-bridge.sh`，在运行 Agent 的主机执行 `bash install-bridge.sh --setup`；安装器按平台下载预构建并校验，普通用户不需要源码目录、Go、Node.js 或 Rust。也可只安装后再运行 `agent-pulse-bridge setup`。它会检查已安装且受支持的 Agent，让你逐项选择启用哪些钩子，并报告修改过的配置文件。Hub RC 已包含当前平台的发布者签名适配包仓库；Bridge 连接 Hub 自动获取 Relay 与公钥，不必逐台配置。适配包由正式发布流程提供时无需自行构建；当前尚未发布 RC。Relay 为可选项。宿主可能需要重新加载配置，Codex 还需要在其界面确认 hook 信任。
3. **使用 Windows Tray**：RC 发布后下载并解压 Windows 便携包，在程序旁的配置文件中填写 Hub 地址后启动。Tray 默认只需 Hub 地址；有 Relay 时会从 Hub 自动发现配置。会话详情可调整当前主机的静默和任务期间保持唤醒设置。
4. **按需部署 Relay**：只有需要跨公网或备用连接时才部署。将 Relay 地址与令牌配置到 Hub；Bridge 从 Hub 获取配置，Tray 也会自动同步。公网连接必须使用 HTTPS/WSS。

Hub 和 Bridge 示例配置可从 [`config/`](config/) 目录查看。Agent 执行部署时，可先阅读[Bridge 示例配置](config/bridge.example.yaml)、[Hub 示例配置](config/hub.example.yaml)和[部署指南](docs/deployment.md)，其中给出了服务地址、适配包及 Relay 的配置形状。正式 RC 镜像内置相应签名仓库和公开公钥；Bridge 首次直连 Hub 后固定信任该公钥。发布者离线维护签名私钥，普通使用者无需处理签名仓库。所有示例地址、名称和令牌均为占位值，不可直接作为实际凭据使用。

手机推送属于可选功能。Bridge 在线不代表 Agent 钩子已生效；请按部署指南运行一次真实 Agent 会话并核对 Hub 中收到的事件。

## 参考文档

- [脚本分类](scripts/README.md)：构建、安装、检查和发布入口。
- [部署指南](docs/deployment.md)：面向使用者的安装、配置、验收和故障处理。
- [Windows 客户端](docs/windows-client.md)：Tray 的运行与便携配置。
- [适配器说明](docs/adapters.md)：受支持的 Agent、钩子与签名包更新。
- [需求文档](docs/agent-pulse-requirements.md)：项目功能范围。
- [实现设计](docs/agent-pulse-design.md)：系统结构和技术边界。
- [开发记录](docs/development-status.md)：实现进度和验证证据。
- [Linux 运行说明](docs/linux-quickstart.md)：Linux Bridge、Hub、Relay 的运行说明。
- [镜像构建与部署](docs/images.md)：容器镜像和架构说明。
- [变更记录](CHANGELOG.md)：已发生的功能和工程变更。
## 开发者：从源码运行

开发与验证命令面向贡献者。部署已发布镜像的使用者无需安装这些工具。
## 工作目录

```text
go/go.mod                     Hub/Relay/Bridge 共用的 Go module
go/cmd/                       服务、测试客户端、发布工具与独立 hook 程序入口
go/internal/                  组件内部实现
go/protocol/                  Go 协议类型
protocol/                     跨语言 JSON 示例和说明
web/hub/                      Vue 3 + TypeScript + Vite 管理界面
adapters/                     来源清单、说明及 OpenCode 插件源码
tray/                         Rust + GPUI 原生桌面客户端
deploy/                       Docker、Compose、代理与 systemd 示例
scripts/build/                开发与发布构建、跨平台打包
scripts/install/              用户预构建安装入口，不调用编译器
scripts/check/                开发检查与脚本回归
scripts/release/              发布维护、仓库合并与草稿生成
tests/                        契约、集成和端到端验证
docs/                         需求、设计与进度
```

空目录通过 `.gitkeep` 保留。根目录是多语言工程入口；Go module 放在 `go/`，不因未来桌面依赖拆分三个服务的 module。

## 本地运行

Go 1.25 或更新的兼容版本，在仓库根目录构建：

```sh
pnpm --dir web/hub install --frozen-lockfile --ignore-scripts
pnpm --dir web/hub build
bash scripts/build/build.sh
./bin/agent-pulse-hub --config config/hub.example.yaml
```

另开两个终端分别运行：

```sh
./bin/agent-pulse-bridge serve --config config/bridge.example.yaml
```

```sh
./bin/agent-pulse-client --watch
```

在 Hub YAML 中设置 `web_directory: "./web/hub/dist"` 即可通过 Hub 根地址打开管理页面。测试客户端输出完整 JSON 快照。OpenCode 安装与真实宿主测试见 [适配器说明](adapters/opencode/README.md)。手机默认不发送；配置已有 HTTP 渠道后启用，具体业务成功码需要按所用渠道核实。

开发检查：

```sh
pnpm --dir adapters/opencode install --frozen-lockfile --ignore-scripts
pnpm --dir web/hub install --frozen-lockfile --ignore-scripts
bash scripts/check/check.sh
```

Linux 服务端开发先准备 Go；Web/TypeScript 使用 Volta + pnpm。Tray 沿用已确认的 Rust + GPUI；出现无法合理解决的关键桌面障碍时才评估 Go + Walk，不采用浏览器或 WebView 页面方案。Windows 原生交互必须在真实桌面验证。

Bridge 首版继续使用 Go；若后续等价功能实测表明 Rust 有实际资源收益，再评估迁移，保持事件协议与 Adapter 独立更新能力。

## 实现约定

- 按当前规模实现，模块内优先用普通函数和具体类型；不预建账号角色、通用仓储、插件平台或完整消息同步框架。
- Hub 保存权威状态；Relay 只持久保存尚未交给 Hub 的上行数据；Bridge 事件队列仅在内存。
- Tray 重连先提交离线已读、重新获取快照，再展示仍有效的提醒。
- 内网个人 Hub/Web 默认免登录，公网 Relay 保留共享令牌；不强制操作系统凭据库。
- 手机渠道复用现有 HTTP 接口；本地/云端来源共用事件格式。
- 人工配置采用 YAML；Adapter 转换逻辑独立于 Bridge，签名索引通过 Hub 分发，主机自动校验、安装和切换。
- 正式文档只维护现行规则，变更历史与开发验证记录独立保存。
- 原生运行、交叉编译和模拟分别记录，不把目录或设计当作功能交付。
