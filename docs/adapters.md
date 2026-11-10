# 来源接入与独立适配包

适配器负责产品事件到统一协议的转换。Bridge 只负责本地入口、转发和适配包安装，不链接产品转换代码。当前 Linux 接入覆盖 OpenCode、Codex、Antigravity CLI（`agy`）和 Qoder 中国版 CLI（`qoderclicn`）；宿主版本以各目录的 `manifest.json` 为准。zcode 挂起，不把占位目录视为支持。

## 支持边界

| 来源 | 接入形态 | 事件及限制 |
| --- | --- | --- |
| OpenCode | TypeScript 原生插件、固定 loader | 原生会话/执行/表单/权限事件；查询会话 `parentID` 排除子代理；插件更新后需要宿主重新加载 |
| Codex | 命令 hook | 会话开始/恢复/关闭、用户提交、本轮结束、用户中断；不接收 SubagentStart/Stop。没有可靠归属信息的工具权限/输入 hook 暂不转换，不能据此宣称已覆盖等待状态 |
| qoderclicn | 命令 hook | 会话、轮次结束、StopFailure、权限请求、问答工具和 MCP 输入提示；包含子代理 ID/type 的事件忽略；工具错误不等于主任务失败 |
| Antigravity CLI | 命令 hook + 主会话启动包装器 | 主会话开始、模型调用前活动、问答/权限等待、Stop 原因；公开字段没有主子关系，所以必须通过包装器绑定主会话，未绑定事件忽略 |

Hook 只返回被动响应，不修改批准决定、注入提示词或要求 Agent 继续执行。命令转换最多运行 150 ms，之后本地递交最多等待 200 ms；异常只发诊断，不让通知故障阻塞 Agent。只传事件类型、会话 ID 和项目名，不上传提示词、工具参数、原始错误或完整回复。

原生 hook 没有统一稳定事件 ID/时间，适配器为每次调用生成 ID；Bridge 网络重试保留转换后的同一份 ID、时间和内容。若宿主自己重新调用 hook，它是另一次调用，当前不能保证跨调用去重。OpenCode 则使用原生事件 ID 生成稳定标识。

