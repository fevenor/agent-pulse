import assert from "node:assert/strict";
import { test } from "node:test";
import { convert, FinalOutput, type NativeEvent, type SessionInfo } from "../src/adapter.ts";
const session = {id:"s",parentID:undefined,title:"会话",location:{directory:"/workspace"}} as SessionInfo;
const event = (type:string,data:unknown)=>({id:type,created:Date.now(),type,data}) as NativeEvent;
test("中断原因与批准结果保留，局部失败不变成主任务失败",()=>{
 assert.equal(convert(event("session.execution.interrupted",{sessionID:"s",reason:"superseded"}),session,"host")?.body,"已被新的执行替代");
 assert.equal(convert(event("permission.replied",{sessionID:"s",reply:"reject",requestID:"p"}),session,"host")?.body,"已拒绝请求");
 const local=convert(event("session.tool.failed",{sessionID:"s",error:{type:"tool",message:"测试错误"}}),session,"host");assert.equal(local?.kind,"tool.failed");assert.ok(local?.body.includes("测试错误"));
});
test("授权只提取明确路径，条件默认值可判断且不展示用户答案",()=>{
 const permission=convert(event("permission.asked",{sessionID:"s",action:"read",resources:["/workspace/file.ts","secret"],message:"读取文件"}),session,"host");assert.ok(permission?.body.includes("/workspace/file.ts"));assert.ok(!permission?.body.includes("secret"));
 const form=convert(event("form.created",{form:{sessionID:"s",title:"选择",fields:[{key:"mode",type:"string",default:"a"},{key:"shown",type:"string",title:"可见字段",when:[{key:"mode",op:"eq",value:"a"}]},{key:"hidden",type:"string",title:"不可见字段",when:[{key:"mode",op:"eq",value:"b"}]}]}}),session,"host");assert.ok(form?.body.includes("可见字段"));assert.ok(!form?.body.includes("不可见字段"));
});
test("缓存分片上限有明确截断提示",()=>{
 const output=new FinalOutput();for(let i=0;i<65;i++)output.observe(event("session.text.ended",{sessionID:"s",assistantMessageID:"m",ordinal:i,text:"短分片"}));assert.ok(output.take("s").includes("已截断"));
});
