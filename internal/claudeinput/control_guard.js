// Installed only for the explicit native-control runtime; legacy guard is unchanged.
let config, worker, timer, initialization, nonce = '';
let started = false, revoked = false, lost = false, pumping = false, sequence = 0, bytes = 0;
const queue = [], turns = new Map(), open = new Map();
const reason = 'Swarm native session ended; explicitly refresh this session';
const encoded = text => new TextEncoder().encode(text);
const id = value => typeof value === 'string' && value.length > 0 && encoded(value).length <= 128 && !/[\x00\r\n]/.test(value);
const origin = value => value && id(value.plugin) && ['prepend','user','append','builtin','core'].includes(value.tier) ? { plugin: value.plugin, tier: value.tier } : undefined;
const actor = event => event.agentId === undefined ? '' : id(event.agentId) ? event.agentId : null;
const current = async $ => !revoked && worker && await $.session.id() === worker.native_session_id && !revoked;
const post = ($, path, body) => $.http.fetch('http://localhost' + path, {
  method: 'POST', socketPath: config.socket_path,
  headers: { Authorization: 'Bearer ' + config.token, 'Content-Type': 'application/json' }, body,
});
const heartbeat = ($, state = '') => post($, '/guard', JSON.stringify({ generation: config.generation, worker,
  state, nonce, captured_sequence: sequence, queued: queue.length, lost }));
