# qoderclicn 适配器

面向 Qoder 中国版 CLI，默认用户配置位于 `~/.qoder-cn/settings.json`。安装和更新见 [来源接入说明](../../docs/adapters.md)，兼容版本见 [包清单](manifest.json)。宿主须信任项目才能加载项目 hook。

真实验证使用 DeepSeek-Flash，当前宿主的命令行标识为 `dfmodel`；显示名直接传给 `--model` 可能被回退到 Auto。测试必须检查输出，不能把回退后的模型记为指定模型通过。

```sh
# 先创建仅用于测试的目录，并在 qoderclicn 内确认信任该目录后退出。
AGENT_PULSE_QODER_WORKSPACE=/绝对路径/可信测试目录 uv run --offline --no-project python tests/e2e/linux_services.py
```

脚本会在该测试目录创建 `.qoder/settings.json`，不要指定日常项目。禁用工具和 MCP、不保存会话，依次验证原生事件、签名包安装、Bridge 转发、Hub 入库和本地 HTTP 推送接收；不会验证真实手机送达。
