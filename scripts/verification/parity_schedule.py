#!/usr/bin/env python3
"""Exercise an isolated native scheduler via rendered timer and fixture App Server."""

import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import plistlib
import shlex
import shutil
import socket
import subprocess
import sys
import tempfile
import time

sys.dont_write_bytecode = True
from parity_runtime import fixtures

LAUNCHCTL = """#!/bin/sh
state="$HOME/active-timer"
case "$1" in
  print)
    if [ -f "$state" ]; then
      printf 'path = %s\n' "$(cat "$state")"
    else
      printf 'Could not find service "com.swarm.schedule" in domain\n'
      exit 1
    fi ;;
  bootstrap) printf '%s' "$3" > "$state" ;;
  bootout) rm -f "$state" ;;
  *) exit 99 ;;
esac
"""

SYSTEMCTL = """#!/bin/sh
[ "$1" = "--user" ] || exit 99
state="$HOME/active-timer"
enable="$HOME/enabled-timer"
dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
case "$2" in
  show)
    path="$dir/$4"
    if [ ! -f "$path" ]; then
      printf 'LoadState=not-found\nActiveState=inactive\nFragmentPath=\nDropInPaths=\n'
      exit 4
    fi
    activity=inactive
    enabled=disabled
    [ -f "$state" ] && activity=active
    [ -f "$enable" ] && enabled=enabled
    printf 'LoadState=loaded\nActiveState=%s\nUnitFileState=%s\nFragmentPath=%s\nDropInPaths=\n' "$activity" "$enabled" "$path" ;;
  daemon-reload) : ;;
  enable) touch "$enable" ;;
  disable) rm -f "$enable" ;;
  start) touch "$state" ;;
  stop) rm -f "$state" ;;
  *) exit 99 ;;
esac
"""


def render_invocation(root):
    if sys.platform == "darwin":
        path = root / "home/Library/LaunchAgents/com.swarm.schedule.plist"
        data = plistlib.loads(path.read_bytes())
        assert len(data["StartCalendarInterval"]) == 60
        return data["ProgramArguments"], data["EnvironmentVariables"]

    directory = root / "home/.config/systemd/user"
    service = (directory / "swarm-schedule.service").read_text()
    timer = (directory / "swarm-schedule.timer").read_text()
    assert "OnCalendar=*-*-* *:*:00" in timer and "Persistent=true" in timer
    args = next(line.removeprefix("ExecStart=:") for line in service.splitlines() if line.startswith("ExecStart=:"))
    env = [shlex.split(line.removeprefix("Environment="))[0] for line in service.splitlines() if line.startswith("Environment=")]
    return shlex.split(args), dict(item.split("=", 1) for item in env)


def call_fixture(app, path, method="GET"):
    with socket.socket(socket.AF_UNIX) as conn:
        conn.settimeout(3)
        conn.connect(str(app))
        conn.sendall(f"{method} {path} HTTP/1.0\r\nHost: fixture\r\n\r\n".encode())
        with conn.makefile("rb") as reply:
            header, body = reply.read().split(b"\r\n\r\n", 1)
    assert b"200 OK" in header, header
    return body


