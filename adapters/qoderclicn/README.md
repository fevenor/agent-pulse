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
- 不将 `summary`、`last-prompt`、首条提示词、正文、项目目录名或会话标识作为名称。会话标识只用于后台定位。

名称读取函数为 `qoderSessionTitle(sessionID, cwd, transcriptPath string) string`。真实事件提供绝对 `transcriptPath` 时优先使用，且文件名必须精确对应当前会话；否则按当前绝对工作目录及宿主配置根目录定位单个文件。不扫描其他项目或历史会话，不读取认证文件。默认根目录为 `~/.qoder-cn`，支持宿主的 `QODERCN_CONFIG_DIR`、`QODERCN_CLI_HOME`、`QODERCN_CONFIG_DIR_NAME` 环境覆盖；命令行 `--config-dir` 等未传入 Bridge 的覆盖应由事件实际文件路径定位，不能凭空推断。

1.1.85 的可配置 hook 枚举不含改名事件。SDK 内部的 `sessionRenamed` 通知不是本适配器的用户配置 hook。因此由调用方在**下一次真实事件**到达时读取名称，不增加定时轮询或文件监听；通用事件转换已接入此读取函数。名称缺失返回空，由调用方保留已有名称，不影响原事件处理。

读取边界与宿主会话列表一致：不超过 128 KiB 的文件完整读取，大文件只读取首尾各 64 KiB，并忽略被窗口截断的行。宿主写入器会把缓存名称补到文件尾部；若名称只存在于未读取的中间区域，本函数返回空，不为此无限读取正文。每次调用重新读取，不缓存旧名称。Bridge 现有的 1024 字节标题上限保持不变，截断不破坏 UTF-8。

### 取证与验证边界

- 安装产物 SHA-256：`09f4459fca9d60209470fc4371c963b7af926e677ae89f11343c1041915f3b16`。REA（用于安装产物取证的工具）确认 ELF 包含 `.bun`，按其中的模块表取得原始捆绑代码，未打开共享原生分析会话。
- `chunk-5s6fas9k.js` 的 `/rename` 动作调用 `persistSessionTitle`；`chunk-7whgj9q8.js` 的交互运行绑定调用 `recordCustomTitle`；`chunk-3jkr9yw7.js` 的写入器保存上述 `custom-title` 记录，其 hook 枚举不含改名事件。
- `chunk-8zc6cb85.js` 的 `fR`、`wg` 按 `sessionId` 精确匹配并优先采用 `customTitle`、其次 `aiTitle`。项目没有采用宿主随后回退到提示词或摘要的行为。`chunk-s02hxn3d.js` 的 `un`、`Pe` 定义工作目录编码，包括 UTF-16 码元和长目录散列后缀。
- 对提取的实际改名动作及写入方法进行了隔离受控重放，两次改名写入同一会话；随后用原安装二进制的 `--list-sessions` 在隔离配置目录确认显示最新名称。重放的外围服务使用测试替身，这不是完整交互终端的 `/rename` 原生验收，也不证明端到端通知。
- Go 测试覆盖连续改名、用户标题优先、异会话与路径拒绝、缺失/损坏元数据、正文与摘要隔离、首尾读取窗口、UTF-8 长度边界及目录编码。没有调用在线模型，没有修改日常配置或既有会话；下一次事件名称刷新已由 Go 转换回归验证；Windows 原生运行另行验证。
