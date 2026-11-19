# 本机进程间通信

IPC（进程间通信）用于本机 Agent hook、OpenCode 插件与 Bridge 之间的递交，以及 Tray 对自有 Bridge 的健康和待发队列检查。Hub/Relay 的网络 HTTP 与 WebSocket、事件格式、持久化、去重和重试规则不变。

## 当前方式

- Linux/WSL 使用 Unix 域流套接字；默认地址位于固定的 `/tmp/agent-pulse-系统用户编号/` 私有目录下，按稳定来源指纹生成名称。定位不依赖 HOME、TMPDIR 或 shell/systemd 的环境变量，避免两类进程找错端点。
- Windows 使用命名管道；名称按当前用户 SID（系统用户身份）和来源指纹生成。Go 使用 go-winio 提供的流监听和连接；显式只授权当前用户，库拒绝远程客户端。
- Windows 和 WSL 各自运行 Bridge、各自保留来源身份，不共享本机端点。WSL mirrored（镜像网络）不参与 IPC 地址分配。
- 本机通路仍在字节流上使用现有 HTTP/JSON 与接收回执；报文中的回环 Host 只是 HTTP 字段，不是 TCP 拨号地址。新 Bridge 不监听本机 TCP 端口，也不提供 TCP 回退。
- IPC 不等于持久化。Bridge 停止仍会丢失尚未交付的内存事件；自动重载必须先检查启动时的旧端点，待发非零或不可核对时保留旧进程。

Linux 端点目录必须由当前用户拥有且权限 700，套接字权限 600。启动时通过进程锁拒绝重复接管；只有当前用户的套接字、明确连接拒绝且文件身份未变化时才能清理崩溃残留，普通文件、链接和不明端点均不覆盖。Windows 命名管道随进程关闭释放，不留下套接字文件。

## 配置、检查与升级

Bridge 的 `listen` 留空或省略即使用默认 IPC 端点；高级使用者可显式设置本平台地址：

```yaml
listen: ""
# Linux 示例：listen: "unix:/home/your-user/private-run/custom.sock"
# Windows 示例使用 YAML 单引号：listen: 'pipe:\\.\pipe\agent-pulse-custom'
```

Unix 路径须为不超过 107 字节的规范绝对路径。命名管道只能使用本机 `\\.\pipe\` 命名空间，不能填写远端 UNC 路径。相同用户和来源只能启动一个对应 Bridge；地址冲突明确失败，不自动换地址或接管其他接收器。

```sh
agent-pulse-bridge check-config --config /配置目录/bridge.yaml
agent-pulse-bridge health --config /配置目录/bridge.yaml
```

`check-config` 只读输出解析后的 IPC 地址；`health` 通过 IPC 输出健康和 `pending`（尚未交付事件数）。手工递交隔离测试事件可用 `emit --address` 指定检查输出；这不是用户侧原生事件实收证明。OpenCode 受管理插件使用 `bridge_address`，安装器同步会写入对应端点；不再使用 `bridge_url`。

从 TCP 本机入口升级前，确认旧 `/healthz` 的 `pending` 为 0、正常停止并备份配置。保留 `source_id`、令牌、公钥和其他私人设置，只将 Bridge 原 `listen` 改为空或删除。先使用对应修订的 Hub 签名仓库，再替换程序、同步适配包和 OpenCode 配置，最后启动并检查 `health`。记忆过滤在独立 Codex 包中，OpenCode IPC 在新版插件中；只替换 Bridge/Tray 程序而仍使用旧适配包不能完成这两项修复。命令 hook 引用配置文件，因此仅改变 `listen` 不要求更换命令或重复信任；适配包变化是否要求重新加载/审核仍以宿主为准。安装器不自动改写已有私人配置；新程序对旧 IP:端口设置给出明确错误，不静默忽略。

公开 RC3 的 raw 安装流程仍使用其旧产物支持的健康方式；这只是安装入口对已发布产物的能力选择，不代表新 Bridge 保留 TCP 通路。升级期间不清空 Hub/Relay 数据库、Tray 缓存或历史记忆记录，不重发历史失败任务。


## App 身份暂存

Antigravity App的首次子会话hook可能早于原生身份索引。适配器把待核对游标和规范化事件一起递交本机，Bridge按已有总事件数/字节配额暂存，异步调用已安装适配器核对；主身份转发、子身份过滤、未知保留等待。health.pending包含这些事件，pending_identity单列；重载和升级仍要求pending为零。游标与本机轮次标识不进入Hub/Relay网络事件，不改变已有持久化结构。旧Bridge不能消费这一新增本机字段，部署必须先更新相应Bridge再启用新适配包，不加旧版本兼容分支。
