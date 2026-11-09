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
  body: string;
  project: string;
  sequence?: number;
};

export const eventTypes = [
  "session.created", "session.deleted", "session.execution.started",
  "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted",
  "permission.asked", "permission.replied", "form.created", "form.replied", "form.cancelled",
] satisfies NativeEvent["type"][];

const kinds: Record<string, [string, string]> = {
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
};

export function sessionID(event: NativeEvent): string | undefined {
  if (event.type === "form.created") return event.data.form.sessionID;
  if ("data" in event && event.data && "sessionID" in event.data && typeof event.data.sessionID === "string") return event.data.sessionID;
  return undefined;
}

// 仅信任宿主提供的完整会话信息，不通过标题、日志文字猜测主/子关系。
export function convert(event: NativeEvent, session: SessionInfo | undefined, source: string): PulseEvent | undefined {
  const mapping = kinds[event.type];
  const id = sessionID(event);
  if (!mapping || !session || session.parentID || !id || session.id !== id || !("created" in event) || typeof event.created !== "number") return;
  let body = mapping[1];
  if (event.type === "permission.asked") body = `需要批准：${event.data.action}`;
  if (event.type === "form.created") body = event.data.form.title;
  // 不推送原始错误、命令、权限资源列表，避免无意携带敏感上下文。
  return {
    event_id: createHash("sha256").update(JSON.stringify([source, "opencode", event.id])).digest("hex"),
    source_id: source,
    agent: "opencode",
    session_id: id,
    scope: "main",
    kind: mapping[0],
    occurred_at: new Date(event.created).toISOString(),
    title: `${mapping[1]}：${session.title || "OpenCode"}`.slice(0, 240),
    body: body.slice(0, 4000),
    project: session.location.directory ?? "",
    ...("durable" in event ? { sequence: event.durable.seq } : {}),
  };
}

export function bridgeURL(value: unknown): string {
  const url = new URL(typeof value === "string" ? value : "http://127.0.0.1:18702");
  if (url.protocol !== "http:" || !["127.0.0.1", "[::1]"].includes(url.hostname) || url.username || url.password || url.search || url.hash || url.pathname !== "/") {
    throw new Error("Bridge 必须为回环 HTTP 根地址");
  }
  return new URL("/api/v1/events", url).href;
}

export async function emit(url: string, event: PulseEvent, signal?: AbortSignal): Promise<void> {
  const timeout = AbortSignal.timeout(200);
  const response = await fetch(url, {
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify(event), signal: signal ? AbortSignal.any([timeout, signal]) : timeout,
    redirect: "error",
  });
  const receipt = await response.json() as { event_id?: string; accepted?: string };
  if (response.status !== 202 || receipt.event_id !== event.event_id || receipt.accepted !== "memory") {
    throw new Error("Bridge 未确认接收");
  }
}
