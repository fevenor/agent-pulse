# 来源接入与独立适配包

适配器负责产品事件到统一协议的转换，Bridge负责本机入口、转发与安装。当前源码的Linux接入包含OpenCode、Codex、Antigravity CLI/WSL桌面执行器、Qoder中国CLI、ZCode Server及DeepSeek Harness（DSH）；Windows接入Codex/ChatGPT本地任务、Antigravity和ZCode桌面。ZCode Server与DSH的Linux包已随0.6.1公开。Windows、WSL和SSH目标按实际hook执行位置分别部署；不抓取界面或构建跨系统代理。实际宿主验证边界见开发记录，OpenCode平台包只承诺Linux宿主。

## 支持边界

| 来源 | 接入形态 | 事件及限制 |
| --- | --- | --- |
| OpenCode | TypeScript 原生插件、固定 loader | 原生会话/执行/表单/权限事件；查询会话 `parentID` 排除子代理；插件更新后需要宿主重新加载 |
| DeepSeek Harness | DSH Host 插件、随包控制入口 | 持久会话日志的轮次开始/终态、工具失败、改名，`approval/request` 与 `user-questions/request` 的等待，以及归档；按会话头部 `origin: subagent`/`delegationDepth` 过滤子代理；事件名随 DSH 版本变化，`x.y` 变化需重新适配 |
| ZCode | Windows 桌面及 Linux Server 原生 process 钩子 | SessionStart、UserPromptSubmit、正常 Stop、提问工具的 PreToolUse、工具结果/失败和 PermissionRequest；过滤明确子代理及数据库中的父会话；工具失败不等于主任务失败，取消与非正常退出不推断 |
| Codex | 命令 hook | 会话开始/恢复/关闭、用户提交、本轮结束、用户中断；不接收 SubagentStart/Stop。没有可靠归属信息的工具权限/输入 hook 暂不转换，不能据此宣称已覆盖等待状态 |
| qoderclicn | 命令 hook | 会话、轮次结束、StopFailure、权限请求、问答工具和 MCP 输入提示；包含子代理 ID/type 的事件忽略；工具错误不等于主任务失败 |
| Antigravity CLI | 原生命令 hook | 普通直接启动采集会话开始、模型活动、问答/权限等待和 Stop 原因；共用本机入口过滤 ACP，明确父/内部会话字段另行过滤，不要求绑定 |

Hook 只返回被动响应，不修改批准决定、注入提示词或要求 Agent 继续执行。命令转换允许最多两秒以覆盖Windows冷启动，Codex的SessionEnd与Interrupt宿主钩子超时三秒，其他事件五秒；之后本地递交最多等待200毫秒；异常只发诊断，不让通知故障阻塞 Agent。默认只传事件类型、会话 ID 和项目名。ZCode、Qoder、Antigravity 与 OpenCode 在完成时另传原生可取得的最终回答；提问时只提取问题、选项标签及说明，不上传用户提示词、思考、历史对话、无关工具参数或完整错误响应。失败/批准可见摘要按原生字段白名单提取，详见[事件与内容覆盖](event-coverage.md)。正文受当前事件 32 KiB 上限约束，超长内容明确标注截断，不丢弃完成/等待状态。

原生 hook 没有统一稳定事件 ID/时间，适配器为每次调用生成 ID；Bridge 网络重试保留转换后的同一份 ID、时间和内容。若宿主自己重新调用 hook，它是另一次调用，当前不能保证跨调用去重。OpenCode 则使用原生事件 ID 生成稳定标识。

