#!/usr/bin/env python3
"""Compare installed Go and Rust release binaries through isolated real PTYs."""

import argparse
from contextlib import ExitStack
import json
import math
import os
from pathlib import Path
import platform
import re
import select
import shutil
import subprocess
import sys
import tempfile
import time

from parity_runtime import attach, control, fixtures, read_frame, send_frame

sys.path.insert(0, str(Path(__file__).resolve().parent / 'opencode-rust'))
from processes import birth, sample

BUDGETS = {'paint_ms': 100, 'echo_ms': 10, 'idle_cpu_percent': 5}
ECHO_FIXTURE = b"""#!/bin/sh
if [ "$1" = --version ]; then
    printf '%s\\n' '2.1.100 (Claude Code)'
    exit 0
fi
printf 'fixture ready\\n'
exec /bin/cat >/dev/null
"""


class Arm:
    def __init__(self, kind, parent):
        self.root = Path(tempfile.mkdtemp(prefix='m11-', dir=parent))
        self.kind = kind
        self.session_ids = []
        self.daemon = None

    def open(self, binary):
        for name in ('bin', 'home', 'runtime', 'tmp', 'work', 'state'):
            (self.root / name).mkdir(mode=0o700)
        self.bin = self.root / 'bin/swarm'
        for source, target in ((binary, self.bin), (fixtures.HERE / 'swarm-parity', self.root / 'bin/driver')):
            shutil.copyfile(source, target)
            target.chmod(0o700)
        agent = self.root / 'bin/claude'
        agent.write_bytes(ECHO_FIXTURE)
        agent.chmod(0o700)
        self.guard = fixtures.guard(self.root)
        self.env = fixtures.environment(self.root)
        self.command = [*self.guard, self.bin]
        with (self.root / 'daemon.stderr').open('wb') as log:
            self.daemon = subprocess.Popen([*self.command, 'daemon'], stdin=subprocess.DEVNULL,
                                           cwd=self.root / 'work', env=self.env, stdout=log, stderr=log)
        fixtures.wait_for(lambda: (self.root / 'd.sock').exists(), 'private owner socket')
        assert json.loads(self.cli('ls', '--json')) == []

    def cli(self, *args):
        reply = subprocess.run([*self.command, *args], cwd=self.root / 'work', env=self.env,
                               capture_output=True, timeout=8)
        assert reply.returncode == 0, (args, reply.stderr)
        return reply.stdout

    def populate(self, size):
        for n in range(len(self.session_ids), size):
            ident = self.cli('spawn', '--cli', 'claude', '--name', f'm11-worker-{n:02}',
                             '--dir', str(self.root / 'work'), '--prompt', 'offline fixture').decode().strip()
            self.session_ids.append(ident)
        assert len(json.loads(self.cli('ls', '--json'))) == size

    def owner(self, ident):
        local = ident.split('/')[-1]
        if self.kind == 'reference':
            obj = json.loads((self.root / 'state' / local / 'meta.json').read_text())
            return obj['shim_pid']
        obj = json.loads((self.root / 'state/rust-v1' / local / 'owner.json').read_text())
        return obj['pid']

    def processes(self):
        matches = fixtures.owned(self.root, None, {})
        # Frozen binaries live in this arm's bin directory. Match each enclosing shim
        # using its native PID in the Linux guard namespace.
        reference_pids = {self.owner(ident) for ident in self.session_ids}
        observed = {}
        for pid in matches:
            if fixtures.executable(pid) != str(self.bin):
                continue
            native = pid
            if sys.platform.startswith('linux'):
                stat = Path(f'/proc/{pid}/status').read_text()
                native = next(int(row.split()[-1]) for row in stat.splitlines() if row.startswith('NSpid:'))
            role = f'shim{native}' if native in reference_pids else 'daemon'
            assert role not in observed, (role, observed)
            observed[role] = (pid, birth(pid), sample(pid))
        assert len(observed) == len(self.session_ids) + 1 and 'daemon' in observed, observed
        return observed

    def close(self):
        if self.daemon and self.daemon.poll() is None:
            self.daemon.terminate()
            try:
                self.daemon.wait(5)
            except subprocess.TimeoutExpired:
                self.daemon.kill()
                self.daemon.wait(5)
        report = fixtures.reap(self.root, None, {})
        assert not report['remaining'], (self.root, report)
        return report

    def paint(self):
        start = time.perf_counter_ns()
        terminal = fixtures.Board(self.command, self.root, self.env)
        try:
            # Both edge rows plus all unique session labels must reach the public PTY.
            needles = [f'm11-worker-{index:02}'.encode() for index in range(len(self.session_ids))]
            deadline = time.monotonic() + 5
            while not all(name in terminal.capture for name in needles):
                remaining = deadline - time.monotonic()
                if remaining <= 0 or not select.select([terminal.master], [], [], remaining)[0]:
                    raise AssertionError('50-session dashboard did not paint within deadline')
                terminal.drain()
            return (time.perf_counter_ns() - start) / 1e6
        finally:
            # Consume the remaining paint while the board restores the terminal.
            if terminal.child.poll() is None:
                terminal.send(b'\x1b[27u')
                quit_deadline = time.monotonic() + 1
                while terminal.child.poll() is None and time.monotonic() < quit_deadline:
                    if select.select([terminal.master], [], [], 0.05)[0]:
                        terminal.capture.clear()
                        terminal.drain()
            terminal.close()

    def echo(self, count):
        connection, fields, _ = attach(self.root / 'd.sock', self.session_ids[0])
        elapsed = []
        # Consume each canonical line in order. The drain agent emits no response of its own.
        with connection:
            for index in range(64 + count):
                alphabet = b'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789'
                keys = bytes((alphabet[index % len(alphabet)],))
                started = time.perf_counter_ns()
                send_frame(connection, 3, keys + b'\n')
                seen = bytearray()
                while keys + b'\r\n' not in seen:
                    tag, payload = read_frame(connection)
                    assert tag == 2, (tag, payload[:60])
                    seen.extend(payload)
                    assert len(seen) < 8192
                if index >= 64:
                    elapsed.append((time.perf_counter_ns() - started) / 1e6)
            control(connection, 'detach', **fields)
        return elapsed

    def cycles(self, count):
        before = self.processes()
        for _ in range(count):
            conn, fields, _ = attach(self.root / 'd.sock', self.session_ids[0])
            with conn:
                control(conn, 'detach', **fields)
        time.sleep(0.5)
        after = self.processes()
        assert {k: v[:2] for k, v in before.items()} == {k: v[:2] for k, v in after.items()}
        return {role: {'before': before[role][2], 'after': after[role][2]} for role in before}


