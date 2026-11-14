// 在临时目录启动独立 OpenCode，不读取用户配置，不调用模型或修改现有 Agent。
import assert from "node:assert/strict";
import { spawn, execFileSync, type ChildProcess } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, mkdir, writeFile, open, readFile, cp } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import { OpenCode } from "@opencode/client";

const adapter = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const root = resolve(adapter, "../..");
const manifest = JSON.parse(await readFile(join(adapter, "manifest.json"), "utf8"));
const packageInfo = JSON.parse(await readFile(join(adapter, "package.json"), "utf8"));
assert.equal(manifest.version, packageInfo.version, "插件与适配包版本必须一致");
const expectedVersion: string = manifest.version;
const temp = await mkdtemp(join(tmpdir(), "agent-pulse-smoke-"));
const project = join(temp, "project");
await mkdir(project);
const packaged = process.env.AGENT_PULSE_PACKAGE_SMOKE === "1";
const repository = join(temp,"repository");
const processes: ChildProcess[] = [];
const logs: Awaited<ReturnType<typeof open>>[] = [];
async function port(): Promise<number> {
  const server = createServer(); server.listen(0, "127.0.0.1"); await once(server, "listening");
  const address = server.address(); assert.ok(address && typeof address !== "string");
  await new Promise<void>((resolve, reject) => server.close(err => err ? reject(err) : resolve()));
  return address.port;
}
async function launch(name: string, command: string, args: string[], env = process.env) {
  const log = await open(join(temp, `${name}.log`), "w"); logs.push(log);
  const child = spawn(command, args, { cwd: project, env, stdio: ["ignore", log.fd, log.fd] });
  child.on("error", () => {}); processes.push(child); return child;
}
async function poll<T>(fn: () => Promise<T | undefined>, label: string): Promise<T> {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    try { const value = await fn(); if (value !== undefined) return value; } catch { /* 等待服务就绪。 */ }
    if (processes.some(child => child.exitCode !== null)) throw new Error(`${label}：子进程提前退出，日志位于 ${temp}`);
    await delay(100);
  }
  throw new Error(`${label}：等待超时，日志位于 ${temp}`);
}
type Snapshot = { run_id: string; sessions: { session_id: string; state: string; title: string; last_event_id: string }[]; unread: { event_id: string; session_id: string; kind: string; valid: boolean }[] };
try {
  const hubPort = await port(), bridgePort = await port(), agentPort = await port();
  const hubURL = `http://127.0.0.1:${hubPort}`, bridgeURL = `http://127.0.0.1:${bridgePort}`;
  if (packaged) {
    await cp(join(root,"dist/repository"),repository,{recursive:true});
    const index = JSON.parse(await readFile(join(repository, "index-input.json"), "utf8"));
    assert.ok(index.packages.some((entry: { id: string; version: string }) => entry.id === manifest.id && entry.version === expectedVersion), "独立包已过期，请先运行 scripts/build/package-adapters.sh");
    execFileSync(join(root,"bin/agent-pulse-adapters"),["keygen","--private",join(temp,"publisher.key"),"--public",join(temp,"publisher.pub")]);
    execFileSync(join(root,"bin/agent-pulse-adapters"),["publish","--private",join(temp,"publisher.key"),"--repository",repository,"--index",join(repository,"index-input.json")]);
    // 已签名测试仓库须随包提供公钥，让 Hub 按正式启动规则验证。
    await cp(join(temp,"publisher.pub"),join(repository,"publisher.pub"));
  }
  await writeFile(join(temp, "hub.yaml"), `listen: "127.0.0.1:${hubPort}"\ndatabase: "${temp}/hub.db"\npush: []\nadapter_directory: "${repository}"\n`);
  await writeFile(join(temp, "bridge.yaml"), `listen: "127.0.0.1:${bridgePort}"\nsource_id: "smoke-linux"\nhub_url: "${hubURL}"\n`);
  if (!packaged) await writeFile(join(project, "opencode.json"), JSON.stringify({ plugins: [{ package: adapter, options: { source_id: "smoke-linux", bridge_url: bridgeURL } }], snapshots: false }));
  if (packaged) {
    const key=(await readFile(join(temp,"publisher.pub"),"utf8")).trim();
    await writeFile(join(temp,"bridge.yaml"),`listen: "127.0.0.1:${bridgePort}"\nsource_id: "smoke-linux"\nhub_url: "${hubURL}"\nadapters:\n  directory: "${temp}/adapters"\n  public_key: "${key}"\n  poll_seconds: 5\n  managed:\n    opencode: "${project}/opencode.json"\n`);
    await writeFile(join(project,"opencode.json"),JSON.stringify({snapshots:false}));
  }
  await mkdir(join(temp, "config/opencode"), { recursive: true });
  await writeFile(join(temp, "config/opencode/opencode.json"), JSON.stringify({ update: "disable", snapshots: false }));
  const hubProcess = await launch("hub", join(root, "bin/agent-pulse-hub"), ["--config", join(temp, "hub.yaml")]);
  await launch("bridge", join(root, "bin/agent-pulse-bridge"), ["serve", "--config", join(temp, "bridge.yaml")]);
  await poll(async () => (await fetch(`${bridgeURL}/healthz`)).ok ? true : undefined, "Bridge 启动");
  if (packaged) await poll(async()=>{
    const state=JSON.parse(await readFile(join(temp,"adapters/state.json"),"utf8"));
    return state.installed.opencode?.package?.version === expectedVersion ? true : undefined;
  },"独立适配包安装");
  const env: NodeJS.ProcessEnv = {
    PATH: process.env.PATH,
    OPENCODE_PASSWORD: "agent-pulse-smoke-password",
    XDG_CONFIG_HOME: join(temp, "config"), XDG_DATA_HOME: join(temp, "data"),
    XDG_STATE_HOME: join(temp, "state"), XDG_CACHE_HOME: join(temp, "cache"),
  };
  await launch("opencode", process.env.AGENT_PULSE_OPENCODE_BIN ?? "opencode", ["serve", "--hostname", "127.0.0.1", "--port", String(agentPort)], env);
  const auth = Buffer.from("opencode:agent-pulse-smoke-password").toString("base64");
  const client = OpenCode.make({ baseUrl: `http://127.0.0.1:${agentPort}`, headers: { Authorization: `Basic ${auth}` } });
  await poll(async () => await client.server.info(), "OpenCode 启动");
  await client.location.get({ location: { directory: project } });
  await writeFile(join(temp, "config.json"), JSON.stringify(await client.config.get({ location: { directory: project } }), null, 2));
  await poll(async () => {
    const plugins = await client.plugin.list({ location: { directory: project } });
    await writeFile(join(temp, "plugins.json"), JSON.stringify(plugins, null, 2));
    return plugins.data.some(p => p.id === "agent-pulse" && p.state.status === "active") ? true : undefined;
  }, "插件激活");
  const main = await client.session.create({ title: "AgentPulse 真实接入验证", location: { directory: project } });
  const snapshot = async () => await (await fetch(`${hubURL}/api/v1/snapshot`)).json() as Snapshot;
  const session = await poll(async () => (await snapshot()).sessions.find(s => s.session_id === main.id), "真实会话创建事件");
  assert.equal(session.title, "AgentPulse 真实接入验证", "Hub 应保留宿主会话标题");
  // 宿主实际创建输入表单，触发原生 form.created；不需要模型请求。
  await client.session.form.create({ sessionID: main.id, title: "请选择验证方案", fields: [{ key: "choice", type: "string", title: "方案" }] });
  const waiting = await poll(async () => (await snapshot()).unread.find(e => e.session_id === main.id && e.kind === "input.required"), "真实输入等待事件");
  assert.equal(waiting.valid, true);
  const beforeRename = await snapshot();
  let latestTitle = "";
  for (const title of ["第一次用户改名", "最新会话名称 ✅"]) {
    await client.session.update({ sessionID: main.id, title });
    await poll(async () => {
      const s = await snapshot();
      return s.sessions.find(item => item.session_id === main.id)?.title === title ? true : undefined;
    }, "原生改名事件抵达 Hub");
    latestTitle = title;
  }
  const afterRename = await snapshot();
  assert.equal(afterRename.sessions.length, beforeRename.sessions.length, "改名不能创建另一会话");
  assert.equal(afterRename.sessions.find(s => s.session_id === main.id)?.state, "waiting", "改名不改变等待状态");
  assert.equal(afterRename.sessions.find(s => s.session_id === main.id)?.last_event_id, beforeRename.sessions.find(s => s.session_id === main.id)?.last_event_id, "改名不是新运行事件");
  assert.deepEqual(afterRename.unread, beforeRename.unread, "改名不产生或清除提醒");
  const read = await fetch(`${hubURL}/api/v1/read`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ event_ids: [waiting.event_id] }) });
  assert.equal(read.status, 200);
  const before = await snapshot();
  assert.equal(before.unread.length, 0);
  await writeFile(join(temp, "snapshot-before.json"), JSON.stringify(before, null, 2));
  // 模拟进程突然退出，验证已经确认的数据不依赖优雅关闭。
  hubProcess.kill("SIGKILL"); await once(hubProcess, "exit");
  processes.splice(processes.indexOf(hubProcess), 1);
  await launch("hub-restarted", join(root, "bin/agent-pulse-hub"), ["--config", join(temp, "hub.yaml")]);
  const after = await poll(async () => { const s = await snapshot(); return s.run_id !== before.run_id ? s : undefined; }, "Hub 崩溃恢复");
  assert.equal(after.unread.length, 0);
  assert.ok(after.sessions.some(s => s.session_id === main.id && s.state === "waiting"));
  assert.equal(after.sessions.find(s => s.session_id === main.id)?.title, latestTitle, "改名后的标题与同一会话身份应跨重启保留");
  await writeFile(join(temp, "snapshot-after.json"), JSON.stringify(after, null, 2));
  if (packaged) { const runtime=JSON.parse(await readFile(join(temp,"adapters/runtime/opencode.json"),"utf8"));assert.equal(runtime.version,expectedVersion); }
  const evidence = { packaged, result: "通过", adapter: expectedVersion, opencode: "2.0.20", project, session_id: main.id, event_id: waiting.event_id, evidence: temp, checks: ["真实插件激活", "真实会话事件与标题", "原生连续改名且不改变状态与未读", "真实表单等待", "Bridge 转发", "Hub 入库", "明确已读", "Hub 强制终止后恢复"] };
  await writeFile(join(temp, "result.json"), JSON.stringify(evidence, null, 2));
  console.log(JSON.stringify(evidence, null, 2));
} finally {
  for (const child of processes.reverse()) {
    if (child.exitCode !== null) continue;
    child.kill("SIGTERM");
    await Promise.race([once(child, "exit"), delay(5000)]);
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
  }
  for (const log of logs) await log.close();
}
