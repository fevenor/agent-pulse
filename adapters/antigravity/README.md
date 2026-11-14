# Antigravity CLI 适配器

安装 hook 后直接运行 `agy`，或继续使用原有 CLI 参数；无需 Bridge 包装启动、特殊授权或绑定文件。配置使用命名 hook 对象与 camelCase 载荷，版本政策见 [来源接入说明](../../docs/adapters.md)。

`agy acp` 是协议调用模式，在本项目中视为子代理运行，不产生面向用户的会话提醒。Bridge 的共用本机接入入口过滤 ACP 来源；适配器另外过滤明确父/子字段及匹配当前 ID 的 `is_internal: true` 内部会话。缺少缓存或标识不阻断普通启动；这些缺失值也不被描述为已经证明主身份。

普通 CLI 的 `model_stop` 对应本轮结束；错误与中断按实际终止值处理，未知停止原因仅标未知。原生被动 hook 回复不替用户批准工具。直接启动及 ACP 元数据过滤的隔离验证见 [端到端说明](../../tests/e2e/README.md)。

## 会话名称

名称读取只随真实 hook 触发，不增加轮询或后台查询。默认 CLI 数据目录为用户目录 `.gemini/antigravity-cli`：

1. 优先按精确 `conversationId` 读取 `annotations/<会话标识>.pbtxt` 的顶层 `title` 字符串，这是宿主明确持久化的用户名称。
2. 注解没有标题或不可用时，回退到 `cache/conversation_metadata.json`，仅采用 `conversations[conversationId].summary` 中匹配的 `ID` 与 `Title`。`summary` 是元数据对象，`Preview` 不作为名称或正文投递。

优先读取注解，是为了不依赖改名后 JSON 缓存何时刷新。当前产物候选 1.5.9 的静态取证确认注解的 `title` 字段、按 ID 保存的 `.pbtxt` 路径及文本序列化写入；此前 1.5.8 的只读 ACP/本地元数据对照确认非空标题与宿主列表一致。两类证据按版本分别记录，不将静态调用或隔离测试当作真实交互 `/rename` 验收。

当前配置 hook 包括会话开始、模型调用、工具调用和停止，没有可配置的改名事件；用户名称随下一次真实事件传递。只读取宿主文本序列化器输出的顶层标量，使用 Go 标准库解码引号及转义，不实现通用 protobuf 文本解析。注解限制 64 KiB，JSON 缓存限制 8 MiB；缺失、不可读、损坏、未知格式时不影响原事件投递，不将摘要、提示词或 ID 作为名称。

当前范围只覆盖默认 CLI 应用目录。显式改变 `--gemini-dir` / `--app-data-dir` 的实例不猜测缓存路径，也不读取会话数据库、认证文件或模型回复。取证、隔离的“注解已改名但 JSON 缓存仍旧”转换回归及原生验证边界见 [开发记录](../../docs/development-status.md)。
