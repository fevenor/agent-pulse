// adapters/dsh/adapter.js —— DeepSeek Harness 原生事件到 AgentPulse 统一事件的纯转换，不依赖宿主 API。
// 正文白名单、UTF-8 安全截断与本机 IPC 递交规则沿用 OpenCode 适配器（adapters/opencode/src/adapter.ts）；两个包独立发布。
import { request } from "node:http";
import { isAbsolute, normalize } from "node:path";
import { createHash } from "node:crypto";

export const AGENT = "dsh";
export const BODY_LIMIT = 32 << 10;
// 转换后网络事件上限 64 KiB；为元数据预留空间，JSON 转义后也不得超过 56 KiB。
const WIRE_LIMIT = 56 << 10;
const TRUNCATED = "[内容超过事件长度上限，已截断]";

export const labels = {
  "session.started": "会话已开始",
  "session.resumed": "会话已继续",
  "session.closed": "会话已归档",
  "activity.observed": "正在处理",
  "turn.completed": "本轮处理完成",
  "run.failed": "本轮处理失败",
  "run.interrupted": "本轮处理已中断",
  "approval.required": "等待批准",
  "input.required": "等待输入",
  "tool.failed": "工具执行失败",
  "session.renamed": "会话名称已更新",
};

export function visibleBody(value, limit = BODY_LIMIT) {
  const text = String(value ?? "").trim();
  if (Buffer.byteLength(text) <= limit && Buffer.byteLength(JSON.stringify(text)) <= WIRE_LIMIT) return text;
  const suffix = "\n\n" + TRUNCATED;
  let prefix = Buffer.from(text).subarray(0, Math.max(0, limit - Buffer.byteLength(suffix)));
  // 不拆开 UTF-8 多字节字符。
  while (prefix.length && (prefix[prefix.length - 1] & 0xc0) === 0x80) prefix = prefix.subarray(0, prefix.length - 1);
  while (prefix.length && Buffer.byteLength(prefix.toString()) > prefix.length) prefix = prefix.subarray(0, prefix.length - 1);
  while (Buffer.byteLength(JSON.stringify(prefix.toString() + suffix)) > WIRE_LIMIT) {
    prefix = prefix.subarray(0, Math.max(0, Math.floor(prefix.length * 0.9)));
    while (prefix.length && Buffer.byteLength(prefix.toString()) > prefix.length) prefix = prefix.subarray(0, prefix.length - 1);
  }
  return prefix.toString() + suffix;
}

export function bridgeAddress(value) {
  if (typeof value !== "string" || value.includes("\0")) throw new Error("Bridge必须为本机IPC地址");
  if (value.startsWith("unix:")) {
    const path = value.slice(5);
    if (!isAbsolute(path) || normalize(path) !== path || Buffer.byteLength(path) > 107) throw new Error("Unix套接字地址无效");
    return path;
  }
  if (/^pipe:\\\\.\\pipe\\[A-Za-z0-9_.-]{1,200}$/.test(value)) return value.slice(5);
  throw new Error("Bridge必须为本机IPC地址");
}

export async function emit(address, event, signal) {
  const timeout = AbortSignal.timeout(200);
  await new Promise((resolve, reject) => {
    const req = request({ socketPath: address, path: "/api/v1/events", method: "POST",
      headers: { Host: "127.0.0.1:1", "Content-Type": "application/json" },
      signal: signal ? AbortSignal.any([timeout, signal]) : timeout }, (response) => {
      const chunks = [];
      let bytes = 0;
      response.on("data", (chunk) => {
        bytes += chunk.length;
        if (bytes > 65536) { response.destroy(new Error("Bridge回执超过上限")); return; }
        chunks.push(chunk);
      });
      response.on("error", reject);
      response.on("end", () => {
        try {
          const receipt = JSON.parse(Buffer.concat(chunks).toString("utf8"));
          if (response.statusCode !== 202 || receipt.event_id !== event.event_id ||
            (receipt.accepted !== "memory" && receipt.accepted !== "filtered")) throw new Error("Bridge未确认接收");
          resolve();
        } catch (error) { reject(error); }
      });
    });
    req.on("error", reject);
    req.end(JSON.stringify({ ...event, process_id: process.pid }));
  });
}

// 只信任宿主会话头部的明确子代理标记；仅带 parentSession 的分叉会话仍是用户可见的主会话。
// 实测根会话头部也带 delegationDepth: 0，只有大于 0 才是子代理。
export function isSubagent(session) {
  const header = session?.header;
  if (!header) return false;
  return header.origin === "subagent" || (typeof header.delegationDepth === "number" && header.delegationDepth > 0);
}

// 只取可见文本块，不取 reasoning、工具调用块或历史消息。
export function textOf(message) {
  const content = Array.isArray(message?.content) ? message.content : [];
  return content.filter((block) => block && block.type === "text" && typeof block.text === "string").map((block) => block.text).join("\n").trim();
}

