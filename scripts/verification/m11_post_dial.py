#!/usr/bin/env python3
"""Measure Rust's Go N-1 initialization and render boundary against a private real owner."""

import argparse
from contextlib import ExitStack
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess


def main():
    if not __debug__:
        raise RuntimeError('run with Python assertions enabled')
    from m11_performance import Arm, BUDGETS, describe, fixtures

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--candidate', required=True, type=fixtures.binary_path)
    parser.add_argument('--candidate-source', required=True)
    parser.add_argument('--test', required=True, type=fixtures.binary_path,
                        help='optimized swarm unit test executable from the same source')
    parser.add_argument('--out', required=True, type=Path)
    parser.add_argument('--scratch', type=Path, default=Path('/tmp').resolve())
    args = parser.parse_args()
    if not re.fullmatch('[0-9a-f]{40}', args.candidate_source):
        parser.error('candidate source must be a full Git commit hash')
    assert not args.out.exists(), 'fresh evidence path required'

    result = {
        'platform': platform.platform(), 'machine': platform.machine(), 'source': args.candidate_source,
        'candidate_sha256': fixtures.hash_file(args.candidate), 'test_sha256': fixtures.hash_file(args.test),
        'budget_p95_ms': BUDGETS['paint_ms'], 'geometry': [120, 200], 'sessions': 50, 'trials': 25,
        'load_before': os.getloadavg(), 'result': 'running',
    }
    try:
        fixtures.table()
        with ExitStack() as cleanup:
            arm = Arm('candidate', args.scratch)
            result['scratch'] = str(arm.root)

            def close():
                result['cleanup'] = arm.close()

            cleanup.callback(close)
            arm.open(args.candidate)
            arm.populate(result['sessions'])
            result['owner_processes'] = len(arm.processes())
            test = arm.root / 'bin/board-test'
            shutil.copyfile(args.test, test)
            test.chmod(0o700)
            run = subprocess.run(
                [*arm.guard, test, '--ignored', '--exact', '--nocapture', 'board::tests::real_owner_first_paint'],
                cwd=arm.root / 'work',
                env=arm.env | {'SWARM_TEST_POST_DIAL_SOCKET': arm.env['SWARM_DAEMON_SOCK']},
                capture_output=True, text=True, timeout=45,
            )
            result.update(test_returncode=run.returncode, test_stdout=run.stdout, test_stderr=run.stderr)
            samples = [int(value) for value in re.findall(r'^post_dial_ns=(\d+)$', run.stdout, re.MULTILINE)]
            result['samples_ns'] = samples
            if samples:
                result['paint_ms'] = describe([value / 1e6 for value in samples])
            run.check_returncode()
            assert len(samples) == result['trials'], samples
            assert result['paint_ms']['p95'] < BUDGETS['paint_ms'], result['paint_ms']
            result['result'] = 'passed'
    except BaseException as exc:
        result.update(result='failed', error=repr(exc))
        raise
    finally:
        result['load_after'] = os.getloadavg()
        fixtures.write_json(args.out, result, replace=False)


if __name__ == '__main__':
    main()
