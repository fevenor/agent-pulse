<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import { displaySessionTitle, api, setToken, socketProtocols, type Device, type Event, type Host, type HostInfo, type Push, type Session, type Snapshot } from "./api";

const snapshot = ref<Snapshot>({ run_id: "", version: 0, sessions: [], unread: [], devices: [] });
const hosts = ref<Host[]>([]), pushes = ref<Push[]>([]), history = ref<Event[]>([]), hostList = ref<HostInfo[]>([]);
const tab = ref("会话"), filter = ref(""), stateFilter = ref(""), archiveFilter = ref("");
const connected = ref(false), error = ref(""), busy = ref(false), changedAt = ref("");
const tokenDraft = ref(""), showToken = ref(false), reading = ref(new Set<string>());
const tabs = ["会话", "未读提醒", "设备", "主机", "适配器", "推送记录", "历史事件"];
const labels: Record<string,string> = { running: "运行中", waiting: "等待操作", idle: "本轮结束", failed: "失败", interrupted: "已中断", closed: "已关闭", unknown: "待确认", bridge: "Bridge · 主机采集", tray: "Tray · 桌面组件", web: "管理页面", client: "测试客户端", direct: "直连", relay: "经中继", pending: "待发送", sending: "发送中", sent: "已发送", skipped: "已抑制", expired: "已失效" };
const stateText = (value: string) => labels[value] ?? value;
const pushText = (value: string) => ({sent:"接口已接受",pending:"等待发送",sending:"正在发送",failed:"发送失败",skipped:"已跳过",unknown:"送达结果未知"} as Record<string,string>)[value] ?? value;
const formatTime = (value: string) => value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "—";
const searching = (values: string[]) => values.join(" ").toLowerCase().includes(filter.value.trim().toLowerCase());
const effectiveState = (session: Session) => session.override_state || session.state;
const displayTitle = displaySessionTitle;
const sourceName = (id: string) => snapshot.value.sources?.[id]?.name || id;
const deviceName = (device: Device) => device.host_name || snapshot.value.sources?.[device.id]?.name || (device.role === "web" ? "管理页面" : device.role === "tray" ? "桌面客户端" : sourceName(device.id));
const sourceEnvironment = (id: string) => snapshot.value.sources?.[id]?.environment || "";
const sessions = computed(() => snapshot.value.sessions.filter(s =>
  (!stateFilter.value || effectiveState(s) === stateFilter.value) &&
  (!archiveFilter.value || Boolean(s.archived) === (archiveFilter.value === "archived")) &&
  searching([displayTitle(s),s.alias ?? "",s.project ?? "",s.source_id,sourceName(s.source_id),sourceEnvironment(s.source_id),s.agent])
).sort((a,b)=>b.updated_at.localeCompare(a.updated_at)));
const unread = computed(() => snapshot.value.unread.filter(e=>searching([e.title,e.source_id,sourceName(e.source_id),sourceEnvironment(e.source_id),e.agent])));
const runningCount = computed(() => snapshot.value.sessions.filter(s=>!s.archived && !snapshot.value.sources?.[s.source_id]?.archived && effectiveState(s) === "running").length);
const waitingCount = computed(() => snapshot.value.sessions.filter(s=>!s.archived && !snapshot.value.sources?.[s.source_id]?.archived && effectiveState(s) === "waiting").length);
let socket: WebSocket | undefined, heartbeat: ReturnType<typeof setInterval> | undefined, retry: ReturnType<typeof setTimeout> | undefined, refreshTimer: ReturnType<typeof setInterval> | undefined;
let disposed = false, refreshing = false, refreshAgain = false;
const deviceID = "web-" + Array.from(crypto.getRandomValues(new Uint8Array(16)),value=>value.toString(16).padStart(2,"0")).join("");

