# Agent 事件及内容覆盖

本项目是状态与提醒工具，不是完整对话客户端。运行中状态通常没有正文；结束、提问、批准或失败事件尽量采用该原生事件已有的可见内容。统一事件正文最多 32 KiB，UTF-8安全截断并明确提示，不采集思考、用户回答、认证材料或历史对话来猜正文。旧事件不追补后来产生的内容。

## 当前采集规则

| Agent | 完成 | 等待输入 | 等待批准 | 失败、中断及结束 | 受限原因 |
| --- | --- | --- | --- | --- | --- |
| ZCode Windows | Stop的last_assistant_message | 提问工具的问题、选项、说明 | PermissionRequest的工具动作/目标及reason；不代替批准 | 普通工具完成/失败有记录；失败不等于主任务失败 | 强制取消、崩溃、关闭仍缺稳定主任务结束hook；原生工具归属与状态恢复必须实测 |
| ZCode Server Linux（WSL / SSH） | Stop的last_assistant_message，复用Windows转换 | 提问工具的问题、选项、说明 | PermissionRequest的工具动作/目标及reason；不代替批准 | 工具结果/失败按现有规则记录，不猜取消或进程退出为完成 | 独立Linux Bridge读取Server用户的钩子与会话库；源码已接入、包未公开，真实验证范围见开发记录 |
| Qoder 中国 CLI | Stop回答；StopFailure可附失败前可见输出 | 原生Elicitation表单及提问工具；ElicitationResult处理结果，不传用户答案 | PermissionRequest；PermissionDenied保留原因并恢复活动 | SessionEnd保留原生原因；工具失败只作普通记录 | 不传完整工具响应、error_details或认证URL；同请求不再重复混合弹框通知 |
| Antigravity CLI / App | CLI仅使用实际提供的finalModelOutput；App 2.32.1及2.33.0的WSL原生Stop未发送回答字段，不从历史补读；正常停止仍遵循完全空闲规则，App主会话后台收尾可观察同轮原生空闲状态；明确错误和取消按独立终态处理 | ask_question的问题、选项及is_multi_select | ask_permission的动作/目标/原因；App的run_command明确BypassSandbox请求的CommandLine/toolSummary，普通命令不报批准 | Stop错误/中断；PostToolUse的工具错误为普通记录 | App/CLI/IDE按原生路径隔离元数据，ACP及内部会话过滤；App 2.32.1的WSL实测确认多选及工具前后事件；内部描述符字段不等于用户hook载荷 |
| OpenCode | 本轮最后助手消息的text.ended缓存；全部截断有提示 | 表单标题/字段/说明/选项；可按原生默认值确定的条件字段 | 批准摘要及明确路径目标；结果区分允许/拒绝 | 失败摘要、中断原因；工具/步骤/整理错误及重试提示为普通记录 | 不从历史补回答，不传用户答案、完整response.body或任意权限元数据；不可确定的条件字段明确受限 |
| Codex CLI / 本地桌面 | 按已登记轮次读取原生最终task_complete的last_agent_message；无持久终态来源时保留Stop提供的字段 | request_user_input等已支持原生提问工具 | PermissionRequest的命令/可见原因 | 原生最终task_complete的error确认整轮失败；Interrupt与SessionEnd；工具返回恢复活动 | 只观察hook登记的主会话轮次及增量终态，不转换其他记录；无持久记录、来源不可读或超限时降为未知，不伪造失败 |

所有等待记录仍以宿主实际发生、主会话归属可用为前提；界面展示已有正文不意味着宿主的所有事件都已产生 hook。内容为空时状态仍保留，文档中的字段来源与真实验证范围分开。

模型调用的采集边界是整轮最终结束，而非每次请求尝试。单次请求失败及自动重试过程不采集、不入库、不提醒。Codex 持久主会话通过 UserPromptSubmit/Stop 登记本轮的增量终态观察，以最终 task_complete 区分正常完成与最终错误；Stop 可被其他钩子要求继续，不提前确认整轮结束。Interrupt 和 SessionEnd 保持原生接入。没有持久终态来源的临时会话不宣称支持最终模型失败，来源持续不可读时降为未知；不以逐次请求 hook 或任务空闲超时判断代替。