// 当前 turn/start 之前没有更早的 turn/start 即为新会话；无法读取日志时不把会话误判为新会话。
export function hasEarlierTurnStart(session, seq) {
  const events = typeof session?.snapshotEvents === "function" ? session.snapshotEvents(0, seq) : null;
  if (!Array.isArray(events)) return true;
  return events.some((event) => event?.type === "turn/start" && (event.seq === undefined || event.seq < seq));
}

function eventID(source, sessionId, kind, seq, salt) {
  return createHash("sha256").update(JSON.stringify([source, AGENT, sessionId, kind, seq !== undefined ? "seq" : "rt", seq ?? salt])).digest("hex");
}

export function buildEvent(source, sessionId, cwd, kind, { body = "", seq, time, salt, sessionTitle = "" } = {}) {
  if (!(kind in labels)) throw new Error("不支持的事件类型");
  const event = {
    event_id: eventID(source, sessionId, kind, seq, salt),
    source_id: source,
    agent: AGENT,
    session_id: sessionId,
    scope: "main",
    kind,
    occurred_at: new Date(time ?? Date.now()).toISOString(),
    title: `${labels[kind]}：${sessionTitle || "DeepSeek Harness"}`.slice(0, 240),
    session_title: String(sessionTitle ?? "").slice(0, 240),
    body: visibleBody(body),
    project: cwd ?? "",
  };
  if (seq !== undefined && seq !== null) event.sequence = seq;
  return event;
}

const cancelLabels = { user: "用户取消", parent: "父代理取消", hook: "钩子要求停止", disposed: "宿主释放" };

// turn/end 是持久化提交后的终态；completed 附带本轮最后一条助手消息的可见文本。
export function turnEnd(source, session, event, finalText = "", sessionTitle = "") {
  const { turn, reason } = event.data ?? {};
  const base = { seq: event.seq, time: event.time, sessionTitle };
  const cwd = session?.header?.cwd ?? "";
  const id = String(session.id);
  if (reason?.kind === "completed") {
    return buildEvent(source, id, cwd, "turn.completed", { ...base, body: finalText || "本轮完成；原生正文未取得，请在 DSH 中查看。" });
  }
  if (reason?.kind === "error") {
    return buildEvent(source, id, cwd, "run.failed", { ...base, body: `失败原因：${reason.error?.message || reason.error?.code || "模型请求失败"}` });
  }
  if (reason?.kind === "aborted" || reason?.kind === "interrupted") {
    const cause = reason.reason?.kind;
    const body = cause ? cancelLabels[cause] ?? `本轮已中止（${cause}）` : "本轮已中止";
    return buildEvent(source, id, cwd, "run.interrupted", { ...base, body });
  }
  // blocked：在任何步骤前关闭（输入被拒绝、为空或取消）；不当作完成提醒。
  if (reason?.kind === "blocked") {
    return buildEvent(source, id, cwd, "run.interrupted", { ...base, body: "本轮未执行任何步骤（输入被拒绝、为空或已取消）" });
  }
  if (reason?.kind === "max-tokens") {
    return buildEvent(source, id, cwd, "turn.completed", { ...base, body: (finalText ? finalText + "\n\n" : "") + "已达最大输出长度，回答可能不完整。" });
  }
  // forked：会话被分叉，由分叉后的会话继续报告；不生成终态。
  return undefined;
}

export function turnStart(source, session, event, kind, sessionTitle = "") {
  return buildEvent(source, String(session.id), session?.header?.cwd ?? "", kind, { seq: event.seq, time: event.time, sessionTitle });
}

// 等待结束后由触发它的持久事件定位，携带同一序号，便于 Hub 按会话内顺序处理。
export function resumed(source, session, event, sessionTitle = "") {
  return buildEvent(source, String(session.id), session?.header?.cwd ?? "", "activity.observed", { seq: event.seq, time: event.time, sessionTitle, body: "等待已结束，继续处理" });
}

// 轮次内心跳：turn/start 是唯一的"进入运行"信号，长轮次里一旦丢失，Hub 会停在 idle 直到 turn/end。
// 每个步骤节流一条带当前序号的运行活动，使状态能自愈，也不在长轮次里刷屏。
export const HEARTBEAT_INTERVAL_MS = 60_000;

export function heartbeat(source, session, event, sessionTitle = "") {
  return buildEvent(source, String(session.id), session?.header?.cwd ?? "", "activity.observed", { seq: event.seq, time: event.time, sessionTitle, body: "正在处理" });
}

// 工具失败只作普通记录；reason 是宿主提供的用户可见原因，不复制工具响应正文。
export function toolFailed(source, session, event, toolName = "", sessionTitle = "") {
  const { error, message } = event.data ?? {};
  if (!error && !message?.isError) return undefined;
  const reason = error?.reason || error?.name || error?.code || "工具返回错误";
  const name = toolName || "工具";
  return buildEvent(source, String(session.id), session?.header?.cwd ?? "", "tool.failed", {
    seq: event.seq, time: event.time, body: `工具 ${name} 失败：${reason}`, sessionTitle,
  });
}

