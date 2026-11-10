# Antigravity CLI 适配器

面向 `agy` 命令。配置使用命名 hook 对象与 camelCase 载荷，支持版本见 [包清单](manifest.json)。必须通过主会话包装器启动，以可靠排除子会话；独立调用 IDE/远程守护进程尚未验证。

```sh
agent-pulse-bridge run-adapter --config /绝对路径/bridge.yaml --adapter antigravity -- --print '你的任务'
```

安装和限制见 [来源接入说明](../../docs/adapters.md)。`model_stop` 对应本轮结束，`error`/`max_steps_exceeded` 对应失败，未知停止原因只标未知。包装器不会替用户批准权限。

agy 1.5.3 ACP 支持通过同一包装器传入 `acp --stdio --process-per-session`。正常停止值 `NO_TOOL_CALL` 只有在 `fullyIdle=true` 时报告完成；一个包装进程只绑定一个主会话。可复现测试见 [端到端说明](../../tests/e2e/README.md)。

跨进程加载原会话时，适配包 0.2.2 支持在宿主参数前传入 `--pulse-main-session 原主会话ID`，再由 ACP 客户端对同一 ID 执行 `session/load`。这是调用者已知的原主会话，不从首次模型活动或子会话猜测归属；实际参数和限制见 [主会话入口](../../docs/adapters.md#antigravity-主会话入口)。
