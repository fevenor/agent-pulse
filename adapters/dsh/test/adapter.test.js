import { test } from "node:test";
import assert from "node:assert/strict";
import { approval, archived, bridgeAddress, buildEvent, hasEarlierTurnStart, isSubagent, questions, renamed, resumed, SessionTracker, textOf, toolFailed, turnEnd, turnStart, visibleBody } from "../adapter.js";

const source = "host-a";
const durable = (type, seq, data, time = 1_700_000_000_000) => ({ type, seq, time, data });

test("visibleBody 截断不拆开 UTF-8 字符并明确标注", () => {
  assert.equal(visibleBody("  正文  "), "正文");
  const long = "汉".repeat(20_000);
  const cut = visibleBody(long);
  assert.ok(cut.endsWith("[内容超过事件长度上限，已截断]"));
  assert.ok(Buffer.byteLength(cut) <= 32 << 10);
  assert.equal(Buffer.from(cut.slice(0, 10)).toString(), "汉".repeat(10));
});

test("bridgeAddress 只接受本机 IPC 地址", () => {
  assert.equal(bridgeAddress("unix:/tmp/x.sock"), "/tmp/x.sock");
  assert.equal(bridgeAddress(String.raw`pipe:\\.\pipe\agent-pulse-1`), String.raw`\\.\pipe\agent-pulse-1`);
  assert.throws(() => bridgeAddress("http://127.0.0.1:1"));
  assert.throws(() => bridgeAddress("unix:relative.sock"));
});

test("isSubagent 只信任头部明确标记", () => {
  assert.equal(isSubagent({ header: { origin: "subagent", parentSession: "p" } }), true);
  assert.equal(isSubagent({ header: { delegationDepth: 1 } }), true);
  // 实测根会话头部带 delegationDepth: 0，不能被当作子代理。
  assert.equal(isSubagent({ header: { delegationDepth: 0, isSeeded: false } }), false);
  assert.equal(isSubagent({ header: { parentSession: "p", isSeeded: true } }), false);
  assert.equal(isSubagent({ header: { cwd: "/x" } }), false);
  assert.equal(isSubagent(undefined), false);
});

test("textOf 只取 text 块", () => {
  assert.equal(textOf({ content: [{ type: "text", text: "回答" }, { type: "reasoning", text: "思考" }, { type: "tool-call", name: "bash" }] }), "回答");
  assert.equal(textOf({ content: [] }), "");
  assert.equal(textOf(undefined), "");
});

test("hasEarlierTurnStart 以日志判断新会话，无法读取时不误判", () => {
  const log = [durable("turn/start", 1, { turn: 1 }), durable("turn/end", 2, { turn: 1 }), durable("turn/start", 3, { turn: 2 })];
  const s = { snapshotEvents: (from, to) => log.filter((e) => e.seq >= from && e.seq < to) };
  assert.equal(hasEarlierTurnStart(s, 3), true);
  assert.equal(hasEarlierTurnStart({ snapshotEvents: (from, to) => [durable("turn/start", 1, {})].filter((e) => e.seq >= from && e.seq < to) }, 1), false);
  assert.equal(hasEarlierTurnStart({ snapshotEvents: () => [durable("turn/start", 1, {})] }, 5), true);
  assert.equal(hasEarlierTurnStart({}, 1), true);
});

test("buildEvent 的 ID 由来源、会话、类型与序号决定且携带 sequence", () => {
  const a = buildEvent(source, "s1", "/w", "turn.completed", { seq: 7, time: 1_700_000_000_000, body: "x" });
  const b = buildEvent(source, "s1", "/w", "turn.completed", { seq: 7, time: 1_700_000_000_999, body: "y" });
  assert.equal(a.event_id, b.event_id);
  assert.equal(a.sequence, 7);
  assert.equal(a.agent, "dsh");
  assert.equal(a.scope, "main");
  assert.equal(a.occurred_at, "2023-11-14T22:13:20.000Z");
  assert.notEqual(buildEvent(source, "s1", "/w", "run.failed", { seq: 7 }).event_id, a.event_id);
  const rt = buildEvent(source, "s1", "/w", "approval.required", { salt: "call:c1", time: 1_700_000_000_000 });
  assert.equal(rt.sequence, undefined);
  assert.equal(rt.event_id, buildEvent(source, "s1", "/w", "approval.required", { salt: "call:c1" }).event_id);
  assert.throws(() => buildEvent(source, "s1", "/w", "unknown.kind"));
});

