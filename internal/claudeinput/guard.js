let config, worker, timer;
let initialized = false, revoked = false;
let activeTurn = '', externalInput = 0;
let projection;
const admissions = new Map(), nativeTools = new Map(), pendingToolClosures = new Map();
const envelopePrefix = '[SWARM_MANAGER_INPUT_V1:';
const toolPrefix = 'mcp__swarm_voice__';
const managerMode = () => Boolean(config?.manager_conversation_id);
const owned = event => event.origin.kind === 'plugin' && event.origin.name.split('@')[0] === (config.sender_name ?? 'swarm-native-input');
const reason = 'Swarm native session ended; explicitly refresh this session';
const report = ($, state = '') => $.http.fetch('http://localhost/guard', {
  method: 'POST', socketPath: config.socket_path,
  headers: { Authorization: 'Bearer ' + config.token, 'Content-Type': 'application/json' },
  body: JSON.stringify({ generation: config.generation, worker, state }),
});
const runtime = ($, path, body) => $.http.fetch('http://localhost' + path, {
  method: 'POST', socketPath: config.socket_path,
  headers: { Authorization: 'Bearer ' + config.token, 'Content-Type': 'application/json' },
  body: JSON.stringify({ generation: config.generation, worker, ...body }),
});
const current = async $ => !revoked && worker &&
  await $.session.id() === worker.native_session_id && !revoked;
