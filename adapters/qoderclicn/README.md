# qoderclicn 适配器

面向 Qoder 中国版 CLI，默认用户配置位于 `~/.qoder-cn/settings.json`。安装和更新见 [来源接入说明](../../docs/adapters.md)，兼容版本见 [包清单](manifest.json)。宿主须信任项目才能加载项目 hook。

真实验证使用 DeepSeek-Flash，当前宿主的命令行标识为 `dfmodel`；显示名直接传给 `--model` 可能被回退到 Auto。测试必须检查输出，不能把回退后的模型记为指定模型通过。

```sh
# 先创建仅用于测试的目录，并在 qoderclicn 内确认信任该目录后退出。
AGENT_PULSE_QODER_WORKSPACE=/绝对路径/可信测试目录 uv run --offline --no-project python tests/e2e/linux_services.py
```

脚本会在该测试目录创建 `.qoder/settings.json`，不要指定日常项目。禁用工具和 MCP、不保存会话，依次验证原生事件、签名包安装、Bridge 转发、Hub 入库和本地 HTTP 推送接收；不会验证真实手机送达。

## 会话名称读取

已核验 Qoder 中国版 CLI **1.1.85** 的安装产物；会话名称不是同名目录中的 `state.json` 数据。宿主将明确的名称元数据追加到 `projects/<编码工作目录>/<会话标识>.jsonl`：

- 用户改名：`type` 为 `custom-title`，名称字段为 `customTitle`。
- 宿主生成的标题：`type` 为 `ai-title`，名称字段为 `aiTitle`。它是明确的标题元数据，不是会话摘要。
- 两类记录都必须包含与当前真实事件完全一致、区分大小写的 `sessionId`；同类名称按文件顺序取最后一条。用户名称优先于自动标题。
- 不将 `summary`、对话正文、项目目录名或会话标识自行生成名称；`last-prompt.lastPrompt` 的宿主默认展示规则见下节。会话标识只用于后台定位。

名称读取函数为 `qoderSessionTitle(sessionID, cwd, transcriptPath string) string`。真实事件提供绝对 `transcriptPath` 时优先使用，且文件名必须精确对应当前会话；否则按当前绝对工作目录及宿主配置根目录定位单个文件。不扫描其他项目或历史会话，不读取认证文件。默认根目录为 `~/.qoder-cn`，支持宿主的 `QODERCN_CONFIG_DIR`、`QODERCN_CLI_HOME`、`QODERCN_CONFIG_DIR_NAME` 环境覆盖；命令行 `--config-dir` 等未传入 Bridge 的覆盖应由事件实际文件路径定位，不能凭空推断。

1.1.85 的可配置 hook 枚举不含改名事件。SDK 内部的 `sessionRenamed` 通知不是本适配器的用户配置 hook。因此由调用方在**下一次真实事件**到达时读取名称，不增加定时轮询或文件监听；通用事件转换已接入此读取函数。名称缺失返回空，由调用方保留已有名称，不影响原事件处理。

读取边界与宿主会话列表一致：不超过 128 KiB 的文件完整读取，大文件只读取首尾各 64 KiB，并忽略被窗口截断的行。宿主写入器会把缓存名称补到文件尾部；若名称只存在于未读取的中间区域，本函数返回空，不为此无限读取正文。每次调用重新读取，不缓存旧名称。Bridge 现有的 1024 字节标题上限保持不变，截断不破坏 UTF-8。

### 验证边界

字段依据来自已安装宿主的捆绑代码；名称读写的隔离重放和原生 `--list-sessions` 对照、Go 转换回归及适用版本见[开发记录](../../docs/development-status.md)。这些验证不等同于完整交互 `/rename` 或端到端通知验收，Windows 原生 Qoder 不在当前支持范围。

### 默认展示名称

手工标题 `custom-title.customTitle` 优先于自动标题 `ai-title.aiTitle`；都不可用时读取同一会话的 `last-prompt.lastPrompt`。这是 Qoder 1.1.89 原生会话列表已经采用的展示元数据，不是解析 `user.message.content` 猜名称。仍按精确会话 ID、首尾有界窗口读取，排除其他会话及无效类型；只在下一次真实事件到来时刷新，不轮询，名称读取不会上传历史消息正文。缺少全部明确名称时显示未命名。

## 完成输出与用户提问

原生 Stop 的 `last_assistant_message` 作为本轮最终回答，不读取用户提示词、思考或历史消息补答案。提问工具 PreToolUse 的 `tool_input.questions` 只提取 question、multiSelect、options 的 label/description，展示问题与选项；同一 AskUserQuestion 的 PermissionRequest 不再另生成批准提醒。其他权限请求保持等待批准，不上传任意工具参数。Notification 的 elicitation_dialog 只采用宿主明确提供的 message。

正文保持当前事件上限 32 KiB；超长时按 UTF-8 边界截断并标明，字段缺失或类型错误保留完成/等待状态而不猜内容。旧事件不会追补后来生成的回答，网络重试仍使用初次转换的同一载荷。
