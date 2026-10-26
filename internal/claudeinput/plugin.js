let config, worker, timer;
let initialized = false, revoked = false;
const reason = 'Swarm native session ended; explicitly refresh this session';
const request = ($, path, body) => $.http.fetch('http://localhost' + path, {
  method: 'POST', socketPath: config.socket_path,
  headers: { Authorization: 'Bearer ' + config.token, 'Content-Type': 'application/json' },
  body: JSON.stringify({ generation: config.generation, worker, ...body }),
});
const current = async $ => !revoked && worker &&
  await $.session.id() === worker.native_session_id && !revoked;
const revoke = async $ => {
  revoked = true;
  timer?.cancel();
  if (config && worker) await request($, '/revoke', {}).catch(() => {});
};
export function register(on) {
  // Retire before commands can replace the native conversation, including a canceled picker.
  on('command.run', async ($, event, next) => {
    if (['clear', 'resume', 'fork', 'branch', 'login', 'logout', 'exit'].includes(event.command)) await revoke($);
    return next(event);
  });
  on('session.end', async ($, event, next) => {
    await revoke($);
    return next(event);
  });
  on('session.start', async ($, event, next) => {
    if (initialized) { await revoke($); return next(event); }
    initialized = true;
    config = JSON.parse(await $.fs.read(__CONFIG_PATH__));
    const native = await $.session.version();
    worker = {
      native_session_id: await $.session.id(),
      generation: globalThis.crypto.randomUUID(),
      version: native.version,
    };
    if (!await current($)) { await revoke($); return next(event); }
    let exchanging = false;
    timer = $.clock.every(250, async () => {
      if (exchanging || revoked) return;
      exchanging = true;
      try {
        if (!await current($)) { await revoke($); return; }
        const reply = await request($, '/exchange', {});
        if (!reply.ok) return;
        const message = JSON.parse(reply.text);
        if (!message) return;
        if (!await current($)) { await revoke($); return; }
        // Native queue admission never types into or answers a permission dialog.
        const pending = $.prompt.submit({ text: message.text });
        await request($, '/exchange', { operation_id: message.operation_id, state: 'submitted' });
        pending.then(async result => {
          await request($, '/exchange', { operation_id: message.operation_id,
            state: result?.drop ? 'rejected' : 'started', reason: String(result?.drop ?? '').slice(0, 512) });
        }, async error => {
          await request($, '/exchange', { operation_id: message.operation_id,
            state: 'uncertain', reason: String(error).slice(0, 512) });
        });
      } finally { exchanging = false; }
    });
    return next(event);
  });
}
