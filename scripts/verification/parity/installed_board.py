#!/usr/bin/env python3
"""Qualify PTY recovery through a disposable installation from a signed release."""
import argparse
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time

import test_board as board

p = board.p


def run(dist, archive, key, evidence):
    root = Path(tempfile.mkdtemp(prefix="pi-", dir=Path("/tmp").resolve()))
    child = driver = None
    previous = {}
    try:
        for name in ("bin", "tmp", "home", "work", "runtime", "effects", "state"):
            (root / name).mkdir(mode=0o700)
        for name in ("installed_board.py", "test_board.py", "swarm-parity", "claude"):
            target = root / "bin" / name
            shutil.copyfile(board.HERE / name, target)
            target.chmod(0o700)
        installer = board.HERE.parents[1] / "release-rust.py"
        subprocess.run([sys.executable, installer, "verify", dist, archive, "--public-key", key,
                        "--prefix", root / "install", "--managed"], check=True, timeout=90)
        p.table()
        command = [*p.guard(root), sys.executable, root / "bin/installed_board.py", "_inside", root]
        child = subprocess.Popen(command, cwd=root / "work", env=p.environment(root), start_new_session=True)
        driver = (child.pid, p.generation(child.pid))
        until = time.monotonic() + 40
        while child.poll() is None:
            p.owned(root, driver, previous)
            if time.monotonic() >= until:
                raise RuntimeError("installed PTY witness timed out")
            time.sleep(0.05)
        if child.returncode:
            raise RuntimeError(f"installed PTY witness exited {child.returncode}")
        receipt = json.loads((root / "receipt.json").read_bytes())
        receipt["archive_sha256"] = p.hash_file(dist / archive)
    finally:
        cleanup = p.reap(root, driver, previous)
        if cleanup["remaining"]:
            raise RuntimeError(f"fixture cleanup has survivors in {root}: {cleanup}")
        if child is not None:
            child.wait(timeout=2)
        shutil.rmtree(root)
    receipt["cleanup"] = cleanup
    p.write_json(evidence, receipt, replace=False)
    print(f"installed managed PTY recovery passed; receipt {evidence}")


if __name__ == "__main__":
    if sys.argv[1:2] == ["_inside"]:
        root = Path(sys.argv[2])
        p.write_json(root / "receipt.json", board.witness(root, managed=root / "install"), replace=False)
    else:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument("--dist", required=True, type=Path)
        parser.add_argument("--archive", required=True)
        parser.add_argument("--public-key", required=True, type=Path)
        parser.add_argument("--evidence", required=True, type=Path)
        opts = parser.parse_args()
        run(opts.dist.resolve(), opts.archive, opts.public_key.resolve(), opts.evidence.resolve())