const retire = async $ => {
  revoked = true;
  timer?.cancel();
  if (config && worker) await report($, 'revoked').catch(() => {});
};
const observe = async ($, path, body) => {
  if (!config.native_turns) return;
  try {
    if (!(await runtime($, path, body)).ok) await retire($);
  } catch { await retire($); }
};
const attest = async ($, path, body) => {
  try {
    const response = await runtime($, path, body);
    if (!response.ok) { await retire($); return null; }
    return { value: JSON.parse(response.text) };
  } catch { await retire($); return null; }
};
// A canceled native tool hook cannot call host services in its expired context.
// Close locally immediately, then publish its tombstone from a fresh lifecycle/timer
// context before publishing completion or accepting another native proof.
const drainToolClosures = async $ => {
  for (const [id, call] of pendingToolClosures) {
    if (!await attest($, '/manager-tool-complete', { call })) return false;
    pendingToolClosures.delete(id);
  }
  return true;
};
const closeTool = async ($, next, call) => {
  nativeTools.set(call.call_id, { ...nativeTools.get(call.call_id), closed: true });
  if (!next.signal.aborted) {
    try {
      const response = await runtime($, '/manager-tool-complete', { call });
      if (!response.ok) await retire($);
      return;
    } catch {
      if (!next.signal.aborted) { await retire($); return; }
    }
  }
  pendingToolClosures.set(call.call_id, call);
};
export function register(on) {
  on('command.run', async ($, event, next) => {
    if (['clear', 'resume', 'fork', 'branch', 'login', 'logout', 'exit'].includes(event.command)) await retire($);
    return next(event);
  });
  on('session.end', async ($, event, next) => { await retire($); return next(event); });
  // A separate native identity is required: Claude skips the submitting plugin's hooks.
  on('prompt.submit', async ($, event, next) => {
    if (owned(event) && (!await current($) || next.signal.aborted)) {
      await retire($); return { drop: reason };
    }
    const machine = managerMode() && event.text.includes(envelopePrefix);
    if (machine) {
      if (!owned(event) || !event.text.startsWith(envelopePrefix) || next.signal.aborted) return { drop: 'Unattested machine input envelope' };
      const proof = await attest($, '/manager-prompt', { text: event.text });
      if (!proof || !proof.value?.nonce || admissions.has(proof.value.nonce)) return { drop: 'Unattested or repeated machine input envelope' };
      admissions.set(proof.value.nonce, event.text);
    }
    if ((!owned(event) || managerMode() && !machine) && await current($)) {
      await observe($, '/activity', { external_input: ++externalInput });
    }
    return next(event);
  });
  on('session.append', async ($, event, next) => {
    const stored = await next(event);
    if (!managerMode() || event.agentId !== undefined || event.door !== 'prompt' || event.message.type !== 'user') return stored;
    projection = undefined;
    if (!owned(event)) return stored;
    if (!stored?.message?.content || stored.uuid !== event.uuid || !stored.message.content.every(block => block.type === 'text')) { await retire($); return stored; }
    const text = stored.message.content.map(block => block.text).join('\n');
    if (!text.includes(envelopePrefix) && admissions.size === 0) return stored;
    const found = [...text.matchAll(/\[SWARM_MANAGER_INPUT_V1:([a-f0-9]{64})\]/g)];
    if (found.length !== 1 || !admissions.has(found[0][1])) { await retire($); return stored; }
    const nonce = found[0][1];
    if (await attest($, '/manager-append', { native_input_id: nonce, message_id: stored.uuid, projection: text })) projection = { nonce, messageID: stored.uuid, text };
    return stored;
  });
  on('turn.step', async function* ($, event, next) {
    if (!managerMode()) return yield* next(event);
    const iterator = next(event);
    for (;;) {
      const step = await iterator.next();
      if (step.done) return step.value;
      const chunk = step.value;
      if (chunk.kind === 'tool' && chunk.name.startsWith(toolPrefix)) {
        const call = { call_id: chunk.id, turn_id: event.turnId, agent_id: event.agentId, tool_name: chunk.name.slice(toolPrefix.length) };
        const old = nativeTools.get(chunk.id);
        if (nativeTools.size >= 1024 || old && JSON.stringify(old) !== JSON.stringify(call)) await retire($);
        else if (await attest($, '/manager-tool-step', { call })) nativeTools.set(chunk.id, call);
      }
      yield chunk;
    }
  });
  on('tool.call', async ($, event, next) => {
    if (!managerMode() || !event.tool.startsWith(toolPrefix)) return next(event);
    const native = nativeTools.get(event.tool_use_id);
    if (!await current($) || next.signal.aborted || !native || native.closed || native.agent_id !== event.agentId || native.tool_name !== event.tool.slice(toolPrefix.length)) return { deny: 'Missing exact native manager call proof' };
    if (!await drainToolClosures($)) return { deny: 'Native manager call closure unavailable' };
    const args = Object.fromEntries(Object.entries(event).filter(([key]) => !['tool', 'tool_use_id', 'agentId', 'consent'].includes(key)));
    const call = { ...native, arguments: args };
    if (!await attest($, '/manager-tool', { call })) return { deny: 'Native manager call proof unavailable' };
    try { return await next(event); }
    finally { await closeTool($, next, call); }
  });
  on('turn.start', async ($, event, next) => {
    if (event.agentId !== undefined) return next(event);
    activeTurn = event.turnId;
    const input = projection;
    projection = undefined;
    if (input && input.text !== event.text) { await retire($); return next(event); }
    const turn = { turn_id: activeTurn, state: 'running' };
    if (input) { turn.native_input_id = input.nonce; turn.native_message_id = input.messageID; admissions.delete(input.nonce); }
    if (await current($)) await observe($, '/turn', { turn, projection: input?.text });
    return next(event);
  });
  on('turn.complete', async ($, event, next) => {
    if (managerMode() && !await drainToolClosures($)) return next(event);
    const main = event.agentId === undefined && event.turnId === activeTurn;
    if (main) {
      activeTurn = '';
      if (await current($)) await observe($, '/turn', { turn: { turn_id: event.turnId, state: 'completed', reason: event.reason } });
    }
    const result = await next(event);
    if (managerMode() && main && activeTurn === '' && await current($)) await attest($, '/manager-idle', { turn_id: event.turnId });
    return result;
  });
  on('session.start', async ($, event, next) => {
    if (initialized) { await retire($); return next(event); }
    initialized = true;
    config = JSON.parse(await $.fs.read(__CONFIG_PATH__));
    const native = await $.session.version();
    worker = { native_session_id: await $.session.id(), generation: globalThis.crypto.randomUUID(), version: native.version };
    if (!await current($)) { await retire($); return next(event); }
    if (!(await report($)).ok) { await retire($); return next(event); }
    await observe($, '/turn', { turn: { turn_id: '', state: 'idle' } });
    if (revoked) return next(event);
    let reporting = false;
    timer = $.clock.every(250, async () => {
      if (reporting || revoked) return;
      reporting = true;
      try {
        if (!await current($)) { await retire($); return; }
        if (managerMode() && !await drainToolClosures($)) return;
        const reply = await report($);
        if (!reply.ok) { await retire($); return; }
        const interrupt = JSON.parse(reply.text);
        if (!interrupt) return;
        let state = 'rejected';
        if (await current($) && activeTurn === interrupt.turn_id) {
          try { await $.turn.abort({ turnId: interrupt.turn_id }); state = 'accepted'; }
          catch { state = 'uncertain'; }
        }
        await runtime($, '/interrupt-result', { operation_id: interrupt.operation_id, turn_id: interrupt.turn_id, state });
      } finally { reporting = false; }
    });
    const result = await next(event);
    if (managerMode() && activeTurn === '' && await current($)) await attest($, '/manager-idle', { turn_id: '' });
    return result;
  });
}
