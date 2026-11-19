# Antigravity CLI 适配器

安装 hook 后直接运行 `agy`，或继续使用原有 CLI 参数；无需 Bridge 包装启动、特殊授权或绑定文件。配置使用命名 hook 对象与 camelCase 载荷，版本政策见 [来源接入说明](../../docs/adapters.md)。

`agy acp` 是协议调用模式，在本项目中视为子代理运行，不产生面向用户的会话提醒。Bridge 的共用本机接入入口过滤 ACP 来源；适配器另外过滤明确父/子字段及匹配当前 ID 的 `is_internal: true` 内部会话。缺少缓存或标识不阻断普通启动；这些缺失值也不被描述为已经证明主身份。

正常停止的 `model_stop` / `NO_TOOL_CALL` 在 `fullyIdle=false` 时保留运行；`NO_TOOL_CALL` 仅在明确完全空闲时确认本轮完成。CLI小写错误/取消与App的ERROR、USER_CANCELED及执行次数/预算上限分别映射失败或中断，不被后台空闲标志吞掉。TERMINAL_STEP_TYPE、自定义hook终止等没有足够最终语义的原因仍标未知。原生被动 hook 回复不替用户批准工具。直接启动及 ACP 元数据过滤的隔离验证见 [端到端说明](../../tests/e2e/README.md)。

## 会话名称

名称读取只随真实 hook 触发，不增加轮询或后台查询。按原生transcriptPath选择App/CLI/IDE数据根，缺失时回退用户目录 `.gemini/antigravity-cli`：

1. 优先按精确 `conversationId` 读取 `annotations/<会话标识>.pbtxt` 的顶层 `title` 字符串，这是宿主明确持久化的用户名称。
2. 注解没有标题或不可用时，回退到 `cache/conversation_metadata.json`，仅采用 `conversations[conversationId].summary` 中匹配的 `ID` 与 `Title`。`summary` 是元数据对象，`Preview` 不作为名称或正文投递。

优先读取注解，是为了不依赖改名后 JSON 缓存何时刷新。名称字段依据和隔离转换验证见[开发记录](../../docs/development-status.md)。静态产物分析与元数据对照不等同于完整交互 `/rename` 验收。

当前配置 hook 包括会话开始、模型调用、工具调用和停止，没有可配置的改名事件；用户名称随下一次真实事件传递。只读取宿主文本序列化器输出的顶层标量，使用 Go 标准库解码引号及转义，不实现通用 protobuf 文本解析。注解限制 64 KiB，JSON 缓存限制 8 MiB；缺失、不可读、损坏、未知格式时不影响原事件投递，不将摘要、提示词或 ID 作为名称。

App会话数据库仅按精确ID读取title、parent_conversation_id、nesting_depth和source；明确父会话、嵌套或内部标志的子事件过滤。名称缺失与身份暂不可确认独立表示；CLI普通启动仍正常采集。App路径明确但身份元数据尚未可用时，本机Bridge暂存本事件并由已安装签名适配器异步核对；主身份确认后按序转发，子身份确认后过滤。等待占用已有内存队列配额，health的pending包含待身份事件并另列pending_identity；长期无法确认时保留等待，退出的内存丢失边界与原队列一致，不默认假定主身份。显式改变默认数据根的实例不猜测路径，不读取认证文件、preview、raw_summary或历史模型回复。取证、隔离的“注解已改名但 JSON 缓存仍旧”转换回归及原生验证边界见 [开发记录](../../docs/development-status.md)。

## 原生事件正文

Stop 的 finalModelOutput 提供本轮可见最终回答，error 提供失败原因；不采集 modelThinking/lastUserInput 或回读 transcript。ask_question 的 toolCall.args.questions 提供问题与字符串选项；ask_permission 仅采用工具名、command/path/description/reason 等可见摘要白名单，不序列化任意参数。内容缺失只保留状态，32 KiB上限及受限原因见[事件与内容覆盖](../../docs/event-coverage.md)。


## 按需身份诊断

适配器接受 `--identity-diagnostics <已存在的绝对目录>`，也可从 `AGENT_PULSE_IDENTITY_DIAGNOSTICS` 读取目录；环境变量可随原生hook传入现有Bridge dispatch，再由子进程继承。默认不启用，不自动创建目录或改动hook。只有对应新构建的适配器才提供此诊断能力，源码存在不代表已发布包已经具备。

每次转换写独立私有JSON，目录已有256份文件后停止新增；并发调用可能有少量边界超出。仅保存原生身份字段的有限字符串、hook名、停止原因/空闲标志、选用数据根及依据、身份结论、数据库/缓存状态、查询耗时和SQLite数值错误码。不保存标题、提示词、正文、工具参数、完整transcript路径或原始错误文本。失败不改变原hook回复与转发，不把本地诊断发送到Hub。

identity的main/child/unknown分别表示明确主会话、明确子会话、暂不可确认；unknown并不证明是主会话。metadata为空表示在元数据读取前已被明确身份过滤，或该hook未转换为事件，结合native_identity和decision判断。此工具用于短时现场取证，不能用后来的查询结果冒充首次调用条件；启用真实hook诊断须在授权后进行并保留恢复方式。


## App 后台收尾

已确认主会话的正常Stop若fullyIdle=false，登记本轮原生空闲观察；仅查询同ID的last_user_input_time、last_user_input_step_index、status、not_fully_idle、killed及父子字段，不读取raw_summary、预览或对话正文。真实活动取消旧观察，新的正常Stop可重新登记；用户轮次改变时旧观察结束。原生状态明确空闲且后台已结束后确认本轮完成，明确killed时报告中断；状态持续不可读取只降为未知，不以时长判断完成。

本机轮次标识用于原生Stop和观察结果去重，不进入Hub网络协议。它是Bridge进程内的有界确认记录，与现有内存入口保证一致，不建设跨重启事件日志。未知身份等待与后台终态观察都要求匹配的新Bridge及适配包；新包不能先投给仍使用旧本机结构的Bridge。
