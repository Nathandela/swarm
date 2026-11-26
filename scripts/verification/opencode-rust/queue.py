#!/usr/bin/env python3
"""Put one frozen OpenCode development build through real elapsed soak stages."""

import argparse
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import time

from processes import alive, birth
from soak import BUDGETS, atomic_json, file_sha

STAGES = (('1h', 3600, 10), ('24h', 86400, 60), ('72h', 259200, 60))
CORE = ('launch.py', 'loopback.py', 'processes.py', 'soak.py', 'queue.py')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('root', type=Path, help='private root created by mktemp')
    parser.add_argument('swarm', type=Path)
    parser.add_argument('source')
    parser.add_argument('--mixed', action='store_true', help='run the 24h mixed workload')
    parser.add_argument('--stage', choices=[name for name, _, _ in STAGES], action='append')
    args = parser.parse_args()
    mac = sys.platform == 'darwin'
    root, binary = args.root, args.swarm
    native = Path(shutil.which('opencode') if mac else Path.home() / '.local/bin/opencode').resolve(strict=True)
    fixtures = Path(__file__).resolve().parent
    runner = 'run-macos-loopback.sh' if mac else 'run-linux.sh'
    assert root.is_absolute() and root == root.resolve()
    assert root.parent == Path('/private/tmp' if mac else '/dev/shm') and root.name.startswith('oc.')
    assert root.stat().st_uid == os.getuid() and root.stat().st_mode & 0o777 == 0o700
    assert binary.is_absolute() and os.access(binary, os.X_OK)
    if not mac:
        assert binary == root / 'swarm' and fixtures == root
    if args.mixed and args.stage and args.stage != ['24h']:
        parser.error('the mixed queue has a single 24h stage')
    stages = [('24h', 86400, 45)] if args.mixed else [
        stage for stage in STAGES if not args.stage or stage[0] in args.stage
    ]
    extras = (fixtures / 'mixed.py', root / 'bin/codex', root / 'bin/claude') if args.mixed else ()
    assert shutil.disk_usage(root).free >= (len(stages) + 1) * BUDGETS['whole_run_disk_bytes'], 'insufficient scratch capacity'
    manifest = {str(path): file_sha(path) for path in
                (binary, native, *(fixtures / name for name in (*CORE, runner)), *extras)}
    assert not (root / 'status.json').exists(), 'queue root already used'

    def metadata(duration, interval):
        flags = ['--duration', str(duration), '--interval', str(interval), '--source', args.source]
        return flags + (['--mixed', '--restart-every', '10'] if args.mixed else [])

    for name, duration, interval in stages:
        subprocess.run([sys.executable, '-B', fixtures / 'loopback.py', root / name, binary,
                        *metadata(duration, interval), '--check-arguments'], check=True)

    queue = {'pid': os.getpid(), 'birth': birth(os.getpid()), 'source_sha': args.source,
             'manifest': manifest, 'completed': [], 'planned': [name for name, _, _ in stages],
             'started_wall': time.time()}
    stop = False

    def cancelled(_signal, _frame):
        nonlocal stop
        stop = True

    signal.signal(signal.SIGTERM, cancelled)
    signal.signal(signal.SIGINT, cancelled)

    def status(**fields):
        queue.update(fields, updated_wall=time.time())
        atomic_json(root / 'status.json', queue)

    def reap(stage, child):
        if child.poll() is None:
            child.terminate()
            try:
                child.wait(90)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(5)
        if mac:
            # Each process is registered by its OS birth identity inside this private stage.
            for pid, started in owned_processes(stage):
                if alive(pid, started):
                    os.kill(pid, signal.SIGKILL)
            until = time.monotonic() + 5
            while any(alive(pid, started) for pid, started in owned_processes(stage)):
                assert time.monotonic() < until, 'owned process survived reaper'
                time.sleep(0.1)

    # Keep enough real blocks to publish the terminal status even when other VM jobs fill tmpfs.
    reserve = root / 'failure-report.reserve'
    with reserve.open('xb') as stream:
        stream.write(bytes(128 * 1024))
        stream.flush()
        os.fsync(stream.fileno())
    try:
        for name, duration, interval in stages:
            assert not stop, 'queue stopped by signal'
            assert all(file_sha(Path(path)) == digest for path, digest in manifest.items()), 'frozen input changed'
            stage = root / name
            command = [fixtures / runner, stage, binary] if mac else ['sh', fixtures / runner, root, name]
            status(state='running', stage=name)
            start = time.monotonic()
            with (root / (name + '.log')).open('xb') as log:
                child = subprocess.Popen([*command, *metadata(duration, interval)], stdin=subprocess.DEVNULL,
                                         stdout=log, stderr=log, start_new_session=True)
                try:
                    status(stage_pid=child.pid, stage_birth=birth(child.pid))
                    while child.poll() is None:
                        if shutil.disk_usage(root).free < 32 * 1024 * 1024:
                            raise RuntimeError('soak volume has less than 32 MiB free')
                        assert not stop, 'queue stopped by signal'
                        heartbeat = stage / 'heartbeat.json'
                        freshness = time.time() - heartbeat.stat().st_mtime if heartbeat.exists() else time.monotonic() - start
                        assert freshness < 180, 'stage stopped producing heartbeats'
                        assert time.monotonic() - start <= duration + 180, 'stage deadline exceeded'
                        time.sleep(2)
                finally:
                    reap(stage, child)
            if not (stage / 'result.json').is_file():
                raise RuntimeError(f'{name} exited {child.returncode} without result.json; see {name}.log')
            result = json.loads((stage / 'result.json').read_text())
            assert child.returncode == 0 and result['state'] == result['cleanup'] == 'passed', result
            assert result['live_elapsed_seconds'] >= duration and result['source_sha'] == args.source
            assert result['swarm_sha256'] == manifest[str(binary)] and result['opencode_sha256'] == manifest[str(native)]
            queue['completed'].append(name)
            status()
        status(state='passed')
    except BaseException as exc:
        reserve.unlink(missing_ok=True)
        status(state='stopped' if stop else 'failed', error=repr(exc)[:4096])
        raise
    finally:
        reserve.unlink(missing_ok=True)


def owned_processes(stage):
    processes = set()
    registry = stage / 'owned.json'
    if registry.is_file():
        processes.update(tuple(pair) for pair in json.loads(registry.read_text()).values())
    for record in (stage / 'state/rust-v1').glob('*/owner.json'):
        owner = json.loads(record.read_text())
        processes.add((owner['pid'], owner['birth']))
        if owner.get('worker_birth'):
            processes.add((owner['worker_pid'], owner['worker_birth']))
    return processes


if __name__ == '__main__':
    assert __debug__, 'assertions enforce qualification gates'
    main()
