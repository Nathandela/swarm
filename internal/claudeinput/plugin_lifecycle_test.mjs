import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { webcrypto } from 'node:crypto';
globalThis.crypto ??= webcrypto;
const senderSource = (await readFile(process.argv[2], 'utf8')).replaceAll('__CONFIG_PATH__', '"descriptor"');
const guardSource = (await readFile(new URL('./guard.js', import.meta.url), 'utf8')).replaceAll('__CONFIG_PATH__', '"descriptor"');
let copies = 0;
async function fixture(guard = false, manager = false) {
  const source = guard ? guardSource : senderSource;
  const hooks = new Map(), calls = [];
  let id = 'original', tick, canceled = false, nextExchange, nextGuard, nextTurn;
  const responses = new Map();
  const nativeQueue = [];
  const $ = {
    fs: { read: async () => JSON.stringify({ generation: 'shim', socket_path: 'private', token: 'own-token', native_turns: true, sender_name: manager ? 'manager-sender' : 'swarm-native-input', manager_conversation_id: manager ? 'conversation' : '' }) },
    session: { id: async () => id, version: async () => ({ version: '2.1.296' }) },
    clock: { every: (_, fn) => { tick = fn; return { cancel() { canceled = true; } }; } },
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
    exchange(fn) { nextExchange = fn; }, guard(fn) {nextGuard=fn;}, turnReply(fn) {nextTurn=fn;}, canceled: () => canceled };
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
  const f = await fixture(true);
  f.turnReply(async()=>({ok:false,text:'refused'}));
  await f.hooks.get('turn.start')(f.$,{turnId:'unobserved-main'},f.next);
  assert(f.canceled()); assert.equal(f.calls.at(-1).body.state,'revoked');
  assert((await f.hooks.get('prompt.submit')(f.$,{text:'later',origin:{kind:'plugin',name:'swarm-native-input'}},f.next)).drop);
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
console.log('plugin lifecycle interleavings passed');
