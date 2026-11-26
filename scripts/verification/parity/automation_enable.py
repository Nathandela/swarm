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
    env["SWARM_FIXTURE_BINARY"] = str(binary)
    active_marker = root / "home/timer-active"
    timer_writes = root / "home/timer-writes"
    race = root / "home/race-spec"
    race_job = root / "home/race-job"
    if sys.platform == "darwin":
        timer = root / "home/Library/LaunchAgents/com.swarm.schedule.plist"
        units = [timer]
        controller = root / "bin/launchctl"
    else:
        units_dir = Path(env["XDG_CONFIG_HOME"]) / "systemd/user"
        timer = units_dir / "swarm-schedule.timer"
        units = [units_dir / "swarm-schedule.service", timer]
        controller = root / "bin/systemctl"
    timer.parent.mkdir(mode=0o700, parents=True)
    controller.write_text("""#!/bin/sh
case "$1" in
  --user)
    shift
    units="$XDG_CONFIG_HOME/systemd/user"
    if test "$1" = show; then
      case "$3" in
        swarm-schedule.service)
          if test -f "$units/$3"; then printf 'LoadState=loaded\nFragmentPath=%s/%s\nDropInPaths=\n' "$units" "$3"
          else printf 'LoadState=not-found\nFragmentPath=\nDropInPaths=\n'; fi ;;
        swarm-schedule.timer)
          if test -f "$units/$3"; then
            state=inactive
            test ! -f "$HOME/timer-active" || state=active
            printf 'LoadState=loaded\nActiveState=%s\nUnitFileState=enabled\nFragmentPath=%s/%s\nDropInPaths=\n' "$state" "$units" "$3"
          else printf 'LoadState=not-found\nActiveState=inactive\nUnitFileState=\nFragmentPath=\nDropInPaths=\n'; fi ;;
        *) exit 2 ;;
      esac
      exit 0
    fi
    ;;
  print)
    if test -f "$HOME/timer-active"; then printf 'path = %s/Library/LaunchAgents/com.swarm.schedule.plist\n' "$HOME"; exit 0; fi
    printf 'Could not find service "com.swarm.schedule" in domain\n' >&2
    exit 113
    ;;
esac
printf '%s\n' "$1" >> "$HOME/timer-writes"
case "$1" in
  daemon-reload|enable) ;;
  bootstrap|start)
    if test -f "$HOME/race-spec"; then
      "$SWARM_FIXTURE_BINARY" schedule edit "$(cat "$HOME/race-job")" --spec-file "$HOME/race-spec" > "$HOME/raced" || exit 3
    fi
    : > "$HOME/timer-active"
    ;;
  *) exit 2 ;;
esac
""")
    controller.chmod(0o700)
    claude = root / "bin/claude"
    hold = root / "home/preflight-held"
    started = root / "home/preflight-started"

    def provider():
        claude.write_text("""#!/bin/sh
test "$1" = --version || exit 70
if test -f "$HOME/preflight-held"; then
    : > "$HOME/preflight-started"
    while test -f "$HOME/preflight-held"; do /bin/sleep .05; done
fi
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
            if not reference:
                old_board = owner_request(env["SWARM_DAEMON_SOCK"], "schedule-catalog-control-v1", {
                    "op": "schedule_catalog_change",
                    "change": {"kind": "state", "id": job["id"], "revision": paused["revision_id"],
                               "expected": "paused", "generation": 1, "updated_at": paused["updated_at"],
                               "value": "active"},
                })
                assert old_board["op"] == "error" and "Save and enable" in old_board["error"], old_board
                assert show() == paused and not timer_writes.exists()
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
                editor = b"Automations / Edit"
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
                for unit in units:
                    unit.write_text("fixture status only\n")
                    unit.chmod(0o600)
                active_marker.touch()
                step(b"p", editor)
                revised = paused["spec"] | {"name": "Owner changed revision"}
                started.unlink(missing_ok=True)
                hold.touch()
                try:
                    step(b"\x13", b"Checking automation activation")
                    p.wait_for(started.exists, "owner passed the initial stale revision check")
                    latest = json.loads(cli("schedule","edit",job["id"],"--spec-file","-", data=json.dumps(revised).encode()))
                finally:
                    hold.unlink()
                board.wait_bytes(b"schedule job revision changed")
                assert show() == latest and latest["state"] == "paused"
                step(b"\x1b[27u", b"History")
                step(b"u", b"Owner changed revision")
                step(b"\r", b"History")
                step(b"p", editor)
                step(b"\x15Saved and enabled", b"Saved and enabled")
                start = len(board.capture)
                step(b"\x13", b"Automations / Saved and enabled")
                active = show()
                assert active["state"] == "active" and active["spec"]["name"] == "Saved and enabled"
                assert active["revision_id"] != latest["revision_id"]
                assert b"Save and enable automation?" not in bytes(board.capture[start:])
                assert not json.loads(cli("schedule","history",job["id"]))
                assert not timer_writes.exists()

                def prepare_install():
                    for unit in units:
                        unit.unlink()
                    active_marker.unlink()
                    timer_writes.unlink(missing_ok=True)
                    saved = json.loads(cli("schedule", "pause", job["id"]))
                    step(b"u", saved["spec"]["name"].encode())
                    step(b"\r", b"History")
                    step(b"p", editor)
                    step(b"\x13", b"Save and enable automation?")
                    assert show() == saved and not active_marker.exists()
                    return saved

                before_install = prepare_install()
                step(b"y", b"Automations / Saved and enabled")
                installed = show()
                assert installed["state"] == "active"
                assert installed["revision_id"] != before_install["revision_id"]
                assert all(unit.is_file() for unit in units) and active_marker.exists()
                actuator = "bootstrap" if sys.platform == "darwin" else "start"
                assert timer_writes.read_text().splitlines().count(actuator) == 1

                before_install = prepare_install()
                race.write_text(json.dumps(before_install["spec"] | {"name": "Revision changed during install"}))
                race_job.write_text(job["id"])
                step(b"y", b"host timer enabled; job save failed:")
                raced = json.loads((root / "home/raced").read_text())
                assert show() == raced and raced["state"] == "paused"
                assert active_marker.exists() and all(unit.is_file() for unit in units)
                assert timer_writes.read_text().splitlines().count(actuator) == 1
                assert not json.loads(cli("schedule", "history", job["id"]))
                print("PASS Rust paused editor, pending preflight, active/no-install, affirmative install, and partial outcome")
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
    if sys.platform not in ("darwin", "linux"):
        parser.error("this witness uses a fixture-only launchctl or systemctl")
    with tempfile.TemporaryDirectory(prefix="sap-") as temp:
        verify(args.binary.resolve(strict=True), Path(temp).resolve(), args.reference_go)
