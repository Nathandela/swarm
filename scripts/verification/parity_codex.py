#!/usr/bin/env python3
"""Drive the Codex adapter with a disposable CLI, state root and PTY."""

import argparse
import json
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile

sys.dont_write_bytecode = True
from parity_runtime import attach, control, fixtures, read_control


def verify(candidate, provider, native):
    root = Path(tempfile.mkdtemp(prefix="sc-", dir=Path("/tmp").resolve()))
    daemon = None
    previous = {}
    try:
        for name in ("bin", "home", "runtime", "tmp", "work", "state", "effects"):
            (root / name).mkdir(mode=0o700)
        for source, name in ((candidate, "swarm"), (provider, "codex")):
            dest = root / "bin" / name
            shutil.copyfile(source, dest)
            dest.chmod(0o700)
        if native:
            home = root / "home/.codex"
            home.mkdir(mode=0o700)
            (home / "config.toml").write_text(f'[projects."{root}/work"]\ntrust_level = "trusted"\n')
        guard = fixtures.guard(root)
        env = fixtures.environment(root)
        command = [*guard, root / "bin/swarm"]

        def invoke(*args):
            result = subprocess.run([*command, *args], env=env, cwd=root / "work",
                                    capture_output=True, timeout=6)
            assert result.returncode == 0, (args, result.returncode, result.stderr)
            return result.stdout

        def start(log):
            process = subprocess.Popen([*command, "daemon"], env=env, cwd=root / "work", stderr=log)
            def ready():
                check = subprocess.run([*command, "ls", "--json"], env=env, cwd=root / "work",
                                       capture_output=True, timeout=3)
                return check.returncode == 0
            fixtures.wait_for(ready, "daemon socket")
            return process

        def launch():
            route = Path(env["SWARM_DAEMON_SOCK"])
            with socket.socket(socket.AF_UNIX) as conn:
                conn.settimeout(16)
                conn.connect(str(route))
                control(conn, "hello", endpoint_id="", protocol_version=1)
                endpoint = read_control(conn)["endpoint_id"]
                control(conn, "launch", endpoint_id=endpoint, launch={
                    "agent": "codex", "name": "codex fixture", "cwd": str(root / "work"),
                    "cols": 100, "rows": 30, "options": {"sandbox": "read-only"},
                })
                launched = read_control(conn)
                assert launched["op"] == "launch", launched
                return launched["session"]["id"]

        with (root / "daemon.log").open("wb") as log:
            daemon = start(log)
            identity = launch()
            row, = json.loads(invoke("ls", "--json"))
            assert row["id"] == identity and row["agent"] == "codex"
            state = root / "state/rust-v1" / identity.split("/")[1]
            owner = json.loads((state / "owner.json").read_text())
            session = json.loads((state / "session.json").read_text())
            assert session["provider"]["mode"] == "managed", session["provider"]
            fixtures.owned(root, None, previous)
            connection, fields, _ = attach(env["SWARM_DAEMON_SOCK"], identity)
            with connection:
                if not native:
                    fixtures.wait_for(lambda: b"ATTACHED" in invoke("peek", identity), "managed Codex TUI attach")
                control(connection, "detach", **fields)
                assert read_control(connection)["op"] == "detach"
            observation = state / "observation.json"

            def observed():
                if observation.exists():
                    return json.loads(observation.read_text()).get("conversation_id")
                return None

            thread = fixtures.wait_for(observed, "exact app-server thread", timeout=10)
            if not native:
                assert thread == "01997f00-face-7000-8000-00000000cde0"
                observed_row = json.loads(invoke("watch", identity, "--until", "ready_for_review", "--timeout", "4s"))
                assert observed_row["status"]["Turn"] == "idle"
            print("managed Codex", session["provider"]["version"], "joined one exact app-server thread")

            daemon.kill()
            daemon.wait(timeout=5)
            daemon = start(log)
            assert json.loads((state / "owner.json").read_text()) == owner
            assert observed() == thread
            assert json.loads(invoke("ls", "--json"))[0]["id"] == identity
            invoke("kill", identity)
            assert json.loads(invoke("watch", identity, "--until", "completed", "--timeout", "4s"))["group"] == "completed"
            print("managed Codex worker and exact native thread survived owner restart")

    finally:
        if daemon and daemon.poll() is None:
            daemon.terminate()
            daemon.wait(timeout=4)
        cleanup = fixtures.reap(root, None, previous)
        print("cleanup", cleanup)
        if cleanup["remaining"]:
            raise RuntimeError(f"fixture processes survived cleanup at {root}")
        shutil.rmtree(root)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", required=True, type=fixtures.binary_path)
    parser.add_argument("--provider", required=True, type=fixtures.binary_path)
    parser.add_argument("--native", action="store_true")
    args = parser.parse_args()
    verify(args.candidate, args.provider, args.native)