async function refresh() {
  if (refreshing) { refreshAgain = true; return; }
  refreshing = true; busy.value = true;
  try {
    const [next, adapters, jobs, knownHosts] = await Promise.all([api<Snapshot>("snapshot"),api<Host[]>("adapters/status"),api<Push[]>("push-jobs"),api<HostInfo[]>("hosts")]);
    snapshot.value = next; hosts.value = adapters; pushes.value = jobs; hostList.value = knownHosts; error.value = ""; changedAt.value = new Date().toISOString();
  } catch (reason) { error.value = String(reason instanceof Error ? reason.message : reason); connected.value = false; }
  finally { busy.value = false; refreshing = false; if (refreshAgain) { refreshAgain = false; void refresh(); } }
}
function connect() {
  if (disposed) return;
  socket = new WebSocket(`${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/api/v1/connect`, socketProtocols());
  const current = socket;
  current.onopen = () => {
    current.send(JSON.stringify({type:"hello",payload:{id:deviceID,role:"web"}}));
    heartbeat = setInterval(()=>{ if(current.readyState===WebSocket.OPEN) current.send(JSON.stringify({type:"heartbeat",payload:{}})); },10000);
  };
  current.onmessage = message => {
    try {
      const value = JSON.parse(message.data);
      if(value.type === "hello" || value.type === "changed") { connected.value = true; void refresh(); }
    } catch { current.close(); }
  };
  current.onclose = () => { connected.value = false; if(heartbeat) clearInterval(heartbeat); if(!disposed) retry=setTimeout(connect,2500); };
  current.onerror = () => current.close();
}
async function markRead(ids: string[]) {
  if (!ids.length) return;
  for(const id of ids) reading.value.add(id);
  try { for(let i=0;i<ids.length;i+=256) await api("read",{event_ids:ids.slice(i,i+256)}); await refresh(); }
  catch(reason) { error.value = reason instanceof Error ? reason.message : "标记失败，请重试"; }
  finally { for(const id of ids) reading.value.delete(id); }
}
async function loadHistory(more = false) {
  try { const last=more ? history.value.at(-1)?.position : undefined; const page=await api<Event[]>("events?limit=50&include_archived=true"+(last?`&before=${last}`:"")); history.value=more?[...history.value,...page]:page; }
  catch(reason) { error.value=reason instanceof Error?reason.message:"历史读取失败"; }
}
function chooseTab(value: string) { tab.value=value; filter.value=""; if(value==="历史事件") void loadHistory(); }
function applyToken() { setToken(tokenDraft.value); tokenDraft.value=""; showToken.value=false; socket?.close(); void refresh(); }
async function patchHost(host: HostInfo, change: {display_name?: string; archived?: boolean}) {
  try { await api(`hosts/${encodeURIComponent(host.source_id)}`, change, "PATCH"); await refresh(); }
  catch(reason) { error.value=reason instanceof Error?reason.message:"主机设置失败"; }
}
async function changeHostName(host: HostInfo) {
  const value=window.prompt("修改主机显示名",host.name);
  if(value===null) return;
  await patchHost(host,{display_name:value});
}
async function deleteHost(host: HostInfo) {
  if (!window.confirm(`永久删除主机“${host.name}”及其全部会话和事件历史？此操作不可恢复。`)) return;
  try { await api(`hosts/${encodeURIComponent(host.source_id)}`, undefined, "DELETE"); await refresh(); }
  catch(reason) { error.value=reason instanceof Error?reason.message:"主机删除失败"; }
}
function hostOnline(source: string) { return connected.value && snapshot.value.devices.some(d=>(d.role==="bridge"||d.role==="tray")&&d.id===source); }
onMounted(()=>{ void refresh(); connect(); refreshTimer=setInterval(()=>void refresh(),15000); });
onUnmounted(()=>{ disposed=true; socket?.close(); if(heartbeat)clearInterval(heartbeat);if(retry)clearTimeout(retry);if(refreshTimer)clearInterval(refreshTimer); });
</script>

