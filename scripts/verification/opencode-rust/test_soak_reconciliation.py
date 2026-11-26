"""Exercise only the fixture's ambiguous admission path against a deterministic clock."""
import hashlib
from pathlib import Path
from subprocess import CompletedProcess, TimeoutExpired
from tempfile import TemporaryDirectory
from unittest.mock import patch

from soak import OUTCOME_UNKNOWN, atomic_json, dispatch_prompt


def verify(root):
    session = 'ep-fixture/exact-session'
    operation = 'semantic:123:0123456789abcdef0123456789abcdef'
    text = 'synthetic prompt'
    persisted = root / 'receipt.json'
    receipt = {'state': 'outcome_unknown', 'session': session, 'operation_id': operation}
    record = {'kind': 'prompt', 'text_hash': hashlib.sha256(text.encode()).hexdigest(), 'receipt': receipt}
    diagnostic = f'message: operation_id={operation}: {OUTCOME_UNKNOWN}\n'
    calls = []
    now = 100.0

    def cli(*args, check, timeout):
        assert check is False
        calls.append((args, timeout))
        if '--text' in args:
            assert timeout == 30
            return CompletedProcess(args, 1, '', diagnostic)
        assert args == ('message', session, '--status', '--operation-id', operation)
        assert timeout == 2
        raise TimeoutExpired(args, timeout)

    def without_status():
        calls.clear()
        try:
            dispatch_prompt(cli, lambda *_: None, session, operation, text, persisted, now)
        except AssertionError as error:
            assert error.args[0][-1] == diagnostic
        else:
            raise AssertionError('a mismatched operation reached reconciliation')
        assert len(calls) == 1 and '--text' in calls[0][0]

    with patch('soak.time.monotonic', side_effect=lambda: now):
        without_status()
        atomic_json(persisted, {**record, 'text_hash': 'different input'})
        without_status()
        atomic_json(persisted, record)
        diagnostic = diagnostic.replace(operation, 'semantic:other')
        without_status()
        diagnostic = f'message: operation_id={operation}: {OUTCOME_UNKNOWN}\n'
        calls.clear()

        def timed_cli(*args, **kwargs):
            nonlocal now
            try:
                return cli(*args, **kwargs)
            finally:
                now += 28 if '--text' in args else 1

        def wait(read, timeout):
            assert timeout == 1 and read() is None
            atomic_json(persisted, {**record, 'receipt': {**receipt, 'state': 'native_accepted'}})
            return read()

        admitted, reconciled_ambiguous = dispatch_prompt(timed_cli, wait, session, operation, text, persisted, now)
        assert admitted['state'] == 'native_accepted' and reconciled_ambiguous
        assert len(calls) == 2 and sum('--text' in args for args, _ in calls) == 1


if __name__ == '__main__':
    with TemporaryDirectory(prefix='oc-receipt-') as tmp:
        verify(Path(tmp))
    print('Passed exact durable reconciliation, no-effect refusal, remaining budget, and no repost')
