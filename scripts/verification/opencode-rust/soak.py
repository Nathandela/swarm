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
import sqlite3
import subprocess
import time

from launch import history_page
from processes import alive, birth, sample, thread_wait_snapshot

MIB = 1024 * 1024
BUDGETS = {'swarm_rss_bytes': 256 * MIB, 'swarm_fds': 128, 'swarm_disk_bytes': 256 * MIB,
           'whole_run_disk_bytes': 512 * MIB, 'dispatch_seconds': 30, 'rejoin_seconds': 15}
OUTCOME_UNKNOWN = 'Native semantic submission outcome is unknown; inspect --status'


def dispatch_prompt(cli, wait, session, operation, text, persisted, begin):
    deadline = begin + BUDGETS['dispatch_seconds']

    def remaining():
        return max(0, deadline - time.monotonic())

    text_hash = hashlib.sha256(text.encode()).hexdigest()

    def recorded():
        try:
            record = json.loads(persisted.read_text())
        except (FileNotFoundError, ValueError):
            return None
        if not isinstance(record, dict):
            return None
        receipt = record.get('receipt')
        if (isinstance(receipt, dict) and isinstance(receipt.get('state'), str)
                and record.get('kind') == 'prompt' and record.get('text_hash') == text_hash
                and receipt.get('session') == session and receipt.get('operation_id') == operation):
            return receipt
        return None

    args = ('message', session, '--operation-id', operation, '--text', text)
    attempt = cli(*args, check=False, timeout=remaining())
    if attempt.returncode == 0:
        initial = json.loads(attempt.stdout)
        if initial['state'] != 'outcome_unknown':
            return initial, False
    else:
        assert (attempt.returncode == 1 and not attempt.stdout
                and attempt.stderr == f'message: operation_id={operation}: {OUTCOME_UNKNOWN}\n'), (
                    args, attempt.stdout, attempt.stderr)
    initial = recorded()
    assert initial is not None, (args, attempt.stdout, attempt.stderr)
    if initial['state'] != 'outcome_unknown':
        return initial, True

    # Read at most once through the live owner, then let the background checkpoint settle.
    timeout = min(5, remaining())
    if timeout > 0:
        try:
            cli('message', session, '--status', '--operation-id', operation, check=False, timeout=timeout)
        except (OSError, subprocess.TimeoutExpired):
            pass

    def resolved():
        receipt = recorded()
        return receipt if receipt and receipt['state'] != 'outcome_unknown' else None

    try:
        return wait(resolved, remaining()), True
    except AssertionError as error:
        raise AssertionError((args, attempt.stdout, attempt.stderr, 'read-only reconciliation deadline')) from error


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


def host_pressure():
    pressure = {}
    for resource in ('cpu', 'io', 'memory'):
        try:
            pressure[resource] = (Path('/proc/pressure') / resource).read_text().splitlines()
        except OSError:
            continue
    return pressure


