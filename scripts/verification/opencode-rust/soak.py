"""Sustained, localhost-only native OpenCode operations and crash recovery."""

from collections import Counter
import errno
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import socket
import time

from launch import history_page
from processes import alive, birth, sample

MIB = 1024 * 1024
BUDGETS = {'swarm_rss_bytes': 256 * MIB, 'swarm_fds': 128, 'swarm_disk_bytes': 256 * MIB,
           'whole_run_disk_bytes': 512 * MIB, 'dispatch_seconds': 30, 'rejoin_seconds': 15}


def file_sha(path):
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(MIB), b''):
            digest.update(block)
    return digest.hexdigest()


def atomic_json(path, body):
    temp = path.with_suffix('.tmp')
    temp.write_text(json.dumps(body, sort_keys=True) + '\n')
    temp.replace(path)


def allocated_bytes(root):
    total = 0
    for path in root.rglob('*'):
        try:
            total += path.lstat().st_blocks * 512
        except FileNotFoundError:
            # A concurrently replaced atomic-write temporary is absent from the sample.
            continue
    return total


def run(args, env, cli, output, wait, start, stop, provider, requests):
    root, swarm = args.root, args.swarm
    launcher = Path(shutil.which('opencode', path=env['PATH']))
    native = args.native or launcher
    launcher_sha = file_sha(launcher)
    started = time.monotonic()
    deadline = started + args.duration
    stop_requested = False

    def cancelled(_signal, _frame):
        nonlocal stop_requested
        stop_requested = True

    signal.signal(signal.SIGTERM, cancelled)
    signal.signal(signal.SIGINT, cancelled)
    config = root / 'config/opencode/opencode.json'
    config_sha = file_sha(config)
    result = {
        'state': 'running', 'pid': os.getpid(), 'target': platform.system(),
        'arch': platform.machine(), 'kernel_release': platform.release(),
        'started_wall': time.time(), 'duration_seconds': args.duration, 'interval_seconds': args.interval,
        'source_sha': args.source, 'swarm_sha256': file_sha(swarm), 'opencode_sha256': file_sha(native),
        'opencode_version': '1.19.3', 'launcher_sha256': launcher_sha, 'fixture_sha256': {
            name: file_sha(Path(__file__).with_name(name))
            for name in ('launch.py', 'loopback.py', 'processes.py', 'soak.py')
        },
        'budgets': BUDGETS, 'inputs': 0, 'restarts': 0, 'cards': 0, 'maxima': {},
    }
    daemon = session = None
    owned = {}
    effect_counts = Counter()
    turn_ids = set()

    def monitor(event):
        health = {role: sample(pid) for role, (pid, started) in owned.items() if alive(pid, started)}
        assert health.keys() == owned.keys(), f'owned process disappeared: {event}'
        usage = allocated_bytes(root)
        state_disk = allocated_bytes(root / 'state') + (root / 'daemon.log').stat().st_blocks * 512
        for role, stats in health.items():
            previous = result['maxima'].setdefault(role, {})
            for key, val in stats.items():
                previous[key] = max(val, previous.get(key, 0))
            if role != 'provider':
                assert stats['rss_bytes'] <= BUDGETS['swarm_rss_bytes'], (role, stats)
                assert stats['fds'] <= BUDGETS['swarm_fds'], (role, stats)
        assert state_disk <= BUDGETS['swarm_disk_bytes'], state_disk
        assert usage <= BUDGETS['whole_run_disk_bytes'], usage
        for key, val in [('state_disk_bytes', state_disk), ('total_disk_bytes', usage)]:
            result['maxima'][key] = max(val, result['maxima'].get(key, 0))
        record = dict(event=event, elapsed_seconds=round(time.monotonic() - started, 3),
                      wall=time.time(), input=result['inputs'], process=health,
                      state_disk_bytes=state_disk, total_disk_bytes=usage)
        with (root / 'metrics.jsonl').open('a') as log:
            log.write(json.dumps(record, sort_keys=True) + '\n')
        atomic_json(root / 'heartbeat.json', dict(result, observation=record))

    def row():
        return next((entry for entry in output('ls', '--json') if entry['id'] == session), None)

    def idle():
        current = row()
        return current and current['status']['Process'] == 'running' and current['status']['Turn'] == 'idle'

    def conversation():
        history = history_page(env, session, 4)
        return tuple(history[field] for field in
                     ('native_session_id', 'native_created_at_ms', 'session_instance')), history['history']

    try:
        daemon = start()
        owned['daemon'] = daemon.pid, birth(daemon.pid)
        atomic_json(root / 'owned.json', owned)
        cli('spawn', '--cli', 'opencode', '--dir', str(root / 'project'),
            '--model', 'fixture/fixture', '--prompt', 'Fixture initialization. Reply fixture only.')
        rows = output('ls', '--json')
        assert len(rows) == 1, rows
        session = rows[0]['id']
        wait(idle, 30)
        state = root / 'state/rust-v1' / session.split('/')[-1]
        identity_path = state / 'owner.json'
        registry = json.loads(identity_path.read_text())
        owned['shim'] = registry['pid'], registry['birth']
        owned['provider'] = registry['worker_pid'], registry['worker_birth']
        atomic_json(root / 'owned.json', owned)
        key, _ = conversation()
        assert all(key), key
        monitor('native_ready')

        while not stop_requested and (time.monotonic() < deadline or result['inputs'] == 0):
            index = result['inputs'] + 1
            marker = f'RUST_SOAK_{index:06}'
            text = f'{marker}. Reply fixture only.'
            if index % 5 == 1:
                text += ' RUST_SEMANTIC'
            operation = f'semantic:{time.time_ns() // 1000000}:{index:032x}'
            begin = time.monotonic()
            initial = output('message', session, '--operation-id', operation, '--text', text)
            assert initial['state'] in ('native_accepted', 'completed'), initial
            observed = time.monotonic() - begin
            assert observed <= BUDGETS['dispatch_seconds'], observed
            result['max_dispatch_seconds'] = max(observed, result.get('max_dispatch_seconds', 0))
            if 'RUST_SEMANTIC' in text:
                kinds = set()
                for _ in range(2):
                    cards = wait(lambda: output('requests', session).get('requests'))
                    pending = cards[0]
                    selector = ['--request-id', pending['request_id'],
                                '--native-session-id', pending['native_session_id']]
                    card = output('requests', session, *selector)['request']
                    choice = (['--reply', 'once'] if card['kind'] == 'permission' else
                              ['--reply', 'answer', '--answers', '[["blue"]]'])
                    receipt = output('decide', session, *selector, '--request-hash',
                                     card['request_hash'], *choice)
                    assert receipt['state'] == 'admitted', receipt
                    assert output('decide', session, '--status', '--operation-id',
                                  receipt['operation_id']) == receipt
                    kinds.add(card['kind'])
                    result['cards'] += 1
                assert kinds == {'permission', 'question'}, kinds
            persisted = state / 'opencode-receipts' / (hashlib.sha256(operation.encode()).hexdigest() + '.json')

            def completed():
                receipt = json.loads(persisted.read_text())['receipt']
                return receipt if receipt['state'] == 'completed' else None

            durable = wait(completed, 30)
            assert durable['outcome_source'] in ('reconciled_message', 'reconciled_snapshot'), durable
            source = durable['outcome_source']
            result.setdefault('outcome_sources', {}).setdefault(source, 0)
            result['outcome_sources'][source] += 1
            assert output('message', session, '--status', '--operation-id', operation) == durable
            assert output('message', session, '--operation-id', operation, '--text', text) == durable
            new_key, messages = conversation()
            assert new_key == key
            mid = durable['native_message_id']
            assert mid not in turn_ids and sum(m['info']['id'] == mid and m['info']['role'] == 'user'
                                             for m in messages) == 1, (mid, messages)
            turn_ids.add(mid)
            assert sum(req['primary'] and req['marker'] == marker for req in requests) == 1, marker
            effect_counts.update(req['marker'] for req in requests if req['marker'] == marker and req['primary'])
            result['inputs'] = index
            monitor('completed')

            if index % args.restart_every == 0:
                before = time.monotonic()
                previous = daemon.pid, birth(daemon.pid)
                daemon.kill()
                daemon.wait(timeout=5)
                assert not alive(*previous)
                daemon = start()
                owned['daemon'] = daemon.pid, birth(daemon.pid)
                atomic_json(root / 'owned.json', owned)
                wait(idle, BUDGETS['rejoin_seconds'])
                assert json.loads(identity_path.read_text()) == registry
                assert conversation()[0] == key
                assert output('message', session, '--status', '--operation-id', operation) == durable
                elapsed = time.monotonic() - before
                assert elapsed <= BUDGETS['rejoin_seconds'], elapsed
                result['max_rejoin_seconds'] = max(elapsed, result.get('max_rejoin_seconds', 0))
                result['restarts'] += 1
                monitor('rejoined')
            pause_until = min(time.monotonic() + args.interval, deadline)
            while not stop_requested and time.monotonic() < pause_until:
                time.sleep(min(0.25, max(0, pause_until - time.monotonic())))

        assert not stop_requested, 'workload stopped by signal'
        result['live_elapsed_seconds'] = round(time.monotonic() - started, 3)
        assert result['live_elapsed_seconds'] >= args.duration
        assert len(turn_ids) == len(effect_counts) == result['inputs'] and all(n == 1 for n in effect_counts.values())
        assert result['restarts'] == result['inputs'] // args.restart_every
        if args.duration >= 3600:
            assert result['restarts'] > 0 and result['inputs'] >= 25, 'insufficient workload activity'
        assert file_sha(config) == config_sha, 'provider configuration changed'
        assert file_sha(swarm) == result['swarm_sha256'] and file_sha(native) == result['opencode_sha256']
        assert file_sha(launcher) == launcher_sha
        result['state'] = 'passed'
    except BaseException as exc:
        result.update(state='failed', error=repr(exc))
        raise
    finally:
        try:
            if session is not None:
                cli('kill', session)
                wait(lambda: row()['status']['Process'] == 'exited', 8)
                wait(lambda: all(not alive(*process) for role, process in owned.items()
                                 if role != 'daemon'), 8)
            if daemon is not None:
                stop(daemon)
            assert all(not alive(*process) for process in owned.values()), 'owned process survived cleanup'
            for path in root.rglob('*.sock'):
                assert path.is_socket(), path
                with socket.socket(socket.AF_UNIX) as check:
                    check.settimeout(0.5)
                    assert check.connect_ex(str(path)) == errno.ECONNREFUSED, path
                path.unlink()
            assert not list(root.rglob('*.sock')), 'owned session socket survived cleanup'
            result['cleanup'] = 'passed'
        except BaseException as exc:
            result.update(state='failed', cleanup='failed', cleanup_error=repr(exc))
            raise
        finally:
            provider.shutdown()
            result['local_provider_calls'] = len(requests)
            result['finished_wall'] = time.time()
            atomic_json(root / 'result.json', result)
            print(json.dumps(result, sort_keys=True), flush=True)