<template>
  <div class="shell">
    <aside class="sidebar">
      <a class="brand" href="/" aria-label="AgentPulse 首页"><span class="mark">a<span>•</span></span><div>AgentPulse<small>会话与通知</small></div></a>
      <nav aria-label="主导航"><button v-for="(item,index) in tabs" :key="item" :class="{active:tab===item}" @click="chooseTab(item)"><span class="nav-number">0{{index+1}}</span>{{item}}<span v-if="item==='未读提醒'&&snapshot.unread.length" class="count">{{snapshot.unread.length}}</span></button></nav>
      <div class="sidebar-foot"><span class="signal" :class="{online:connected}"></span>{{ connected ? '实时连接正常' : '正在连接 Hub' }}<small>个人工作空间</small></div>
    </aside>
    <main>
      <header><div><p class="eyebrow">工作空间 / {{tab}}</p><h1>{{tab}}</h1></div><div class="header-actions"><span class="updated">{{changedAt ? '更新于 '+new Date(changedAt).toLocaleTimeString('zh-CN',{hour12:false}) : '等待首次同步'}}</span><button class="quiet" @click="showToken=!showToken">连接设置</button><button class="button" :disabled="busy" @click="refresh">{{busy?'同步中…':'刷新'}}</button></div></header>
      <form v-if="showToken" class="connection-form" @submit.prevent="applyToken"><label>共享令牌（仅 Hub 启用时需要）<input v-model="tokenDraft" type="password" autocomplete="off" placeholder="只保存在当前页面内存中" /></label><button class="button">连接</button></form>
      <div v-if="error" class="notice error" role="alert">{{error}}。页面保留最后一次成功读取的数据。</div>
      <div v-else-if="!connected" class="notice" role="status">实时连接暂不可用，正在重连；当前列表可能已过期。</div>
      <section class="stats" aria-label="工作空间概况"><div><span>进行中的会话</span><strong>{{runningCount}}<small>运行中</small></strong></div><div><span>需要你的操作</span><strong>{{waitingCount}}<small>等待输入或批准</small></strong></div><div><span>未读提醒</span><strong>{{snapshot.unread.length}}<small>跨设备同步</small></strong></div><div><span>在线设备</span><strong>{{connected?snapshot.devices.filter(d=>d.role!=='web').length:'—'}}<small>{{connected?'当前连接':'状态待确认'}}</small></strong></div></section>
      <section class="panel">
        <div class="panel-head"><div><h2>{{tab==='会话'?'所有主会话':tab}}</h2><p>{{tab==='适配器'?'目标版本、已安装版本与最近实际加载版本分别展示。':tab==='未读提醒'?'明确标记后同步到所有设备；失效提醒不会再次通知。':tab==='主机'?'在 Hub 统一维护显示名、归档或永久删除；主机指纹仅用于识别目标。':'从各主机汇总，随连接实时更新。'}}</p></div><div class="filters"><input v-if="tab==='会话'||tab==='未读提醒'" v-model="filter" aria-label="搜索会话或主机" placeholder="搜索会话、项目或主机" /><select v-if="tab==='会话'" v-model="stateFilter" aria-label="筛选会话状态"><option value="">全部状态</option><option v-for="s in ['running','waiting','idle','failed','interrupted','closed','unknown']" :value="s">{{stateText(s)}}</option></select><select v-if="tab==='会话'" v-model="archiveFilter" aria-label="筛选归档状态"><option value="">全部会话</option><option value="active">未归档</option><option value="archived">已归档</option></select><button v-if="tab==='未读提醒'&&unread.length" class="button" :disabled="reading.size>0" @click="markRead(unread.map(e=>e.event_id))">全部标为已读</button></div></div>
        <div v-if="tab==='会话'" class="table-wrap">
          <table><thead><tr><th>会话 / 项目</th><th>来源</th><th>主机</th><th>状态</th><th>最后更新</th></tr></thead>
            <tbody><tr v-for="s in sessions" :key="JSON.stringify([s.source_id,s.agent,s.session_id])">
              <td><strong :title="displayTitle(s)">{{displayTitle(s)}}</strong><small v-if="s.alias && s.alias!==s.title">备注：{{s.alias}}</small><small>{{s.project||'未提供项目名'}}</small><span v-if="s.archived" class="badge">已归档</span></td>
              <td><span class="agent-tag">{{s.agent}}</span></td>
              <td><strong>{{sourceName(s.source_id)}}</strong><small v-if="sourceEnvironment(s.source_id)">{{sourceEnvironment(s.source_id)}}</small></td>
              <td><span class="badge" :class="effectiveState(s)">{{stateText(effectiveState(s))}}</span><small v-if="s.override_state">人工修正 · 来源：{{stateText(s.state)}}</small><small v-if="s.override_state && s.override_at">修正于 {{formatTime(s.override_at)}}</small></td>
              <td class="muted">{{formatTime(s.updated_at)}}</td>
            </tr></tbody>
          </table>
          <div v-if="!sessions.length" class="empty"><span>○</span><h3>{{filter||stateFilter||archiveFilter?'没有匹配的会话':'还没有会话'}}</h3><p>{{filter||stateFilter||archiveFilter?'调整搜索、状态或归档筛选后重试。':'连接主机 Bridge，并在已接入的 Agent 中开始一个会话。'}}</p></div>
        </div>
        <div v-else-if="tab==='未读提醒'" class="reminders"><article v-for="e in unread" :key="e.event_id"><div class="reminder-symbol" :class="{'muted':!e.valid}">{{e.valid?'•':'○'}}</div><div class="reminder-content"><h3>{{e.title}} <span v-if="!e.valid" class="badge">已失效</span></h3><p v-if="e.body">{{e.body}}</p><small>{{e.agent}} · {{sourceName(e.source_id)}} · {{formatTime(e.occurred_at)}}</small></div><button class="button" :disabled="reading.has(e.event_id)" @click="markRead([e.event_id])">标为已读</button></article><div v-if="!unread.length" class="empty"><span>✓</span><h3>没有未读提醒</h3><p>新通知会自动出现在这里。</p></div></div>
        <div v-else-if="tab==='设备'" class="table-wrap"><table><thead><tr><th>设备</th><th>用途</th><th>连接路径</th><th>桌面状态</th><th>最近心跳</th></tr></thead><tbody><tr v-for="d in snapshot.devices" :key="d.role+d.id"><td><strong>{{deviceName(d)}}</strong><small v-if="d.role==='bridge' && sourceEnvironment(d.id)">{{sourceEnvironment(d.id)}}</small><small>{{connected?'当前在线':'连接中断，待确认'}}</small></td><td>{{stateText(d.role)}}</td><td>{{stateText(d.path)}}</td><td>{{d.role==='tray'?(d.locked===undefined?'锁屏状态未知':d.locked?'已锁屏':'未锁屏'):'—'}}{{d.fullscreen?' · 全屏':''}}</td><td>{{formatTime(d.last_seen)}}</td></tr></tbody></table></div>
        <div v-else-if="tab==='主机'" class="table-wrap"><table><thead><tr><th>显示名</th><th>环境</th><th>状态</th><th>主机指纹</th><th>最近连接</th><th>操作</th></tr></thead><tbody><tr v-for="host in hostList" :key="host.source_id"><td><strong>{{host.name}}</strong><small>{{stateText(host.role||'bridge')}} · 默认名称：{{host.default_name||'首次连接主机名'}}</small></td><td>{{host.environment||'—'}}</td><td><span class="badge" :class="{running:host.online}">{{host.archived?'已归档':host.online?'在线':'离线'}}</span></td><td class="mono">{{host.source_id}}</td><td>{{formatTime(host.last_seen||'')}}</td><td class="host-actions"><button class="button" @click="changeHostName(host)">修改名称</button><button class="button" @click="patchHost(host,{archived:!host.archived})">{{host.archived?'解除归档':'归档'}}</button><button class="button danger" @click="deleteHost(host)">永久删除</button></td></tr></tbody></table><div v-if="!hostList.length" class="empty"><span>⌂</span><h3>尚无已连接主机</h3><p>Bridge 或 Tray 首次连接 Hub 后，主机会显示在这里。</p></div></div>
        <div v-else-if="tab==='适配器'" class="adapter-hosts"><article v-for="host in hosts" :key="host.source_id"><div class="host-head"><h3>{{sourceName(host.source_id)}} <span class="badge" :class="{running:hostOnline(host.source_id)}">{{hostOnline(host.source_id)?'在线':'离线或待确认'}}</span></h3><small>报告于 {{formatTime(host.updated_at)}}</small></div><p v-if="host.error" class="inline-error">{{host.error}}</p><div class="table-wrap"><table><thead><tr><th>来源</th><th>目标</th><th>已安装</th><th>最近运行</th><th>宿主版本</th><th>结果</th></tr></thead><tbody><tr v-for="a in host.items" :key="a.id"><td><strong>{{a.id}}</strong></td><td>{{a.target||'—'}}</td><td>{{a.installed||'未安装'}}</td><td>{{a.running||'未确认'}}</td><td>{{a.agent_version||'—'}}</td><td class="adapter-result">{{a.error||(a.installed!==a.running?'待宿主重新加载或首次调用':'已确认加载')}}<p v-if="a.warning" class="muted">{{a.warning}}</p></td></tr></tbody></table></div></article><div v-if="!hosts.length" class="empty"><span>↻</span><h3>还没有适配器报告</h3><p>配置 Bridge 的适配包更新后，版本状态会显示在这里。</p></div></div>
        <div v-else-if="tab==='推送记录'" class="table-wrap"><table><thead><tr><th>渠道</th><th>结果 / 原因</th><th>HTTP / 业务状态</th><th>尝试次数</th><th>事件</th><th>下次尝试</th></tr></thead><tbody><tr v-for="p in pushes" :key="p.event_id+p.channel"><td>{{p.channel}}</td><td><span class="badge" :class="p.status">{{pushText(p.status)}}</span><small v-if="p.failure_reason">{{p.failure_reason}}</small></td><td>{{p.http_status || '—'}} / {{p.business_status ?? '—'}}</td><td>{{p.attempts}}</td><td class="mono">{{p.event_id}}</td><td>{{p.status==='pending'?formatTime(p.next_at):'—'}}</td></tr></tbody></table><div v-if="!pushes.length" class="empty"><span>↗</span><h3>暂无推送记录</h3><p>配置手机 HTTP 渠道后，这里展示最近 200 条投递结果。</p></div></div>
        <div v-else class="table-wrap"><table><thead><tr><th>事件</th><th>来源 / 主机</th><th>通知状态</th><th>时间</th></tr></thead><tbody><tr v-for="e in history" :key="e.event_id"><td><strong>{{e.title}}</strong><small>{{e.kind}}</small></td><td>{{e.agent}} · {{sourceName(e.source_id)}}</td><td>{{e.read?'已读':e.valid?'有效':'已失效或无需提醒'}}</td><td>{{formatTime(e.occurred_at)}}</td></tr></tbody></table><div class="history-footer"><button class="button" @click="loadHistory(true)">加载更早记录</button></div></div>
      </section>
      <footer>AgentPulse <span>人工修正保留来源状态 · 归档不清除未读</span></footer>
    </main>
  </div>
</template>