function retire($, failure = false) {
  lost ||= failure;
  if (revoked) return;
  revoked = true; nonce = '';
  try { timer?.cancel(); } catch {}
  try { if (config && worker) void heartbeat($, 'revoked').catch(() => {}); } catch {}
}
async function initialize($) {
  initialization ??= (async () => {
    config = JSON.parse(await $.fs.read(__CONFIG_PATH__));
    const native = await $.session.version();
    if (config.required_runtime !== 'claude-native-control' || native.version !== '2.1.310') throw Error('runtime mismatch');
    worker = { native_session_id: await $.session.id(), generation: globalThis.crypto.randomUUID(), version: native.version };
    timer = $.clock.every(250, async () => { await pump($); });
  })().catch(() => { retire($, true); });
  await initialization;
  try { if (!await current($)) retire($, true); } catch { retire($, true); }
}
async function pump($) {
  if (pumping || revoked || !worker) return;
  pumping = true;
  try {
    if (!await current($)) { retire($, true); return; }
    const response = await heartbeat($);
    if (revoked) return;
    if (!response.ok) { retire($, true); return; }
    const state = JSON.parse(response.text);
    if (!id(state.nonce)) { retire($, true); return; }
    nonce = state.nonce;
    const head = queue[0];
    if (!head) return;
    let accepted;
    try {
      const response = await post($, '/control', head.body);
      if (revoked) return;
      if (!response.ok) { if (response.status !== 503) retire($, true); return; }
      accepted = response;
    } catch { return; } // Lost ACK retries the exact retained bytes on the existing tick.
    const ack = JSON.parse(accepted.text); // A malformed successful ACK retires in the outer catch.
    const hash = [...new Uint8Array(await globalThis.crypto.subtle.digest('SHA-256', encoded(head.body)))].map(v => v.toString(16).padStart(2, '0')).join('');
    if (revoked) return;
    if (ack.generation !== config.generation || ack.worker_generation !== worker.generation || ack.sequence !== head.sequence || ack.report_hash !== hash || !Number.isSafeInteger(ack.spool_sequence) || ack.spool_sequence <= 0) { retire($, true); return; }
    queue.shift(); bytes -= head.size;
  } catch { retire($, true); }
  finally { pumping = false; }
}
function enqueue($, event, phase, invocation, context, data) {
  if (revoked || lost || !worker) return;
  try {
    const seq = ++sequence;
    const body = JSON.stringify({ generation: config.generation, worker, sequence: seq, invocation: invocation ?? seq, event, phase, context, data });
    const size = encoded(body).length;
    if (size > 8192 || queue.length >= 128 || bytes + size > 262144) { retire($, true); return; }
    queue.push({ body, size, sequence: seq }); bytes += size;
    return seq;
  } catch { retire($, true); }
}
function contextFor(event, next, kind) {
  const callOrigin = origin(next.origin), context = callOrigin ? { origin: callOrigin } : { origin_unknown: true };
  const a = actor(event);
  if (a === null) throw Error('invalid actor');
  if (a) context.agent_id = a;
  if (kind.startsWith('turn.')) {
    if (!id(event.turnId)) throw Error('invalid turn');
    context.turn_id = event.turnId;
    if (kind === 'turn.step') {
      if (!Number.isSafeInteger(event.index) || event.index < 0 || !Number.isSafeInteger(event.messageCount) || event.messageCount < 0) throw Error('invalid step');
      context.index = event.index; context.message_count = event.messageCount;
    }
  } else if (kind === 'tool.call' || kind === 'tool.check') {
    if (typeof event.tool !== 'string' || !event.tool || encoded(event.tool).length > 256) throw Error('invalid tool');
    context.tool = event.tool;
    if (event.tool_use_id !== undefined) { if (!id(event.tool_use_id)) throw Error('invalid call'); context.tool_use_id = event.tool_use_id; }
    if (kind === 'tool.call' && !context.tool_use_id) throw Error('missing call');
    const candidates = [...turns.values()].filter(t => t.actor === a);
    // No actor also covers out-of-loop plugin calls; definition provider is unrelated.
    if (context.tool_use_id && callOrigin?.plugin === 'engine' && callOrigin.tier === 'core' && candidates.length === 1) {
      context.turn_id = candidates[0].turn_id;
      if (candidates[0].index !== undefined) context.index = candidates[0].index;
    } else context.context_unknown = true;
  } else if (kind === 'agent.spawn') {
    if (!id(event.tool_use_id)) throw Error('invalid spawn call');
    context.tool_use_id = event.tool_use_id;
    if (event.parentAgentId !== undefined) { if (!id(event.parentAgentId)) throw Error('invalid parent'); context.parent_agent_id = event.parentAgentId; }
  }
  return context;
}
function spawnData(event) {
  if (typeof event.background !== 'boolean' || typeof event.fork !== 'boolean' || typeof event.subagentType !== 'string' || !event.subagentType || encoded(event.subagentType).length > 256 || !origin(event.provider)) throw Error('invalid spawn');
  const data = { background: event.background, fork: event.fork, subagent_type: event.subagentType, provider: origin(event.provider),
    is_teammate_present: event.isTeammate !== undefined, workflow_present: event.workflow !== undefined, resume_present: event.resume !== undefined, name_present: event.name !== undefined };
  if (event.resume !== undefined) { if (!id(event.resume?.agentId)) throw Error('invalid resume'); data.resume_agent_id = event.resume.agentId; }
  return data;
}
function uses(value) {
  if (!Array.isArray(value) || value.length > 128) throw Error('invalid tool uses');
  return value.map(v => { if (!id(v.id) || typeof v.name !== 'string' || !v.name || encoded(v.name).length > 256) throw Error('invalid tool use'); return { id: v.id, name: v.name }; });
}
function begin($, kind, event, next) {
  if (revoked) return;
  try {
    if (open.size >= 128) throw Error('open invocation capacity');
    const context = contextFor(event, next, kind);
    let data = {};
    if (kind === 'turn.start' || kind === 'turn.step') {
      const key = JSON.stringify([context.agent_id ?? '', context.turn_id]);
      if (!turns.has(key) && turns.size >= 128) throw Error('turn context capacity');
      turns.set(key, { actor: context.agent_id ?? '', turn_id: context.turn_id, index: context.index });
    }
    if (kind === 'turn.complete') {
      if (!['answer','aborted','refusal','error'].includes(event.reason) || typeof event.isAborted !== 'boolean') throw Error('invalid completion');
      data = { reason: event.reason, is_aborted: event.isAborted };
    } else if (kind === 'agent.spawn') data = spawnData(event);
    else if (kind === 'tool.check' && event.approver !== undefined) {
      if (!['host','person'].includes(event.approver)) throw Error('invalid approver'); data.approver = event.approver;
    } else if (kind === 'ui.render') {
      if (!id(event.requestId) || !Array.isArray(event.props?.questions) || event.props.questions.length < 1 || event.props.questions.length > 128) throw Error('invalid question');
      data = { request_id: event.requestId, question_count: event.props.questions.length, component: 'AskUserQuestion', surface: 'terminal' };
    }
    const invocation = enqueue($, kind, 'entered', undefined, context, data);
    if (!invocation) return;
    const frame = { kind, invocation, context, data };
    open.set(invocation, frame); return frame;
  } catch { retire($, true); }
}
function finish($, frame, phase, value) {
  if (!frame || revoked) return;
  try {
    let data = phase === 'threw' ? {} : { ...frame.data };
    if (frame.kind === 'ui.render') data = frame.data;
    if (phase === 'returned') {
      if (frame.kind === 'turn.start') {
        if (value?.turnId !== frame.context.turn_id) throw Error('invalid returned turn'); data = { turn_id: value.turnId };
      } else if (frame.kind === 'turn.step') {
        if (value?.turnId !== frame.context.turn_id || value.index !== frame.context.index || ![null,'end_turn','max_tokens','stop_sequence','tool_use','pause_turn','compaction','refusal','model_context_window_exceeded'].includes(value.stopReason)) throw Error('invalid returned step');
        data = { turn_id: value.turnId, index: value.index, stop_reason: value.stopReason, tool_uses: uses(value.toolUses) };
        if (value.serverToolUses !== undefined) data.server_tool_uses = uses(value.serverToolUses);
      } else if (frame.kind === 'tool.check') {
        if (!['allow','ask','deny'].includes(value?.decision)) throw Error('invalid decision'); data.decision = value.decision;
      } else if (frame.kind === 'tool.call') {
        if (typeof value?.isError === 'boolean') data.is_error = value.isError;
        if (typeof value?.deny === 'string') data.denied = true;
        else if (frame.context.tool === 'Agent') {
          if (['completed','async_launched','remote_launched'].includes(value?.result?.status)) {
            data.agent_status = value.result.status;
            if (value.result.agentId !== undefined) { if (!id(value.result.agentId)) throw Error('invalid Agent result actor'); data.agent_id = value.result.agentId; }
          } else data.agent_result_unknown = true;
        }
      } else if (frame.kind === 'agent.spawn') {
        if (typeof value?.deny === 'string') data.denied = true;
        else { if (!id(value?.agentId)) throw Error('missing spawned actor'); data.agent_id = value.agentId;
          if (value.teammateId !== undefined) { if (!id(value.teammateId)) throw Error('invalid teammate'); data.teammate_id = value.teammateId; } }
      }
    }
    enqueue($, frame.kind, phase, frame.invocation, frame.context, data);
    open.delete(frame.invocation);
    if (frame.kind === 'turn.complete' && phase === 'returned') turns.delete(JSON.stringify([frame.context.agent_id ?? '', frame.context.turn_id]));
  } catch { retire($, true); }
}
export function register(on) {
  on('command.run', async ($, event, next) => {
    if (['clear','resume','fork','branch','login','logout','exit'].includes(event.command)) retire($);
    return next(event);
  });
  on('session.end', async ($, event, next) => { retire($); return next(event); });
  on('session.start', async ($, event, next) => {
    if (started) { retire($); return next(event); }
    started = true; await initialize($); return next(event);
  });
  on('prompt.submit', async ($, event, next) => {
    if (event.origin.kind === 'plugin' && event.origin.name.split('@')[0] === 'swarm-native-input') {
      await initialize($);
      if (!await current($) || next.signal.aborted) { retire($); return { drop: reason }; }
    }
    return next(event);
  });
  on('turn.start', async ($, event, next) => {
    await initialize($); const frame = begin($, 'turn.start', event, next);
    try { const result = await next(event); finish($, frame, 'returned', result); return result; }
    catch (error) { finish($, frame, 'threw'); throw error; }
  });
  on('turn.step', async function* ($, event, next) {
    await initialize($); const frame = begin($, 'turn.step', event, next); let terminal = false;
    try { const result = yield* next(event); terminal = true; finish($, frame, 'returned', result); return result; }
    catch (error) { terminal = true; finish($, frame, 'threw'); throw error; }
    finally { if (!terminal) retire($, true); }
  });
  on('turn.complete', async ($, event, next) => {
    await initialize($); const frame = begin($, 'turn.complete', event, next);
    try { const result = await next(event); finish($, frame, 'returned', result); return result; }
    catch (error) { finish($, frame, 'threw'); throw error; }
  });
  on('tool.call', async ($, event, next) => {
    await initialize($); const frame = begin($, 'tool.call', event, next);
    try { const result = await next(event); finish($, frame, 'returned', result); return result; }
    catch (error) { finish($, frame, 'threw'); throw error; }
  });
  on('tool.check', async ($, event, next) => {
    await initialize($); const frame = begin($, 'tool.check', event, next);
    try { const result = await next(event); finish($, frame, 'returned', result); return result; }
    catch (error) { finish($, frame, 'threw'); throw error; }
  });
  on('agent.spawn', async ($, event, next) => {
    await initialize($); const frame = begin($, 'agent.spawn', event, next);
    try { const result = await next(event); finish($, frame, 'returned', result); return result; }
    catch (error) { finish($, frame, 'threw'); throw error; }
  });
  on('ui.render', { component: 'AskUserQuestion', surface: 'terminal' }, async ($, event, next) => {
    await initialize($); const frame = begin($, 'ui.render', event, next);
    try { const node = await next(event); finish($, frame, 'returned'); return node; }
    catch (error) { finish($, frame, 'threw'); throw error; }
  });
}
