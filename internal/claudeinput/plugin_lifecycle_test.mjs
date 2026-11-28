import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { webcrypto } from 'node:crypto';
globalThis.crypto ??= webcrypto;
const senderSource = (await readFile(process.argv[2], 'utf8')).replaceAll('__CONFIG_PATH__', '"descriptor"');
const guardSource = (await readFile(process.env.SWARM_GUARD_FIXTURE_SOURCE ?? new URL('./guard.js', import.meta.url), 'utf8')).replaceAll('__CONFIG_PATH__', '"descriptor"');
let copies = 0;
async function fixture(guard = false, manager = false) {
  const source = guard ? guardSource : senderSource;
  const hooks = new Map(), calls = [];
  let id = 'original', tick, expire, canceled = false, nextExchange, nextGuard, nextTurn;
  const responses = new Map();
  const nativeQueue = [];
  const $ = {
    fs: { read: async () => JSON.stringify({ generation: 'shim', socket_path: 'private', token: 'own-token', native_turns: true, sender_name: manager ? 'manager-sender' : 'swarm-native-input', manager_conversation_id: manager ? 'conversation' : '' }) },
    session: { id: async () => id, version: async () => ({ version: '2.1.296' }) },
    clock: {
      every: (_, fn) => { tick = fn; return { cancel() { canceled = true; } }; },
      after: (_, fn) => { expire = fn; return { cancel() {} }; },
    },
    http: { fetch: async (url, options) => {
      const path = new URL(url).pathname, body = JSON.parse(options.body);
      calls.push({ path, body });
      if (responses.has(path)) return responses.get(path)(body);
      if (path === '/exchange' && !body.operation_id && nextExchange) return nextExchange();
      if (path === '/guard' && nextGuard) return nextGuard();
      if (path === '/turn' && nextTurn) return nextTurn();
      return { ok: true, text: 'null' };
    } },
    prompt: { submit: ({ text }) => new Promise(resolve => nativeQueue.push({ text, resolve })) },
    turn: { abort: async ({turnId}) => { calls.push({path:'native-abort',body:{turnId}}); } },
  };
  const mod = await import('data:text/javascript;base64,' + Buffer.from(source + '\n//' + copies++).toString('base64'));
  mod.register((name, fn) => { hooks.set(name, fn); });
  const next = async event => event;
  next.signal = new AbortController().signal;
  await hooks.get('session.start')($, {}, next);
  return { $, calls, hooks, next, nativeQueue, response(path,fn) {responses.set(path,fn);}, tick: () => tick(), setID(value) { id = value; },
    exchange(fn) { nextExchange = fn; }, guard(fn) {nextGuard=fn;}, turnReply(fn) {nextTurn=fn;}, canceled: () => canceled, expire: () => expire() };
}
{
  const f = await fixture(); f.setID('replacement'); await f.tick();
  assert.equal(f.calls.filter(c => c.path === '/exchange').length, 0);
  assert.equal(f.calls.at(-1).path, '/revoke'); assert(f.canceled());
}
{
  const f = await fixture();
  f.exchange(async () => { f.setID('replacement'); return { ok: true, text: JSON.stringify({ operation_id: 'old', text: 'must not enqueue' }) }; });
  await f.tick(); assert.equal(f.nativeQueue.length, 0); assert.equal(f.calls.at(-1).path, '/revoke');
}
{
  const f = await fixture(true);
  f.setID('replacement');
  const result = await f.hooks.get('prompt.submit')(f.$, { text: 'queued before transition', origin: { kind: 'plugin', name: 'swarm-native-input@inline' } }, f.next);
  assert(result.drop); assert.equal(f.calls.at(-1).body.state, 'revoked');
}