export function renamed(source, session, event) {
  const title = event.data?.title;
  if (typeof title !== "string") return undefined;
  return buildEvent(source, String(session.id), session?.header?.cwd ?? "", "session.renamed", {
    seq: event.seq, time: event.time, body: "会话名称已更新", sessionTitle: title,
  });
}

export function archived(source, sessionId, cwd, sessionTitle = "", time = Date.now()) {
  return buildEvent(source, String(sessionId), cwd ?? "", "session.closed", { time, sessionTitle });
}

export function localized(text) {
  if (typeof text === "string") return text;
  if (!text || typeof text !== "object") return "";
  return text.zh || text.en || Object.values(text).find((value) => typeof value === "string") || "";
}

// 批准只采集工具名、宿主展示原因；不复制参数、命令或资源列表。
export function approval(source, agent, req, sessionTitle = "", time = Date.now()) {
  const subject = req?.subject?.name;
  const lines = [`需要批准：${req?.toolName || subject || "操作"}`];
  const reason = localized(req?.displayReason) || req?.reason;
  if (reason) lines.push(String(reason));
  return buildEvent(source, String(agent.id), agent.session?.header?.cwd ?? "", "approval.required", {
    time, salt: req?.callId ?? `approval:${req?.toolName}:${time}`, body: lines.join("\n"), sessionTitle,
  });
}

// 问题只提取 question/detail/header、选项标签与说明；不传用户答案。带 approval 意图的问题按等待批准处理。
export function questions(source, agent, request, sessionTitle = "", time = Date.now()) {
  const items = Array.isArray(request?.questions) ? request.questions : [];
  const cwd = agent.session?.header?.cwd ?? "";
  const salt = request?.wait?.callId ?? items.map((item) => item?.intent?.callId).find(Boolean) ?? `questions:${time}`;
  const approvalItem = items.find((item) => item?.intent?.kind === "approval");
  if (approvalItem) {
    const intent = approvalItem.intent;
    const lines = [`需要批准：${intent.subject?.name || approvalItem.header || "操作"}`];
    const text = [localized(intent.display?.question), localized(intent.display?.detail), approvalItem.detail].filter(Boolean).join("\n");
    if (text) lines.push(text);
    return buildEvent(source, String(agent.id), cwd, "approval.required", { time, salt, body: lines.join("\n"), sessionTitle });
  }
  const blocks = items.map((item) => {
    const lines = [];
    if (item?.question) lines.push((item.header ? `${item.header}：` : "") + item.question);
    else if (item?.header) lines.push(item.header);
    if (item?.detail) lines.push(item.detail);
    if (Array.isArray(item?.options)) {
      item.options.forEach((option, index) => lines.push(`${index + 1}. ${option?.label ?? ""}${option?.description ? " — " + option.description : ""}`));
    }
    if (item?.multiSelect && lines.length) lines.push("（可多选）");
    return lines.join("\n");
  }).filter(Boolean);
  if (!blocks.length) return undefined;
  return buildEvent(source, String(agent.id), cwd, "input.required", { time, salt, body: blocks.join("\n\n"), sessionTitle });
}

// 每个会话有界缓存：本轮最后一条助手文本、工具名映射、等待状态与标题；不缓存用户输入、思考或历史。
export class SessionTracker {
  constructor(limit = 256) {
    this.limit = limit;
    this.sessions = new Map();
  }
  get(id) {
    let state = this.sessions.get(id);
    if (!state) {
      state = { seen: false, waiting: false, title: "", turns: new Map(), tools: new Map(), pending: new Map() };
      this.sessions.set(id, state);
      if (this.sessions.size > this.limit) this.sessions.delete(this.sessions.keys().next().value);
    }
    return state;
  }
  observe(id, event) {
    const state = this.get(id);
    const data = event.data ?? {};
    if (event.type === "tool/call") {
      if (data.callId && data.name) {
        state.tools.set(String(data.callId), String(data.name));
        if (state.tools.size > 256) state.tools.delete(state.tools.keys().next().value);
      }
    } else if (event.type === "assistant/message" && !data.interrupted) {
      const text = textOf(data.message);
      if (text) {
        const current = state.turns.get(data.turn);
        // 只保留本轮最高步骤的助手文本，即本轮最后一条可见回答。
        if (!current || data.step >= current.step) state.turns.set(data.turn, { step: data.step, text });
        if (state.turns.size > 16) state.turns.delete(state.turns.keys().next().value);
      }
    }
  }
  finalText(id, turn) {
    const state = this.get(id);
    const current = state.turns.get(turn);
    state.turns.delete(turn);
    return current?.text ?? "";
  }
  toolName(id, callId) {
    return callId ? this.get(id).tools.get(String(callId)) ?? "" : "";
  }
  // 同一等待可能经 approval/request 与 user-questions/request 两条通路到达；短时间内按 callId 或主题去重。
  claim(id, key, now = Date.now()) {
    if (!key) return true;
    const pending = this.get(id).pending;
    const last = pending.get(key);
    if (last !== undefined && now - last < 5000) return false;
    pending.set(key, now);
    if (pending.size > 32) pending.delete(pending.keys().next().value);
    return true;
  }
}
