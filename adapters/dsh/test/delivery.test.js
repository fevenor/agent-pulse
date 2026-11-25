import { test } from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createSender } from "../delivery.js";

const pause = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
async function until(check) {
  for (let i = 0; i < 400; i++) { if (check()) return; await pause(5); }
  assert.fail("递交未在期限内完成");
}
async function fixture(t, accept) {
  const dir = mkdtempSync(join(tmpdir(), "pulse-delivery-")), socket = join(dir, "s");
  const errors = [], controller = new AbortController();
  const server = createServer((req, res) => {
    let body = ""; req.on("data", (chunk) => { body += chunk; });
    req.on("end", () => accept(JSON.parse(body), res));
  });
  await new Promise((resolve) => server.listen(socket, resolve));
  t.after(async () => { controller.abort(); server.closeAllConnections(); await new Promise((resolve) => server.close(resolve)); rmSync(dir, { recursive: true, force: true }); });
  return { send: createSender(socket, controller.signal, (error) => errors.push(error)), errors, controller };
}
const event = (id, session = "main") => ({ event_id: id, session_id: session, body: "完整且不变的正文" });
function confirm(res, e) { res.writeHead(202); res.end(JSON.stringify({ event_id: e.event_id, accepted: "memory" })); }

test("同会话等待确认后才发送下一条，其他会话不受阻塞", async (t) => {
  const requests = [], accepted = []; let release;
  const { send, errors } = await fixture(t, (e, res) => {
    requests.push(e.event_id);
    const done = () => { accepted.push(e.event_id); confirm(res, e); };
    if (e.event_id === "wait") release = done; else done();
  });
  send(event("wait")); send(event("end")); send(event("other", "other-session"));
  await until(() => release && accepted.includes("other"));
  assert.equal(requests.includes("end"), false);
  release(); await until(() => accepted.includes("end"));
  assert.deepEqual(accepted.filter((id) => id !== "other"), ["wait", "end"]);
  assert.deepEqual(errors, []);
});

test("503 后保持 ID、时间及正文重试成功，再发送同会话终态", async (t) => {
  const requests = [];
  const { send, errors } = await fixture(t, (e, res) => {
    requests.push(e);
    if (requests.length === 1) { res.writeHead(503); res.end('{"error":"队列暂满"}'); }
    else confirm(res, e);
  });
  send({ ...event("wait"), occurred_at: "2026-01-01T00:00:00Z" }); send(event("end"));
  await until(() => requests.length === 3);
  assert.deepEqual(requests[0], requests[1]);
  assert.deepEqual(requests.map((e) => e.event_id), ["wait", "wait", "end"]);
  assert.deepEqual(errors, []);
});

test("永久拒绝不反复发送，瞬时故障最多三次，不拖住后续事件", async (t) => {
  const requests = [];
  const { send, errors } = await fixture(t, (e, res) => {
    requests.push(e.event_id);
    if (e.event_id === "bad") { res.writeHead(400); res.end('{"error":"无效事件"}'); }
    else if (e.event_id === "offline") { res.writeHead(503); res.end('{"error":"暂不可用"}'); }
    else confirm(res, e);
  });
  send(event("bad")); send(event("offline")); send(event("end"));
  await until(() => requests.includes("end"));
  assert.deepEqual(requests, ["bad", "offline", "offline", "offline", "end"]);
  assert.equal(errors.length, 2);
});

test("断连重试复用载荷，插件卸载中止在途与排队递交", async (t) => {
  const requests = [];
  const { send, errors, controller } = await fixture(t, (e, res) => {
    requests.push(e);
    if (requests.length === 1) res.destroy();
    else if (e.event_id === "hold") { /* 等待卸载取消 */ }
    else confirm(res, e);
  });
  send(event("first")); send(event("hold")); send(event("queued"));
  await until(() => requests.some((e) => e.event_id === "hold"));
  assert.deepEqual(requests[0], requests[1]);
  controller.abort(); send(event("after-dispose"));
  await pause(30);
  assert.equal(requests.some((e) => ["queued", "after-dispose"].includes(e.event_id)), false);
  assert.deepEqual(errors, []);
});

test("接收端停滞时队列按事件数和字节限制内存", async (t) => {
  const { send, errors } = await fixture(t, () => {});
  for (let i = 0; i < 257; i++) send(event(String(i)));
  assert.equal(errors.length, 1);
  const other = await fixture(t, () => {});
  for (let i = 0; i < 80; i++) other.send({ ...event(String(i)), body: "x".repeat(32768) });
  assert.ok(other.errors.length > 0);
  assert.match(other.errors[0].message, /队列已满/);
});

test("200ms 超时后重试，后续终态仍在等待事件之后递交", async (t) => {
  const requests = []; let slowResponse;
  const { send, errors } = await fixture(t, (e, res) => {
    requests.push(e.event_id);
    if (requests.length === 1) slowResponse = res;
    else confirm(res, e);
  });
  send(event("wait")); send(event("end"));
  await until(() => requests.includes("end"));
  assert.deepEqual(requests, ["wait", "wait", "end"]);
  assert.deepEqual(errors, []);
  slowResponse.destroy();
});
