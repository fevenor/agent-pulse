import { test, after } from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { apply } from "../index.js";
import { HEARTBEAT_INTERVAL_MS } from "../adapter.js";

// 每个用例使用独立会话；Unix 套接字接收真实 HTTP 递交，不调用宿主或模型。
const dir = mkdtempSync(join(tmpdir(), "pulse-dsh-"));
const socket = join(dir, "bridge.sock"), received = [];
const server = createServer((req, res) => {
  const chunks = [];
  req.on("data", (chunk) => chunks.push(chunk));
  req.on("end", () => {
    const body = JSON.parse(Buffer.concat(chunks));
    received.push(body);
    res.writeHead(202, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ event_id: body.event_id, accepted: "memory" }));
  });
});
await new Promise((resolve) => server.listen(socket, resolve));
after(async () => { server.closeAllConnections(); await new Promise((resolve) => server.close(resolve)); rmSync(dir, { recursive: true, force: true }); });

const pause = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
async function until(id, count) {
  for (let i = 0; i < 400; i++) {
    const events = received.filter((e) => e.session_id === id);
    if (events.length >= count) return events;
    await pause(5);
  }
  assert.fail(`会话 ${id} 未收到 ${count} 条事件`);
}
function pending() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
function fakeCtx(title = "项目标题") {
  const listeners = new Map(), effects = [];
  return {
    listeners, effects,
    on(name, fn, options) {
      if (!listeners.has(name)) listeners.set(name, []);
      if (options?.prepend) listeners.get(name).unshift(fn); else listeners.get(name).push(fn);
    },
    get: (name) => name === "sessionTitle" ? { get: () => ({ title }) } : undefined,
    effect: (fn) => effects.push(fn()),
  };
}
const main = (id, events = []) => ({ id, header: { cwd: "/work/project", delegationDepth: 0 }, snapshotEvents: (from, to) => events.filter((e) => e.seq >= from && e.seq < to) });
const ev = (type, seq, data = {}) => ({ type, seq, time: 1700000000000 + seq, data });
const config = { source_id: "host-a", bridge_address: `unix:${socket}` };
function fixture(t, id) {
  const ctx = fakeCtx(), session = main(id), agent = { id, session };
  apply(ctx, config);
  t.after(() => ctx.effects.forEach((dispose) => dispose()));
  return { ctx, session, agent, on: (name) => ctx.listeners.get(name)[0] };
}

test("config 无效时不注册监听器", () => {
  const ctx = fakeCtx();
  apply(ctx, { source_id: "", bridge_address: config.bridge_address });
  apply(ctx, { source_id: "host-a", bridge_address: "http://127.0.0.1:1" });
  assert.equal(ctx.listeners.size, 0);
});

test("waterfall prepend 先观察，原样返回答案；真实结束后报告恢复", async (t) => {
  const ctx = fakeCtx(), id = "waterfall", session = main(id);
  ctx.on("user-questions/request", async () => ({ answers: [{ id: "q1", selected: ["ok"] }] }));
  apply(ctx, config); t.after(() => ctx.effects.forEach((dispose) => dispose()));
  const chain = ctx.listeners.get("user-questions/request");
  const dispatch = (i, req) => chain[i](req, () => dispatch(i + 1, req));
  const answer = await dispatch(0, { agent: { id, session }, questions: [{ id: "q1", question: "继续吗？", options: [{ label: "ok" }] }] });
  assert.deepEqual(answer, { answers: [{ id: "q1", selected: ["ok"] }] });
  const events = await until(id, 2);
  assert.deepEqual(events.map((e) => e.kind), ["input.required", "activity.observed"]);
  assert.equal(events[0].body, "继续吗？\n1. ok");
});

test("多个未决等待：其他工具与用户输入不解除，最后一个请求完成才恢复", async (t) => {
  const id = "parallel-waits", { agent, session, on } = fixture(t, id);
  const first = pending(), second = pending();
  const one = on("approval/request")({ agent, callId: "a", toolName: "read" }, () => first.promise);
  const two = on("user-questions/request")({ agent, wait: { callId: "b" }, questions: [{ question: "选择？" }] }, () => second.promise);
  await until(id, 2);
  on("session/event")(session, ev("tool/result", 3, { message: { toolCallId: "unrelated", isError: false } }));
  on("session/event")(session, ev("user/message", 4));
  on("session/event")(session, ev("step/start", 5));
  // 同会话的改名是递交队列屏障，确认前面的无关事件没有恢复或心跳。
  on("session/event")(session, ev("session/title", 6, { title: "仍在等待" }));
  let events = await until(id, 3);
  assert.deepEqual(events.map((e) => e.kind), ["approval.required", "input.required", "session.renamed"]);
  first.resolve("allowed-once"); assert.equal(await one, "allowed-once");
  on("session/event")(session, ev("session/title", 7, { title: "还有一个等待" }));
  events = await until(id, 4); assert.equal(events[3].kind, "session.renamed");
  second.resolve({ answers: [] }); await two;
  events = await until(id, 5);
  assert.equal(events[4].kind, "activity.observed");
  assert.equal(events[4].body, "等待已结束，继续处理");
});

