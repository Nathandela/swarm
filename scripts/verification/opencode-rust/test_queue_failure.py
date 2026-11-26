"""Exercise a full volume at stage registration without allocating the host's disk."""
import errno
from importlib.util import module_from_spec, spec_from_file_location
import json
from pathlib import Path
import sys
import tempfile
from unittest.mock import patch

from processes import alive

spec = spec_from_file_location("soak_queue", Path(__file__).with_name("queue.py"))
queue = module_from_spec(spec)
spec.loader.exec_module(queue)


def verify(root):
    app = root / "swarm"
    native = root / ".local/bin/opencode"
    native.parent.mkdir(parents=True)
    native.touch()
    app.touch(mode=0o700)
    for fixture in queue.CORE:
        (root / fixture).touch()
    runner = root / ("run-macos-loopback.sh" if sys.platform == "darwin" else "run-linux.sh")
    runner.write_text("#!/bin/sh\nexec /bin/sleep 60\n")
    runner.chmod(0o700)
    atomic = queue.atomic_json

    def full_at_registration(path, status):
        if status.get("stage_pid") and status["state"] == "running":
            assert (root / "failure-report.reserve").stat().st_size >= 4096
            raise OSError(errno.ENOSPC, "injected storage exhaustion")
        if status["state"] == "failed":
            assert not (root / "failure-report.reserve").exists()
        atomic(path, status)

    with (
        patch.object(queue, "__file__", str(root / "queue.py")),
        patch.object(Path, "home", return_value=root),
        patch.object(queue.shutil, "which", return_value=str(native)),
        patch.object(queue, "BUDGETS", {"whole_run_disk_bytes": 1}),
        patch.object(queue, "atomic_json", side_effect=full_at_registration),
        patch.object(sys, "argv", ["queue.py", str(root), str(app), "a"*40, "--stage", "1h"]),
    ):
        try:
            queue.main()
        except OSError as error:
            assert error.errno == errno.ENOSPC
        else:
            raise AssertionError("stage registration unexpectedly succeeded")

    status = json.loads((root / "status.json").read_text())
    assert status["state"] == "failed" and status["completed"] == []
    assert not alive(status["stage_pid"], status["stage_birth"])


if __name__ == "__main__":
    parent = "/private/tmp" if sys.platform == "darwin" else "/dev/shm"
    with tempfile.TemporaryDirectory(prefix="oc.", dir=parent) as tmp:
        verify(Path(tmp))
    print("Passed stage registration ENOSPC, terminal status and child cleanup")