def run(args, env, cli, output, wait, start, stop, provider, requests):
    root, swarm = args.root, args.swarm
    launcher = Path(shutil.which('opencode', path=env['PATH']))
    native = args.native or launcher
    launcher_sha = file_sha(launcher)
    started = time.monotonic()
    deadline = started + args.duration
    stop_signal = None

    def cancelled(number, _frame):
        nonlocal stop_signal
        stop_signal = signal.Signals(number).name

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
    dispatch = None
    owned = {}
    effect_counts = Counter()
    turn_ids = set()
    mixed = None
    if args.mixed:
        from mixed import Mixed
        mixed = Mixed(root, env, cli, output, wait, owned)
        result['mixed'] = mixed.stats
        result['fixture_sha256']['mixed.py'] = file_sha(Path(__file__).with_name('mixed.py'))
        result['fixture_sha256'].update({name: file_sha(root / 'bin' / name) for name in ('claude', 'codex')})

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
                      state_disk_bytes=state_disk, total_disk_bytes=usage, host_pressure=host_pressure())
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

    def completed_initial_input():
        key, messages = conversation()
        user = opening['bridge_receipt']['messageID']
        assert key == tuple(opening['identity'][field] for field in
                            ('rootID', 'nativeCreatedAtMS', 'sessionInstance')), key
        assert user
        if not any(m['info']['id'] == user and m['info']['role'] == 'user' for m in messages):
            return None
        if not any(m['info']['role'] == 'assistant' and m['info'].get('parentID') == user
                   and m['info'].get('finish') == 'stop' and m['info'].get('time', {}).get('completed')
                   for m in messages):
            return None
        return key if idle() else None

    try:
        daemon = start()
        owned['daemon'] = daemon.pid, birth(daemon.pid)
        atomic_json(root / 'owned.json', owned)
        cli('spawn', '--cli', 'opencode', '--dir', str(root / 'project'),
            '--model', 'fixture/fixture', '--prompt', 'Fixture initialization. Reply fixture only.')
        rows = output('ls', '--json')
        assert len(rows) == 1, rows
        session = rows[0]['id']
        state = root / 'state/rust-v1' / session.split('/')[-1]
        identity_path = state / 'owner.json'
        registry = json.loads(identity_path.read_text())
        owned['shim'] = registry['pid'], registry['birth']
        owned['provider'] = registry['worker_pid'], registry['worker_birth']
        atomic_json(root / 'owned.json', owned)
        opening = json.loads((state / 'opencode-initial.json').read_text())
        assert opening['state'] == 'native_accepted', opening['state']
        # The launch receipt proves native admission. Roster idle can still predate the initial completion.
        key = wait(completed_initial_input, 30)
        assert all(key), key
        if mixed:
            mixed.start()
            atomic_json(root / 'owned.json', owned)
        monitor('native_ready')

        while stop_signal is None and (time.monotonic() < deadline or result['inputs'] == 0):
            index = result['inputs'] + 1
            marker = f'RUST_SOAK_{index:06}'
            text = f'{marker}. Reply fixture only.'
            if index % 5 == 1:
                text += ' RUST_SEMANTIC'
            operation = f'semantic:{time.time_ns() // 1000000}:{index:032x}'
            begin = time.monotonic()
            dispatch = (index, marker, operation)
            persisted = state / 'opencode-receipts' / (hashlib.sha256(operation.encode()).hexdigest() + '.json')
            initial, reconciled_ambiguous = dispatch_prompt(cli, wait, session, operation, text, persisted, begin)
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
            def completed():
                receipt = json.loads(persisted.read_text())['receipt']
                return receipt if receipt['state'] == 'completed' else None

            durable = wait(completed, 30)
            assert durable['outcome_source'] in ('reconciled_message', 'reconciled_snapshot'), durable
            source = durable['outcome_source']
            result.setdefault('outcome_sources', {}).setdefault(source, 0)
            result['outcome_sources'][source] += 1
            assert output('message', session, '--status', '--operation-id', operation) == durable
            if not reconciled_ambiguous:
                assert output('message', session, '--operation-id', operation, '--text', text) == durable
            new_key, messages = conversation()
            assert new_key == key
            mid = durable['native_message_id']
            assert mid not in turn_ids and sum(m['info']['id'] == mid and m['info']['role'] == 'user'
                                             for m in messages) == 1, (mid, messages)
            turn_ids.add(mid)
            assert sum(req['primary'] and req['marker'] == marker for req in requests) == 1, marker
            effect_counts.update(req['marker'] for req in requests if req['marker'] == marker and req['primary'])
            dispatch = None
            result['inputs'] = index
            if mixed:
                mixed.interaction(index)
                mixed.tick()
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
                if mixed:
                    mixed.rejoined()
                monitor('rejoined')
            pause_until = min(time.monotonic() + args.interval, deadline)
            while stop_signal is None and time.monotonic() < pause_until:
                if mixed:
                    mixed.tick()
                time.sleep(min(0.25, max(0, pause_until - time.monotonic())))

        assert stop_signal is None, f'workload stopped by {stop_signal}'
        result['live_elapsed_seconds'] = round(time.monotonic() - started, 3)
        assert result['live_elapsed_seconds'] >= args.duration
        assert len(turn_ids) == len(effect_counts) == result['inputs'] and all(n == 1 for n in effect_counts.values())
        assert result['restarts'] == result['inputs'] // args.restart_every
        if args.duration >= 3600:
            assert result['restarts'] > 0 and result['inputs'] >= 25, 'insufficient workload activity'
        if mixed:
            assert mixed.stats['schedule_ticks'] == mixed.stats['scheduled_completions'] > 0
            assert mixed.stats['schedule_ticks'] >= args.duration // 60 - 1
            assert mixed.stats['operator_attaches'] == mixed.stats['discord_publications'] == result['inputs']
        assert file_sha(config) == config_sha, 'provider configuration changed'
        assert file_sha(swarm) == result['swarm_sha256'] and file_sha(native) == result['opencode_sha256']
        assert file_sha(launcher) == launcher_sha
        result['state'] = 'passed'
    except BaseException as exc:
        result.update(state='failed', error=repr(exc))
        result['host_pressure_at_failure'] = host_pressure()
        if 'shim' in owned:
            result['shim_threads_at_failure'] = thread_wait_snapshot(*owned['shim'])
        if dispatch is not None:
            index, marker, operation = dispatch
            failure = result['dispatch_witness'] = {
                'index': index,
                'operation': operation,
                'elapsed_seconds': time.monotonic() - begin,
                'primary_model_requests': sum(r['marker'] == marker and r['primary'] for r in requests),
            }
            record = {}
            try:
                record = json.loads(persisted.read_text())
                if 'dispatch_observation' in record:
                    failure['dispatch_observation'] = record['dispatch_observation']
            except (OSError, ValueError):
                pass
            try:
                # A status lookup may reconcile the saved receipt. It never reissues input.
                attempt = cli('message', session, '--status', '--operation-id', operation, check=False, timeout=5)
                if attempt.returncode == 0:
                    receipt = json.loads(attempt.stdout)
                    failure['status'] = receipt['state']
                    failure['outcome_source'] = receipt.get('outcome_source')
            except Exception as witness_error:
                failure['witness_error'] = type(witness_error).__name__
            try:
                with sqlite3.connect((root / 'data/opencode/opencode.db').as_uri() + '?mode=ro',
                                     uri=True, timeout=3) as db:
                    failure['native_parts_with_marker'] = db.execute(
                        'SELECT count(*) FROM part WHERE data LIKE ?', (f'%{marker}%',)
                    ).fetchone()[0]
                    if record.get('message_id') and record.get('target'):
                        identity = (record['target'], record['message_id'])
                        failure['exact_native_user_rows'] = db.execute(
                            "SELECT count(*) FROM message WHERE session_id=? AND id=? "
                            "AND json_extract(data,'$.role')='user'", identity
                        ).fetchone()[0]
                        failure['exact_native_text_parts'] = db.execute(
                            "SELECT count(*) FROM part WHERE session_id=? AND message_id=? "
                            "AND json_extract(data,'$.type')='text' AND json_extract(data,'$.text')=?",
                            (*identity, text)
                        ).fetchone()[0]
                        failure['exact_native_assistant_children'] = db.execute(
                            "SELECT count(*) FROM message WHERE session_id=? "
                            "AND json_extract(data,'$.parentID')=? AND json_extract(data,'$.role')='assistant'",
                            identity
                        ).fetchone()[0]
            except Exception as witness_error:
                failure['native_witness_error'] = type(witness_error).__name__
        raise
    finally:
        try:
            if mixed and daemon and daemon.poll() is None:
                mixed.close()
                for entry in output('ls', '--json'):
                    if entry['id'] != session and entry['status']['Process'] == 'running':
                        cli('kill', entry['id'])
                        cli('watch', entry['id'], '--until', 'completed', '--timeout', '8s')
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
            result['cleanup_live_roles'] = [role for role, process in owned.items() if alive(*process)]
            raise
        finally:
            if daemon is not None and daemon.poll() is None:
                stop(daemon)
            provider.shutdown()
            result['local_provider_calls'] = len(requests)
            result['finished_wall'] = time.time()
            atomic_json(root / 'result.json', result)
            print(json.dumps(result, sort_keys=True), flush=True)