test("turn/end 按 reason 映射终态", () => {
  const s = { id: "s1", header: { cwd: "/w" } };
  const done = turnEnd(source, s, durable("turn/end", 9, { turn: 1, reason: { kind: "completed" } }), "最终回答", "标题");
  assert.equal(done.kind, "turn.completed");
  assert.equal(done.body, "最终回答");
  assert.equal(done.session_title, "标题");
  assert.equal(done.title, "本轮处理完成：标题");
  assert.equal(turnEnd(source, s, durable("turn/end", 9, { turn: 1, reason: { kind: "completed" } })).body, "本轮完成；原生正文未取得，请在 DSH 中查看。");
  const failed = turnEnd(source, s, durable("turn/end", 9, { turn: 1, reason: { kind: "error", error: { message: "quota", code: "e" } } }));
  assert.equal(failed.kind, "run.failed");
  assert.equal(failed.body, "失败原因：quota");
  assert.equal(turnEnd(source, s, durable("turn/end", 9, { turn: 1, reason: { kind: "aborted", reason: { kind: "user" } } })).body, "用户取消");
  assert.equal(turnEnd(source, s, durable("turn/end", 9, { turn: 1, reason: { kind: "interrupted" } })).kind, "run.interrupted");
  assert.equal(turnEnd(source, s, durable("turn/end", 9, { turn: 1, reason: { kind: "blocked" } })).kind, "run.interrupted");
  const maxed = turnEnd(source, s, durable("turn/end", 9, { turn: 1, reason: { kind: "max-tokens" } }), "前半");
  assert.equal(maxed.kind, "turn.completed");
  assert.match(maxed.body, /已达最大输出长度/);
  assert.equal(turnEnd(source, s, durable("turn/end", 9, { turn: 1, reason: { kind: "forked" } })), undefined);
});

test("turnStart、resumed、renamed、archived 的类型与序号", () => {
  const s = { id: "s1", header: { cwd: "/w" } };
  const started = turnStart(source, s, durable("turn/start", 1, { turn: 1 }), "session.started", "名");
  assert.equal(started.kind, "session.started");
  assert.equal(started.sequence, 1);
  assert.equal(resumed(source, s, durable("step/start", 4, {}), "名").sequence, 4);
  assert.equal(resumed(source, s, durable("step/start", 4, {})).kind, "activity.observed");
  assert.equal(resumed(source, s, durable("step/start", 4, {})).body, "等待已结束，继续处理");
  const r = renamed(source, s, durable("session/title", 5, { title: "新名字", source: { kind: "user" } }));
  assert.equal(r.kind, "session.renamed");
  assert.equal(r.session_title, "新名字");
  assert.equal(renamed(source, s, durable("session/title", 5, {})), undefined);
  const closed = archived(source, "s1", "/w", "名", 1_700_000_000_000);
  assert.equal(closed.kind, "session.closed");
  assert.equal(closed.sequence, undefined);
});

test("toolFailed 只转换带错误的工具结果并使用工具名映射", () => {
  const s = { id: "s1", header: { cwd: "/w" } };
  assert.equal(toolFailed(source, s, durable("tool/result", 3, { message: { isError: false } }), "bash"), undefined);
  const failed = toolFailed(source, s, durable("tool/result", 3, { error: { name: "ExitError", code: "1", reason: "exit status 1" }, message: { isError: true } }), "bash", "名");
  assert.equal(failed.session_title, "名");
  assert.equal(failed.kind, "tool.failed");
  assert.equal(failed.body, "工具 bash 失败：exit status 1");
  assert.equal(toolFailed(source, s, durable("tool/result", 3, { message: { isError: true } })).body, "工具 工具 失败：工具返回错误");
});

