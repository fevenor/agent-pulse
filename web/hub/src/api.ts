export interface Session { source_id: string; agent: string; session_id: string; title: string; state: string; project?: string; updated_at: string; alias?: string; archived?: boolean; override_state?: string; override_at?: string }
export interface Event { event_id: string; source_id: string; agent: string; session_id: string; kind: string; title: string; body: string; project?: string; occurred_at: string; position: number; valid: boolean; read: boolean }
export interface Device { id: string; role: string; host_name?: string; path: string; locked?: boolean; fullscreen?: boolean; last_seen: string }
export interface SourceLabel { name: string; environment: string; archived?: boolean }
export interface Snapshot { run_id: string; version: number; sessions: Session[]; unread: Event[]; devices: Device[]; sources?: Record<string, SourceLabel> }
export interface Adapter { id: string; target: string; installed: string; running: string; agent_version: string; error?: string }
export interface Host { source_id: string; items: Adapter[]; error?: string; updated_at: string }
export interface HostInfo { source_id: string; name: string; default_name?: string; environment: string; archived: boolean; last_seen?: string; online: boolean; role?: string }
export interface Push { event_id: string; channel: string; status: string; attempts: number; next_at: string }

let token = "";
export function setToken(value: string) { token = value.trim(); }
export function socketProtocols(): string[] { return token ? ["agent-pulse", "pulse-token." + btoa(unescape(encodeURIComponent(token))).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "")] : []; }
export async function api<T>(path: string, body?: unknown, method?: string): Promise<T> {
  const response = await fetch("/api/v1/" + path, {
    method: method ?? (body === undefined ? "GET" : "POST"),
    headers: { ...(token ? { Authorization: "Bearer " + token } : {}), ...(body === undefined ? {} : { "Content-Type": "application/json" }) },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(8000), cache: "no-store",
  });
  if (!response.ok) throw new Error(response.status === 401 ? "需要有效的共享令牌" : `读取失败（HTTP ${response.status}），请检查 Hub 连接`);
  return response.json() as Promise<T>;
}
