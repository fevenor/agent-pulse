# Codex 适配器

采用独立命令 hook 包，支持版本和安装事件见 [包清单](manifest.json)。会话、轮次和中断事件转换代码位于 `go/internal/event/hooks.go`，编译到独立产物，不进入 Bridge。

安装、信任确认、自动更新与当前覆盖限制见 [来源接入说明](../../docs/adapters.md)。不要关闭 Codex 的全局 hook 信任检查。子代理事件忽略；没有可靠归属字段的工具权限与输入等待暂不转换。

本地验证使用 `uv run --offline --no-project python tests/e2e/codex_native.py`，脚本只连接本地 Responses 模拟服务，每次一个测试实例。真实服务受并发额度限制时优先使用 qoderclicn 测试，不批量启动 Codex。

## 会话标题

官方 hook 的共享字段提供会话标识和 transcript 路径，但不提供稳定的会话标题契约。命令适配器在本机已存在的 `CODEX_HOME/session_index.jsonl`（未设置时为用户目录 `.codex/session_index.jsonl`）中，按精确 `session_id` 查找 `id` 对应的 `thread_name`，只把原生缓存的标题填入协议 `session_title`；不读取 transcript、首条提示词、完整回复或认证文件。通知标题和项目名不变。

该索引是当前已观察到的可选本地元数据，不承诺所有 Codex 版本都提供；不存在、不可读、格式变化、没有匹配标题或超过 8 MiB 时保留无标题事件，Hub 保留已有名称；首次无名称时由界面显示“未命名会话”。标题满足 1024 字节限制并保持有效 UTF-8。同一会话有多条缓存时取最后有效标题，读取不写入索引。不进行定时名称轮询；没有原生改名事件时，在下一次真实 hook 读取最新名称。

需要发布新版独立 Codex 适配包，不能仅靠升级 Bridge 生效；先确保 Hub/Relay 支持 `session_title`，再使用原公钥的递增签名索引发布。初次事件可提供名称，后续真实 hook 附带最新名称；不改写历史正文、任务状态或会话备注。当前协议要求本轮支持名称事件的 Hub、Relay 和 Bridge 构建。字段依据见[官方 Codex hook 文档](https://developers.openai.com/codex/hooks/)。
