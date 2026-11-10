# 文档索引

- [`deployment.md`](deployment.md)：首次部署的完整路径，覆盖 Hub、签名适配包、Bridge、可选 Relay、验收、排错和升级。
- [`agent-pulse-requirements.md`](agent-pulse-requirements.md)：产品需求、范围和验收标准。
- [`agent-pulse-design.md`](agent-pulse-design.md)：组件边界、目录结构、协议、数据模型、部署和分阶段实现设计。
- [`development-status.md`](development-status.md)：已完成事项、验证平台与版本、复现命令和待验证项。
- [`development-plan.md`](development-plan.md)：跨平台收尾、实际接入、自动检查和部署验收的后续顺序与完成条件。
- [`linux-quickstart.md`](linux-quickstart.md)：当前可运行的 Linux 构建、启动、事件接入和 HTTP 推送说明。
- [`adapters.md`](adapters.md)：来源接入边界、独立包构建、签名发布、多主机安装与更新。
- [`images.md`](images.md)：GitHub 镜像构建发布、amd64/arm64 容器验证和 Relay 部署。
- [`windows-development.md`](windows-development.md)：Windows 工作副本位置、原生工具链及 Tray 接续顺序。
- [`windows-client.md`](windows-client.md)：Windows 原生客户端运行、离线已读、Bridge 管理、适配包和打包。
- [`windows-desktop-design.md`](windows-desktop-design.md)：已确认的托盘/悬浮/会话详情交互、单入口交付、单实例、开发演示与 GPUI 可行性。
- [`web/hub/README.md`](../web/hub/README.md)：管理页面构建与同源部署。
- [`CHANGELOG.md`](../CHANGELOG.md)：需求、设计和工程调整的独立变更历史。

需求、设计和运行说明维护现行规则，不穿插日期式修订说明或历史方案叙述；修改经过集中写入变更记录，运行验证结果记入开发记录。

仓库文档、配置示例和验证证据不得写入开发者的真实绝对路径、用户名、主机标识、硬件清单或私人软件配置。示例使用相对路径、环境变量或明确占位符；安装状态和本机偏好不写成项目规则。验证记录仅保留复现所需的平台、相关版本、命令、结果与边界，并注明验证日期；更换机器时重新检查，不沿用旧机器的环境结论。

实际实例的运行快照、当前会话、在线设备和临时部署安排不提交到代码仓库，由部署者保存在私有记录中。

实施过程的原始日志、审计结果、截图、录屏、临时设计预览和一次性脚本统一放在仓库根目录的 `tmp/`，由 Git 和 Docker 构建上下文排除。开发记录保留脱敏后的文字结论、复现命令和未验证边界，不链接仅本机存在的临时文件。测试内部可使用系统临时目录并按测试约定清理；需保留或汇集的诊断产物放入 `tmp/`。正式程序资源、可复用测试样例和维护中的设计文档继续随源码管理。

OpenCode 安装及能力边界见 [`adapters/opencode/README.md`](../adapters/opencode/README.md)。跨语言样例放在 [`protocol/`](../protocol/README.md)，Go 类型放在 `go/protocol/`；不提前建设协议代码生成或插件框架。
