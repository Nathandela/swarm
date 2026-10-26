import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { webcrypto } from 'node:crypto';
globalThis.crypto ??= webcrypto;
const senderSource = (await readFile(process.argv[2], 'utf8')).replaceAll('__CONFIG_PATH__', '"descriptor"');
const guardSource = (await readFile(new URL('./guard.js', import.meta.url), 'utf8')).replaceAll('__CONFIG_PATH__', '"descriptor"');
let copies = 0;
async function fixture(guard = false) {
  const source = guard ? guardSource : senderSource;
  const hooks = new Map(), calls = [];
  let id = 'original', tick, canceled = false, nextExchange;
  const nativeQueue = [];
  const $ = {
    fs: { read: async () => JSON.stringify({ generation: 'shim', socket_path: 'private', token: 'own-token' }) },
    session: { id: async () => id, version: async () => ({ version: '2.1.296' }) },
    clock: { every: (_, fn) => { tick = fn; return { cancel() { canceled = true; } }; } },
    http: { fetch: async (url, options) => {
      const path = new URL(url).pathname, body = JSON.parse(options.body);
      calls.push({ path, body });
      if (path === '/exchange' && !body.operation_id && nextExchange) return nextExchange();
      return { ok: true, text: 'null' };
    } },
    prompt: { submit: ({ text }) => new Promise(resolve => nativeQueue.push({ text, resolve })) },
  };
  const mod = await import('data:text/javascript;base64,' + Buffer.from(source + '\n//' + copies++).toString('base64'));
  mod.register((name, fn) => { hooks.set(name, fn); });
  const next = async event => event;
  next.signal = new AbortController().signal;
  await hooks.get('session.start')($, {}, next);
  return { $, calls, hooks, next, nativeQueue, tick: () => tick(), setID(value) { id = value; },
    exchange(fn) { nextExchange = fn; }, canceled: () => canceled };
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
console.log('plugin lifecycle interleavings passed');
