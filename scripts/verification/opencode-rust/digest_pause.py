"""Pause an owned native TUI across one read-only digest deadline."""

import hashlib
import json
import os
import signal
import sqlite3
import threading
import time

from processes import alive


def verify_fault(mode, root, row, cli, output, history, requests):
    state = root / 'state/rust-v1' / row.split('/')[-1]
    owner = json.loads((state / 'owner.json').read_text())
    pid, born = owner['worker_pid'], owner['worker_birth']
    assert alive(pid, born)
    text = 'RUST_SOAK_123456. Digest deadline fault witness.'
    operation = f'semantic:{time.time_ns() // 1000000}:00112233445566778899aabbccddeeff'
    receipt_path = state / 'opencode-receipts' / (hashlib.sha256(operation.encode()).hexdigest() + '.json')
    database = root / 'data/opencode/opencode.db'

    def input_count():
        with sqlite3.connect(database.as_uri() + '?mode=ro', uri=True, timeout=3) as sql:
            return sql.execute("SELECT count(*) FROM part WHERE data LIKE ?", (f'%{text}%',)).fetchone()[0]

    def resume():
        assert alive(pid, born)
        os.kill(pid, signal.SIGCONT)

    assert input_count() == 0 and not receipt_path.exists()
    pause = 7.25 if mode == 'exhaust' else 4.25
    recovery = threading.Timer(pause, resume)
    os.kill(pid, signal.SIGSTOP)
    recovery.start()
    started = time.monotonic()
    try:
        first = cli('message', row, '--operation-id', operation, '--text', text, check=False)
        elapsed = time.monotonic() - started
    finally:
        recovery.cancel()
        recovery.join()
        resume()

    if mode != 'retry':
        error = ('OpenCode bridge request failed' if mode == 'old' else
                 'OpenCode dispatch preflight: OpenCode bridge request timed out')
        assert first.returncode != 0 and error in first.stderr, first.stderr
        assert pause - 1.55 <= elapsed < pause, elapsed
        assert not receipt_path.exists() and input_count() == 0
        assert output('message', row, '--status', '--operation-id', operation)['state'] == 'unknown_operation'
        conflict = cli('message', row, '--operation-id', operation, '--text', text + ' Different.', check=False)
        assert conflict.returncode != 0 and not receipt_path.exists() and input_count() == 0
        first = cli('message', row, '--operation-id', operation, '--text', text)
    else:
        assert first.returncode == 0 and 4.0 <= elapsed < 10, (elapsed, first.stderr)

    admitted = json.loads(first.stdout)
    assert admitted['state'] in ('native_accepted', 'completed'), admitted
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        done = output('message', row, '--status', '--operation-id', operation)
        if done['state'] == 'completed':
            break
        time.sleep(0.1)
    assert done['state'] == 'completed' and alive(pid, born)
    assert output('message', row, '--operation-id', operation, '--text', text) == done
    assert json.loads(receipt_path.read_text())['receipt'] == done
    assert input_count() == 1
    assert sum(req['marker'] == 'RUST_SOAK_123456' and req['primary'] for req in requests) == 1
    assert any(m['info']['id'] == done['native_message_id'] for m in history()['history'])
    print(json.dumps({'fault': 'verified native TUI paused across first digest deadline',
                      'mode': mode, 'first_seconds': elapsed, 'native_parts': input_count(),
                      'exact_replay_once': True, 'terminal_receipt': done['state']}), flush=True)