## 内容来源证据

- ZCode 3.18.2 的实际捆绑运行代码将 Stop 的 responseText 序列化为 last_assistant_message，并将工具名、参数和子代理身份写入 Pre/PostToolUse。采用原生提问工具前后事件，不操作权限决定。
- ZCode Server 3.18.2 Linux产物提供相同的原生process/argv钩子与标准输入字段，默认读取所在Linux用户的`.zcode/cli/config.json`；其会话表具有`title/title_source/parent_id`。WSL与SSH目标使用相同Server产物，不因桌面连接方式引入另一套转换；静态字段一致不代替各模式真实事件验收。
- Qoder 1.1.89 的实际模块 fireStopEvent、firePreToolUseEvent、firePermissionRequestEvent、fireNotificationEvent 明确提供上述字段。
- Antigravity 1.5.9 的实际二进制 protobuf 描述符：StopHookArgs.final_model_output/error，HookToolCall.args，PostInvocation.model_thinking 与模型输出是独立字段。提问工具原生 schema 提供 questions/question/options；不用字段猜测或扫描会话正文补答案。静态取证不等同真实模型任务验收。
- OpenCode 2.0.20 的锁定原生 schema：session.text.ended 提供 assistantMessageID/ordinal/text；execution.succeeded 只带 sessionID；form/permission/failed 的可见字段按本页白名单提取。缓存仅当前最后助手消息，分片与会话数有界，重放/重试保留事件首次转换内容。
- Codex 0.167.0 的真实宿主配合本地 Responses 模拟服务留下 Stop 字段记录，只有 session_id/hook_event_name/turn_id；不是在线模型调用，也不据此宣称未来宿主版本永不支持正文。

## 展示与阅读

事件时间线按持久化位置倒序，最新在上。默认“关键事件”由Hub先筛除普通运行记录再分页，每页30条可见事件；工具错误、状态待确认及有正文的记录保留。“查看完整活动”按需读取全部原始记录，降噪不改变Hub完整事件、运行状态、未读集合或手机规则。

- 打开详情优先定位该会话最近一条未读并展开，没有未读才显示最新；未读可跨越任意数量的运行记录直接定位，失效未读仍可查看。“下一条未读”按持久位置向更早记录导航，末尾循环到最新未读。
- 自动定位只在主动打开或明确导航时发生。标读、新事件和快照刷新不自动跳到另一条未读。
- 阅读最新区域时新增事件可跟随；阅读历史或长正文时保持事件和视口偏移，固定底栏提供“回到最新”。定位到历史段时，较新的未加载历史与实时扫描进度分开，防止跳过区域被误当完整历史。
- 增量查询返回独立扫描位置，即使新事件全是隐藏运行记录也推进；断线超过30条关键事件时逐页补齐，不因过滤后空页反复查询。
- 前台、展开、可信在场且标题完整可见连续1.5秒后自动已读；滚出、折叠、切后台、锁屏或离开会清除本次连续计时。仅出现提示或新事件不自动全部标读，长正文不要求滚到底。
- Hub接受有效的主会话开始、恢复或运行活动时，自动标读同一来源、Agent和会话此前全部未读，包括已失效记录；表示旧提醒已处理，不读取Agent原生已读状态。下一次结束或等待仍产生新的未读；重复、历史及不能生效的旧运行事件不触发。完整规则见[已读同步需求](agent-pulse-requirements.md#6-已读同步与可靠性)。
- 离线时可显示本机已缓存的未读内容并保留离线已读，联网后核对历史和Hub自动已读；缓存内容不冒充完整历史。

实现与验收证据见[开发记录](development-status.md)。真正原生输出、提问内容及用户实收的结果需使用对应最新适配包验证；模拟事件仅证明链路/界面，不冒充宿主或手机验收。
