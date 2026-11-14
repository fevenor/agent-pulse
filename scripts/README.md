# 脚本入口

脚本按执行对象和用途分类，避免把开发构建步骤误当作普通用户安装要求。

| 目录 | 执行者与职责 | 主要入口 |
| --- | --- | --- |
| `build/` | 开发者或发布维护者；使用编译工具链构建、交叉编译和打包，不代表部署 | `build.sh`、`build.ps1`、`build-targets.sh`、`package-bridge.sh`、`package-adapters.*`、`package-windows.ps1` |
| `install/` | 普通用户；下载或显式复制已核验预构建，不调用编译器 | `bridge.sh` |
| `check/` | 开发者；常规检查与脚本回归，只在隔离目录验证安装 | `check.sh`、`check.ps1`、`test_*.py` |
| `release/` | 发布维护者；合并仓库、准备完整 Hub 镜像上下文、核验 CI 产物并创建草稿 | `merge_adapter_repositories.py`、`prepare-hub-release.py`、`create_rc_draft.py` |

## 用户安装

Linux 使用 `bash scripts/install/bridge.sh --setup`。安装器自动识别 Linux amd64/arm64，默认下载 GitHub 最新稳定发布；指定 `--version v版本` 可选择已公开的候选版本。二进制、同一 Release 的校验清单及架构验证通过后才安装；`--setup` 明确授权随后逐项配置 Agent 并启动用户服务：先说明进入配置，再展示 Agent 名称/版本/路径；配置结束后才说明以后如何用 setup 修改。OpenCode 自动识别已有配置，只有本次启用成功的 Codex CLI 提示额外授权。没有公开发布或附件缺失时会报错，不自动编译。

本地预构建模式 `--version v版本 --file 文件 --sha256 摘要` 只用于维护者提供的测试或离线交付。两种方式都不需要 Go、Node.js、Rust。已有配置和自定义服务保留；运行中的 Bridge 先确认队列清空并停止。公网下载通过 GitHub HTTPS 和同发布摘要校验，下载本身不替代 Agent 钩子信任或真实通知验收。

公开下载命令、平台边界和运行验收见[部署指南](../docs/deployment.md)。当前候选草稿是否可下载以 GitHub 实际发布状态为准，不把本地流程修改当成远端已发布。

## 开发与发布

- 源码构建：`bash scripts/build/build.sh`；跨平台构建：`bash scripts/build/build-targets.sh`。
- 常规检查：`bash scripts/check/check.sh`；Python 回归通过 `uv run --offline --no-project python scripts/check/test_install_bridge.py` 执行。
- Linux Bridge 发布构建：维护者准备固定 Go 1.27.4 后执行 `bash scripts/build/package-bridge.sh`，产出两架构二进制、构建信息及 SHA-256 清单。该脚本不会安装到用户目录或上传。
- RC 草稿消费同一工作流的 `linux-bridge` 产物，不临时替用户构建；远端写操作仍由发布者授权执行。
- Windows 构建、检查和便携打包分别见对应子目录，辅助工具 `windows-tools.ps1` 只属于开发构建。

本次整理不保留根目录含混的旧脚本别名；工程及文档统一使用上述现行路径。历史验证记录中的入口路径已按现行目录整理，不表示重新执行过历史验证。
