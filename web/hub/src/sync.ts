import { api, type Device, type Event, type Session, type Snapshot, type SourceLabel } from "./api.ts";
export interface Delta {
 run_id:string; version:number; from:number; reset?:Snapshot;
 sessions?:Session[];sessions_removed?:{source_id:string;agent:string;session_id:string}[];
 unread?:Event[];unread_removed?:string[];sources?:Record<string,SourceLabel>;sources_removed?:string[];
 devices?:Device[];devices_removed?:string[];
}
const key=(s:{source_id:string;agent:string;session_id:string})=>JSON.stringify([s.source_id,s.agent,s.session_id]);
export function applyDelta(old:Snapshot,d:Delta):Snapshot{
 if(!d.run_id||d.version<1)throw new Error("同步位置无效");
 if(d.reset){if(d.reset.run_id!==d.run_id||d.reset.version!==d.version)throw new Error("同步基准不一致");return d.reset;}
 if(old.run_id!==d.run_id||old.version!==d.from||d.version!==d.from+1)throw new Error("同步存在缺口");
 const sessions=new Map(old.sessions.map(s=>[key(s),s]));for(const s of d.sessions_removed??[])sessions.delete(key(s));for(const s of d.sessions??[])sessions.set(key(s),s);
 const unread=new Map(old.unread.map(e=>[e.event_id,e]));for(const id of d.unread_removed??[])unread.delete(id);for(const e of d.unread??[])unread.set(e.event_id,e);
 const devices=new Map(old.devices.map(d=>[d.role+":"+d.id,d]));for(const id of d.devices_removed??[])devices.delete(id);for(const x of d.devices??[])devices.set(x.role+":"+x.id,x);
 const sources={...old.sources,...d.sources};for(const id of d.sources_removed??[])delete sources[id];
 return {run_id:d.run_id,version:d.version,sources,sessions:[...sessions.values()],unread:[...unread.values()].sort((a,b)=>a.position-b.position),devices:[...devices.values()]};
}
export async function decodeSync(raw:string|ArrayBuffer):Promise<{type:string;payload:unknown}>{
 if(typeof raw==="string"){if(new TextEncoder().encode(raw).length>8<<20)throw new Error("同步消息过大");return JSON.parse(raw);}
 if(raw.byteLength>8<<20)throw new Error("同步消息过大");
 const stream=new Blob([raw]).stream().pipeThrough(new DecompressionStream("gzip"));const reader=stream.getReader();let size=0;const parts:Uint8Array[]=[];
 for(;;){const {done,value}=await reader.read();if(done)break;size+=value.length;if(size>8<<20){await reader.cancel();throw new Error("解压消息过大");}parts.push(value);}
 const result=new Uint8Array(size);let at=0;for(const p of parts){result.set(p,at);at+=p.length;}return JSON.parse(new TextDecoder().decode(result));
}
const bodies=new Map<string,string>();
export async function fillBodies(events:Event[],known:Event[]=[]):Promise<void>{
 for(const e of known)if(e.body_hash&&e.body)bodies.set(e.body_hash,e.body);
 const missing=events.filter(e=>e.body_hash&&!bodies.has(e.body_hash));
 for(let i=0;i<missing.length;i+=32){const group=missing.slice(i,i+32);const query=new URLSearchParams();for(const e of group)query.append("id",e.event_id);
  const values=await api<{event_id:string;body_hash:string;body:string}[]>("events/bodies?"+query.toString());if(values.length!==group.length)throw new Error("正文数量不一致");
  for(let j=0;j<values.length;j++){const v=values[j],e=group[j];if(v.event_id!==e.event_id||v.body_hash!==e.body_hash)throw new Error("正文标识不一致");bodies.set(v.body_hash,v.body);}
 }
 for(const e of events)if(e.body_hash)e.body=bodies.get(e.body_hash)??"";
 while(bodies.size>1024)bodies.delete(bodies.keys().next().value!);
}
