import { request } from "node:http";
import { isAbsolute, normalize } from "node:path";
import { createHash } from "node:crypto";
import type { Plugin } from "@opencode/plugin";

type Stream = ReturnType<Plugin.Context["event"]["subscribe"]>;
export type NativeEvent = Stream extends AsyncIterable<infer E> ? E : never;
export type SessionInfo = Awaited<ReturnType<Plugin.Context["session"]["get"]>>;

export type PulseEvent = {
  event_id: string;
  source_id: string;
  agent: "opencode";
  session_id: string;
  scope: "main";
  kind: string;
  occurred_at: string;
  title: string;
  session_title?: string;
  body: string;
  project: string;
  sequence?: number;
};

export const eventTypes = [
  "session.created", "session.deleted", "session.renamed", "session.execution.started",
  "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted",
  "session.text.ended", "session.tool.failed", "session.step.failed", "session.compaction.failed",
  "session.retry.scheduled", "session.status",
  "permission.asked", "permission.replied", "form.created", "form.replied", "form.cancelled",
] satisfies NativeEvent["type"][];

const kinds: Record<string, [string, string]> = {
  "session.renamed": ["session.renamed", "会话名称已更新"],
  "session.created": ["session.started", "会话已创建"],
  "session.deleted": ["session.closed", "会话已删除"],
  "session.execution.started": ["session.resumed", "开始处理"],
  "session.execution.succeeded": ["turn.completed", "本轮处理完成"],
  "session.execution.failed": ["run.failed", "本轮处理失败"],
  "session.execution.interrupted": ["run.interrupted", "本轮处理已中断"],
  "permission.asked": ["approval.required", "等待批准"],
  "permission.replied": ["activity.observed", "批准请求已处理"],
  "form.created": ["input.required", "等待输入"],
  "form.replied": ["activity.observed", "输入已提交"],
  "form.cancelled": ["activity.observed", "输入请求已取消"],
  "session.tool.failed": ["tool.failed", "工具执行失败"],
  "session.step.failed": ["tool.failed", "执行步骤失败"],
  "session.compaction.failed": ["tool.failed", "上下文整理失败"],
  "session.retry.scheduled": ["tool.failed", "模型请求重试"],
  "session.status": ["tool.failed", "执行提示"],
};

const BODY_LIMIT = 32 << 10;
export function visibleBody(value: string, limit = BODY_LIMIT): string {
  const text = value.trim();
  if (Buffer.byteLength(text) <= limit && Buffer.byteLength(JSON.stringify(text)) <= 56 << 10) return text;
  const suffix = "\n\n[内容超过事件长度上限，已截断]";
  let prefix = Buffer.from(text).subarray(0, limit - Buffer.byteLength(suffix));
  // 不拆开 UTF-8 多字节字符。
  while (prefix.length && (prefix[prefix.length - 1]! & 0xc0) === 0x80) prefix = prefix.subarray(0, prefix.length - 1);
  while (prefix.length && Buffer.byteLength(prefix.toString()) > prefix.length) prefix = prefix.subarray(0, prefix.length - 1);
  while (Buffer.byteLength(JSON.stringify(prefix.toString()+suffix)) > 56 << 10) {
    prefix = prefix.subarray(0, Math.max(0, Math.floor(prefix.length * 0.9)));
    while (prefix.length && Buffer.byteLength(prefix.toString()) > prefix.length) prefix=prefix.subarray(0,prefix.length-1);
  }
  return prefix.toString() + suffix;
}

