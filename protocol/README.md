# HTTP 与实时订阅协议

协议版本位于 `/api/v1/`，消息采用 JSON。Go 类型见 [`go/protocol/protocol.go`](../go/protocol/protocol.go)。适配包格式和部署见 [适配器说明](../docs/adapters.md)。

## 事件与顺序

事件示例见 [`event.example.json`](event.example.json)。网络事件必须提供稳定的 `event_id`、`source_id`、`agent`、`session_id`、`kind`、RFC3339 `occurred_at`、非空 `title` 和 `scope: main`；`body` 允许为空，`project`、`session_title` 可选；后者是宿主提供的稳定会话标题，最长 1024 字节，不等于本次事件标题。会话首次缺少原生标题时用项目或会话标识，之后仅由非空原生标题更新。标识最长 256 字节、标题和项目最长 1024 字节、正文最长 32 KiB，请求体最长 64 KiB。

会话由 `(source_id, agent, session_id)` 唯一确定。子代理和未知 scope 返回 400。Bridge 可为缺省 `source_id` 填充本地配置值；来源不一致返回 400。`emit` 命令还可补随机事件 ID 和当前时间，显式提供稳定 ID 才能安全地跨多次命令调用重试。

Bridge 首次 `setup` 为每个观察环境生成随机稳定的 `source_id` 主机指纹，并保存在本机 Bridge 配置中；同一物理电脑上的 Windows 与 WSL 是不同观察环境。Bridge 与 Tray 的 `host_name` 默认取操作系统主机名；Hub 管理页维护的显示名会覆盖默认名并同步给所有 Tray。产品界面不提供修改指纹的功能，使用者仍可自行编辑配置。Bridge 与 Tray hello 都登记各自设备名称；只有 Bridge hello 更新 Bridge 主机默认名称并自动解除归档，Tray 不会覆盖同指纹 Bridge 的资料或解除归档。

`sequence` 是来源提供的会话内递增原生序号，必须能跨观察进程重启比较，不能用插件启动时归零的计数器。较旧序号只入历史，不覆盖会话。省略序号的实时事件按到达顺序处理，同时保留先前已知原生序号；无可靠顺序的补报必须设置 `historical: true`，只入历史且不生成强提醒。不得拿客户端时间代替可靠序号。

相同 ID 和相同业务内容返回成功且 `duplicate: true`；相同 ID 内容不同返回 409。时间规范化为 UTC 后比较，未知字段返回 400。

## HTTP 接口

| 方法与路径 | 请求/响应 |
| --- | --- |
| `POST /api/v1/events` | 统一事件。Hub 返回 200，`{event_id, accepted:"durable", duplicate}` 表示事务提交；Relay 返回 202、`accepted:"durable"` 表示中继落盘；Bridge 返回 202、`accepted:"memory"` 仅表示内存接收 |
| `POST /api/v1/read` | `{"event_ids":["..."]}`，一次 1 至 256 个 ID；Hub 200 后已读持久化，允许早于事件到达。Relay 200 表示 Hub 已确认，202 表示只在中继暂存 |
| `GET /api/v1/client-config` | 仅直连 Hub 获取 `{relay_url,relay_token,public_key}`，供 Tray/Bridge 发现 Relay、Bridge 固定适配包发布公钥；不经 Relay 转发 |
| `GET /api/v1/hosts` | 返回主机指纹、Hub 显示名、默认主机名、环境、归档、最近连接和在线状态 |
| `PATCH /api/v1/hosts/{source_id}` | 设置 `display_name` 或 `archived`；空显示名恢复系统主机名 |
| `DELETE /api/v1/hosts/{source_id}` | 永久清理该主机数据并登记删除墓碑；旧 Bridge/积压事件被拒绝 |
| `GET /api/v1/snapshot` | `{run_id,version,sessions,unread,devices,sources}`，完整会话、强提醒未读和当前连接设备 |
| `GET /api/v1/events?before=0&limit=50` | 历史倒序数组；下一页 `before` 使用上一页最后一条 `position`；limit 1 至 200 |
| `POST /api/v1/sessions/manage` | 会话键、最近事件 ID 和管理版本，加要变更的归档/别名/人工状态；Hub 200 才表示已保存，Relay 离线返回 503，不排队 |
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

客户端配置接口沿用 Hub 可选 Bearer 鉴权，响应为 `Cache-Control: no-store`，只返回中继地址/凭据和适配包发布公钥，不返回其他服务配置。公钥来自 Hub 的 `adapter_public_key`；Bridge 首次从已配置 Hub 直连获取并按 Hub 地址和主机指纹固定，后续换钥会拒绝自动接受。首次信任不经 Relay 建立。两个中继字段均为空表示未配置中继；响应不会包含在快照中，Relay 查询白名单不包含该接口。Tray 与 Bridge 将成功结果按 Hub 地址缓存，手动 Relay 配置优先。Hub 选择本身是发布公钥的首次信任来源；可信个人内网可使用受控 HTTP，公网部署必须配置 HTTPS 和访问控制。

## 会话详情与管理

历史查询可同时提供 `source_id`、`agent`、`session_id`，服务端先按完整会话键过滤再分页；三项必须同时提供。会话快照中的 `sources` 将来源指纹映射为 Hub 当前统一维护的 `{name, environment, archived}`。归档主机的会话和未读对 Tray 隐藏，事件历史默认排除；Hub 管理页可显式查询归档历史。会话快照保留 `state`、`last_event_id`、`last_kind`、`updated_at`，并附 `alias`、会话级 `archived`、`override_state`、`override_at`、`management_version`。`last_kind` 区分等待输入与批准，不改变来源状态枚举。

管理请求示例：

```json
{"source_id":"desktop-demo","agent":"opencode","session_id":"session-demo","last_event_id":"event-demo","management_version":0,"override_state":"interrupted"}
```

可修改字段为 `alias`（最多 256 字节，空串清除）、`archived`（布尔值）、`override_state`（空串恢复自动，或 idle/closed/interrupted/unknown），至少提供一项。会话不存在返回 404；最近事件 ID 或管理版本不符返回 409；合法变更在同一事务提交并触发原有快照通知。来源状态不被改写，归档不删除事件或已读事实。

新有效来源活动清除人工状态并递增管理版本，归档和别名保留。旧原生序号或 `historical: true` 不解除覆盖；无序号实时事件仍按到达顺序，不用客户端时钟推断先后。修正为结束/中断使之前等待提醒失效，不生成虚假 Agent 事件或手机通知。恢复自动不会重新激活已失效的旧提醒。

Relay 仅转发会话管理请求，Hub 不可达时返回 503，绝不以 202 伪装保存成功。主机管理只直连 Hub。请求结果未知时客户端先刷新确认，不自动换通路重复提交人工修改。Hub 数据库当前版本为 5，启动时执行顺序迁移；升级前备份，回退旧程序需恢复旧库。

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

`bridge dispatch` 从 stdin 接收最多 8 MiB 的宿主 JSON（仅本地输入，网络统一事件仍限 64 KiB），调用独立包的 `--hook <事件名> --source <来源>`。stdout 返回 `{ "event": <统一事件>, "reply": {} }`；忽略时省略 event。dispatch 校验事件后提交本地 Bridge，并将被动 reply 返回宿主。仅允许空对象或单个 `decision: allow|stop` 字段，不允许通过适配器输出修改工具授权、注入内容或强制继续。此协议只用于本机，不增加远程执行接口。
