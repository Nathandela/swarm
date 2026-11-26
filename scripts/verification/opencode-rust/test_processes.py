"""Witness the thread snapshot on a disposable stopped Linux child."""
import signal
import subprocess
import sys
import time

from processes import birth, thread_wait_snapshot


def verify():
    with subprocess.Popen(['/bin/sleep', '30']) as child:
        try:
            started = birth(child.pid)
            child.send_signal(signal.SIGSTOP)
            deadline = time.monotonic() + 3
            snapshot = None
            while time.monotonic() < deadline:
                snapshot = thread_wait_snapshot(child.pid, started)
                if snapshot and any(task['state'] == 'T' for task in snapshot['threads']):
                    break
                time.sleep(0.01)
            else:
                raise AssertionError(f'child stop not observed: {snapshot!r}')
            assert thread_wait_snapshot(child.pid, started + 1) is None
        finally:
            child.kill()
    assert thread_wait_snapshot(child.pid, started) is None


if __name__ == '__main__':
    if sys.platform == 'linux':
        verify()
        print('Passed stopped thread snapshot and PID birth fencing')
    else:
        print('Skipped Linux procfs thread snapshot')