产品配置参考：[Codex hooks](https://developers.openai.com/codex/hooks)、[Qoder hooks](https://docs.qoder.com/cli/hooks)、[Antigravity hooks](https://antigravity.google/docs/hooks)。实际验证证据见 [开发记录](development-status.md)，不以在线文档代替本机版本验证。

## 发布者：构建与签名

发布私钥只留在发布者机器；Hub 分发目录只需要 `index.json` 和 `packages/`。以下操作在本地生成文件，不提交 Git，也不自动推送到服务器。

```sh
pnpm --dir adapters/opencode install --frozen-lockfile --ignore-scripts
bash scripts/package-adapters.sh ./dist/repository 1
./bin/agent-pulse-adapters keygen --private /安全目录/publisher.key --public /安全目录/publisher.pub
./bin/agent-pulse-adapters publish --repository ./dist/repository --private /安全目录/publisher.key --index ./dist/repository/index-input.json
```

`package-adapters.sh` 构建 Linux amd64 命令包和独立 OpenCode 插件包；`package-adapters.ps1` 构建 Windows amd64 命令包，要求 Bridge 0.3.0 或更新兼容版本。OpenCode 0.3.0 包增加可选原生会话标题，最低 Bridge 0.4.0；先更新 Hub/Relay，再更新 Bridge 与插件，避免旧服务严格校验拒绝新字段。其他架构需交叉编译适配器并同时修改清单中的 `platform`，不能仅修改清单冒充对应二进制。Go 产品适配器共用编译入口 `go/cmd/agent-pulse-adapter`，构建时注入来源 ID；每个包独立于 Bridge 发布。OpenCode 包打包所需 SDK，安装时不执行包管理器或任意安装脚本。

每次发布递增 `sequence`。`packages` 是本次可用的版本集合，同一来源默认选择当前平台的最高兼容版本。集中回滚时，在新的更高序号索引里为该来源选择已有旧包，不复用旧序号。保留多个版本可供单机 `pins` 固定使用。相同版本不得更改内容，修改必须发布新版本。

同时分发 Linux 和 Windows 包时，将两侧构建仓库放到发布者本机后合并，再用同一个发布私钥签名：

```sh
uv run --offline --no-project python scripts/merge_adapter_repositories.py \
  ./dist/repository ./dist/repository-windows --output ./dist/repository-combined --sequence 2
./bin/agent-pulse-adapters publish --repository ./dist/repository-combined \
  --private /安全目录/publisher.key --index ./dist/repository-combined/index-input.json
```

示例序号需替换为大于已发布序号的值。合并器校验包摘要与大小，保留每个平台的包；通用 OpenCode 包清单和文件内容一致时，忽略 ZIP 时间/权限差异，保留第一个输入仓库的既有 ZIP。内容或兼容清单不同的同版本包会报错，不自动挑选或覆盖；失败时不替换既有索引。合并只生成 `index-input.json` 和不可变包，不读取私钥，也不自动部署。

将发布目录放到 Hub 的 `adapter_directory`。发布能力只有本地命令，Web 不提供可执行包上传或目标修改接口。

## 主机：首次接入与自动更新

在 Bridge YAML 中配置绝对路径、公钥和允许管理的宿主配置文件：

```yaml
adapters:
  directory: "/home/your-user/.local/share/agent-pulse/adapters"
  public_key: "publisher.pub 内的一行 Base64 公钥"
  poll_seconds: 300
  pins: {}
  exclude: []
  managed:
    opencode: "/home/your-user/.config/opencode/opencode.json"
    codex: "/home/your-user/.codex/hooks.json"
    antigravity: "/home/your-user/.gemini/config/hooks.json"
    qoderclicn: "/home/your-user/.qoder-cn/settings.json"
```

`managed` 是本机明确授权的文件范围，不扫描或修改其他产品配置。Qoder 国际版的默认目录与中国版不同；这里的命令和路径针对 `qoderclicn`。项目配置也可使用明确的绝对路径。配置必须为 JSON 对象；发现无法解析的 JSON/JSONC 时拒绝覆盖，应先人工整理为标准 JSON。保留用户其他 hook/plugin 条目，仅维护 AgentPulse 自己的命令。

启动 Bridge 后自动检查，也可手动执行：

```sh
./bin/agent-pulse-bridge sync-adapters --config config/bridge.local.yaml
```

签名、包摘要、大小、解压路径、平台及 Bridge/Agent 版本校验通过后才切换。Linux 与 Windows 均使用系统文件锁，进程退出会自动释放。中断的安装根据 `pending.json` 恢复原配置和原版本；若用户在此期间修改了文件，停止自动恢复并报告，避免覆盖用户新配置。保留包目录，不删除正在运行的版本。

Hub 不可达时通过已配置 Relay 分块下载；两条通路使用同一受信发布公钥。失败保留旧包。明确的入口不可启动触发回滚并隔离坏包；普通不合法输入或超时不把整个包判坏。更高序号的新发布可解除隔离。Windows 安装器使用隔离宿主验证，真实 Agent 的 hook 加载和信任仍需逐项验收；实现与命令引用规则见 [Windows 客户端](windows-client.md#windows-适配包)。其他平台继续拒绝自动安装。

Codex 和 qoderclicn 的宿主信任提示由用户在宿主中确认，安装器不持久关闭信任检查。OpenCode 已运行实例不会被强制重启；“已安装”与“最近运行”不同，管理页会提示未确认或等待重新加载。

## Antigravity 主会话入口

公开 hook 没有主/子代理字段。通过稳定入口启动 CLI，在第一次 SessionStart 时绑定主会话，后续只处理该 ID。子会话即使继承了环境也无法改写绑定：

```sh
./bin/agent-pulse-bridge run-adapter --config config/bridge.local.yaml --adapter antigravity -- --print '你的任务'
```

也可直接使用 `bin/agent-pulse-adapter-antigravity run -- ...` 做开发验证。临时绑定只保存会话 ID，不保存事件或文本；进程退出后删除。IDE 和远程守护进程需要另行取得主会话证据，当前不把它们当成已支持。

ACP（Agent Client Protocol，编辑器通过标准输入输出调用 Agent 的协议）使用同一包装器：

```sh
./bin/agent-pulse-bridge run-adapter --config config/bridge.local.yaml --adapter antigravity -- acp --stdio --process-per-session
```

ACP 在新进程中 `session/load` 原会话时，可能没有新的 SessionStart。适配包 0.2.2 起，由调用者在启动时明确指定之前取得的主会话 ID：

```sh
./bin/agent-pulse-bridge run-adapter --config config/bridge.local.yaml --adapter antigravity -- \
  --pulse-main-session 原主会话ID acp --stdio --process-per-session
```

`--pulse-main-session` 必须位于宿主参数之前，由包装器消费，不传给 agy。之后 ACP 客户端对同一 ID 调用 `session/load`。绑定后的其他会话仍被过滤；不能用任意子会话 ID 冒充主会话，也不会自动把新出现的 ID 替换为主会话。此能力随独立适配包更新，不要求为此重新部署 Bridge。

保持 agy 默认登录目录，由宿主自行使用已登录凭据。一个包装进程只绑定一个主会话，不用于同进程多主会话复用。agy 1.5.3 的正常 ACP Stop 实测为 `NO_TOOL_CALL`，仅在 `fullyIdle=true` 时转换为完成；旧版 `model_stop` 仍支持，显式未空闲时只记活动。未知原因保留为状态未知。ACP 的 `stopReason=end_turn` 属于协议响应，本适配器仍以原生 hook 为事件来源。
