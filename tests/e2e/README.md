# 端到端测试

测试使用临时数据库和回环监听，不修改 Git。默认测试不向手机发送；真实手机脚本须明确传入 `--send`。宿主测试保留临时诊断目录，容器和手机测试清理自身资源。原始输出如需保存，重定向或复制到 Git 忽略的 `tmp/verification/`；提交的开发记录只保留脱敏后的命令、结果与边界，不提交原始日志、截图或机器路径。构建前提：

```sh
pnpm --dir adapters/opencode install --frozen-lockfile --ignore-scripts
bash scripts/package-adapters.sh ./dist/repository 1
```

## Codex

```sh
uv run --offline --no-project python tests/e2e/codex_native.py
```

使用 Codex 0.167.0 和本地 Responses 模拟服务，单实例串行运行，不调用在线模型。临时项目中的 `hooks.json` 由真实 Bridge 从签名包安装，捕获原生 SessionStart、UserPromptSubmit、Stop、SessionEnd，并核对 Hub 历史及适配器运行版本。

测试在本次调用中信任自己生成并审阅的 hook，不持久修改用户信任设置，不读取用户模型配置。`projects` 通过 TOML 内联表传递临时目录的信任值；不能把带引号路径的点分参数误当成 TOML 表。正式安装仍由用户在 Codex 中确认 hook 信任。

## qoderclicn

使用已登录的 qoderclicn 1.1.85，会产生一条真实 DeepSeek-Flash 请求。模型参数为 `dfmodel`；脚本检查宿主未回退到 auto。工具、MCP 和会话持久化均关闭，不访问项目文件内容。

准备专用空测试目录，先在其中启动 qoderclicn，由宿主正常确认该测试项目可信，然后退出。不要指定日常工作项目。未信任的项目可能正常回答但不执行 hook；脚本会以缺少原生事件判为失败。

```sh
AGENT_PULSE_QODER_WORKSPACE=/专用测试工作区 uv run --offline --no-project python tests/e2e/linux_services.py
```

脚本暂时写入该目录的 `.qoder/settings.json`，结束时恢复原内容；若外部在测试中修改该文件，则保留外部修改。默认新建的临时目录通常尚未被宿主信任，适合定位加载限制，不保证直接通过。

验证签名包安装、真实 hook、Bridge 转发、Hub 入库、完成后会话关闭仍保留提醒、本地 HTTP 推送以及实际运行版本。等待 Hub 启动保护窗口会消耗约 35 秒。结果写入临时目录的 `evidence.json`；本地请求成功不代表手机实收。

## OpenCode

```sh
AGENT_PULSE_PACKAGE_SMOKE=1 pnpm --dir adapters/opencode smoke
```

需要 OpenCode 2.0.20，并先构建当前独立包。使用临时配置和插件仓库，验证独立包 loader、原生会话标题和表单、已读及 Hub 强制重启恢复；版本从当前清单读取，旧产物会提示重新打包。默认不调用模型；需要另做真实模型测试时使用 build 角色及 OpenCode 提供的免费模型。具体脚本位于 `adapters/opencode/test/smoke.ts`。

## 管理网页与跨平台包

```sh
pnpm --dir web/hub build
bash scripts/build.sh
uv run --offline --no-project python tests/e2e/hub_web.py
uv run --offline --no-project python scripts/test_merge_adapter_repositories.py
```

网页测试需要已安装的 agent-browser 和 Chromium，启动独立浏览器会话与真实 Hub，验证别名与来源状态、归档/生效状态计数及筛选、友好名称搜索、恢复自动跟随、未读保留和明确已读、移动布局。使用临时数据，截图留在临时诊断目录，结束后关闭自建进程。包合并测试只使用本地合成 ZIP，覆盖跨平台合并、通用插件内容去重、冲突与损坏拒绝，并已纳入 Linux/Windows Workflow。

## 常规可靠性测试

`bash scripts/check.sh` 包括 Go 竞态检测、本地网络集成、TypeScript 类型检查、映射样例和 Web 构建。Relay 离线队列、双向连接和适配包经中继下载在 Go 集成测试中验证；同机隔离状态目录模拟多个主机，不能替代物理多机部署验收。