def verify(candidate, provider):
    root = Path(tempfile.mkdtemp(prefix="sr-", dir=Path("/tmp").resolve()))
    daemon = None
    previous = {}
    try:
        for name in ("bin", "home", "runtime", "tmp", "work", "state", "effects"):
            (root / name).mkdir(mode=0o700)
        for source, name in ((candidate, "swarm"), (provider, "codex")):
            dest = root / "bin" / name
            shutil.copyfile(source, dest)
            dest.chmod(0o700)
        fake = root / "bin" / ("launchctl" if sys.platform == "darwin" else "systemctl")
        fake.write_text(LAUNCHCTL if sys.platform == "darwin" else SYSTEMCTL)
        fake.chmod(0o700)

        guard = fixtures.guard(root)
        env = fixtures.environment(root)
        executable = root / "bin/swarm"
        env.pop("XDG_CONFIG_HOME", None)

        def invoke(*args, expected=0, stdin=None, effective=None):
            command = [*guard, executable, *args]
            result = subprocess.run(command, cwd=root / "work", env=effective or env,
                                    input=stdin, capture_output=True, text=True, timeout=15)
            assert result.returncode == expected, (args, result.returncode, result.stdout, result.stderr)
            return result.stdout

        def schedule(*args, **kwargs):
            return invoke("schedule", *args, **kwargs)

        def rows():
            return json.loads(invoke("ls", "--json"))

        def history():
            return json.loads(schedule("history", job["id"])) or []

        with (root / "interactive.stderr").open("w") as log:
            daemon = subprocess.Popen([*guard, executable, "daemon"], env=env,
                                      cwd=root / "work", stderr=log)
            fixtures.wait_for(lambda: Path(env["SWARM_DAEMON_SOCK"]).exists(), "fixture owner socket")
            fixtures.wait_for(lambda: (root / "state/daemon.env").exists(), "saved native environment")
            prompt = "--scheduled fixture\nExact 東京\n"
            job = json.loads(schedule(
                "create", "--name", "Cold schedule", "--dir", str(root / "work"),
                "--agents", "codex", "--authority", "workspace-write", "--workspace", "directory",
                "--cron", "* * * * *", "--timezone", "UTC", "--catch-up", "2m", "--prompt-file", "-",
                stdin=prompt,
            ))
            schedule("timer", "install")
            assert schedule("timer", "status").strip() == "installed, active"
            tick_argv, tick_env = render_invocation(root)
            assert tick_argv[-3:] == [str(executable), "schedule", "tick"]
            assert tick_env["SWARM_DAEMON_STATE"] == str(root / "state")

            daemon.kill()
            daemon.wait(timeout=5)
            now = datetime.now(timezone.utc)
            created = datetime.fromisoformat(job["created_at"].replace("Z", "+00:00"))
            until_due = (int(created.timestamp()) // 60 + 1) * 60 - now.timestamp()
            if until_due > 0:
                time.sleep(until_due + 0.1)
            # Invoke exactly the installed unit with no fixture provider on the timer's PATH.
            cold = {"PATH": "/usr/bin:/bin", "HOME": str(root / "none"), **tick_env}
            completed = subprocess.run([*guard, *tick_argv], cwd=root / "work", env=cold,
                                       text=True, capture_output=True, timeout=15)
            assert completed.returncode == 0, completed.stderr
            first, = history()
            assert first["state"] == "working" and not first["manual"], first
            worker, = rows()
            session = worker["id"]
            session_dir = root / "state/rust-v1" / session.split("/")[1]
            obs_path = session_dir / "observation.json"

            def bound():
                return obs_path.exists() and json.loads(obs_path.read_text()).get("schedule_ready")

            fixtures.wait_for(bound, "exact idle native App Server thread", timeout=8)
            schedule("tick")
            app = session_dir / "b"

            def probe():
                return json.loads(call_fixture(app, "/probe"))

            scope = fixtures.wait_for(lambda: probe() if probe()["receipt"] else None, "accepted scoped worker receipt")
            assert scope["turnStarts"] == 1 and prompt in scope["lastText"]
            assert scope["scheduleProof"] and not scope["scheduleOwner"]
            assert not scope["openai"] and not scope["anthropic"]
            assert "sandbox_mode=\"workspace-write\"" in scope["appArgs"]
            assert scope["turnPolicy"] == {"approval": "never", "sandbox": {
                "type": "workspaceWrite", "networkAccess": False, "writableRoots": [],
                "excludeSlashTmp": True, "excludeTmpdirEnvVar": True,
            }}
            state = json.loads((session_dir / "session.json").read_text())
            assert state["schedule"]["authority"] == "workspace-write"
            assert not any(k in state["schedule"] for k in ("proof", "token"))
            receipt, = history()
            assert receipt["state"] == "working" and receipt["completion"]["summary"] == "Fixture assistant result"
            # Keep only this occurrence while repeating ticks (including at the next real minute).
            schedule("pause", job["id"])
            call_fixture(app, "/fixture/schedule-finish", "POST")
            fixtures.wait_for(lambda: (session_dir / "schedule-finish.json").exists(), "independent final assistant message")
            schedule("tick")
            ready, = history()
            assert ready["state"] == "ready" and ready["id"] == first["id"]
            assert ready["attempts"][0]["id"] == first["attempts"][0]["id"]
            assert rows()[0]["tag"] == "automation" and rows()[0]["scheduled_completed"]
            schedule("tick")
            assert probe()["turnStarts"] == 1
            invoke("kill", session)
            invoke("watch", session, "--until", "completed", "--timeout", "6s")
            def workspace_released():
                schedule("tick")
                return history()[0]["released_at"] != "0001-01-01T00:00:00Z"

            fixtures.wait_for(workspace_released, "verified shim exit and workspace release")
            schedule("timer", "uninstall")
            assert schedule("timer", "status").strip() == "not installed, inactive"
            print("Cold generated timer restored private PATH, admitted one exact worker, scoped receipt, verified final assistant item and released workspace.")
    finally:
        if daemon and daemon.poll() is None:
            daemon.terminate()
            daemon.wait(timeout=5)
        cleanup = fixtures.reap(root, None, previous)
        print("cleanup", cleanup)
        if cleanup["remaining"]:
            raise RuntimeError(f"fixture processes survived cleanup at {root}")
        shutil.rmtree(root)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", required=True, type=fixtures.binary_path)
    parser.add_argument("--provider", required=True, type=fixtures.binary_path)
    args = parser.parse_args()
    verify(args.candidate, args.provider)
