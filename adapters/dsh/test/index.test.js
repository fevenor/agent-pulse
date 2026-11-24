import { test, after } from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { apply } from "../index.js";
import { HEARTBEAT_INTERVAL_MS } from "../adapter.js";

// 用本机 Unix 套接字模拟 Bridge 回执，验证监听器真实递交行为。
const dir = mkdtempSync(join(tmpdir(), "agent-pulse-dsh-"));
const socket = join(dir, "bridge.sock");
const received = [];
const server = createServer((req, res) => {
  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => {
    const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
    received.push({ url: req.url, host: req.headers.host, body });
    res.writeHead(202, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ event_id: body.event_id, accepted: "memory" }));
  });
});
await new Promise((resolve) => server.listen(socket, resolve));
after(() => { server.close(); rmSync(dir, { recursive: true, force: true }); });

// 每个用例在独立游标上等待，避免并行用例共享同一收到列表。
async function until(count, timeout = 3000, start = 0) {
  const deadline = Date.now() + timeout;
  while (received.length < start + count) {
    if (Date.now() > deadline) throw new Error(`只收到 ${received.length - start}/${count} 条事件`);
    await new Promise((r) => setTimeout(r, 5));
  }
  return received.slice(start);
}

function fakeCtx(title) {
  const listeners = new Map();
  const effects = [];
  return {
    listeners, effects,
    on(name, fn, options) {
      if (!listeners.has(name)) listeners.set(name, []);
      // 与 Cordis 一致：prepend 的监听器排在已有监听器之前。
      if (options?.prepend) listeners.get(name).unshift(fn); else listeners.get(name).push(fn);
    },
    get(name) { return name === "sessionTitle" ? { get: () => (title ? { title } : undefined) } : undefined; },
    effect(fn) { effects.push(fn); },
  };
}

const main = (id, events = []) => ({ id, header: { cwd: "/work/project" }, snapshotEvents: (from, to) => events.filter((e) => e.seq >= from && e.seq < to) });
const ev = (type, seq, data, time = 1_700_000_000_000 + seq) => ({ type, seq, time, data });
const config = { source_id: "host-a", bridge_address: `unix:${socket}` };

test("config 无效时保持未启用且不注册监听器", () => {
  const ctx = fakeCtx();
  apply(ctx, { source_id: "", bridge_address: `unix:${socket}` });
  apply(ctx, { source_id: "host-a", bridge_address: "http://127.0.0.1:1" });
  assert.equal(ctx.listeners.size, 0);
});

test("waterfall 观察者以 prepend 注册，先于认领请求的 answerer 看到提问", async () => {
  // 模拟 Cordis waterfall：先注册的 answerer 认领请求后不再调用 next()，后注册的监听器被否决。
  const base = received.length;
  const ctx = fakeCtx("项目标题");
  ctx.on("user-questions/request", async (request) => ({ answers: [{ id: request.questions[0].id, selected: ["ok"] }] }));
  ctx.on("approval/request", async () => "allowed-once");
  apply(ctx, config);
  const chain = ctx.listeners.get("user-questions/request");
  assert.equal(chain.length, 2);
  assert.equal(chain[0].length <= 2, true, "观察者应是双参数监听器并排在首位");
  // 真实分发：最外层监听器先运行；观察者递交后调用 next 进入 answerer。
  const dispatch = (fns, req) => {
    const run = (i) => (i >= fns.length ? undefined : fns[i](req, () => run(i + 1)));
    return run(0);
  };
  const agent = { id: "s-wait", session: main("s-wait") };
  const answer = await dispatch(chain, { agent, questions: [{ id: "q1", question: "继续吗？", options: [{ label: "ok" }] }] });
  assert.deepEqual(answer, { answers: [{ id: "q1", selected: ["ok"] }] });
  const events = await until(1, 3000, base);
  assert.equal(events[0].body.kind, "input.required");
  assert.equal(events[0].body.body, "继续吗？\n1. ok");
  const approvals = ctx.listeners.get("approval/request");
  assert.equal(approvals.length, 2);
  assert.equal(await dispatch(approvals, { agent, toolName: "bash", callId: "c1" }), "allowed-once");
  assert.equal((await until(2, 3000, base))[1].body.kind, "approval.required");
});

