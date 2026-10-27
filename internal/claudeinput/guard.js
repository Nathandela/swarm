let config, worker, timer;
let initialized = false, revoked = false;
const reason = 'Swarm native session ended; explicitly refresh this session';
const report = ($, state = '') => $.http.fetch('http://localhost/guard', {
  method: 'POST', socketPath: config.socket_path,
  headers: { Authorization: 'Bearer ' + config.token, 'Content-Type': 'application/json' },
  body: JSON.stringify({ generation: config.generation, worker, state }),
});
const current = async $ => !revoked && worker &&
  await $.session.id() === worker.native_session_id && !revoked;
const retire = async $ => {
  revoked = true;
  timer?.cancel();
  if (config && worker) await report($, 'revoked').catch(() => {});
};
export function register(on) {
  on('command.run', async ($, event, next) => {
    if (['clear', 'resume', 'fork', 'branch', 'login', 'logout', 'exit'].includes(event.command)) await retire($);
    return next(event);
  });
  on('session.end', async ($, event, next) => { await retire($); return next(event); });
  // A separate native identity is required: Claude skips the submitting plugin's hooks.
  on('prompt.submit', async ($, event, next) => {
    if (event.origin.kind === 'plugin' && event.origin.name.split('@')[0] === 'swarm-native-input' &&
        (!await current($) || next.signal.aborted)) {
      await retire($);
      return { drop: reason };
    }
    return next(event);
  });
  on('session.start', async ($, event, next) => {
    if (initialized) { await retire($); return next(event); }
    initialized = true;
    config = JSON.parse(await $.fs.read(__CONFIG_PATH__));
    const native = await $.session.version();
    worker = { native_session_id: await $.session.id(), generation: globalThis.crypto.randomUUID(), version: native.version };
    if (!await current($)) { await retire($); return next(event); }
    if (!(await report($)).ok) { await retire($); return next(event); }
    let reporting = false;
    timer = $.clock.every(250, async () => {
      if (reporting || revoked) return;
      reporting = true;
      try {
        if (!await current($)) { await retire($); return; }
        if (!(await report($)).ok) await retire($);
      } finally { reporting = false; }
    });
    return next(event);
  });
}
