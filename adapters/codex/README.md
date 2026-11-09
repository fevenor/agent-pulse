# Codex 适配器

采用独立命令 hook 包，支持版本和安装事件见 [包清单](manifest.json)。会话、轮次和中断事件转换代码位于 `go/internal/event/hooks.go`，编译到独立产物，不进入 Bridge。

安装、信任确认、自动更新与当前覆盖限制见 [来源接入说明](../../docs/adapters.md)。不要关闭 Codex 的全局 hook 信任检查。子代理事件忽略；没有可靠归属字段的工具权限与输入等待暂不转换。

本地验证使用 `uv run --offline --no-project python tests/e2e/codex_native.py`，脚本只连接本地 Responses 模拟服务，每次一个测试实例。真实服务受并发额度限制时优先使用 qoderclicn 测试，不批量启动 Codex。
