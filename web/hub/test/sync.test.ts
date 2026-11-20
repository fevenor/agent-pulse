import test from "node:test";
import assert from "node:assert/strict";
import { applyDelta } from "../src/sync.ts";
import type { Snapshot } from "../src/api.ts";
const original:Snapshot={run_id:"run",version:1,sessions:[],unread:[],devices:[],sources:{}};
test("增量同步保留无关对象并明确处理已读及删除",()=>{
 const added=applyDelta(original,{run_id:"run",from:1,version:2,sessions:[{source_id:"s",agent:"test",session_id:"one",title:"会话",state:"idle",updated_at:""}],unread:[{event_id:"e",source_id:"s",agent:"test",session_id:"one",kind:"turn.completed",title:"完成",body:"",position:1,occurred_at:"",read:false,valid:true}]});
 assert.equal(added.unread.length,1);assert.equal(original.unread.length,0);
 const read=applyDelta(added,{run_id:"run",from:2,version:3,unread_removed:["e"]});assert.equal(read.unread.length,0);assert.equal(read.sessions.length,1);
 const deleted=applyDelta(read,{run_id:"run",from:3,version:4,sessions_removed:[{source_id:"s",agent:"test",session_id:"one"}]});assert.equal(deleted.sessions.length,0);
});
test("增量断档与运行代次变化不能被静默接受",()=>{
 assert.throws(()=>applyDelta(original,{run_id:"run",from:0,version:2}));assert.throws(()=>applyDelta(original,{run_id:"new",from:1,version:2}));
 assert.equal(applyDelta(original,{run_id:"new",from:0,version:1,reset:{...original,run_id:"new"}}).run_id,"new");
});
