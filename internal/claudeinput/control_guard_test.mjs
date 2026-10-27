import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { webcrypto, createHash } from 'node:crypto';
globalThis.crypto ??= webcrypto;
const source=(await readFile(new URL('./control_guard.js',import.meta.url),'utf8')).replaceAll('__CONFIG_PATH__','"descriptor"');
let copies=0; const vectors=[];
async function fixture(){
 const hooks=new Map(), calls=[]; let tick, timers=0, canceled=false, retry=false, malformed=false, id='native';
 const $={fs:{read:async()=>JSON.stringify({required_runtime:'claude-native-control',sender_enabled:false,generation:'shim',token:'broker-token',socket_path:'private'})},session:{id:async()=>id,version:async()=>({version:'2.1.310'})},clock:{every:(ms,fn)=>{assert.equal(ms,250);tick=fn;timers++;return{cancel(){canceled=true}}}},http:{fetch:async(url,options)=>{
  const path=new URL(url).pathname,body=JSON.parse(options.body);calls.push({path,body,raw:options.body});
  if(path==='/control'){ vectors.push(body);
   if(retry){retry=false;throw new Error('lost ACK')}
   if(malformed)return{ok:true,status:200,text:'{'};
   return{ok:true,status:200,text:JSON.stringify({generation:'shim',worker_generation:body.worker.generation,sequence:body.sequence,report_hash:createHash('sha256').update(options.body).digest('hex'),spool_sequence:body.sequence})};
  }
  return{ok:true,status:200,text:JSON.stringify({nonce:'nonce',live:false})};
 }}};
 const mod=await import('data:text/javascript;base64,'+Buffer.from(source+'\n//'+copies++).toString('base64'));
 mod.register((name,...args)=>{hooks.set(name,args.at(-1));if(name==='ui.render')assert.deepEqual(args[0],{component:'AskUserQuestion',surface:'terminal'})});
 const next=async e=>e;next.origin={plugin:'engine',tier:'core'};next.signal=new AbortController().signal;
 return{$,hooks,calls,next,tick:async()=>{assert(tick);await tick()},timers:()=>timers,canceled:()=>canceled,loseAck(){retry=true},malformedAck(){malformed=true},setID(v){id=v}};
}
{
 const f=await fixture();assert(f.hooks.has('turn.step'),'observation installation must register turn.step');
 const event={turnId:'turn',text:'SECRET PROMPT'};const result={turnId:'turn'};
 const next=async e=>{assert.equal(e,event);return result};next.origin=f.next.origin;
 assert.equal(await f.hooks.get('turn.start')(f.$,event,next),result);
 assert.equal(f.timers(),1);await f.hooks.get('session.start')(f.$,{},f.next);assert.equal(f.timers(),1);
 f.loseAck();await f.tick();await f.tick();await f.tick();
 const reports=f.calls.filter(x=>x.path==='/control');assert(reports.length>=3);assert.equal(reports[0].raw,reports[1].raw);
 assert(!JSON.stringify(reports).includes('SECRET'));assert(!JSON.stringify(reports).includes('broker-token'));
 await f.hooks.get('session.start')(f.$,{},f.next);assert(f.canceled());
}
{
 const f=await fixture();const chunk={type:'text',text:'SECRET CHUNK'},final={turnId:'turn',index:0,answer:'SECRET ANSWER',toolUses:[],stopReason:'end_turn',serverToolUses:[]};
 const next=async function*(){yield chunk;return final};next.origin=f.next.origin;
 const it=f.hooks.get('turn.step')(f.$,{turnId:'turn',index:0,messageCount:1},next);
 assert.equal((await it.next()).value,chunk);assert.equal((await it.next()).value,final);await f.tick();await f.tick();
 const reports=f.calls.filter(x=>x.path==='/control');assert.equal(reports.length,2);assert.equal(reports[1].body.phase,'returned');assert(!JSON.stringify(reports).includes('SECRET'));
}
{
 const f=await fixture();const error={secret:'opaque'},next=async()=>{throw error};next.origin=f.next.origin;
 await assert.rejects(f.hooks.get('tool.call')(f.$,{tool:'Read',tool_use_id:'call',file_path:'SECRET'},next),e=>e===error);
 await f.tick();await f.tick();const reports=f.calls.filter(x=>x.path==='/control');assert.equal(reports[1].body.phase,'threw');assert(!JSON.stringify(reports).includes('opaque'));
}
{
 const f=await fixture();const node={};node.self=node;const next=async()=>node;next.origin=f.next.origin;
 assert.equal(await f.hooks.get('ui.render')(f.$,{requestId:'call',props:{questions:[{question:'SECRET'}]}},next),node);await f.tick();await f.tick();
 assert(!JSON.stringify(f.calls).includes('SECRET'));
}
{
 const f=await fixture();let cleaned=false;
 const next=async function*(){try{yield {type:'text'}}finally{cleaned=true}};next.origin=f.next.origin;
 const it=f.hooks.get('turn.step')(f.$,{turnId:'turn',index:0,messageCount:1},next);
 await it.next();const sentinel={consumer:'return'};assert.equal((await it.return(sentinel)).value,sentinel);assert(cleaned);
 await f.tick();assert(!f.calls.some(c=>c.path==='/control'&&c.body.phase==='returned'));assert(f.canceled());
}
{
 const f=await fixture();const next=async()=>undefined;next.origin={plugin:'third-party',tier:'user'};
 assert.equal(await f.hooks.get('tool.call')(f.$,{tool:'Read',tool_use_id:'plugin-call'},next),undefined);
 await f.tick();await f.tick();const r=f.calls.find(c=>c.path==='/control').body;assert.equal(r.context.origin.plugin,'third-party');assert.equal(r.context.context_unknown,true);assert(!r.context.turn_id);
}
{
 const f=await fixture(); const next=async()=>undefined;next.origin=f.next.origin;
 await f.hooks.get('turn.complete')(f.$,{turnId:'cancel-turn',reason:'aborted',isAborted:true},next);await f.tick();await f.tick();
 const r=f.calls.filter(c=>c.path==='/control');assert.equal(r[1].body.data.is_aborted,true);assert.equal(r[1].body.data.reason,'aborted');
}
{
 const f=await fixture(); const event={tool_use_id:'spawn',background:true,fork:false,subagentType:'general-purpose',provider:{plugin:'engine',tier:'core'},prompt:'SECRET'};
 let release;const next=()=>new Promise(r=>{release=r});next.origin=f.next.origin;
 const spawning=f.hooks.get('agent.spawn')(f.$,event,next);await new Promise(r=>setImmediate(r));
 const childNext=async function*(){return{turnId:'child-turn',index:0,toolUses:[],serverToolUses:[],stopReason:null}};childNext.origin=f.next.origin;
 const it=f.hooks.get('turn.step')(f.$,{turnId:'child-turn',agentId:'child',index:0,messageCount:1},childNext);await it.next();
 release({agentId:'child',model:'unused'});await spawning;
 for(let i=0;i<4;i++)await f.tick();
 const reports=f.calls.filter(c=>c.path==='/control');assert.deepEqual(reports.map(r=>r.body.event),['agent.spawn','turn.step','turn.step','agent.spawn']);assert(!JSON.stringify(reports).includes('SECRET'));
}
{
 const f=await fixture();let release;const next=()=>new Promise(r=>{release=r});next.origin=f.next.origin;
 const pending=f.hooks.get('tool.call')(f.$,{tool:'AskUserQuestion',tool_use_id:'question'},next);await new Promise(r=>setImmediate(r));
 const node={};const uiNext=async()=>node;uiNext.origin=f.next.origin;
 assert.equal(await f.hooks.get('ui.render')(f.$,{requestId:'question',props:{questions:[{}]}},uiNext),node);
 release(undefined);await pending;for(let i=0;i<4;i++)await f.tick();
 assert.equal(f.calls.filter(c=>c.path==='/control').at(-1).body.phase,'returned');
}
{
 const f=await fixture();const error={downstream:true};const next=async function*(){yield {type:'text'};throw error};next.origin=f.next.origin;
 const it=f.hooks.get('turn.step')(f.$,{turnId:'turn',index:0,messageCount:1},next);await it.next();await assert.rejects(it.next(),e=>e===error);await f.tick();await f.tick();assert.equal(f.calls.filter(c=>c.path==='/control').at(-1).body.phase,'threw');
}
{
 const f=await fixture();const next=async e=>({turnId:e.turnId});next.origin=f.next.origin;
 for(let i=0;i<128;i++){await f.hooks.get('turn.start')(f.$,{turnId:'turn-'+i},next);await f.tick();await f.tick()}
 assert(!f.canceled());await f.hooks.get('turn.start')(f.$,{turnId:'overflow'},next);assert(f.canceled());
}
{
 const f=await fixture();const pending=[],releases=[];
 for(let i=0;i<128;i++){
  const next=()=>new Promise(r=>releases.push(r));next.origin=f.next.origin;
  pending.push(f.hooks.get('tool.call')(f.$,{tool:'Read',tool_use_id:'call-'+i},next));await new Promise(r=>setImmediate(r));await f.tick();
 }
 assert(!f.canceled());const next=async()=>undefined;next.origin=f.next.origin;
 await f.hooks.get('tool.call')(f.$,{tool:'Read',tool_use_id:'overflow'},next);assert(f.canceled());for(const resolve of releases)resolve(undefined);await Promise.all(pending);
}
{
 const f=await fixture();const next=async()=>undefined;next.origin=f.next.origin;
 for(let i=0;i<65;i++)await f.hooks.get('tool.call')(f.$,{tool:'Read',tool_use_id:'call-'+i},next);
 assert(f.canceled(),'full FIFO must not evict facts');
}
{
 const f=await fixture();const result={tool:'Agent',tool_use_id:'spawn',result:{status:'async_launched',agentId:'child',prompt:'SECRET',description:'SECRET',outputFile:'SECRET'}};
 const next=async()=>result;next.origin=f.next.origin;
 assert.equal(await f.hooks.get('tool.call')(f.$,{tool:'Agent',tool_use_id:'spawn',prompt:'SECRET'},next),result);await f.tick();await f.tick();
 const r=f.calls.filter(c=>c.path==='/control').at(-1).body;assert.equal(r.data.agent_status,'async_launched');assert.equal(r.data.agent_id,'child');assert(!JSON.stringify(r).includes('SECRET'));
}
{
 const f=await fixture();const next=async()=>undefined;next.origin=f.next.origin;
 await f.hooks.get('tool.call')(f.$,{tool:'Read',tool_use_id:'call'},next);f.malformedAck();await f.tick();assert(f.canceled(),'malformed successful ACK must retire, not retry');
}
console.log(JSON.stringify({reports:vectors}));
