#!/usr/bin/env python3
"""Exercise the paused automation control on a private owner and PTY."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

from test_board import owner_request, p


def verify(binary, root, reference):
    for name in ("bin", "home", "state", "runtime", "tmp", "work"):
        (root / name).mkdir(mode=0o700)
    (root / "bin/driver").symlink_to(Path(__file__).with_name("swarm-parity").resolve())
    env = p.environment(root)
    timer = root / "home/Library/LaunchAgents/com.swarm.schedule.plist"
    timer.parent.mkdir(mode=0o700, parents=True)
    controller = root / "bin/launchctl"
    controller.write_text("""#!/bin/sh
test "$1" = print || { printf '%s\n' "$1" >> "$HOME/timer-writes"; exit 1; }
unit="$HOME/Library/LaunchAgents/com.swarm.schedule.plist"
if test -f "$unit"; then printf 'path = %s\n' "$unit"; exit 0; fi
printf 'Could not find service "com.swarm.schedule" in domain\n' >&2
exit 113
""")
    controller.chmod(0o700)
    claude = root / "bin/claude"
    hold = root / "home/preflight-held"

    def provider():
        claude.write_text("""#!/bin/sh
test "$1" = --version || exit 70
while test -f "$HOME/preflight-held"; do /bin/sleep .05; done
printf '2.1.329 (Claude Code)\n'
""")
        claude.chmod(0o700)

    provider()

    def cli(*args, data=None):
        out = subprocess.run([binary, *args], cwd=root/"work", env=env, input=data,
                             capture_output=True, timeout=8)
        assert out.returncode == 0, (args, out.stderr)
        return out.stdout

    def show():
        return json.loads(cli("schedule", "show", job["id"]))

    with (root / "owner.log").open("ab") as log:
        daemon = subprocess.Popen([binary,"daemon"], cwd=root/"work", env=env, stdout=log, stderr=log)
        def ready():
            try:
                return owner_request(env["SWARM_DAEMON_SOCK"], "subscribe", None)
            except OSError:
                return None

        try:
            p.wait_for(ready, "private owner")
            prompt = root/"task"
            prompt.write_text("Inspect the private scratch directory.")
            job = json.loads(cli("schedule","create","--name","Enable probe","--dir",str(root/"work"),
                                "--workspace","directory","--agents","claude","--authority","agent-default",
                                "--once","2099-01-01","--time","08:00","--timezone","UTC","--prompt-file",str(prompt)))
            paused = json.loads(cli("schedule","pause",job["id"]))
            board = p.Board([str(binary)], root, env)

            def step(keys, marker):
                start = len(board.capture)
                board.send(keys)
                board.wait_bytes(marker, start)

            try:
                board.wait_bytes(b"swarm")
                step(b"m", b"More")
                step(b"a", b"Enable probe")
                step(b"\r", b"History")
                editor = b"Job name" if reference else b"Edit automation"
                step(b"p", editor)
                assert show() == paused, "opening the editor changed the paused job"
                step(b"\x1b[27u", b"History")
                assert show() == paused, "cancel changed the paused job"
                if reference:
                    print("PASS Go paused p opens editor and cancel preserves the saved revision")
                    return
                claude.unlink()
                step(b"p", editor)
                step(b"\x13", b"find claude")
                assert show() == paused, "failed preflight edited or enabled the job"
                step(b"\x1b[27u", b"History")
                provider()
                step(b"p", editor)
                hold.touch()
                try:
                    step(b"\x13", b"Checking automation activation")
                    board.send(b"\x13\x1b[27u")
                    assert show() == paused
                finally:
                    hold.unlink()
                board.wait_bytes(b"Save and enable automation?")
                assert show() == paused and not timer.exists()
                step(b"\x1b[27u", editor)
                assert show() == paused and not timer.exists()
                step(b"\x1b[27u", b"History")

                # Register only the synthetic unit, then verify that the active
                # timer path skips confirmation and stores the edited revision.
                timer.write_text("fixture status only\n")
                timer.chmod(0o600)
                step(b"p", editor)
                revised = paused["spec"] | {"name": "Owner changed revision"}
                latest = json.loads(cli("schedule","edit",job["id"],"--spec-file","-", data=json.dumps(revised).encode()))
                step(b"\x13", b"revision or state changed")
                assert show() == latest and latest["state"] == "paused"
                step(b"\x1b[27u", b"History")
                step(b"u", b"Owner changed revision")
                step(b"\r", b"History")
                step(b"p", editor)
                step(b"\x15Saved and enabled", b"Saved and enabled")
                start = len(board.capture)
                step(b"\x13", b"Status")
                active = show()
                assert active["state"] == "active" and active["spec"]["name"] == "Saved and enabled"
                assert active["revision_id"] != latest["revision_id"]
                assert b"Save and enable automation?" not in bytes(board.capture[start:])
                assert not json.loads(cli("schedule","history",job["id"]))
                assert not (root/"home/timer-writes").exists()
                print("PASS Rust paused editor, cancel, failed preflight, timer confirmation, stale revision, and atomic enable")
            finally:
                os.close(board.slave)
                os.close(board.master)
                try:
                    board.child.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    board.child.kill()
                    board.child.wait(timeout=5)
        finally:
            daemon.terminate()
            daemon.wait(timeout=8)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("--reference-go", action="store_true")
    args = parser.parse_args()
    if sys.platform != "darwin":
        parser.error("this witness uses a fixture-only macOS launchctl")
    with tempfile.TemporaryDirectory(prefix="sap-", dir="/tmp") as temp:
        verify(args.binary.resolve(strict=True), Path(temp).resolve(), args.reference_go)
