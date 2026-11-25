// adapters/dsh/index.js —— DeepSeek Harness Host 插件入口。
// 未标记的监听器在根 ctx 上保持全局，可收到全部会话（含子代理）的事件；子代理只按会话头部标记过滤。
// 审批与提问是 waterfall：宿主自带的 answerer 一旦认领请求就不再调用 next()，后注册的监听器看不到它。
// 因此观察者必须以 prepend 排在所有 answerer 之前，只递交事件并把决定权原样交还 next()。
import { approval, archived, bridgeAddress, hasEarlierTurnStart, heartbeat, HEARTBEAT_INTERVAL_MS, isSubagent, questions, renamed, resumed, SessionTracker, toolFailed, turnEnd, turnStart } from "./adapter.js";

import { createSender } from "./delivery.js";
import { mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { dirname } from "node:path";

export const inject = [];

const DURABLE_TYPES = new Set(["session/title", "tool/call", "tool/result", "assistant/message", "turn/start", "turn/end", "step/start"]);

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
  const send = createSender(url, controller.signal, (error) => warn(describe(error)));
  const currentTitle = (session, id) => {
    const state = tracker.get(id);
    if (state.title) return state.title;
    let title;
    try { title = ctx.get?.("sessionTitle")?.get?.(session)?.title; } catch { title = undefined; }
    if (typeof title === "string" && title) state.title = title;
    return state.title;
  };
  // 轮次内心跳：每个步骤节流一条运行活动，使丢失 turn/start 的长轮次也能回到 running。
  const pulse = (session, id, state, event, title) => {
    if (state.waits.size) return;
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
          return;
        case "step/start":
          pulse(session, id, state, event, title);
          return;
        case "tool/result": {
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
          state.waits.clear();
          state.seen = true;
          // 新轮次已发进入运行信号，重置心跳计时，避免紧接着再发一条。
          state.lastActivity = Date.now();
          send(turnStart(source, session, event, kind, title()));
          return;
        }
        case "turn/end": {
          state.waits.clear();
          send(turnEnd(source, session, event, tracker.finalText(id, event.data?.turn), title()));
          return;
        }
      }
    } catch (error) { warn(`转换 ${error?.name ?? "Error"}:${error?.message ?? "?"}`); }
  });

  // 每次 waterfall 调用各持一个等待令牌；同 callId 的嵌套通路只提醒一次，全部结束才恢复。
  // 请求结束由 next() 的结果或宿主取消信号确认，其他工具的结果不会解除等待。
  async function observeWait(request, next, convert, callId) {
    let finish = () => {};
    try {
      const agent = request?.agent;
      if (agent && !isSubagent(agent.session) && !request.signal?.aborted) {
        const id = String(agent.id), state = tracker.get(id);
        const token = Symbol(), key = callId ? `call:${callId}` : token;
        const duplicate = [...state.waits.values()].includes(key);
        const notification = duplicate ? undefined : convert(source, agent, request, currentTitle(agent.session, id));
        if (duplicate || notification) {
          state.cwd = agent.session?.header?.cwd ?? state.cwd ?? "";
          state.waits.set(token, key);
          send(notification);
          finish = () => {
            request.signal?.removeEventListener("abort", finish);
            if (!state.waits.delete(token) || state.waits.size || controller.signal.aborted || tracker.sessions.get(id) !== state) return;
            state.lastActivity = Date.now();
            try { send(resumed(source, agent.session, {}, currentTitle(agent.session, id))); }
            catch (error) { warn(`转换 ${error?.name ?? "Error"}:${error?.message ?? "?"}`); }
          };
          request.signal?.addEventListener("abort", finish, { once: true, signal: controller.signal });
        }
      }
    } catch (error) { warn(`转换 ${error?.name ?? "Error"}:${error?.message ?? "?"}`); }
    try { return await next(); }
    finally { finish(); }
  }

  ctx.on("approval/request", (req, next) => observeWait(req, next, approval, req?.callId), { prepend: true });
  ctx.on("user-questions/request", (request, next) => {
    const items = Array.isArray(request?.questions) ? request.questions : [];
    const callId = request?.wait?.callId ?? items.map((item) => item?.intent?.callId).find(Boolean);
    return observeWait(request, next, questions, callId);
  }, { prepend: true });

  ctx.on("workspace/session-archived", (request) => {
    try {
      const id = request?.sessionId ? String(request.sessionId) : "";
      if (!id || !tracker.sessions.has(id)) return;
      const state = tracker.get(id);
      state.waits.clear();
      send(archived(source, id, state.cwd, state.title));
    } catch (error) { warn(`转换 ${error?.name ?? "Error"}:${error?.message ?? "?"}`); }
  });

  ctx.effect(() => () => controller.abort());
  // 回执由适配包自身记录；宿主配置直接引用不可变入口，不读取 Bridge 的状态或恢复日志。
  if (config.runtime_receipt) {
    try {
      const { version } = JSON.parse(readFileSync(new URL("./package.json", import.meta.url), "utf8"));
      const target = config.runtime_receipt, temporary = `${target}.${process.pid}`;
      mkdirSync(dirname(target), { recursive: true });
      writeFileSync(temporary, JSON.stringify({ version, at: new Date().toISOString() }), { mode: 0o600 });
      renameSync(temporary, target);
    } catch (error) { warn(`运行回执 ${error?.code ?? error?.name ?? "Error"}`); }
  }
}
