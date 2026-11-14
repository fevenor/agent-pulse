import assert from "node:assert/strict";
import test from "node:test";
import { displaySessionTitle, type Session } from "../src/api.ts";

const session: Session = { source_id: "host", agent: "codex", session_id: "backend-only-session", title: "", state: "running", updated_at: "", project: "项目名" };

test("会话 ID 和项目名不得作为会话名称回退", () => {
  for (const title of ["", " ", session.session_id, `${session.agent} · ${session.session_id}`]) {
    assert.equal(displaySessionTitle({ ...session, title }), "未命名会话");
  }
});
test("同一 ID 改名后展示最新名称，备注不覆盖原生名称", () => {
  const original = { ...session, title: "原名称", alias: "本地备注" };
  const renamed = { ...original, title: "Agent 中修改的新名称" };
  assert.equal(renamed.session_id, original.session_id);
  assert.equal(displaySessionTitle(original), "原名称");
  assert.equal(displaySessionTitle(renamed), "Agent 中修改的新名称");
});
