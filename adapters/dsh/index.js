// adapters/dsh/index.js —— DeepSeek Harness Host 插件入口。
// 未标记的监听器在根 ctx 上保持全局，可收到全部会话（含子代理）的事件；子代理只按会话头部标记过滤。
// 审批与提问是 waterfall：宿主自带的 answerer 一旦认领请求就不再调用 next()，后注册的监听器看不到它。
// 因此观察者必须以 prepend 排在所有 answerer 之前，只递交事件并把决定权原样交还 next()。
import { approval, archived, bridgeAddress, emit, hasEarlierTurnStart, heartbeat, HEARTBEAT_INTERVAL_MS, isSubagent, questions, renamed, resumed, SessionTracker, toolFailed, turnEnd, turnStart } from "./adapter.js";

export const inject = [];

const DURABLE_TYPES = new Set(["session/title", "tool/call", "tool/result", "assistant/message", "turn/start", "turn/end", "step/start", "user/message"]);

export function apply(ctx, config) {
  const source = config?.source_id;
  if (typeof source !== "string" || source.trim() === "" || Buffer.byteLength(source) > 256) {
    console.warn("AgentPulse 未启用：请配置与 Bridge 相同的 source_id");
    return;
  }
  let url;
  try { url = bridgeAddress(config?.bridge_address); }
  catch { console.warn("AgentPulse 未启用：bridge_address 必须使用本机IPC地址"); return; }
  const tracker = new SessionTracker();
  const controller = new AbortController();
  let lastWarning = 0;
  // 诊断只含错误类别与目标端点末 8 位，不含事件正文、令牌或会话内容。
  const warn = (reason) => {
    if (Date.now() - lastWarning > 10_000) {
      const detail = reason ? `（${reason}）` : "";
      console.warn(`AgentPulse 未能采集或递交部分事件${detail}；请检查 Bridge 和会话归属。通知失败不影响 DSH。`);
      lastWarning = Date.now();
    }
  };
  const describe = (error) => `${error?.name ?? "Error"}:${error?.code ?? error?.message ?? "?"}@…${url.slice(-8)}`;
  // 递交一律异步进行，不让通知故障阻塞宿主事件流。
  const send = (event) => {
    if (!event) return;
    emit(url, event, controller.signal).catch((error) => { if (!controller.signal.aborted) warn(describe(error)); });
  };
  const currentTitle = (session, id) => {
    const state = tracker.get(id);
    if (state.title) return state.title;
    let title;
    try { title = ctx.get?.("sessionTitle")?.get?.(session)?.title; } catch { title = undefined; }
    if (typeof title === "string" && title) state.title = title;
    return state.title;
  };
  // 等待中的会话再次出现持久活动时先恢复运行状态，使旧的等待提醒失效。
  const settle = (session, id, state, event, title) => {
    if (!state.waiting) return;
    state.waiting = false;
    send(resumed(source, session, event, title));
  };
  // 轮次内心跳：每个步骤节流一条运行活动，使丢失 turn/start 的长轮次也能回到 running。
  const pulse = (session, id, state, event, title) => {
    const now = Date.now();
    if (now - (state.lastActivity ?? 0) < HEARTBEAT_INTERVAL_MS) return;
    state.lastActivity = now;
    send(heartbeat(source, session, event, title()));
  };

  ctx.on("session/event", (session, event) => {
    if (!session?.header || !event || typeof event.type !== "string" || !DURABLE_TYPES.has(event.type) || isSubagent(session)) return;
    const id = String(session.id);
    const state = tracker.get(id);
    state.cwd = session.header.cwd ?? state.cwd ?? "";
    const title = () => currentTitle(session, id);
    try {
      switch (event.type) {
        case "session/title": {
          const next = renamed(source, session, event);
          if (next) state.title = next.session_title;
          send(next);
          return;
        }
        case "tool/call":
          tracker.observe(id, event);
          return;
        case "assistant/message":
          tracker.observe(id, event);
          settle(session, id, state, event, title());
          return;
        case "step/start":
          settle(session, id, state, event, title());
          pulse(session, id, state, event, title);
          return;
        case "user/message":
          settle(session, id, state, event, title());
          return;
        case "tool/result": {
          settle(session, id, state, event, title());
          // 实测 tool/result 同时携带 message.toolCallId 与 message.source.callId，二者皆可配对 tool/call。
          const callId = event.data?.message?.toolCallId ?? event.data?.message?.source?.callId;
          send(toolFailed(source, session, event, tracker.toolName(id, callId), title()));
          return;
        }
        case "turn/start": {
          // 首个 turn/start 之前没有更早轮次即新会话；宿主重启后恢复的会话只在用户真正继续时报告一次。
          // 后续每轮开始都是一次运行活动，使 Hub 的会话状态回到 running。
          const fresh = !hasEarlierTurnStart(session, event.seq);
          const kind = fresh ? "session.started" : state.seen ? "activity.observed" : "session.resumed";
          state.waiting = false;
          state.seen = true;
          // 新轮次已发进入运行信号，重置心跳计时，避免紧接着再发一条。
          state.lastActivity = Date.now();
          send(turnStart(source, session, event, kind, title()));
          return;
        }
        case "turn/end": {
          state.waiting = false;
          send(turnEnd(source, session, event, tracker.finalText(id, event.data?.turn), title()));
          return;
        }
      }
    } catch (error) { warn(`转换 ${error?.name ?? "Error"}:${error?.message ?? "?"}`); }
  });

  ctx.on("approval/request", (req, next) => {
    try {
      const agent = req?.agent;
      if (agent && !isSubagent(agent.session)) {
        const id = String(agent.id);
        const key = req.callId ? `call:${req.callId}` : `tool:${req.toolName ?? ""}`;
        if (tracker.claim(id, key)) {
          tracker.get(id).waiting = true;
          send(approval(source, agent, req, currentTitle(agent.session, id)));
        }
      }
    } catch (error) { warn(`转换 ${error?.name ?? "Error"}:${error?.message ?? "?"}`); }
    return next();
  }, { prepend: true });

  ctx.on("user-questions/request", (request, next) => {
    try {
      const agent = request?.agent;
      if (agent && !isSubagent(agent.session)) {
        const id = String(agent.id);
        const items = Array.isArray(request.questions) ? request.questions : [];
        const callId = request.wait?.callId ?? items.map((item) => item?.intent?.callId).find(Boolean);
        const isApproval = items.some((item) => item?.intent?.kind === "approval");
        const key = callId ? `call:${callId}` : isApproval ? `tool:${items[0]?.intent?.subject?.name ?? items[0]?.header ?? ""}` : "";
        if (tracker.claim(id, key)) {
          tracker.get(id).waiting = true;
          send(questions(source, agent, request, currentTitle(agent.session, id)));
        }
      }
    } catch (error) { warn(`转换 ${error?.name ?? "Error"}:${error?.message ?? "?"}`); }
    return next();
  }, { prepend: true });

  ctx.on("workspace/session-archived", (request) => {
    try {
      const id = request?.sessionId ? String(request.sessionId) : "";
      if (!id || !tracker.sessions.has(id)) return;
      const state = tracker.get(id);
      send(archived(source, id, state.cwd, state.title));
    } catch (error) { warn(`转换 ${error?.name ?? "Error"}:${error?.message ?? "?"}`); }
  });

  ctx.effect(() => () => controller.abort());
}
