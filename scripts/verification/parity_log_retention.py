#!/usr/bin/env python3
"""Verify bounded live log storage using actual Go/Rust daemon and orphaned shim processes."""

import argparse
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time

LIMIT = 8 << 20
FIXTURE = """#!/bin/sh
if test "$1" = "--version"; then echo "2.1.100 (Claude Code)"; exit; fi
echo 'worker ready'
exec /bin/cat
"""


def wait(check, label, seconds=12):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if check():
            return
        time.sleep(0.08)
    raise AssertionError(label)


def observe(binary, root):
    for part in ("bin", "state", "home", "work"):
        (root / part).mkdir(mode=0o700, parents=True)
    native = root / "bin/claude"
    native.write_text(FIXTURE)
    native.chmod(0o700)
    app = root / "bin/swarm"
    shutil.copy2(binary, app)
    log = root / "state/daemon.log"
    sock = root / "state/d"
    env = {
        "PATH": f"{root}/bin:/usr/bin:/bin",
        "HOME": str(root / "home"),
        "TERM": "xterm-256color",
        "SWARM_DAEMON_STATE": str(root / "state"),
        "SWARM_DAEMON_SOCK": str(sock),
        "SWARM_DAEMON_LOG": str(log),
        "SWARM_DAEMON_LOCK": str(root / "state/l"),
    }

    def start():
        process = subprocess.Popen(
            [app, "daemon"],
            env=env,
            cwd=root / "work",
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        try:
            wait(
                lambda: sock.is_socket()
                and subprocess.run(
                    [app, "ls", "--json"], env=env, capture_output=True, timeout=2
                ).returncode
                == 0,
                "owner socket",
            )
        except BaseException:
            process.terminate()
            process.wait(timeout=4)
            raise
        return process

    def stop(process):
        if process.poll() is None:
            process.terminate()
            process.wait(timeout=5)

    worker = None
    daemon = start()
    try:

        def fill():
            with log.open("ab") as f:
                f.truncate(LIMIT + 1)
            return log.stat().st_ino

        inode = fill()
        wait(lambda: log.stat().st_size <= LIMIT, "live supervisor retention")
        assert log.stat().st_ino == inode
        child = subprocess.run(
            [
                app,
                "spawn",
                "--cli",
                "claude",
                "--dir",
                str(root / "work"),
                "--prompt",
                "Read the isolated fixture",
            ],
            env=env,
            text=True,
            capture_output=True,
            timeout=20,
            check=True,
        )
        worker = child.stdout.strip()
        stop(daemon)
        inode = fill()
        wait(lambda: log.stat().st_size <= LIMIT, "surviving shim retention")
        assert log.stat().st_ino == inode
        old = root / "original.log"
        log.rename(old)
        with log.open("wb") as replacement:
            replacement.truncate(LIMIT + 1)
        log.chmod(0o600)
        # A complete maintenance interval must pass without truncating the replacement.
        time.sleep(5.8)
        assert log.stat().st_size == LIMIT + 1
        # Restore the original binding before starting a second owner or draining its worker.
        log.rename(root / "replacement.log")
        old.rename(log)
    finally:
        if worker:
            if daemon.poll() is not None:
                daemon = start()
            subprocess.run(
                [app, "kill", worker],
                env=env,
                capture_output=True,
                timeout=12,
                check=True,
            )
        stop(daemon)
    print(
        binary.name,
        "bounded diagnostic log before and after daemon exit; replacement inode preserved",
    )


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("reference", type=Path)
    parser.add_argument("candidate", type=Path)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="lg-", dir=Path("/tmp").resolve()) as tmp:
        for i, binary in enumerate((args.reference, args.candidate)):
            observe(binary.resolve(), Path(tmp) / str(i))
