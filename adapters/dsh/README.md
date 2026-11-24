# DeepSeek Harness 适配器（dsh-agent-pulse-adapter）

本包面向 DeepSeek Harness（DSH）`0.2.3` 版本线，形态为 **DSH Host 插件**：由 DSH 宿主进程直接加载，监听 Cordis 事件总线上的会话事件，转换为本项目统一事件后经本机 IPC 递交 Bridge。它不是命令 hook，不修改审批决定，也不要求通过 Bridge 包装启动。

接口依据为运行中 DSH `0.2.3-alpha.2` 的 `cordis_inspect` 事件与服务目录（`agent/created`、`session/event`、`approval/request`、`user-questions/request`、`workspace/session-archived`、`SessionHeader`、`TurnEndReason`）以及 `@deepseek-ai/dsh-scope` 的全局监听器语义；事件名在后续 alpha 版本可能变化，`x.y` 变化需重新适配。

## 映射规则

| DSH 原生事件 | AgentPulse 事件 | 说明 |
| --- | --- | --- |
| `session/event` `turn/start`，之前没有更早的 `turn/start` | `session.started` | 新会话 |
| `session/event` `turn/start`，本进程首次看到该会话 | `session.resumed` | 宿主重启后恢复的会话只在用户真正继续时报告一次，不在启动时批量报告 |
| `session/event` `turn/start`，后续轮次 | `activity.observed` | 等待中的会话先恢复运行 |
| `session/event` `assistant/message` | 不发事件，缓存本轮最高步骤的可见 text 块 | 不取 reasoning、工具调用块或历史 |
| `session/event` `turn/end` reason=`completed` | `turn.completed`，附缓存的最终回答 | 正文为空时明确提示未取得 |
| `turn/end` reason=`error` | `run.failed`，正文为 `error.message` | |
| `turn/end` reason=`aborted` / `interrupted` | `run.interrupted`，正文按取消来源 | |
| `turn/end` reason=`blocked` | `run.interrupted`，正文"本轮未执行任何步骤" | 不当作完成提醒 |
| `turn/end` reason=`max-tokens` | `turn.completed`，附"已达最大输出长度"提示 | |
| `turn/end` reason=`forked` | 忽略 | 由分叉后的会话继续报告 |
| `approval/request` | `approval.required`，正文为工具名与宿主展示原因 | waterfall，递交后 `next()` |
| `user-questions/request` | `input.required`，正文为 question/detail/header、选项标签及说明；`intent.kind=approval` 时为 `approval.required` | 不传用户答案 |
| `session/event` `tool/result` 带 `error` 或 `isError` | `tool.failed`，正文为工具名与 `error.reason` | 普通记录，不等于主任务失败 |
| `session/event` `session/title` | `session.renamed` | 由 `sessionTitle` 包 merge 的扩展事件 |
| `workspace/session-archived` | `session.closed` | 仅对本进程观察过的主会话 |

`agent/error` 不单独转换：轮次终态以持久化的 `turn/end` 为准，避免同一失败产生两条提醒。

**子代理过滤**：只信任 `SessionHeader.origin === 'subagent'` 或 `delegationDepth > 0` 的会话头部标记（`dsh-subagent` 与 `dsh-subagent-codex` 都写入这两个字段；实测根会话头部也带 `delegationDepth: 0`，不能据此过滤）。仅带 `parentSession` 的分叉会话仍是用户可见的主会话，不过滤。未标记的监听器在根 ctx 上保持全局，因此过滤必须在本插件内完成。

**waterfall 观察者必须 `prepend`**：`approval/request` 与 `user-questions/request` 是 Cordis waterfall，宿主自带的 answerer（浏览器 UI、MCP consent 等）一旦认领请求就不再调用 `next()`，后注册的监听器永远看不到真实提问。两个观察者以 `{ prepend: true }` 注册，排在所有 answerer 之前，只递交事件并把决定权原样交还 `next()`。

**字段来源**：`session_id = session.id`；`project = session.header.cwd`；持久事件携带 `sequence = event.seq`（会话日志序号，跨进程重启可比）；审批/提问等实时事件省略序号，`event_id` 由来源、会话与 `callId` 计算。同一等待经 `approval/request` 与 `user-questions/request` 两条通路到达时，5 秒内按 `callId` 或主题去重。

## 安装和开发

### 手动安装（适合本地开发）

1. 启动配置了固定 `source_id` 的 Bridge，用 `agent-pulse-bridge check-config` 取得本机 IPC 地址。
2. 在 DSH 的 **Plugins › Add plugin** 中安装本目录，或在 profile 目录执行 `dsh plugin --profile <name> add /absolute/path/agent-pulse/adapters/dsh`。
3. 在该 profile 的 `cordis.patch.yml` 中覆盖 `id: agent-pulse` 行的 `config`：

```yaml
- id: agent-pulse
  config:
    source_id: "my-linux"
    bridge_address: "unix:/home/your-user/private-run/bridge-example.sock"
```

`source_id` 必须与本机 Bridge 一致，`bridge_address` 使用 `check-config` 输出的本机 IPC 地址，不使用 TCP 端口。config 留空时插件保持未启用并在日志提示。

### 受管理安装

Bridge `setup` 选择 DeepSeek Harness 后，`adapters.managed` 中登记该 profile 的 `cordis.patch.yml` 绝对路径；Bridge 在适配目录写入固定 loader，并在 profile patch 中维护一条 `id: agent-pulse` 的 `insert` 行（`name` 为 loader 的 `file:` URL，`config` 为上述两个字段），保留其他条目、注释和 `!!js` 标签。手动安装的 bundle 与受管理行不能同时存在；发现其他来源的同名行时停止并提示。

### 开发检查

```sh
pnpm test
pnpm check
```

插件为纯 ESM JavaScript，由 DSH 自身的 Node 运行时加载，无需构建步骤。

## 验证边界

`test/adapter.test.js` 覆盖映射、子代理过滤、正文白名单与截断、有界缓存；`test/index.test.js` 用本机 Unix 套接字模拟 Bridge 回执，验证监听器行为与 waterfall 返回 `next()`。真实 DSH 会话的 `session/title` 投递、`askTimed` 提问载荷、`blocked`/`forked` 触发场景需按[开发记录](../../docs/development-status.md)实测，不以单元测试代替。