// 只缓存原生 text.ended 的最后一个助手消息，不缓存 reasoning、用户输入或历史对话。
export class FinalOutput {
  private readonly sessions = new Map<string, { message: string; parts: Map<number, string>; truncated: boolean }>();
  observe(event: NativeEvent): void {
    const id = sessionID(event);
    if (!id) return;
    if (event.type === "session.execution.started" || event.type === "session.deleted") { this.sessions.delete(id); return; }
    if (event.type !== "session.text.ended") return;
    const { assistantMessageID, ordinal, text } = event.data;
    let current = this.sessions.get(id);
    if (!current || current.message !== assistantMessageID) current = { message: assistantMessageID, parts: new Map(), truncated: false };
    if (current.parts.size >= 64 && !current.parts.has(ordinal)) { current.truncated = true; return; }
        const used = [...current.parts].filter(([key]) => key !== ordinal).reduce((sum,[,part]) => sum + Buffer.byteLength(part) + 1, 0);
    if (BODY_LIMIT - used < 100) { current.truncated = true; return; }
    current.parts.set(ordinal, visibleBody(text, BODY_LIMIT - used));
    this.sessions.set(id, current);
    if (this.sessions.size > 128) this.sessions.delete(this.sessions.keys().next().value!);
  }
  take(id: string): string {
    const current = this.sessions.get(id);this.sessions.delete(id);
    if (!current) return "";
    let text = [...current.parts].sort(([a],[b])=>a-b).map(([,part])=>part).join("\n");
    if (current.truncated && !text.includes("已截断")) text += "\n\n[内容超过事件长度上限，已截断]";
    return visibleBody(text);
  }
}

function formBody(form: Extract<NativeEvent,{type:"form.created"}>["data"]["form"]): string {
  const lines = [form.title];
  const defaults = new Map(form.fields.filter(f => "default" in f && f.default !== undefined).map(f => [f.key, "default" in f ? f.default : undefined]));
  let unknownCondition = false;
  for (const field of form.fields) {
    // 无当前表单答案，不能把依赖条件的隐藏/未来字段当作当前正在问的问题。
    if ("hidden" in field && field.hidden) continue;
    if ("when" in field && field.when?.length) {
      if (field.when.some(c=>!defaults.has(c.key))) { unknownCondition=true; continue; }
      if (!field.when.every(c=>c.op==="eq" ? defaults.get(c.key)===c.value : defaults.get(c.key)!==c.value)) continue;
    }
    lines.push((field.title || field.key) + (field.type === "multiselect" ? "（可多选）" : ""));
    if (field.description) lines.push(field.description);
    if ("options" in field && field.options) {
      for (const [index, option] of field.options.entries()) lines.push(`${index + 1}. ${option.label}${option.description ? " — " + option.description : ""}`);
    }
  }
  if (unknownCondition) lines.push("部分条件字段需在Agent中查看。");
  return visibleBody(lines.filter(Boolean).join("\n"));
}

export function sessionID(event: NativeEvent): string | undefined {
  if (event.type === "form.created") return event.data.form.sessionID;
  if ("data" in event && event.data && "sessionID" in event.data && typeof event.data.sessionID === "string") return event.data.sessionID;
  return undefined;
}

