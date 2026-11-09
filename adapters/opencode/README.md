# OpenCode 适配器

本包面向 OpenCode **2.0.20**，使用 TypeScript 宿主插件；不兼容 1.x 插件 API，也不承诺其他 2.x 版本未经验证即可使用。转换逻辑在此目录，Bridge 只处理统一事件，不内置 OpenCode 专属规则。

## 安装和开发

先启动配置了固定 `source_id` 的 Bridge。将下面条目合并进项目的 `opencode.json` 或用户 OpenCode 配置，保留已有的其他条目；`package` 填本插件目录的绝对路径：

```json
{
  "plugins": [
    {
      "package": "/absolute/path/agent-pulse/adapters/opencode",
      "options": {
        "source_id": "my-linux",
        "bridge_url": "http://127.0.0.1:18702"
      }
    }
  ]
}
```

`source_id` 必须与本地 Bridge 一致，`bridge_url` 仅接受 `127.0.0.1` 或 `[::1]` 的 HTTP 根地址。保留目录根部的 `index.ts`，OpenCode 2.0.20 的本地目录加载实际需要该入口，不能只依赖 package.json 中的嵌套 exports。

插件代码直接由宿主加载；开发者检查时安装锁定 SDK 和 TypeScript：

```sh
pnpm install --frozen-lockfile --ignore-scripts
pnpm check
pnpm test
```

SDK 为开发/宿主依赖，不把整套 Node 服务部署进 Bridge。安装后检查 OpenCode 的插件清单，确认 ID `agent-pulse` 的 `state.status` 为 `active`；插件异步激活，刚启动时清单可能为空。已运行宿主的热加载效果尚未专门验收，首次接入建议正常重启宿主后再验证。不要因为文件已下载就宣称插件正在生效。

## 映射规则

| OpenCode 原生事件 | AgentPulse 事件 |
| --- | --- |
| `session.created` | `session.started` |
| `session.execution.started` | `session.resumed` |
| `session.execution.succeeded` | `turn.completed`，仅表示本轮执行完成 |
| `session.execution.failed` | `run.failed` |
| `session.execution.interrupted` | `run.interrupted` |
| `session.deleted` | `session.closed`，需已有可识别会话信息 |
| `permission.asked` | `approval.required` |
| `form.created` | `input.required` |
| `permission.replied` / `form.replied` / `form.cancelled` | `activity.observed`，使旧等待提醒失效 |

不将 `session.idle` 当作完成，不将单工具失败当作主会话失败，不复制错误详情或命令/权限资源列表进手机正文。事件 ID 由来源、Agent 名和原生事件 ID 计算，原生持久事件使用 `durable.seq`；批准/输入等待等实时事件无原生序号时按实时事件递交。

插件读取真实会话信息，有 `parentID` 即过滤；读失败、会话缺失、目录不匹配时忽略，不推断为主代理。每个实例只处理自己的目录；删除事件使用最多 1024 条的会话缓存，缓存缺失则忽略。获取会话和递交 Bridge 各限 200 毫秒，失败仅有限频诊断，不阻塞 Agent 操作或自动批准。

## 验证边界

`pnpm smoke` 使用本机真实 OpenCode，在临时目录生成配置，验证插件激活、创建会话、原生输入表单事件、Bridge 转发、Hub 已读和强制终止后恢复。测试代码见 [`test/smoke.ts`](test/smoke.ts)，主/子过滤和生命周期转换样例见 [`test/adapter.test.ts`](test/adapter.test.ts)。完成/失败/中断的映射经过 SDK 类型与样例测试，尚未用模型实际执行触发；真实子代理运行、热重载和多主机自动更新仍待验收。

当前不自动修改用户 Agent 配置、不分发更新包。独立版本目录和自动更新属于后续实施；保留本插件为独立包，普通映射修改不需要升级 Bridge。

接口依据：锁定的 `@opencode/plugin` / `@opencode/client` 2.0.20 类型、本机真实事件，以及 [官方插件说明](https://opencode.ai/docs/plugins/)、[服务端插件 API](https://opencode.ai/docs/build/plugins/server/)。在线文档可能对应更高版本，应以锁定版本和运行结果为准。

## 独立包更新

多主机部署使用 [签名包与固定 loader](../../docs/adapters.md)，无需更新 Bridge。`AGENT_PULSE_PACKAGE_SMOKE=1 pnpm smoke` 在隔离宿主中验证安装、真实激活和运行版本回执。直接目录接入仍适合本地开发，不能与固定 loader 同时注册同一个插件 ID。
