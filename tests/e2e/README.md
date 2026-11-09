# Linux 端到端测试

测试使用临时数据库和回环监听，不修改 Git。默认测试不向手机发送；真实手机脚本须明确传入 `--send`。宿主测试保留临时诊断目录，容器和手机测试清理自身资源。构建前提：

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

需要 OpenCode 2.0.20。使用临时配置和插件仓库，验证独立包 loader、原生会话和表单、已读及 Hub 强制重启恢复，不调用模型。具体脚本位于 `adapters/opencode/test/smoke.ts`。

## 常规可靠性测试

`bash scripts/check.sh` 包括 Go 竞态检测、本地网络集成、TypeScript 类型检查、映射样例和 Web 构建。Relay 离线队列、双向连接和适配包经中继下载在 Go 集成测试中验证；同机隔离状态目录模拟多个主机，不能替代物理多机部署验收。

## Antigravity ACP

```sh
uv run --offline --no-project python tests/e2e/antigravity_acp.py
```

需要已登录的 agy 1.5.3；会产生一条真实模型请求。脚本使用默认登录目录，不读取认证文件、不自动批准工具。专用临时项目通过签名包安装 hooks，经主会话包装器调用 ACP，核对开始、活动、完成、未读及实际运行版本；不保存模型回复正文。真实子代理和会话续接另行验收。

## 真实手机渠道

```sh
uv run --offline --no-project python tests/e2e/phone_channel.py --send
```

读取 `~/.local/bin/codex-notify.sh` 中既有 `CODEX_NOTIFY_API_URL` 默认值，或使用当前同名环境变量。只解析已知字面量，不执行脚本；密钥和完整 URL 不写入文件、命令参数或证据。Hub 向临时本地转发器发送，转发器最多调用真实渠道一次，并仅保留 HTTP 状态、数值/布尔业务码及是否出现成功提示。该转发器用于核对真实响应，不作为正式部署组件；正式 Hub 直接调用渠道并解析业务状态。收到成功响应后仍需在手机上核对实收。

## 镜像部署与恢复

```sh
uv run --offline --no-project python tests/e2e/relay_containers.py \
  --arch amd64 --hub-image agent-pulse-hub:test-amd64 --relay-image agent-pulse-relay:test-amd64
```

先按 [镜像说明](../../docs/images.md) 构建。需要 Linux Docker；测试只创建随机名称的容器和卷，使用回环端口、宿主网络及临时测试令牌。覆盖未鉴权拒绝、离线 503、离线持久化、重复投递、Relay/Hub 强制终止后的恢复、反向查询、页面与已读。结束后移除自身容器和卷。`--arch arm64` 需要 arm64 主机或已注册的 QEMU；模拟运行不等于真机验收。