test("长轮次心跳：步骤活动按间隔节流发出运行活动，turn/start 重置计时", async (t) => {
  // 心跳按 Date.now() 节流；用假时钟推进，不真实等待。
  t.mock.timers.enable({ apis: ["Date"], now: Date.now() });
  const base = received.length;
  const ctx = fakeCtx("项目标题");
  apply(ctx, config);
  const [onEvent] = ctx.listeners.get("session/event");
  const s = main("s-long", [ev("turn/start", 1, { turn: 1 })]);
  // 进入轮次：一条 activity.observed（本进程首次看到该会话 → session.resumed），并重置心跳计时。
  onEvent(s, ev("turn/start", 2, { turn: 2 }));
  await until(1, 3000, base);
  // 节流窗口内的多个步骤：不发心跳。
  t.mock.timers.tick(HEARTBEAT_INTERVAL_MS - 1000);
  onEvent(s, ev("step/start", 3, { turn: 2, step: 1 }));
  onEvent(s, ev("step/start", 4, { turn: 2, step: 2 }));
  await until(1, 200, base);
  assert.equal(received.length - base, 1, "节流窗口内不应发心跳");
  // 窗口过后：发一条带当前序号的运行活动。
  t.mock.timers.tick(HEARTBEAT_INTERVAL_MS);
  onEvent(s, ev("step/start", 5, { turn: 2, step: 3 }));
  const beat = await until(2, 3000, base);
  const last = beat[1].body;
  assert.equal(last.kind, "activity.observed");
  assert.equal(last.sequence, 5);
  assert.equal(last.body, "正在处理");
  assert.equal(last.session_title, "项目标题");
  // 新轮次重置计时：紧随 turn/start 的步骤不再发心跳。
  t.mock.timers.tick(HEARTBEAT_INTERVAL_MS);
  onEvent(s, ev("turn/start", 6, { turn: 3 }));
  const after = await until(3, 3000, base);
  assert.equal(after[2].body.kind, "activity.observed");
  onEvent(s, ev("step/start", 7, { turn: 3, step: 1 }));
  await until(3, 200, base);
  assert.equal(received.length - base, 3, "turn/start 后应重置心跳计时");
  t.mock.timers.reset();
});

