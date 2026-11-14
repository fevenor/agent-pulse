import assert from "node:assert/strict";
import test from "node:test";
import { bridgeURL, convert, type NativeEvent, type SessionInfo } from "../src/adapter.ts";

const session = { id: "main", title: "检查任务", location: { directory: "/project" } } as SessionInfo;
const completed = {
  id: "native-event", created: 1794220800000,
  type: "session.execution.succeeded", data: { sessionID: "main" },
  durable: { aggregateID: "main", seq: 12, version: 1 },
} as NativeEvent;

test("稳定 ID、来源隔离和完成语义", () => {
  const one = convert(completed, session, "linux")!;
  assert.equal(one.kind, "turn.completed");
  assert.equal(one.sequence, 12);
  assert.equal(one.session_title, "检查任务");
  assert.notEqual(one.title, one.session_title);
  assert.deepEqual(one, convert(completed, session, "linux"));
  assert.notEqual(one.event_id, convert(completed, session, "windows")!.event_id);
});

test("子代理、缺少会话和归属不匹配都忽略", () => {
  assert.equal(convert(completed, { ...session, parentID: "parent" }, "linux"), undefined);
  assert.equal(convert(completed, undefined, "linux"), undefined);
  assert.equal(convert(completed, { ...session, id: "other" }, "linux"), undefined);
});

test("工具错误不伪装成主会话失败，空闲状态不伪装成完成", () => {
  for (const type of ["session.tool.failed", "session.idle", "session.status"]) {
    assert.equal(convert({ ...completed, type } as NativeEvent, session, "linux"), undefined);
  }
});

test("批准和表单等待有独立语义", () => {
  const permission = { id: "p", created: 1794220800000, type: "permission.asked", data: { id: "p", sessionID: "main", action: "shell", resources: ["secret"] } } as NativeEvent;
  const e = convert(permission, session, "linux")!;
  assert.equal(e.kind, "approval.required");
  assert.equal(e.sequence, undefined);
  assert.ok(!e.body.includes("secret"));
  const form = { id: "f", created: 1794220800000, type: "form.created", data: { form: { id: "f", sessionID: "main", title: "选择方案", fields: [] } } } as unknown as NativeEvent;
  assert.equal(convert(form, session, "linux")!.kind, "input.required");
});

test("本地地址限制", () => {
  assert.equal(bridgeURL(undefined), "http://127.0.0.1:18702/api/v1/events");
  for (const url of ["https://127.0.0.1", "http://remote", "http://localhost", "http://user:secret@127.0.0.1", "http://127.0.0.1/path"]) {
    assert.throws(() => bridgeURL(url));
  }
});

test("原生改名保持会话 ID，生成不改变运行状态的名称事件", () => {
  const renamed = { ...completed, id: "rename-event", type: "session.renamed", data: { sessionID: "main", title: "新会话名" }, durable: { aggregateID: "main", seq: 13, version: 1 } } as NativeEvent;
  const e = convert(renamed, { ...session, title: "新会话名" }, "linux")!;
  assert.equal(e.kind, "session.renamed");
  assert.equal(e.session_id, "main");
  assert.equal(e.session_title, "新会话名");
  assert.equal(e.sequence, 13);
  assert.deepEqual(e, convert(renamed, { ...session, title: "稍后再次改名" }, "linux"));
  assert.equal(convert(renamed, { ...session, parentID: "parent" }, "linux"), undefined);
});

test("本机进程信息仅在发送边界附加，过滤确认不是发送失败", async () => {
  const { emit } = await import("../src/adapter.ts");
  const original = globalThis.fetch;
  const e = convert(completed, session, "linux")!;
  let posted: Record<string, unknown> | undefined;
  globalThis.fetch = (async (_url, options) => {
    posted = JSON.parse(String(options?.body));
    return new Response(JSON.stringify({ event_id: e.event_id, accepted: "filtered" }), { status: 202 });
  }) as typeof fetch;
  try {
    await emit("http://127.0.0.1/api/v1/events", e);
    assert.equal(posted?.process_id, process.pid);
    assert.equal("process_id" in e, false, "转换后的公共事件不能被发送过程改写");
  } finally { globalThis.fetch = original; }
});
