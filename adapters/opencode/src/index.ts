import { Plugin } from "@opencode/plugin";
import { bridgeURL, convert, emit, eventTypes, sessionID, type SessionInfo } from "./adapter.ts";

export default Plugin.define({
  id: "agent-pulse",
  setup(ctx) {
    const source = ctx.options.source_id;
    if (typeof source !== "string" || source.trim() === "" || Buffer.byteLength(source) > 256) {
      console.warn("AgentPulse 未启用：请配置与 Bridge 相同的 source_id");
      return;
    }
    let url: string;
    try { url = bridgeURL(ctx.options.bridge_url); }
    catch { console.warn("AgentPulse 未启用：bridge_url 必须使用回环 HTTP 地址"); return; }
    const controller = new AbortController();
    // 有界缓存用于删除事件到达后仍能识别原会话，未命中则安全忽略。
    const sessions = new Map<string, SessionInfo>();
    let lastWarning = 0;
    const warn = () => {
      if (Date.now() - lastWarning > 10_000) {
        console.warn("AgentPulse 未能采集或递交部分事件；请检查 Bridge 和会话归属。通知失败不影响 Agent。");
        lastWarning = Date.now();
      }
    };
    void (async () => {
      try {
        for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
          try {
            if (!(eventTypes as readonly string[]).includes(event.type)) continue;
            const id = sessionID(event);
            if (!id) continue;
            let session = sessions.get(id);
            if (event.type !== "session.deleted") {
              session = await ctx.session.get({ sessionID: id }, { signal: AbortSignal.any([controller.signal, AbortSignal.timeout(200)]) });
              // 全局事件流中只处理本插件实例所属目录，防止多个项目实例重复递交。
              if (session.location.directory !== ctx.location.directory) continue;
              sessions.set(id, session);
              if (sessions.size > 1024) sessions.delete(sessions.keys().next().value!);
            }
            const converted = convert(event, session, source);
            if (converted) await emit(url, converted, controller.signal);
            if (event.type === "session.deleted") sessions.delete(id);
          } catch { if (!controller.signal.aborted) warn(); }
        }
      } catch { if (!controller.signal.aborted) warn(); }
    })();
    return () => controller.abort();
  },
});
