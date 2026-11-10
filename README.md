# AgentPulse

AgentPulse 是供个人使用的多主机 Agent 会话通知工具。Hub（会话中心）保存会话与已读状态，Relay（公网中继）提供备用通路，Bridge（本地接入进程）采集事件，Tray（桌面小组件）显示提醒。

Linux 组件包括 Hub、Relay、Bridge、Vue 管理页面、命令行测试客户端、手机 HTTP 推送和独立适配包更新。Windows 单入口客户端提供紧凑托盘菜单、可贴边悬浮图标、会话小窗/详情、归档与人工状态修正、提醒卡片和离线已读恢复，使用 Rust + GPUI。接入 OpenCode、Codex、Antigravity CLI 与 qoderclicn；各来源及桌面行为的验证边界见开发记录。

- [需求文档](docs/agent-pulse-requirements.md)：范围与验收。
- [实现设计](docs/agent-pulse-design.md)：简化后的结构、可靠性、Tray 选型及扩展边界。
- [开发记录](docs/development-status.md)：环境、已完成事项和后续工作。
- [Linux 运行说明](docs/linux-quickstart.md)：构建、启动、接入 OpenCode、配置 HTTP 推送和排查。
- [来源接入和更新](docs/adapters.md)：四种来源、签名发布、自动安装与兼容限制。
- [镜像构建与部署](docs/images.md)：GitHub 自动构建、双架构容器验证和 Relay 镜像部署。
- [Windows 开发](docs/windows-development.md)：原生工具链、检查命令、Debian 联调和剩余验收。
- [Windows 客户端](docs/windows-client.md)：运行、离线恢复、Bridge 管理和原生打包。
- [变更记录](CHANGELOG.md)：需求、设计和工程变更历史。

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
scripts/                      按需添加的开发/安装脚本
tests/                        契约、集成和端到端验证
docs/                         需求、设计与进度
```

空目录通过 `.gitkeep` 保留。根目录是多语言工程入口；Go module 放在 `go/`，不因未来桌面依赖拆分三个服务的 module。

## 本地运行

Go 1.25 或更新的兼容版本，在仓库根目录构建：

```sh
pnpm --dir web/hub install --frozen-lockfile --ignore-scripts
pnpm --dir web/hub build
bash scripts/build.sh
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
bash scripts/check.sh
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
