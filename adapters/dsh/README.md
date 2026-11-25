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

**字段来源**：`session_id = session.id`；`project = session.header.cwd`；持久事件携带 `sequence = event.seq`（会话日志序号，跨进程重启可比）；审批/提问等实时事件省略序号，`event_id` 由来源、会话与 `callId` 计算；每次归档与实时恢复另有独立身份。同一等待经 `approval/request` 与 `user-questions/request` 两条通路到达时，在请求生命周期内按 `callId` 合并。每个 waterfall 调用独立跟踪，全部未决请求完成或取消后才恢复运行；其他工具结果、用户追加消息或步骤不会提前清除等待。终态会回收当前等待，迟到回调不能重新开启会话。

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

Bridge `setup` 从签名包的控制入口取得名称、宿主版本和默认 profile 路径，选择启用后在 `adapters.managed` 登记 `cordis.patch.yml` 绝对路径。随包控制程序生成受管理的 `id: agent-pulse` 行，Bridge 只负责提交该授权文件与安装状态。`name` 直接指向 `packages/<id>/<version>/<sha256>/index.js` 的绝对 `file:` URL，`config` 包含上述连接字段与本机 `runtime_receipt` 路径。

插件不读取 `state.json` 或 `pending.json`，同一入口始终对应同一包；升级/恢复通过配置中的不可变路径选版，已有包保留供在途宿主使用。`apply` 完成注册后写运行回执，不能把该回执当成事件实收。写配置本身不触发 DSH 重组，仍需在宿主重新加载配置后验收。

原有固定 loader 行会按归属更新为包入口；其他条目、注释与 `!!js` 保留，手动安装 bundle 和其他来源的同名行继续报告冲突。

### 开发检查

```sh
pnpm test
pnpm check
```

事件插件为 ESM JavaScript，由 DSH 自身的 Node 运行时加载。受管理包另带预构建 Go 控制程序，按 Linux amd64/arm64 分包，使用既有 `scripts/build/package-adapters.sh` 构建；目标安装不运行编译器。

## 验证边界

`test/adapter.test.js` 覆盖映射、子代理过滤、正文白名单与截断、空最终回答和归档身份；`test/index.test.js` 用本机 Unix 套接字检查并行/嵌套等待、取消与异常、终态后的迟到回调和心跳；`test/delivery.test.js` 验证保序、503/断连/超时重试、永久拒绝、卸载和内存上限。真实 DSH 会话的 `session/title` 投递、`askTimed` 提问载荷、`blocked`/`forked` 触发场景需按[开发记录](../../docs/development-status.md)实测，不以单元测试代替。


本机递交在后台按会话串行进行，单次 IPC 仍限 200ms；短暂断连、超时、429/5xx 最多尝试三次，间隔 50/100ms，同一次事件保持 ID、时间和正文。队列最多 256 条、2 MiB；永久拒绝、重试耗尽或容量满会诊断，宿主继续运行。队列只在内存，插件卸载会中止递交，不保证跨宿主重启或长时间故障补发；步骤心跳不能替代完成/失败/等待事件的交付。DSH ACP 本轮不纳入支持验收。
