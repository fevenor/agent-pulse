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