def describe(samples):
    ordered = sorted(samples)
    return {
        'p50': ordered[math.ceil(len(ordered) * .50) - 1],
        'p95': ordered[math.ceil(len(ordered) * .95) - 1],
        'p99': ordered[math.ceil(len(ordered) * .99) - 1],
        'tail': ordered[-5:],
        'samples': samples,
    }


def main():
    assert __debug__
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--reference', required=True, type=fixtures.binary_path)
    parser.add_argument('--reference-source', required=True)
    parser.add_argument('--candidate', required=True, type=fixtures.binary_path)
    parser.add_argument('--candidate-source', required=True)
    parser.add_argument('--out', required=True, type=Path)
    parser.add_argument('--scratch', type=Path, default=Path('/tmp').resolve())
    parser.add_argument('--smoke', action='store_true', help='short calibration, no gate claim')
    parser.add_argument('--quiet-host', action='store_true', help='operator confirmed no concurrent workload')
    args = parser.parse_args()
    for source in (args.reference_source, args.candidate_source):
        if not re.fullmatch('[0-9a-f]{40}', source):
            parser.error('both source pins must be full Git commit hashes')
    assert not args.out.exists(), 'fresh evidence path required'
    paint_count, echo_count, idle_seconds, cycles = (2, 30, 3, 3) if args.smoke else (25, 1000, 60, 100)
    result = {'platform': platform.platform(), 'machine': platform.machine(),
              'budgets': BUDGETS, 'operator_quiet_host': args.quiet_host,
              'smoke': args.smoke, 'result': 'running', 'arm': {},
              'load_before': os.getloadavg()}
    fixtures.GEOMETRY = (120, 200)
    try:
        fixtures.table()
        with ExitStack() as cleanup:
            pair = []
            for binary, kind, source in ((args.reference, 'reference', args.reference_source),
                                         (args.candidate, 'candidate', args.candidate_source)):
                arm = Arm(kind, args.scratch)
                evidence = result['arm'][kind] = {'sha256': fixtures.hash_file(binary), 'source': source,
                                                 'paint_ms': [], 'scratch': str(arm.root)}

                def close(owned=arm, entry=evidence):
                    entry['cleanup'] = owned.close()

                cleanup.callback(close)
                arm.open(binary)
                arm.populate(3)
                pair.append((arm, evidence))
            for arm, evidence in pair:
                evidence['echo_ms'] = describe(arm.echo(echo_count))
                evidence['attach_cycles'] = arm.cycles(cycles)
                first = arm.processes()
                start = time.monotonic()
                time.sleep(idle_seconds)
                second = arm.processes()
                elapsed = time.monotonic() - start
                assert {k: v[:2] for k, v in first.items()} == {k: v[:2] for k, v in second.items()}
                usage = sum(second[role][2]['cpu_seconds'] - view[2]['cpu_seconds']
                            for role, view in first.items())
                evidence['idle'] = {'elapsed_seconds': elapsed, 'one_core_percent': 100 * usage / elapsed,
                                    'start': {k: v[2] for k, v in first.items()},
                                    'end': {k: v[2] for k, v in second.items()}}
            for arm, _ in pair:
                arm.populate(50)
            for number in range(paint_count):
                for arm, evidence in pair[::1 if number % 2 == 0 else -1]:
                    evidence['paint_ms'].append(arm.paint())
            for _, evidence in pair:
                evidence['paint_ms'] = describe(evidence['paint_ms'])
                evidence['numeric_gates_passed'] = (
                    evidence['paint_ms']['p95'] < BUDGETS['paint_ms']
                    and evidence['echo_ms']['p95'] < BUDGETS['echo_ms']
                    and evidence['idle']['one_core_percent'] <= BUDGETS['idle_cpu_percent']
                )
            result['result'] = ('calibrated' if args.smoke or not args.quiet_host else
                                'passed' if all(v['numeric_gates_passed'] for _, v in pair) else 'failed')
    except BaseException as exc:
        result.update(result='failed', error=repr(exc))
        raise
    finally:
        result['load_after'] = os.getloadavg()
        fixtures.write_json(args.out, result, replace=False)
    return result['result'] == 'failed'


if __name__ == '__main__':
    sys.exit(main())
