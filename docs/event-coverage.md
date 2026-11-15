# Agent 事件及内容覆盖

本项目是状态与提醒工具，不是完整对话客户端。运行中状态通常没有正文；结束、提问、批准或失败事件尽量采用该原生事件已有的可见内容。统一事件正文最多 32 KiB，UTF-8安全截断并明确提示，不采集思考、用户回答、认证材料或历史对话来猜正文。旧事件不追补后来产生的内容。

## 当前采集规则

| Agent | 完成 | 等待输入 | 等待批准 | 失败、中断及结束 | 受限原因 |
| --- | --- | --- | --- | --- | --- |
| ZCode Windows | Stop 的 last_assistant_message | 仅 AskUserQuestion/ask_user_question 的 PreToolUse：问题、选项、说明；PostToolUse 恢复活动 | 不采集普通权限；提问不重复报批准 | 当前无稳定取消、失败或关闭 hook；不推断 | 原生 Stop 不保证强制取消/崩溃触发。普通工具主/子归属未验证，只接入提问并过滤明确父/子身份；不扩展到所有工具 |
| Qoder 中国 CLI | Stop 的 last_assistant_message | 提问工具 questions；elicitation_dialog 的 message | PermissionRequest 的工具名、命令/路径及说明白名单；提问权限请求忽略避免重复 | StopFailure 的 error/错误类型；SessionEnd 只记结束状态 | 任意工具响应、error_details、MCP 元数据不全量序列化；缺失内容保留状态。并非所有中断都有可配置原生事件 |
| Antigravity CLI | Stop 的 finalModelOutput | ask_question 的 toolCall.args.questions，支持原生字符串选项 | ask_permission 的 toolCall.args 可见动作白名单 | error 终止值用 error；取消/中断只采用终止值，无详细原因时只记状态；未知终止值标待确认 | 完成须符合完全空闲规则；不读取 modelThinking、lastUserInput 或 transcript。ACP与明确子代理过滤；hook 字段缺失不证明全部内部归属 |
| OpenCode | 观察原生 session.text.ended，按助手消息和 ordinal 汇总最后一个助手消息，在 execution.succeeded 发送；不采集 reasoning/delta | form.created 的表单标题、可见字段、说明、选项；表单回复/取消后恢复活动 | permission.asked 的 action 与 message，不传权限资源/元数据 | execution.failed 的 error.message，不传原始 response.body；interrupted/deleted 只记对应状态 | 完成事件本身只有 sessionID；插件错过文本事件或启动于本轮中途时不从历史回填，明确提示未取得正文。动态条件字段缺当前答案时不猜是否可见，隐藏/条件字段不展示 |
| Codex CLI | 当前 Stop 仅提供 session_id、turn_id，无原生最终回答字段；只记完成状态 | 当前不接工具提问 | 当前不接工具批准 | Interrupt/SessionEnd 只记状态，原生载荷无详细内容 | 已验证安装基线的 Stop 仅有身份和轮次；工具 hook 可能共享父会话ID，尚无可靠主/子归属，因此不增加通知。不给缺口编造回答，不读取完整 transcript 补内容 |

所有等待记录仍以宿主实际发生、主会话归属可用为前提；界面展示已有正文不意味着宿主的所有事件都已产生 hook。内容为空时状态仍保留，文档中的字段来源与真实验证范围分开。

## 内容来源证据

- ZCode 3.18.2 的实际捆绑运行代码将 Stop 的 responseText 序列化为 last_assistant_message，并将工具名、参数和子代理身份写入 Pre/PostToolUse。采用原生提问工具前后事件，不操作权限决定。
- Qoder 1.1.89 的实际模块 fireStopEvent、firePreToolUseEvent、firePermissionRequestEvent、fireNotificationEvent 明确提供上述字段。
- Antigravity 1.5.9 的实际二进制 protobuf 描述符：StopHookArgs.final_model_output/error，HookToolCall.args，PostInvocation.model_thinking 与模型输出是独立字段。提问工具原生 schema 提供 questions/question/options；不用字段猜测或扫描会话正文补答案。静态取证不等同真实模型任务验收。
- OpenCode 2.0.20 的锁定原生 schema：session.text.ended 提供 assistantMessageID/ordinal/text；execution.succeeded 只带 sessionID；form/permission/failed 的可见字段按本页白名单提取。缓存仅当前最后助手消息，分片与会话数有界，重放/重试保留事件首次转换内容。
- Codex 0.167.0 的真实宿主配合本地 Responses 模拟服务留下 Stop 字段记录，只有 session_id/hook_event_name/turn_id；不是在线模型调用，也不据此宣称未来宿主版本永不支持正文。

## 展示与阅读

事件时间线按持久化事件位置倒序，最新在上。订阅发现当前会话新活动后，以 after 位置升序分批拉取并去重加入；断线超过30条仍补齐中间记录。已加载旧记录的展开与已读状态保留，不按时间戳排序。

- 处于顶部或最新事件标题处时，新增事件自动定位到新标题。
- 向下阅读历史或长正文时，按当前事件及视口偏移保持阅读位置；固定底栏显示新事件数与“回到最新”。
- 长正文保留换行、自动折行；无需完整对话渲染、富文本编辑器或嵌套滚动。折叠可收回长内容，32 KiB 截断说明在正文中明确可见。
- 不因为新事件弹出而自动全部标已读；前台、展开、可见累计3秒的现行规则保持。

实现与验收证据见[开发记录](development-status.md)。真正原生输出、提问内容及用户实收的结果需使用对应最新适配包验证；模拟事件仅证明链路/界面，不冒充宿主或手机验收。