test("同 callId 的嵌套批准与提问只提醒一次，不靠五秒窗口去重", async (t) => {
  const id = "nested-waits", { agent, on } = fixture(t, id), answer = pending();
  const result = on("approval/request")({ agent, callId: "c1", toolName: "bash" }, () =>
    on("user-questions/request")({ agent, questions: [{ question: "允许？", intent: { kind: "approval", callId: "c1" } }] }, () => answer.promise));
  assert.equal((await until(id, 1))[0].kind, "approval.required");
  answer.resolve("allowed-once"); assert.equal(await result, "allowed-once");
  assert.deepEqual((await until(id, 2)).map((e) => e.kind), ["approval.required", "activity.observed"]);
});

test("终态之后迟到的等待结果不会重新报告运行", async (t) => {
  const id = "late-wait", { agent, session, on } = fixture(t, id), answer = pending();
  const result = on("approval/request")({ agent, callId: "c1", toolName: "bash" }, () => answer.promise);
  on("session/event")(session, ev("turn/end", 9, { turn: 1, reason: { kind: "aborted", reason: { kind: "user" } } }));
  answer.resolve("cancelled"); await result;
  on("session/event")(session, ev("session/title", 10, { title: "已结束" }));
  assert.deepEqual((await until(id, 3)).map((e) => e.kind), ["approval.required", "run.interrupted", "session.renamed"]);
});

test("请求取消和异常保持宿主结果，等待恢复只发送一次", async (t) => {
  const id = "cancel-wait", { agent, on } = fixture(t, id), answer = pending(), cancellation = new AbortController();
  const result = on("user-questions/request")({ agent, signal: cancellation.signal, questions: [{ question: "选择？" }] }, () => answer.promise);
  await until(id, 1); cancellation.abort();
  await until(id, 2);
  const failure = new Error("宿主取消");
  const checked = assert.rejects(result, (error) => error === failure);
  answer.reject(failure); await checked;
  assert.equal(received.filter((e) => e.session_id === id && e.kind === "activity.observed").length, 1);
});

test("长等待恢复后紧接 step/start 不重复心跳，后续仍按间隔报告", async (t) => {
  t.mock.timers.enable({ apis: ["Date"], now: Date.now() });
  const id = "heartbeat", { agent, session, on } = fixture(t, id);
  on("session/event")(session, ev("turn/start", 1, { turn: 1 }));
  on("session/event")(session, ev("step/start", 2));
  const answer = pending();
  const result = on("approval/request")({ agent, callId: "c1", toolName: "bash" }, () => answer.promise);
  await until(id, 2);
  t.mock.timers.tick(HEARTBEAT_INTERVAL_MS * 2);
  answer.resolve("allowed-once"); await result;
  on("session/event")(session, ev("step/start", 3));
  on("session/event")(session, ev("session/title", 4, { title: "恢复" }));
  const events = await until(id, 4);
  assert.deepEqual(events.map((e) => e.kind), ["session.started", "approval.required", "activity.observed", "session.renamed"]);
  t.mock.timers.tick(HEARTBEAT_INTERVAL_MS);
  on("session/event")(session, ev("step/start", 5));
  const heartbeat = (await until(id, 5))[4];
  assert.equal(heartbeat.sequence, 5); assert.equal(heartbeat.body, "正在处理");
  assert.equal(new Set(received.filter((e) => e.session_id === id).map((e) => e.event_id)).size, 5);
});

test("持久事件、最终正文、工具名、重复归档和子代理过滤", async (t) => {
  const id = "lifecycle", { session, on } = fixture(t, id);
  const child = { id: "ignored-child", header: { origin: "subagent", delegationDepth: 1 } };
  on("session/event")(child, ev("turn/start", 1));
  await on("approval/request")({ agent: { id: child.id, session: child } }, () => "allowed-once");
  await on("user-questions/request")({ agent: { id: child.id, session: child }, questions: [{ question: "?" }] }, () => undefined);
  on("session/event")(session, ev("turn/start", 1, { turn: 1 }));
  on("session/event")(session, ev("tool/call", 2, { callId: "c1", name: "bash" }));
  on("session/event")(session, ev("tool/result", 3, { error: { reason: "exit 1" }, message: { isError: true, toolCallId: "c1" } }));
  on("session/event")(session, ev("assistant/message", 4, { turn: 1, step: 1, message: { content: [{ type: "text", text: "最终回答" }] } }));
  on("session/event")(session, ev("turn/end", 5, { turn: 1, reason: { kind: "completed" } }));
  on("workspace/session-archived")({ sessionId: id });
  session.snapshotEvents = () => [ev("turn/start", 1)];
  on("session/event")(session, ev("turn/start", 6, { turn: 2 }));
  on("workspace/session-archived")({ sessionId: id });
  on("workspace/session-archived")({ sessionId: "unknown" });
  const events = await until(id, 6);
  assert.deepEqual(events.map((e) => e.kind), ["session.started", "tool.failed", "turn.completed", "session.closed", "activity.observed", "session.closed"]);
  assert.equal(events[0].source_id, config.source_id); assert.equal(events[0].process_id, process.pid);
  assert.equal(events[0].project, "/work/project"); assert.equal(events[0].session_title, "项目标题");
  assert.equal(events[1].body, "工具 bash 失败：exit 1"); assert.equal(events[2].body, "最终回答");
  assert.notEqual(events[3].event_id, events[5].event_id);
  assert.equal(received.some((e) => e.session_id === child.id || e.session_id === "unknown"), false);
});
