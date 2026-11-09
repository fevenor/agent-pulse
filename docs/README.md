# 文档索引

- [`agent-pulse-requirements.md`](agent-pulse-requirements.md)：产品需求、范围和验收标准。
- [`agent-pulse-design.md`](agent-pulse-design.md)：组件边界、目录结构、协议、数据模型、部署和分阶段实现设计。
- [`development-status.md`](development-status.md)：已完成事项、本机环境、复现命令、实施清单与待验证项。
- [`linux-quickstart.md`](linux-quickstart.md)：当前可运行的 Linux 构建、启动、事件接入和 HTTP 推送说明。
- [`adapters.md`](adapters.md)：来源接入边界、独立包构建、签名发布、多主机安装与更新。
- [`images.md`](images.md)：GitHub 镜像构建发布、amd64/arm64 容器验证和 Relay 部署。
- [`windows-development.md`](windows-development.md)：Windows 工作副本位置、原生工具链及 Tray 接续顺序。
- [`web/hub/README.md`](../web/hub/README.md)：管理页面构建与同源部署。
- [`CHANGELOG.md`](../CHANGELOG.md)：需求、设计和工程调整的独立变更历史。

需求、设计和运行说明维护现行规则，不穿插日期式修订说明或历史方案叙述；修改经过集中写入变更记录，运行验证结果记入开发记录。

OpenCode 安装及能力边界见 [`adapters/opencode/README.md`](../adapters/opencode/README.md)。跨语言样例放在 [`protocol/`](../protocol/README.md)，Go 类型放在 `go/protocol/`；不提前建设协议代码生成或插件框架。