test("approval 只含工具名与展示原因", () => {
  const agent = { id: "s1", session: { header: { cwd: "/w" } } };
  const e = approval(source, agent, { toolName: "bash", reason: "raw", displayReason: { en: "run command", zh: "运行命令" }, callId: "c1" }, "会话名", 1_700_000_000_000);
  assert.equal(e.session_title, "会话名");
  assert.equal(e.title, "等待批准：会话名");
  assert.equal(e.kind, "approval.required");
  assert.equal(e.body, "需要批准：bash\n运行命令");
  assert.equal(e.event_id, approval(source, agent, { toolName: "bash", callId: "c1" }, 1_700_000_000_999).event_id);
  assert.equal(approval(source, agent, { toolName: "bash" }, 1).body, "需要批准：bash");
});

test("questions 提取问题、说明与选项，审批意图按批准处理", () => {
  const agent = { id: "s1", session: { header: { cwd: "/w" } } };
  const e = questions(source, agent, { questions: [
    { id: "q1", header: "确认", question: "继续吗？", detail: "将修改文件", options: [{ label: "是", description: "执行" }, { label: "否" }] },
    { id: "q2", question: "选颜色", multiSelect: true, options: [{ label: "红" }] },
  ], wait: { callId: "c9" } }, 1_700_000_000_000);
  assert.equal(e.kind, "input.required");
  assert.equal(e.body, "确认：继续吗？\n将修改文件\n1. 是 — 执行\n2. 否\n\n选颜色\n1. 红\n（可多选）");
  assert.equal(e.event_id, questions(source, agent, { questions: [{ id: "q1", question: "x" }], wait: { callId: "c9" } }, 7).event_id);
  const a = questions(source, agent, { questions: [{ id: "q1", question: "ok?", intent: { kind: "approval", approve: "yes", reject: "no", subject: { name: "write" }, display: { question: { en: "Allow?" } } } }] }, 1);
  assert.equal(a.kind, "approval.required");
  assert.equal(a.body, "需要批准：write\nAllow?");
  assert.equal(questions(source, agent, { questions: [] }), undefined);
});

test("SessionTracker 有界缓存最后助手文本与工具名", () => {
  const t = new SessionTracker(2);
  t.observe("s1", durable("assistant/message", 1, { turn: 1, step: 1, message: { content: [{ type: "text", text: "先" }] } }));
  t.observe("s1", durable("assistant/message", 2, { turn: 1, step: 2, message: { content: [{ type: "text", text: "后" }] } }));
  t.observe("s1", durable("assistant/message", 3, { turn: 1, step: 3, interrupted: true, message: { content: [{ type: "text", text: "中断" }] } }));
  t.observe("s1", durable("tool/call", 4, { callId: "c1", name: "bash", arguments: "{}" }));
  assert.equal(t.finalText("s1", 1), "后");
  assert.equal(t.finalText("s1", 1), "");
  assert.equal(t.toolName("s1", "c1"), "bash");
  assert.equal(t.toolName("s1", "zzz"), "");
  t.get("s2");
  t.get("s3");
  assert.equal(t.sessions.size, 2);

});


test("最终步骤无文本时不把中间说明当作最终回答", () => {
  const tracker = new SessionTracker();
  tracker.observe("s1", durable("assistant/message", 1, { turn: 1, step: 1, message: { content: [{ type: "text", text: "我先检查一下" }] } }));
  tracker.observe("s1", durable("assistant/message", 2, { turn: 1, step: 2, message: { content: [{ type: "reasoning", text: "不采集" }] } }));
  const final = turnEnd(source, { id: "s1", header: {} }, durable("turn/end", 3, { turn: 1, reason: { kind: "completed" } }), tracker.finalText("s1", 1));
  assert.equal(final.body, "本轮完成；原生正文未取得，请在 DSH 中查看。");
});

test("每次归档和实时等待恢复具有独立身份，同毫秒也不冲突", () => {
  const one = archived(source, "s1", "/w", "名", 1700000000000);
  const two = archived(source, "s1", "/w", "名", 1700000000000);
  assert.notEqual(one.event_id, two.event_id);
  const session = { id: "s1", header: {} };
  assert.notEqual(resumed(source, session, {}).event_id, resumed(source, session, {}).event_id);
});
