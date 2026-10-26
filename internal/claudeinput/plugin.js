export function register(on) {
  on('session.start', async ($, event, next) => {
    const config = JSON.parse(await $.fs.read(__CONFIG_PATH__));
    const native = await $.session.version();
    const worker = {
      native_session_id: await $.session.id(),
      generation: globalThis.crypto.randomUUID(),
      version: native.version,
    };
    let exchanging = false;
    $.clock.every(250, async () => {
      if (exchanging) return;
      exchanging = true;
      try {
        const reply = await $.http.fetch('http://localhost/exchange', {
          method: 'POST', socketPath: config.socket_path,
          headers: { Authorization: 'Bearer ' + config.token, 'Content-Type': 'application/json' },
          body: JSON.stringify({ generation: config.generation, worker }),
        });
        if (!reply.ok) return;
        const message = JSON.parse(reply.text);
        if (!message) return;
        // Native queue admission never types into or answers a permission dialog.
        const pending = $.prompt.submit({ text: message.text });
        await $.http.fetch('http://localhost/exchange', {
          method: 'POST', socketPath: config.socket_path,
          headers: { Authorization: 'Bearer ' + config.token, 'Content-Type': 'application/json' },
          body: JSON.stringify({ generation: config.generation, worker, operation_id: message.operation_id, state: 'submitted' }),
        });
        pending.then(async result => {
          await $.http.fetch('http://localhost/exchange', {
            method: 'POST', socketPath: config.socket_path,
            headers: { Authorization: 'Bearer ' + config.token, 'Content-Type': 'application/json' },
            body: JSON.stringify({ generation: config.generation, worker, operation_id: message.operation_id,
              state: result?.drop ? 'rejected' : 'started', reason: String(result?.drop ?? '').slice(0, 512) }),
          });
        }, async error => {
          await $.http.fetch('http://localhost/exchange', {
            method: 'POST', socketPath: config.socket_path,
            headers: { Authorization: 'Bearer ' + config.token, 'Content-Type': 'application/json' },
            body: JSON.stringify({ generation: config.generation, worker, operation_id: message.operation_id,
              state: 'uncertain', reason: String(error).slice(0, 512) }),
          });
        });
      } finally { exchanging = false; }
    });
    return next(event);
  });
}
