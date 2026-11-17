import assert from "node:assert/strict";
import test from "node:test";
import { bridgeAddress, convert, type NativeEvent, type SessionInfo } from "../src/adapter.ts";

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

test("本地IPC地址限制", () => {
  assert.equal(bridgeAddress(String.raw`pipe:\\.\pipe\pulse-test`),String.raw`\\.\pipe\pulse-test`);
  if (process.platform !== "win32") assert.equal(bridgeAddress("unix:/tmp/pulse-test.sock"),"/tmp/pulse-test.sock");
  for (const address of [undefined,"127.0.0.1:18702","http://127.0.0.1:18702", "http://remote", "unix:relative", String.raw`pipe:\\remote\pipe\pulse`]) assert.throws(()=>bridgeAddress(address));
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

test("本机进程信息仅在IPC发送边界附加，过滤确认不是发送失败", async () => {
  const {emit}=await import("../src/adapter.ts");
  const {createServer}=await import("node:http");
  const {mkdtemp,rm}=await import("node:fs/promises");
  const {tmpdir}=await import("node:os");const {join,resolve}=await import("node:path");
  const dir=await mkdtemp(join(tmpdir(),"pulse-ipc-"));
  const address=process.platform==="win32" ? String.raw`\\.\pipe\pulse-test-`+process.pid+"-"+Date.now() : join(dir,"bridge.sock");
  const e=convert(completed,session,"linux")!;let posted:Record<string,unknown>|undefined;
  const server=createServer((req,res)=>{let body="";req.on("data",chunk=>body+=chunk);req.on("end",()=>{
    posted=JSON.parse(body);res.writeHead(202,{"Content-Type":"application/json"});res.end(JSON.stringify({event_id:e.event_id,accepted:"filtered"}));
  });});
  await new Promise<void>((resolve,reject)=>{server.once("listening",resolve);server.once("error",reject);server.listen(address);});
  try {await emit(address,e);assert.equal(posted?.process_id,process.pid);assert.equal("process_id" in e,false);}
  finally {await new Promise<void>((r,j)=>server.close(error=>error?j(error):r()));assert.ok(resolve(dir).startsWith(resolve(tmpdir())+ (process.platform==="win32"?"\\":"/")));await rm(dir,{recursive:true,force:true});}
});

test("完成正文从原生文本事件收集，不混入思考、其他会话或上一轮", async () => {
 const { FinalOutput } = await import("../src/adapter.ts");
 const output = new FinalOutput();
 const text = (id: string, message: string, ordinal: number, value: string) => ({id:"text-"+id+ordinal,created:1794220800000,type:"session.text.ended",data:{sessionID:id,assistantMessageID:message,ordinal,text:value}} as NativeEvent);
 output.observe(text("main","old",0,"之前的工具前说明"));
 output.observe(text("main","final",1,"第二段"));output.observe(text("main","final",0,"第一段"));
 output.observe(text("child","other",0,"不能混入"));
 assert.equal(output.take("main"),"第一段\n第二段");assert.equal(output.take("main"),"");
 output.observe(text("main","new",0,"上一轮"));output.observe({...completed,type:"session.execution.started"} as NativeEvent);
 assert.equal(output.take("main"),"");
 assert.equal(convert(completed,session,"host","原生最终回答")!.body,"原生最终回答");
});

test("表单字段、选项和批准说明展示，不上传答案、默认值、隐藏或条件字段", () => {
 const form={id:"f",created:1794220800000,type:"form.created",data:{form:{id:"f",sessionID:"main",title:"选择饮品",fields:[
  {key:"drink",type:"string",title:"你想喝什么？",description:"请选择",options:[{value:"a",label:"咖啡",description:"提神"},{value:"b",label:"茶"}]},
  {key:"secret",type:"string",title:"隐藏问题",hidden:true,default:"秘密"},
  {key:"conditional",type:"string",title:"尚未满足条件",when:[{key:"drink",op:"eq",value:"b"}]},
 ]}}} as unknown as NativeEvent;
 const body=convert(form,session,"host")!.body;
 assert.ok(body.includes("你想喝什么？\n请选择\n1. 咖啡 — 提神\n2. 茶"));
 assert.ok(!body.includes("秘密") && !body.includes("隐藏问题") && !body.includes("尚未满足条件"));
 const permission={id:"p",created:1794220800000,type:"permission.asked",data:{id:"p",sessionID:"main",action:"shell",resources:["不采集"],message:"需要执行测试命令"}} as NativeEvent;
 assert.equal(convert(permission,session,"host")!.body,"需要批准：shell\n需要执行测试命令");
 const failure={...completed,type:"session.execution.failed",data:{sessionID:"main",error:{type:"api",message:"当前模型不可用",response:{body:"不上传原始响应"}}}} as NativeEvent;
 assert.equal(convert(failure,session,"host")!.body,"失败原因：当前模型不可用");
});

test("长正文和流缓存按UTF-8及32KiB边界保留并明确截断", async () => {
 const { visibleBody, FinalOutput }=await import("../src/adapter.ts");
 const output=new FinalOutput();
 for(let i=0;i<64;i++)output.observe({id:"text"+i,created:1794220800000,type:"session.text.ended",data:{sessionID:"main",assistantMessageID:"m",ordinal:i,text:"中文🙂".repeat(5000)}} as NativeEvent);
 const body=output.take("main");assert.ok(Buffer.byteLength(body)<=32768);assert.ok(body.includes("已截断"));assert.ok(!body.includes("�"));
 assert.equal(visibleBody(" 短正文 "),"短正文");
});
