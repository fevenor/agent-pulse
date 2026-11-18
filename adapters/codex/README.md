# Codex接入

支持Codex CLI和本地桌面执行器，主会话沿用正常直接启动；云端编排和ACP子代理不以本地工具执行冒充支持。适配器与Bridge独立发布，安装入口及版本政策见[适配说明](../../docs/adapters.md)。

## 当前事件规则

- SessionStart/UserPromptSubmit维护会话开始和继续；PermissionRequest及提问工具保持原生等待事件，PostToolUse用于恢复活动。
- 持久主会话在UserPromptSubmit/Stop登记本轮终态观察。适配器从登记时的位置增量读取对应原生文件，仅将目标turn_id的最终task_complete转换为正常完成或整轮失败；不采集单次模型请求错误或自动重试过程。Stop可能被其他钩子要求继续，不能提前作为持久主会话的最终完成。
- 最终事件自身的last_agent_message和error.message提供可见正文，遵循现有32 KiB上限；不转换其他记录，不读取思考、用户输入或完整历史来补内容。Interrupt和SessionEnd保持原生中断与关闭；关闭后交付的真实终态不重新打开会话。
- 缺少持久终态来源的临时会话保留Stop已有完成字段，但最终模型失败能力明确受限。来源持续不可读取只降为状态未知，不伪造任务失败，也不以长时间无活动判结束。

宿主目录跟随CODEX_HOME，身份数据库跟随CODEX_SQLITE_HOME，缺省为用户.codex目录。观察登记保留宿主目录边界，不要求Bridge与交互宿主具有相同环境变量。文件来源只在宿主的sessions/archived_sessions内；按精确会话或宿主路径核对身份，并过滤子代理、记忆及后台审查。临时记忆线程没有持久身份时，精确识别CODEX_HOME下的memories/memories_v2专用工作目录，不按标题或普通项目末尾猜测。

标题仍从本机已有session_index.jsonl按精确会话ID读取thread_name，不使用用户提示词或回答生成名称；不改写索引和信任记录。Windows与WSL共享目录由WSL管理自有条目，按原生command_windows选择实际执行平台，保留其他用户钩子。

实现与验证边界见[事件覆盖](../../docs/event-coverage.md)及[开发记录](../../docs/development-status.md)。模型模拟服务只能证明真实执行器的事件与接入链，不冒充在线模型、日常桌面错误或手机实收验证。