产品配置参考：[Codex hooks](https://developers.openai.com/codex/hooks)、[Qoder hooks](https://docs.qoder.com/cli/hooks)、[Antigravity hooks](https://antigravity.google/docs/hooks)。实际验证证据见 [开发记录](development-status.md)，不以在线文档代替本机版本验证。

## 宿主版本政策

`x.y.z` 分别表示主、次、补丁版本。同一已验 `x.y` 下补丁变化默认支持；同一主版本内次版本变化允许继续使用但不承诺兼容；主版本变化需重新测试后纳入支持，不要求每个补丁版本重复验收。该政策与已验版本证据分开记录。Bridge 按清单 `agent_min`/`agent_max` 的主次版本线选择：补丁差异不阻断，同主版本内超出已验次版本线仍安装并报告警告，主版本超出声明则拒绝。Hub 将警告与安装失败分开显示；签名、当前格式和平台保持原校验，不因宿主政策放宽；本项目不维护旧接收器最低版本门槛。

## 发布者：构建与签名

发布私钥只留在发布者机器；签名发布仓库包含 `index.json`、`packages/` 和公开的 `publisher.pub`，不能包含私钥或未签名 `index-input.json`。以下操作在本地生成文件，不提交 Git，也不自动推送到服务器。

```sh
pnpm --dir adapters/opencode install --frozen-lockfile --ignore-scripts
bash scripts/build/package-adapters.sh ./dist/repository 1
./bin/agent-pulse-adapters keygen --private /安全目录/publisher.key --public /安全目录/publisher.pub
./bin/agent-pulse-adapters publish --repository ./dist/repository --private /安全目录/publisher.key --index ./dist/repository/index-input.json
```

`package-adapters.sh` 构建 Linux amd64 的 Codex、Antigravity、Qoder、ZCode 命令包、带控制入口的两架构 OpenCode 与 DSH 插件包；ZCode 使用 `adapters/zcode/manifest-linux.json`。`package-adapters.ps1` 构建Windows amd64的ZCode、Codex与Antigravity包，内部调用仅Go构建入口。候选统一使用当前格式，不设置旧接收器版本门槛。其他架构需交叉编译适配器并同时修改清单中的 `platform`，不能仅修改清单冒充对应二进制。Go 产品适配器共用编译入口 `go/cmd/agent-pulse-adapter`，构建时注入来源 ID；每个包独立于 Bridge 发布。OpenCode包携带平台Go控制入口及所需SDK，目标安装时不执行包管理器或任意安装脚本；DSH 事件插件为 ESM JavaScript，包内 Go 控制入口负责探测与 profile 配置，按 Linux amd64/arm64 分包。

ZCode 使用 `process-hooks` 配置格式；当前候选的 Bridge 已实现该格式。结构变更按同一现行构建更新，不提供旧接收器兼容。

每次发布递增 `sequence`。`packages` 是本次可用的版本集合，同一来源默认选择当前平台的最高兼容版本。集中回滚时，在新的更高序号索引里为该来源选择已有旧包，不复用旧序号。保留多个版本可供单机 `pins` 固定使用。相同版本不得更改内容，修改必须发布新版本。

同时分发 Linux 和 Windows 包时，将两侧构建仓库放到发布者本机后合并，再用同一个发布私钥签名：

Windows侧执行 `./scripts/build/package-adapters.ps1`，生成三类Windows原生包；`-NativeOnly` 保留为既有命令的兼容参数，不再额外生成 OpenCode 包。OpenCode插件统一采用Linux仓库中的一次JS构建，Go控制入口按linux/amd64和linux/arm64分包，当前支持 Linux 宿主。Windows Workflow按三包清单验收，Linux Workflow提供六类来源，OpenCode与DSH控制入口各按两架构分包，合并Windows三包为十一包。

```sh
uv run --offline --no-project python scripts/release/merge_adapter_repositories.py \
  ./dist/repository ./dist/repository-windows --output ./dist/repository-combined --sequence 2
./bin/agent-pulse-adapters publish --repository ./dist/repository-combined \
  --private /安全目录/publisher.key --index ./dist/repository-combined/index-input.json
```

示例序号需替换为大于已发布序号的值。合并器校验包摘要与大小，保留每个平台的包；同一平台包的清单、内容及摘要必须一致；带Go控制入口的OpenCode按平台分别保留，不跨平台去重。内容或兼容清单不同的同版本包会报错，不自动挑选或覆盖；失败时不替换既有索引。合并只生成 `index-input.json` 和不可变包，不读取私钥，也不自动部署。

ZIP 成员时间统一为固定值，同一文件内容及文件权限不因重建时间不同而改变包摘要；不因此承诺不同编译器、依赖或 bundle 来源注释下的构建产物逐字相同。修改实际包内容仍须递增版本，不能覆盖已分发的同版本包。

同一插件在不同平台重复打包时，pnpm 路径长度处理可能使 bundle 的来源注释不同；合并器不会忽略这些内容差异。公用索引应使用上述单次通用构建方式，不通过放宽摘要检查或覆盖同版本包规避冲突。

Hub 未签名应用构建由工作流保存为发布者专用 OCI 归档 `hub-build.tar`，不单独公开第三个镜像，普通用户 RC 镜像须附带该 RC 的已签名仓库和公钥。完成签名后，按[镜像发布说明](images.md#准备普通用户-hub-rc-镜像)运行 `scripts/release/prepare-hub-release.py`，使用匹配的 OCI 内部构建生成新的 Docker 构建目录并构建正式 Hub 镜像。准备脚本复用客户端验签规则，拒绝无效签名、丢失或摘要不符的包，复制时排除私钥及未签名索引。正式镜像首次启动从`/repository`固定公钥并初始化`/data/adapters`，后续使用持久活动仓库，镜像种子不覆盖它；管理员显式配置 `adapter_public_key` 时仍以显式配置为准。普通使用者下载包含此能力的完整镜像后不需要挂载活动仓库或配置公钥；该能力已随0.6.1发布。发布能力仍为本地命令，Web 不提供可执行包上传或目标修改接口。

**分发、安装与产品逻辑分离**：Hub 的 `Manifest.Validate` 只检查仓库结构与分发完整性，不解释宿主命令、hook 名、配置格式和 Agent 兼容版本。Bridge 在本机按既有宿主版本政策选包；带 `control` 的可信包自行提供探测与配置，不需要 Bridge 增加 `kind`/`config_style`/hook 名白名单。无控制入口的包仍须使用本机已有安装方式。签名、摘要、路径、不可变版本和安装恢复保持。新统一事件能力或新系统运行机制仍需相应核心实现；仅改变包内宿主逻辑不需要升级 Hub/Bridge。

## Hub：独立在线发布与离线更新

Hub默认每天02:00（Asia/Shanghai）检查，可通过`adapter_repository_updates.daily_at/timezone/disabled`调整或关闭。独立`adapters-<序号>`发布携带签名索引和平台包，不依赖产品latest。管理员可用`agent-pulse-hub --config /配置/hub.yaml --update-adapters`手工检查，或`--import-adapters /离线/adapter-repository.zip`导入目录/ZIP。全部平台包就绪后才推广索引，旧包保留，活动仓库持久化；Web没有上传入口。新包的`name/control`接管探测和配置生成，普通新增来源、映射及hook修改无需升级Hub/Bridge。具体契约、独立发布工具和安全规则见[更新设计](adapter-repository-update-proposal.md)。首次上线需升级Hub/Bridge，后续独立包更新无需重建镜像。

## 主机：首次接入与按需更新

普通使用者运行 `agent-pulse-bridge setup`，按提示填写 Hub 地址并逐项选择要启用的 Agent。Bridge 会创建稳定的随机主机指纹、以系统主机名作为默认显示名，并只修改用户明确选择的宿主配置。配置开始前说明操作，结束后统一显示名称、宿主版本、实际配置路径及结果，再说明 setup 的维护用途。OpenCode 自动识别已有 JSON/JSONC，已有明确路径仍优先。额外的钩子授权提醒只针对本次启用且配置完成的 Codex CLI；旧进程未加载时再按故障排查处理。

日常配置无需手动填写发布公钥。Bridge 首次通过配置的 Hub 地址直连获取公钥，并按 Hub 地址和本机指纹保存首次信任记录；之后若 Hub 返回不同公钥会停止自动更新并要求人工核验。首次信任不得经 Relay 建立。手动配置 `adapters.public_key` 仅用于管理员已通过独立渠道核验并固定公钥的部署。公网部署必须正确配置 HTTPS 和访问控制；可信个人内网可使用受控的 HTTP Hub。

`managed` 是本机明确授权 Bridge 修改的文件范围，不扫描或修改其他产品配置。setup 发现已有配置时会优先沿用；Qoder 国际版的默认目录与中国版不同。既有内置JSON型来源的配置根必须为JSON对象；带控制入口的包自行解析产品配置格式。OpenCode使用hujson保留注释、尾逗号、原始数值及非本软件插件；无效结构或重复键拒绝覆盖，不要求把合法JSONC转换为JSON。保留用户其他 hook/plugin 条目，仅维护 AgentPulse 自己的命令。

高级部署可在 Bridge YAML 中调整适配包目录和版本策略：

```yaml
adapters:
  directory: "/home/your-user/.local/share/agent-pulse/adapters"
  min_check_seconds: 300 # 仅setup和显式同步；启动总是检查
  pins: {}
  exclude: []
  managed: {}
```

每次启动Bridge检查一次；正常运行或重连不检查，不耗费资源检测adapter更新。setup和显式同步按上述最小间隔检查，--force-sync绕过节流但不重下相同包，也不绕过安全校验。检查失败时setup使用可信本地副本；选中来源缺必需包时明确报告未完成。也可手动执行：

```sh
./bin/agent-pulse-bridge sync-adapters --config config/bridge.local.yaml
```

签名、包摘要、大小、解压路径、当前格式、平台及宿主 Agent 版本校验通过后才切换。Linux 与 Windows 均使用系统文件锁，进程退出会自动释放。中断的安装根据 `pending.json` 恢复原配置和原版本；若用户在此期间修改了文件，停止自动恢复并报告，避免覆盖用户新配置。保留包目录，不删除正在运行的版本。

Hub 不可达时通过已配置 Relay 分块下载；两条通路使用同一受信发布公钥。失败保留旧包。明确的入口不可启动触发回滚并隔离坏包；普通不合法输入或超时不把整个包判坏。更高序号的新发布可解除隔离。Windows 安装器使用隔离宿主验证，真实 Agent 的 hook 加载和信任仍需逐项验收；实现与命令引用规则见 [Windows 客户端](windows-client.md#windows-适配包)。其他平台继续拒绝自动安装。

Codex 和 qoderclicn 的宿主信任提示由用户在宿主中确认，安装器不持久关闭信任检查。OpenCode 已运行实例不会被强制重启；“已安装”与“最近运行”不同，管理页会提示未确认或等待重新加载。

## ZCode 桌面接入

ZCode 已实测基线为 Windows amd64 的 3.17.2，主/次/补丁版本政策见上文。安装器从默认用户/系统安装位置或 PATH 定位 `ZCode.exe`，读取文件版本资源，避免为了探测版本启动桌面程序；非默认安装位置需将可执行文件所在目录加入 PATH。同一版本线的兼容承诺不表示每个补丁均有实测证据；已知事件结构不兼容情况需单列。

Tray 启用内置 Bridge，并在 Bridge 配置的 `adapters.managed` 中增加 `zcode`，值为本机 `%USERPROFILE%/.zcode/cli/config.json` 的实际绝对路径；保留原有公钥、包目录及其他来源。不要把示例百分号变量直接写入 YAML 期待自动展开。经签名的 Windows ZCode 包安装后，设置页“钩子”可看到 AgentPulse 管理的三个生命周期及提问前后事件。同版本已安装也会核对当前授权文件并补回缺失钩子；配置无变化不重复写入。向导以实际文件及总开关核验结果报告完成，不只依赖安装记录。

配置采用 `hooks.events` 中的 `type: process`，可执行路径和参数数组分开保存，避免 PowerShell/cmd 引号及编码转换。已有 `hooks.enabled: false` 会被尊重，需用户在 ZCode 中明确开启；没有该字段时安装器启用钩子。保留其他用户钩子、超时和错误策略，仅替换本软件同来源同配置的 dispatch 条目；配置路径按同文件身份核对，普通路径、扩展路径、短路径或不同大小写不会留下重复自有钩子。其他 Bridge 配置和用户钩子保留；升级内置 Bridge 的版本目录不会重复安装。

开始和用户提交分别映射会话开始/继续，正常 Stop 映射本轮完成并读取原生 `last_assistant_message` 最终回答。对提问工具登记PreToolUse，对所有工具登记PostToolUse和PostToolUseFailure：提问前显示等待输入及问题/选项，工具返回后恢复活动状态。RC3新增原生PermissionRequest的授权原因/工具目标及普通工具完成、工具错误记录；同一提问不另记等待批准。明确 `agent_id`、`agent_type`、`agentName` 或父会话的子代理输入仍被忽略，不通过缺少字段声称全部内部子代理已验证。强制取消、崩溃、模型异常不保证触发 Stop，不推断失败或关闭；可在详情人工修正状态。

原始钩子输入上限为 8 MiB，转换后网络事件上限保持 64 KiB，正文最多 32 KiB并按 UTF-8 边界明确截断。不读取完整对话文件来补答案。真实测试流程见 [端到端测试](../tests/e2e/README.md#zcode-windows-桌面)。

## ZCode Server 的 Linux 接入

ZCode Server 是远程主机上的 Linux 执行器，WSL 和 SSH 是桌面连接它的方式，不是两套适配来源。当前清单基线为 Server 3.18.2、Linux amd64，包已随0.6.1发布；Windows桌面版本、Server宿主版本和AgentPulse适配包版本分别记录。

在 Server 所在 Linux 主机、相同用户下运行独立 `agent-pulse-bridge`。安装向导读取该用户 `.zcode/server/current.json`，核对其中选中的版本目录、产物清单及运行文件；不启动Server、不依赖PATH或`WSL_DISTRO_NAME`，不扫描历史目录取最高版本，也不借用Windows程序版本。WSL与SSH复用相同Linux路径及事件转换，不由Tray代理远程hook或管理远程服务。

向导管理该Linux用户 `.zcode/cli/config.json`，使用 `type: process` 和独立参数数组调用Linux Bridge。已有服务复用来源标识、公钥及适配目录；Windows配置保持独立。钩子保留用户其他条目及显式关闭的总开关，正常从ZCode桌面启动远程任务即可采集，不要求Bridge包装启动。

会话标题和父身份只读查询所在Linux用户 `.zcode/cli/db/db.sqlite`，沿用Windows的字段与子会话过滤；缺库或锁竞争不阻断事件，不从完整对话补正文。Server采用自定义会话数据库位置时，当前默认路径查询不保证名称及父身份补充；事件中已有的明确子身份仍过滤。用户服务退出登录后的持续运行要求见[部署指南](deployment.md#2-在-linux-主机安装-bridge)。

真实验收使用[Linux ZCode测试入口](../tests/e2e/README.md#zcode-server-linuxwslssh)，区分安装与运行回执、真实原生事件和提醒实收。WSL通过不代替SSH实际任务、arm64或异常退出验收；实际平台支持以签名清单为准。

## DeepSeek Harness 的 Linux 接入

DSH 没有外部命令 hook；接入形态是 Host 插件 [`dsh-agent-pulse-adapter`](../adapters/dsh/README.md)，由 DSH 宿主进程直接加载，监听 Cordis 事件总线并经本机 IPC 递交 Bridge。事件映射、正文白名单与子代理过滤规则见该 README 与[事件覆盖表](event-coverage.md)。

在运行 DSH 的 Linux 主机、相同用户下运行独立 `agent-pulse-bridge setup`，选择 DeepSeek Harness 并确认 profile 的 `cordis.patch.yml` 路径（默认 `$DSH_HOME/profiles/web/cordis.patch.yml`，未设置 `DSH_HOME` 时为 `~/.dsh/profiles/web/`；`tui`、`desktop` 等其他 profile 需改成对应路径）。向导探测 `dsh --version`，按 `x.y.z` 版本线选择兼容包，预发布后缀只给出提示；随后在 Bridge 的 `adapters.managed` 中登记 `dsh` → 该 patch 的绝对路径。

受管理安装不调用 `dsh plugin add`、不依赖 pnpm 或 profile 的 `node_modules`。随包控制入口生成 `id: agent-pulse` 的 `insert` 行，`name` 直接指向不可变 `packages/dsh/<version>/<sha256>/index.js` 的 `file:` URL，`config` 包含 `source_id`、`bridge_address` 和本机运行回执路径。Bridge 只提交授权文件与安装状态，不生成 DSH loader，也不解释 DSH YAML。

其他条目、注释和 `!!js` 标签保留；YAML 序列化不保证字节一致。升级切换该行的包路径，恢复切回旧配置；插件无需读取安装状态与恢复日志，不存在新 URL 缓存旧选择的问题。已有固定 loader 行可由同一安装目录接管。插件在实际注册后写 `runtime/dsh.json`，只说明加载，不能代替采集实收。文件改动本身不会触发 DSH 重组，须由宿主重新加载后核对 running 与真实事件。

以下情况 Bridge 拒绝写入并保留原文件：profile 的 `package.json` 已登记 `dsh-agent-pulse-adapter` bundle（手动安装，二者不能并存）；patch 中已有其他安装目录的 `agent-pulse` 行；存在 `id: agent-pulse` 的配置覆盖行。手动安装请在 DSH 的 Plugins 页添加 `adapters/dsh` 目录，并在 profile patch 中覆盖该行 `config`；该方式不写运行回执。

当前 DSH 包只提供 Linux amd64/arm64，Windows 接入与 DSH ACP 暂不纳入验收；`workspace/session-archived` 以外的会话关闭（如宿主退出）不报告 `session.closed`，避免每次重启把全部会话标为关闭。

## 正常启动与派生运行过滤

安装 hook 后，各 Agent 保持原来的正常启动方式。例如直接运行 `agy` 或 `agy --print '你的任务'`，不经 Bridge 启动，不创建主会话绑定文件，不要求换命令。

ACP（Agent Client Protocol，其他程序通过协议调用 Agent 的方式）启动在本项目中视为子代理运行，不向用户推送。例如 `agy acp --stdio --process-per-session` 的事件会被过滤。实现位置不是需求约束：本项目在 Bridge 的共用本机入口检查已安装 Agent 的启动来源，命令 hook 和 OpenCode 插件共用同一检查，新来源可复用现有清单的 `command`，仍须验证其实际启动形式；解释器启动的 DSH ACP 暂未覆盖。

启动进程信息只在本机判断运行方式，不进入 Hub/Relay 的事件、存储或界面。程序识别启动子命令/开关，不因提示词里出现 acp 文本而过滤。Linux 和 Windows 均查询本机进程祖先；Windows 对应系统 API 及共用入口已原生执行进程测试。该机制测试不等于各宿主完整 ACP 任务已经实测。

原生载荷中明确的子代理/父会话字段由各产品的薄适配转换过滤。Antigravity 的默认外部 hook 没有公开父/根字段；本地缓存按当前 ID 明确标记 `is_internal: true` 时也会过滤内部会话。缓存缺失或 false 不拿来证明主身份，也不以信息缺失阻断普通启动。缓存首次可用性及所有内部子代理覆盖不作未经实测的承诺。

DSH 的子代理身份在会话头部明确：`dsh-subagent` 与 `dsh-subagent-codex` 都写入 `origin: 'subagent'`、`parentSession` 与 `delegationDepth`，插件只按 `origin` 或大于 0 的 `delegationDepth` 过滤（实测根会话头部也带 `delegationDepth: 0`），不查询进程祖先；仅带 `parentSession` 的分叉会话仍是用户可见的主会话。`dsh headless`、定时任务与目标续跑都是顶层会话，按主会话采集。

审批与提问事件是 waterfall：宿主自带的 answerer 一旦认领请求就不再调用 `next()`，后注册的监听器看不到它。插件把这两个观察者以 `prepend` 注册在所有 answerer 之前，只递交事件并把决定权原样交还；它们不能、也不修改审批或回答。DSH 按请求生命周期跟踪多个未决等待，仅在全部请求真正完成/取消后恢复运行；按会话在后台保序递交，短暂 IPC 故障最多三次尝试，内存队列与重启边界见适配器 README。

### 会话身份与名称来源

会话身份按来源、Agent 和原生会话 ID 定位，名称是可变属性。无名称时显示“未命名会话”，项目单独展示；本项目别名仅作备注，不覆盖 Agent 最新名称。名称只从宿主明确保存并用于展示的字段获取，不使用对话正文、摘要或 ID 自行生成。

- Codex CLI：每次真实 hook 按 ID 读取可选 `session_index.jsonl` 的 `thread_name`，见 [Codex 适配器](../adapters/codex/README.md#会话标题)。
- Qoder 中国版 CLI：每次真实 hook 按 ID 读取会话文件中的 `custom-title.customTitle`，用户名优先；其次采用 `ai-title.aiTitle`，都缺失时采用宿主 1.1.89 明确用于列表展示的 `last-prompt.lastPrompt` 元数据。该字段是宿主展示来源，不是从对话正文推断名称；见 [Qoder 适配器](../adapters/qoderclicn/README.md#会话名称读取)。
- Antigravity CLI：每次真实 hook 按 ID 优先读取持久化注解 `annotations/<会话标识>.pbtxt` 的顶层 `title`；不可用时才回退 JSON 缓存 `summary.ID/Title`，不使用 `Preview`，见 [Antigravity 适配器](../adapters/antigravity/README.md#会话名称)。
- OpenCode：接入原生 `session.renamed`；普通事件也携带原生名称，改名事件使用当时的原始新名称，网络重试或重放不因后续改名改变载荷。
- ZCode Windows：每次原生 hook 按精确会话 ID 只读查询 `%USERPROFILE%/.zcode/cli/db/db.sqlite` 的 `session.title/title_source/parent_id`。默认占位名称不作为真实名称，显式父会话过滤。改名没有独立配置钩子，下一次真实事件同步新名称；缺库、未知结构或锁竞争不阻断事件，首次无名称显示未命名。Windows 3.18.2 的真实改名及下一轮事件已验证，详见开发记录。

不进行名称轮询；没有可接入改名事件的来源，等下一次真实事件读取名称。元数据不可用时不丢弃原事件，也不清除已知名称。已有非零名称序号时，旧序号和无序号载荷不能回滚新名称；普通无序号来源按到达顺序处理。使用已具备名称协议的Hub/Bridge后，后续产品名称读取由独立适配包更新；单独更新网页不能让旧包获取标题。正式发布与实际升级另行授权。

## RC3命令型入口边界

Windows命令型hook通过固定CMD执行边界和编码参数启动PowerShell，再直接调用预构建EXE；兼容宿主CMD与PowerShell，原始stdin不走文本管道。安装路径含不可安全表达的字符时明确拒绝，避免变量展开误执行。ZCode原生参数数组入口不受此限制。Codex/ChatGPT在宿主界面审核信任，安装器不绕过。Antigravity App/CLI共用一套受管理hook，WSL桌面执行器按版本目录探测，不以独立agy版本冒充App。