Windows 联调先用 `scripts/build.ps1 -DevelopmentTools` 生成 `bin/dev/` 测试服务 和 `scripts/check.ps1 -Tray -Integration`。Rust 中的真实服务测试默认忽略，`-Integration` 显式启用并验证 Windows Hub/Relay、客户端离线已读和重启恢复。Windows 安装器测试在普通 Go 检查中运行。

默认 `scripts/build.ps1` 只提供用户 Tray，`scripts/build.ps1 -Demo` 提供前台演示。关闭其他 Tray 实例后执行 `uv run --offline --no-project python tests/e2e/windows_desktop.py`，验证发布参数拒绝、中文空格便携目录和不同启动工作目录、跨安装路径且不同配置的单实例拦截、内置 Bridge 启动和父进程结束后的清理；测试不操作桌面界面。加入 `--with-demo` 时，检查发布/演示二进制的 GUI/控制台子系统和演示专属文案隔离，另在测试专属隐藏控制台验证默认不打开设置、前台日志、真实 Ctrl+C 正常退出、不读取指定生产配置且不产生生产状态。须先构建 `-Demo`。Rust 单元测试在临时目录创建真实 `.lnk` 验证自启动维护，不向系统启动文件夹写入，不写注册表；禁用状态解析单独验证。原生菜单、悬浮拖拽、窗口滚动缩放和真实登录启动仍需单独验收。

Windows 与 Debian 的双来源联调使用 `uv run --offline --no-project python tests/e2e/windows_wsl.py`；前提是已构建 Windows 程序、测试标签的 Docker 镜像和 Linux Bridge，准备命令见 [Windows 开发](../../docs/windows-development.md#debian-与容器)。脚本清理自己创建的临时容器、文件与进程，不使用真实 Agent 或手机配置。

## Antigravity ACP

```sh
uv run --offline --no-project python tests/e2e/antigravity_acp.py
# 两条真实请求：创建会话后结束 ACP 进程，再加载原会话继续。
uv run --offline --no-project python tests/e2e/antigravity_acp.py --resume
```

需要已登录的 agy 1.5.3；默认一条真实模型请求，`--resume` 共两条。脚本使用默认登录目录，不读取认证文件、不自动批准工具。专用临时项目通过签名包安装 hooks，经主会话包装器调用 ACP，核对开始、活动、完成、未读及实际运行版本；不保存模型回复正文。续接测试使用 `--pulse-main-session` 指定原主会话，检查跨进程恢复后仍只有同一主会话且仅最新完成提醒有效。真实子代理和非正常退出另行验收。

## 真实手机渠道

```sh
uv run --offline --no-project python tests/e2e/phone_channel.py --send
```

先在本次进程环境中提供 `CODEX_NOTIFY_API_URL`；脚本不会默认读取任何开发机的私人通知脚本。兼容已有通知脚本时可显式传入 `--script <脚本路径>`，仅在环境变量缺失时解析其中已知的渠道字面量，不执行脚本。密钥和完整 URL 不写入文件、命令参数或证据。Hub 向临时本地转发器发送，转发器最多调用真实渠道一次，并仅保留 HTTP 状态、数值/布尔业务码及是否出现成功提示。该转发器用于核对真实响应，不作为正式部署组件；正式 Hub 直接调用渠道并解析业务状态。收到成功响应后仍需在手机上核对实收。

## 镜像部署与恢复

```sh
uv run --offline --no-project python tests/e2e/relay_containers.py \
  --arch amd64 --hub-image agent-pulse-hub:test-amd64 --relay-image agent-pulse-relay:test-amd64
```

先按 [镜像说明](../../docs/images.md) 构建。需要 Linux Docker；测试只创建随机名称的容器和卷，使用回环端口、宿主网络及临时测试令牌。覆盖未鉴权拒绝、离线 503、离线持久化、重复投递、Relay/Hub 强制终止后的恢复、反向查询、页面与已读。结束后移除自身容器和卷。`--arch arm64` 需要 arm64 主机或已注册的 QEMU；模拟运行不等于真机验收。
