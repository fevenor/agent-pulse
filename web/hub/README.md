# Hub 管理页面

使用 Vue 3、TypeScript 和 Vite，构建为由 Hub 同源提供的静态文件。提供会话筛选、明确已读、设备路径与心跳、适配器版本、推送结果和历史分页，不承担桌面弹窗，也不发布可执行代码。

```sh
pnpm install --frozen-lockfile --ignore-scripts
pnpm build
```

Hub 配置 `web_directory` 指向本目录的 `dist/` 后，访问 Hub 根地址。开发时先在 `127.0.0.1:18700` 启动 Hub，再执行 `pnpm dev`；开发服务器代理 API 和 WebSocket，生产部署不需要 Node 服务。

首次加载和断线重连获取完整快照，实时变化触发刷新，15 秒轮询补充心跳和报告时间。连接断开显示数据可能过期，不把缓存设备标为在线。标记已读等待 Hub 确认，失败保留原状态，不实现浏览器离线已读队列。

可信内网默认免登录。Hub 配置共享令牌时，在“连接设置”输入，仅保存在页面内存，HTTP 使用 Authorization，WebSocket 使用请求子协议携带令牌；不写 URL、localStorage 或缓存文件。代理不应记录 Authorization 或完整 WebSocket 子协议。Web 首版同源访问 Hub，不作为公网 Relay 管理端。