test("监听器过滤子代理、按日志区分新会话、等待恢复与终态递交；waterfall 返回 next()", async () => {
  const base = received.length;
  const ctx = fakeCtx("项目标题");
  apply(ctx, config);
  assert.deepEqual([...ctx.listeners.keys()].sort(), ["approval/request", "session/event", "user-questions/request", "workspace/session-archived"]);
  const [onEvent] = ctx.listeners.get("session/event");
  const [onApproval] = ctx.listeners.get("approval/request");
  const [onQuestions] = ctx.listeners.get("user-questions/request");
  const [onArchived] = ctx.listeners.get("workspace/session-archived");

  // 子代理事件全部忽略
  const child = { id: "child", header: { cwd: "/w", origin: "subagent", parentSession: "s-main", delegationDepth: 1 } };
  onEvent(child, ev("turn/start", 1, { turn: 1 }));
  onEvent(child, ev("turn/end", 2, { turn: 1, reason: { kind: "completed" } }));
  onApproval({ agent: { id: "child", session: child } }, () => {});
  onQuestions({ agent: { id: "child", session: child }, questions: [{ question: "?" }] }, () => {});

  // 新会话：首个 turn/start 之前无更早轮次
  const log = [];
  // 主会话头部带 delegationDepth: 0（实测），不能被当作子代理过滤。
  const s = main("s-main", log);
  s.header.delegationDepth = 0;
  onEvent(s, ev("turn/start", 1, { turn: 1 }));
  log.push(ev("turn/start", 1, { turn: 1 }));
  onEvent(s, ev("tool/call", 2, { callId: "c1", name: "bash", arguments: "{}" }));
  log.push(ev("tool/call", 2, { callId: "c1", name: "bash", arguments: "{}" }));
  onEvent(s, ev("assistant/message", 3, { turn: 1, step: 1, message: { content: [{ type: "text", text: "先看" }, { type: "reasoning", text: "思考" }] } }));
  log.push(ev("assistant/message", 3, { turn: 1, step: 1, message: { content: [] } }));
  onEvent(s, ev("tool/result", 4, { turn: 1, step: 2, error: { name: "E", code: "1", reason: "exit 1" }, message: { isError: true, toolCallId: "c1", source: { kind: "tool", callId: "c1" } } }));
  log.push(ev("tool/result", 4, { turn: 1, step: 2, message: {} }));
  // 只带 source.callId 的旧形状也能配对。
  onEvent(s, ev("tool/result", 5, { turn: 1, step: 2, error: { name: "E2", code: "2" }, message: { isError: true, source: { kind: "tool", callId: "c1" } } }));
  log.push(ev("tool/result", 5, { turn: 1, step: 2, message: {} }));
  onEvent(s, ev("assistant/message", 5, { turn: 1, step: 2, message: { content: [{ type: "text", text: "最终回答" }] } }));
  log.push(ev("assistant/message", 5, { turn: 1, step: 2, message: { content: [] } }));
  onEvent(s, ev("turn/end", 6, { turn: 1, reason: { kind: "completed" } }));

  // 第二轮：已观察过的会话只报告活动
  log.push(ev("turn/end", 6, { turn: 1, reason: { kind: "completed" } }));
  onEvent(s, ev("turn/start", 7, { turn: 2 }));
  log.push(ev("turn/start", 7, { turn: 2 }));
  onEvent(s, ev("session/title", 8, { title: "新标题", source: { kind: "user" } }));

  const events = await until(6, 3000, base);
  const kinds = events.map((e) => e.body.kind);
  assert.deepEqual(kinds, ["session.started", "tool.failed", "tool.failed", "turn.completed", "activity.observed", "session.renamed"]);
  assert.equal(events[0].body.title, "会话已开始：项目标题");
  assert.equal(events[0].body.sequence, 1);
  assert.equal(events[0].body.project, "/work/project");
  assert.equal(events[0].body.process_id, process.pid);
  assert.equal(events[0].url, "/api/v1/events");
  assert.equal(events[0].host, "127.0.0.1:1");
  assert.equal(events[1].body.body, "工具 bash 失败：exit 1");
  assert.equal(events[2].body.body, "工具 bash 失败：E2");
  assert.equal(events[3].body.body, "最终回答");
  assert.equal(events[4].body.sequence, 7);
  assert.equal(events[4].body.body, "");
  assert.equal(events[5].body.session_title, "新标题");
  assert.equal(received.length - base, 6, "子代理与缓存事件不应递交");

  // 审批：递交后把决定权交还；同一等待经两条通路到达时去重
  const agent = { id: "s-main", session: s };
  let delegated = 0;
  const next = async () => { delegated++; return "allowed-once"; };
  const outcome = await onApproval({ agent, toolName: "bash", callId: "c9", displayReason: { zh: "运行命令" } }, next);
  assert.equal(outcome, "allowed-once");
  assert.equal(delegated, 1);
  await onQuestions({ agent, questions: [{ question: "继续吗？", intent: { kind: "approval", callId: "c9", subject: { name: "write" } } }] }, next);
  await onQuestions({ agent, questions: [{ header: "确认", question: "继续吗？", options: [{ label: "是" }] }], wait: { callId: "c10" } }, next);
  assert.equal(delegated, 3);
  const waiting = await until(8, 3000, base);
  assert.equal(waiting[6].body.kind, "approval.required");
  assert.equal(waiting[6].body.body, "需要批准：bash\n运行命令");
  assert.equal(waiting[6].body.session_title, "新标题");
  assert.equal(waiting[7].body.kind, "input.required");
  assert.equal(waiting[7].body.sequence, undefined);
  assert.equal(waiting[7].body.session_title, "新标题");
  assert.equal(waiting[7].body.event_id.length, 64);

  // 等待中的会话再次出现持久活动时恢复运行，然后归档
  onEvent(s, ev("step/start", 20, { turn: 2, step: 1 }));
  const resumedEvents = await until(9, 3000, base);
  assert.equal(resumedEvents[8].body.kind, "activity.observed");
  assert.equal(resumedEvents[8].body.sequence, 20);
  onArchived({ sessionId: "unknown" });
  onArchived({ sessionId: "s-main" });
  const closed = await until(10, 3000, base);
  assert.equal(closed[9].body.kind, "session.closed");
  assert.equal(closed[9].body.session_title, "新标题");
  await new Promise((r) => setTimeout(r, 20));
  assert.equal(received.length - base, 10, "未观察过的会话不报告归档");

  // 卸载时中止在途递交
  assert.equal(ctx.effects.length, 1);
  const dispose = ctx.effects[0]();
  assert.equal(typeof dispose, "function");
  dispose();
});