// 仅信任宿主提供的完整会话信息，不通过标题、日志文字猜测主/子关系。
export function convert(event: NativeEvent, session: SessionInfo | undefined, source: string, finalOutput = ""): PulseEvent | undefined {
  const mapping = kinds[event.type];
  const id = sessionID(event);
  if (!mapping || !session || session.parentID || !id || session.id !== id || !("created" in event) || typeof event.created !== "number") return;
  if (event.type === "session.status" && (!event.data.status || event.data.status.type !== "retry" || !event.data.status.action)) return;
  if ((event.type === "session.execution.failed" || event.type === "session.tool.failed" || event.type === "session.step.failed" || event.type === "session.compaction.failed" || event.type === "session.retry.scheduled") && (!event.data.error || typeof event.data.error.message !== "string")) return;
  const nativeTitle = event.type === "session.renamed" ? event.data.title : session.title;
  let body = mapping[1];
  if (event.type === "session.execution.succeeded") body = finalOutput || "本轮完成；原生正文未取得，请在 Agent 中查看。";
  if (event.type === "session.execution.failed" || event.type === "session.tool.failed" || event.type === "session.step.failed" || event.type === "session.compaction.failed") body = `失败原因：${event.data.error.message || event.data.error.type}`;
  if (event.type === "session.retry.scheduled") body = `第${event.data.attempt}次重试：${event.data.error.message || event.data.error.type}`;
  if (event.type === "session.status" && event.data.status.type === "retry") body = [event.data.status.message,event.data.status.action?.title,event.data.status.action?.message,event.data.status.action?.label].filter(Boolean).join("\n");
  if (event.type === "session.execution.interrupted") body = ({user:"用户中断",shutdown:"宿主关闭",superseded:"已被新的执行替代",inactivity:"闲置中断"})[event.data.reason];
  if (event.type === "permission.replied") body = ({once:"已允许本次请求",always:"已允许后续同类请求",reject:"已拒绝请求"})[event.data.reply];
  if (event.type === "permission.asked") body = [`需要批准：${event.data.action}`, event.data.message || "", ...event.data.resources.filter(target=> /^(?:[A-Za-z]:[\\/]|\/|\.\.?\/)/.test(target) && !/[\r\n]/.test(target)).slice(0,8).map(target=>`目标：${target}`)].filter(Boolean).join("\n");
  if (event.type === "form.created") body = formBody(event.data.form);
  // 不推送原始错误、命令、权限资源列表，避免无意携带敏感上下文。
  return {
    event_id: createHash("sha256").update(JSON.stringify([source, "opencode", event.id])).digest("hex"),
    source_id: source,
    agent: "opencode",
    session_id: id,
    scope: "main",
    kind: mapping[0],
    occurred_at: new Date(event.created).toISOString(),
    title: `${mapping[1]}：${nativeTitle || "OpenCode"}`.slice(0, 240),
    session_title: (nativeTitle || "").slice(0, 240),
    body: visibleBody(body),
    project: session.location.directory ?? "",
    ...("durable" in event ? { sequence: event.durable.seq } : {}),
  };
}

export function bridgeAddress(value: unknown): string {
  if (typeof value !== "string" || value.includes("\0")) throw new Error("Bridge必须为本机IPC地址");
  if (value.startsWith("unix:")) {
    const path = value.slice(5);
    if (!isAbsolute(path) || normalize(path) !== path || Buffer.byteLength(path) > 107) throw new Error("Unix套接字地址无效");
    return path;
  }
  if (/^pipe:\\\\.\\pipe\\[A-Za-z0-9_.-]{1,200}$/.test(value)) return value.slice(5);
  throw new Error("Bridge必须为本机IPC地址");
}

export async function emit(address: string, event: PulseEvent, signal?: AbortSignal): Promise<void> {
  const timeout = AbortSignal.timeout(200);
  await new Promise<void>((resolve, reject) => {
    const req = request({socketPath: address, path: "/api/v1/events", method: "POST",
      headers: {"Host": "127.0.0.1:1", "Content-Type": "application/json"},
      signal: signal ? AbortSignal.any([timeout, signal]) : timeout}, response => {
      const chunks: Buffer[] = []; let bytes = 0;
      response.on("data", (chunk: Buffer) => {
        bytes += chunk.length;
        if (bytes > 65536) { response.destroy(new Error("Bridge回执超过上限")); return; }
        chunks.push(chunk);
      });
      response.on("error", reject);
      response.on("end", () => {
        try {
          const receipt = JSON.parse(Buffer.concat(chunks).toString("utf8")) as {event_id?: string; accepted?: string};
          if (response.statusCode !== 202 || receipt.event_id !== event.event_id ||
            (receipt.accepted !== "memory" && receipt.accepted !== "filtered")) throw new Error("Bridge未确认接收");
          resolve();
        } catch (error) { reject(error); }
      });
    });
    req.on("error", reject);
    req.end(JSON.stringify({...event, process_id: process.pid}));
  });
}