{
  const f = await fixture();
  const next = async event => { assert.equal(f.calls.at(-1).path, '/revoke'); assert(f.canceled()); return event; };
  await f.hooks.get('command.run')(f.$, { command: 'clear' }, next);
  const count = f.calls.length; await f.tick(); assert.equal(f.calls.length, count);
  f.setID('replacement'); await f.hooks.get('session.start')(f.$, {}, f.next);
  await f.tick(); assert(!f.calls.slice(count).some(c => c.path === '/exchange'));
  const g = await fixture(true); await g.hooks.get('session.end')(g.$, {}, g.next);
  const owner = { text: 'normal owner prompt', origin: { kind: 'composer' } };
  assert.equal(await g.hooks.get('prompt.submit')(g.$, owner, g.next), owner);
  const other = { text: 'other plugin', origin: { kind: 'plugin', name: 'other' } };
  assert.equal(await g.hooks.get('prompt.submit')(g.$, other, g.next), other);
}
{
  const f = await fixture(true);
  await f.hooks.get('session.end')(f.$, { sessionId: 'original' }, f.next);
  const count = f.calls.length; await f.tick(); assert.equal(f.calls.length, count);
  const old = { text: 'stale', origin: { kind: 'plugin', name: 'swarm-native-input' } };
  assert((await f.hooks.get('prompt.submit')(f.$, old, f.next)).drop);
}
{
  const f = await fixture(true);
  await f.hooks.get('turn.start')(f.$, {turnId:'main'}, f.next);
  const start = f.calls.find(c=>c.path==='/turn' && c.body.turn.state==='running');
  assert.equal(start.body.turn.turn_id,'main');
  await f.hooks.get('turn.start')(f.$,{turnId:'child',agentId:'agent'},f.next);
  assert.equal(f.calls.filter(c=>c.path==='/turn' && c.body.turn.state==='running').length,1);
  await f.hooks.get('turn.complete')(f.$,{turnId:'child',agentId:'agent',reason:'answer'},f.next);
  assert(!f.calls.some(c=>c.path==='/turn' && c.body.turn.state==='completed'));
  f.guard(async()=>({ok:true,text:JSON.stringify({operation_id:'stop-old',turn_id:'old'})}));
  await f.tick(); assert(!f.calls.some(c=>c.path==='native-abort'));
  f.guard(async()=>({ok:true,text:JSON.stringify({operation_id:'stop-main',turn_id:'main'})}));
  await f.tick(); assert.equal(f.calls.filter(c=>c.path==='native-abort').length,1);
  await f.hooks.get('turn.complete')(f.$,{turnId:'main',reason:'aborted'},f.next);
  assert.equal(f.calls.at(-1).body.turn.reason,'aborted');
  await f.tick(); assert.equal(f.calls.filter(c=>c.path==='native-abort').length,1);
  const native = {text:'identical text',origin:{kind:'plugin',name:'swarm-native-input'}};
  await f.hooks.get('prompt.submit')(f.$,native,f.next);
  assert(!f.calls.some(c=>c.path==='/activity'));
  await f.hooks.get('prompt.submit')(f.$,{text:'identical text',origin:{kind:'composer'}},f.next);
  assert.equal(f.calls.at(-1).body.external_input,1);
}
{
  const f = await fixture(true, true);
  f.turnReply(async()=>({ok:false,text:'refused'}));
  await f.hooks.get('turn.start')(f.$,{turnId:'unobserved-main'},f.next);
  assert(f.canceled()); assert.equal(f.calls.at(-1).body.state,'revoked');
  assert((await f.hooks.get('prompt.submit')(f.$,{text:'later',origin:{kind:'plugin',name:'manager-sender'}},f.next)).drop);
}
for (const fault of ['observation', 'canceled-prompt']) {
  const f = await fixture(true);
  const hook = f.hooks.get('prompt.submit');
  const owner = {text:'owner input',origin:{kind:'composer'}};
  const native = {text:'native input',origin:{kind:'plugin',name:'swarm-native-input'}};
  await f.hooks.get('turn.start')(f.$, {turnId:'old'}, f.next);
  if (fault === 'observation') {
    f.response('/activity', async()=>{throw new Error('temporary transport failure');});
    assert.equal(await hook(f.$, owner, f.next), owner);
  } else {
    let release, entered;
    const inFlight = new Promise(resolve=>{entered=resolve;});
    f.guard(()=>new Promise(resolve=>{release=resolve; entered();}));
    const tick = f.tick();
    await inFlight;
    const canceled = async()=>assert.fail('canceled input forwarded');
    canceled.signal = AbortSignal.abort();
    assert((await hook(f.$, native, canceled)).drop);
    release({ok:true,text:JSON.stringify({operation_id:'in-flight',turn_id:'old'})});
    await tick;
    assert.equal(f.calls.at(-1).body.state,'rejected');
  }
  assert(!f.canceled(), fault + ' retired the same conversation');
  const expiring = hook(f.$, native, f.next);
  f.guard(async()=>({ok:true,text:'false'}));
  await f.tick();
  assert.equal(f.calls.at(-1).body.state, 'revalidate');
  f.expire();
  assert((await expiring).drop, 'unverified proof did not time out');
  let forwarded = false;
  const next = async event=>{forwarded=true; return event;};
  next.signal = f.next.signal;
  const waiting = hook(f.$, native, next);
  f.guard(async()=>({ok:true,text:'true'}));
  await f.tick();
  assert(!forwarded, 'input forwarded before guard confirmation');
  const before = f.calls.length;
  assert.equal(await hook(f.$, owner, f.next), owner);
  assert.equal(f.calls.length, before, 'owner input restarted recovery during confirmation');
  f.guard(async()=>({ok:true,text:'null'}));
  await f.tick();
  assert.equal(f.calls.at(-1).body.state, '');
  assert.equal(await waiting, native);
  assert(forwarded);
  f.guard(async()=>({ok:true,text:JSON.stringify({operation_id:'old-interrupt',turn_id:'old'})}));
  await f.tick();
  assert(!f.calls.some(call=>call.path==='native-abort'));
  f.setID('replacement');
  await f.tick();
  assert(f.canceled());
  assert.equal(f.calls.at(-1).body.state, 'revoked');
}
{
  const f = await fixture(true);
  const id = f.$.session.id;
  let release;
  f.$.session.id = () => new Promise(resolve=>{release=resolve;});
  const stale = f.hooks.get('turn.start')(f.$, {turnId:'old'}, f.next);
  f.$.session.id = id;
  const canceled = async()=>assert.fail('canceled input forwarded');
  canceled.signal = AbortSignal.abort();
  const input = {text:'native input',origin:{kind:'plugin',name:'swarm-native-input'}};
  assert((await f.hooks.get('prompt.submit')(f.$, input, canceled)).drop);
  f.guard(async()=>({ok:true,text:'true'}));
  await f.tick();
  f.guard(async()=>({ok:true,text:'null'}));
  await f.tick();
  release(await id());
  await stale;
  assert(!f.calls.some(call=>call.path==='/turn' && call.body.turn.turn_id==='old'));
  await f.hooks.get('turn.start')(f.$, {turnId:'new'}, f.next);
  assert(f.calls.some(call=>call.path==='/turn' && call.body.turn.turn_id==='new'));
}
{
  const f = await fixture(true);
  const submit = f.hooks.get('prompt.submit');
  f.response('/activity', async()=>({ok:false,text:'refused'}));
  await submit(f.$, {text:'owner input',origin:{kind:'composer'}}, f.next);
  const waiting = submit(f.$, {text:'native input',origin:{kind:'plugin',name:'swarm-native-input'}}, f.next);
  f.guard(async()=>({ok:true,text:'true'}));
  await f.tick();
  f.setID('replacement');
  await f.tick();
  assert((await waiting).drop, 'held prompt reached the replacement conversation');
  assert(f.canceled());
}
{
  const f = await fixture(true);
  const submit = f.hooks.get('prompt.submit');
  f.response('/activity', async()=>({ok:false,text:'refused'}));
  await submit(f.$, {text:'owner input',origin:{kind:'composer'}}, f.next);
  const signal = new AbortController();
  const canceled = async()=>assert.fail('canceled input forwarded');
  canceled.signal = signal.signal;
  const input = {text:'native input',origin:{kind:'plugin',name:'swarm-native-input'}};
  const waiting = submit(f.$, input, canceled);
  f.guard(async()=>({ok:true,text:'true'}));
  await f.tick();
  const id = f.$.session.id;
  let identityReads = 0;
  f.$.session.id = async()=>{
    if (++identityReads === 2) {
      signal.abort();
      throw new Error('native hook context canceled');
    }
    return id();
  };
  f.guard(async()=>({ok:true,text:'null'}));
  await f.tick();
  assert((await waiting).drop);
  assert(!f.canceled(), 'same conversation retired while checking canceled prompt');
  assert.equal(await submit(f.$, input, f.next), input);
}
{
  const f=await fixture(true,true), nonce='a'.repeat(64), envelope='[SWARM_MANAGER_INPUT_V1:'+nonce+']\nhuman words';
  assert((await f.hooks.get('prompt.submit')(f.$,{text:envelope,origin:{kind:'composer'}},f.next)).drop);
  f.response('/manager-prompt',async()=>({ok:true,text:JSON.stringify({nonce})}));
  const owned={text:envelope,origin:{kind:'plugin',name:'manager-sender@inline'}};
  assert.equal(await f.hooks.get('prompt.submit')(f.$,owned,f.next),owned);
  const projected='The manager-sender plugin sent a message:\n'+envelope+"\n\nThis is how Claude Code surfaces a prompt a plugin submits between turns — it starts this turn in the user's place. Address the message above.";
  const append={message:{type:'user',content:[{type:'text',text:'before downstream rewrite'}]},door:'prompt',uuid:'native-message',origin:owned.origin};
  const stored=async()=>({message:{type:'user',content:[{type:'text',text:projected}]},uuid:'native-message'});
  await f.hooks.get('session.append')(f.$,append,stored);
  await f.hooks.get('turn.start')(f.$,{turnId:'child',agentId:'agent',text:projected},f.next);
  await f.hooks.get('turn.start')(f.$,{turnId:'main',text:projected},f.next);
  const bound=f.calls.find(c=>c.path==='/turn'&&c.body.turn.turn_id==='main');
  assert.equal(bound.body.turn.native_input_id,nonce);assert.equal(bound.body.turn.native_message_id,'native-message');
  const next=async function*(){yield {kind:'tool',id:'call',name:'mcp__swarm_voice__publish'};return {answer:'done'}};
  for await(const c of f.hooks.get('turn.step')(f.$,{turnId:'main'},next)) assert.equal(c.id,'call');
  const tool={tool:'mcp__swarm_voice__publish',tool_use_id:'call',consent:'reserved metadata',text:'phrase',sequence:1};
  assert.equal(await f.hooks.get('tool.call')(f.$,tool,f.next),tool);
  const attestation=f.calls.find(c=>c.path==='/manager-tool');
  assert.deepEqual(attestation.body.call.arguments,{text:'phrase',sequence:1});
  assert.equal(attestation.body.call.turn_id,'main');assert.equal(attestation.body.call.tool_name,'publish');
}
{
  const f=await fixture(true,true);
  await f.hooks.get('turn.start')(f.$,{turnId:'permission-turn'},f.next);
  const stream=async function*(){yield {kind:'tool',id:'permission-call',name:'mcp__swarm_voice__publish'}};
  for await(const _ of f.hooks.get('turn.step')(f.$,{turnId:'permission-turn'},stream)) {}
  const aborted=new AbortController();
  const canceled=async e=>{aborted.abort();return e};canceled.signal=aborted.signal;
  f.response('/manager-tool-complete',async()=>{throw new Error('hook native context canceled')});
  const tool={tool:'mcp__swarm_voice__publish',tool_use_id:'permission-call',text:'neutral'};
  await f.hooks.get('tool.call')(f.$,tool,canceled);
  assert(!f.canceled(),'canceled tool context retired the session before fresh native completion');
  assert((await f.hooks.get('tool.call')(f.$,tool,f.next)).deny,'closed call reused while closure awaited fresh context');
  f.response('/manager-tool-complete',async()=>({ok:true,text:'null'}));
  const from=f.calls.length;
  await f.hooks.get('turn.complete')(f.$,{turnId:'permission-turn',reason:'answer'},f.next);
  const events=f.calls.slice(from);
  assert(events.findIndex(c=>c.path==='/manager-tool-complete')<events.findIndex(c=>c.path==='/turn'));
  assert.equal(events.at(-1).path,'/manager-idle');assert(!f.canceled());
}
{
  const f=await fixture(true,true);
  await f.hooks.get('turn.start')(f.$,{turnId:'long-main'},f.next);
  for(let i=0;i<1100;i++){
    const id='long-call-'+i;
    const stream=async function*(){yield {kind:'tool',id,name:'mcp__swarm_voice__publish'}};
    for await(const _ of f.hooks.get('turn.step')(f.$,{turnId:'long-main'},stream)){}
    const tool={tool:'mcp__swarm_voice__publish',tool_use_id:id,text:'neutral'};
    assert.equal(await f.hooks.get('tool.call')(f.$,tool,f.next),tool,'closed local cache exhausted native call admission');
  }
  assert(!f.canceled(),'closed broker-owned negative guards retained local full call cache');
}
console.log('plugin lifecycle interleavings passed');
