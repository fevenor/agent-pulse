# HTTP 与实时订阅协议

协议版本位于 `/api/v1/`，消息采用 JSON。Go 类型见 [`go/protocol/protocol.go`](../go/protocol/protocol.go)。适配包格式和部署见 [适配器说明](../docs/adapters.md)。

## 事件与顺序

事件示例见 [`event.example.json`](event.example.json)。网络事件必须提供稳定的 `event_id`、`source_id`、`agent`、`session_id`、`kind`、RFC3339 `occurred_at`、非空 `title` 和 `scope: main`；`body` 允许为空，`project` 可选。标识最长 256 字节、标题和项目最长 1024 字节、正文最长 32 KiB，请求体最长 64 KiB。

会话由 `(source_id, agent, session_id)` 唯一确定。子代理和未知 scope 返回 400。Bridge 可为缺省 `source_id` 填充本地配置值；来源不一致返回 400。`emit` 命令还可补随机事件 ID 和当前时间，显式提供稳定 ID 才能安全地跨多次命令调用重试。

`sequence` 是来源提供的会话内递增原生序号，必须能跨观察进程重启比较，不能用插件启动时归零的计数器。较旧序号只入历史，不覆盖会话。省略序号的实时事件按到达顺序处理，同时保留先前已知原生序号；无可靠顺序的补报必须设置 `historical: true`，只入历史且不生成强提醒。不得拿客户端时间代替可靠序号。

相同 ID 和相同业务内容返回成功且 `duplicate: true`；相同 ID 内容不同返回 409。时间规范化为 UTC 后比较，未知字段返回 400。

## HTTP 接口

| 方法与路径 | 请求/响应 |
| --- | --- |
| `POST /api/v1/events` | 统一事件。Hub 返回 200，`{event_id, accepted:"durable", duplicate}` 表示事务提交；Relay 返回 202、`accepted:"durable"` 表示中继落盘；Bridge 返回 202、`accepted:"memory"` 仅表示内存接收 |
| `POST /api/v1/read` | `{"event_ids":["..."]}`，一次 1 至 256 个 ID；Hub 200 后已读持久化，允许早于事件到达。Relay 200 表示 Hub 已确认，202 表示只在中继暂存 |
| `GET /api/v1/snapshot` | `{run_id,version,sessions,unread,devices}`，完整会话、强提醒未读和当前连接设备 |
| `GET /api/v1/events?before=0&limit=50` | 历史倒序数组；下一页 `before` 使用上一页最后一条 `position`；limit 1 至 200 |
| `GET /api/v1/connect` | WebSocket 变化订阅和设备状态 |
| `GET /api/v1/relay` | Hub 主动连接 Relay 的 WebSocket 通道，不是普通客户端入口 |
| `GET /api/v1/push-jobs` | 最近 200 条推送记录，包括事件、渠道、状态、尝试次数和下次尝试时间 |
| `GET /api/v1/adapters/index` | 发布者签名的目标包索引 |
| `GET /api/v1/adapters/packages/{hash}` | 按 SHA-256 读取包，支持 HTTP Range；Bridge 每块下载 256 KiB |
| `GET /api/v1/adapters/status` | 各主机最近一次适配器报告，在线状态以 devices 为准 |
| `POST /api/v1/adapters/status` | Bridge 上报 source_id、items、updated_at；items 区分 target、installed、running 和 error |
| `GET /healthz` | 进程状态；Bridge 额外报告 pending、bytes、rejected、failed、last_failed_event_id；Relay 报告 hub_connected、pending、blocked |

Hub 的 `unread` 是完整集合，包含已经失效但未读的历史提醒。事件视图的 `read` 表示明确已读，`valid` 表示仍可提醒；活动恢复、新状态或有效期到达会使旧提醒失效。历史和快照均不会隐式标记已读。快照超过 8 MiB 返回错误，不截断为不完整成功响应。

错误体为 `{"error":"中文说明","retryable":true|false}`。401/403、400/409/413 等必须检查具体原因；503 可重试。Bridge 对已被 Hub 明确拒绝的无效事件记录计数和 ID 后移出队列，避免阻塞后续数据；网络错误、鉴权失败、意外响应仍保留内存重试。日志不含事件正文或令牌。

可选令牌用 `Authorization: Bearer ...`。内网默认不要求，Relay 始终要求。浏览器 WebSocket 在 `agent-pulse` 子协议之外提供 `pulse-token.<Base64URL(令牌)>`，服务端仅协商 `agent-pulse`，令牌不放入 URL。HTTP JSON 变更接口和 WebSocket 检查同源 Origin，不提供跨站 CORS。Bridge 还要求回环 IP Host。代理终止 HTTPS 时须正确覆盖 `X-Forwarded-Proto`。

## WebSocket

客户端连接后 10 秒内发送：

```json
{"type":"hello","payload":{"id":"linux-test-client","role":"client"}}
```

角色支持 `client`、`web`、`bridge`、`tray`。Tray 可附 `locked` 和 `fullscreen` 布尔值，省略代表未知。连接 ID 在同角色内唯一，新连接替换旧连接，旧连接关闭不影响新连接状态。

设备列表中的 `path` 由 Hub 根据实际入口填写 `direct` 或 `relay`，不信任客户端自行声称的路径。Hub 重启和中继断开后清除相关在线设备。

服务端先登记订阅，再返回 `hello`，后续变化通过 `changed` 告知：

```json
{"type":"changed","payload":{"run_id":"hub-run-id","version":12}}
```

收到 `hello` 后获取 HTTP 快照；收到更高版本时重新获取。快照旧于已经收到的变化版本时继续刷新；`run_id` 改变则丢弃旧版本判断。变化可合并，版本不持久化，不承诺事件增量重放。已读等操作仍使用 HTTP。

每 10 秒发送 `{"type":"heartbeat"}`；更新桌面状态用 `{"type":"state","payload":{"locked":false,"fullscreen":true}}`。Hub 35 秒未收到消息关闭连接。心跳返回当前版本的 `changed`，没有变化时可跳过快照；客户端同样以 35 秒无回应判定失联。未提供桌面状态的测试客户端不会抑制手机推送。

## Relay 的确认和恢复

Relay 只持久化事件与已读请求。Hub 断线时查询和包下载返回 503，不返回旧快照；连接恢复后按持久队列重发，只有 Hub 200 才删除。Hub 明确拒绝的无效项保留为 blocked，其他项继续交付。事件 ID 只在当前待发队列中去重，Hub 始终执行最终业务去重。

下行查询、设备连接和适配器报告通过同一反向通道转发，不持久化。通道使用有界的请求/响应帧和虚拟连接帧，临时 ID 仅用于关联本次响应。断线关闭相关虚拟连接，客户端重新连接并获取快照。适配器报告丢失由下一次周期报告补齐，不占用事件队列。

## 本地命令适配协议

`bridge dispatch` 从 stdin 接收宿主 JSON，调用独立包的 `--hook <事件名> --source <来源>`。stdout 返回 `{ "event": <统一事件>, "reply": {} }`；忽略时省略 event。dispatch 校验事件后提交本地 Bridge，并将被动 reply 返回宿主。仅允许空对象或单个 `decision: allow|stop` 字段，不允许通过适配器输出修改工具授权、注入内容或强制继续。此协议只用于本机，不增加远程执行接口。
