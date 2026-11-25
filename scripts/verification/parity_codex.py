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
import time

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
        if not native:
            env["OPENAI_API_KEY"] = "synthetic-provider-canary"
            env["ANTHROPIC_API_KEY"] = "synthetic-unrelated-canary"
        command = [*guard, root / "bin/swarm"]

        def invoke(*args, expected=0, overrides=None):
            effective = {**env, **(overrides or {})}
            result = subprocess.run([*command, *args], env=effective, cwd=root / "work",
                                    capture_output=True, timeout=6)
            assert result.returncode == expected, (args, result.returncode, result.stderr)
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
            if not native:
                operation = invoke("spawn", "--new-operation-id").decode().strip()
                wrong_origin = {"OPENAI_BASE_URL": "https://discarded-origin.invalid/"}
                base = ("spawn", "--cli", "codex", "--dir", str(root / "work"), "--prompt", "none")
                invoke(*base, expected=1, overrides=wrong_origin)
                invoke(*base, "--operation-id", operation, expected=1, overrides=wrong_origin)
                assert json.loads(invoke("ls", "--json")) == []
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
                    assert b"VIEWER openai=false anthropic=false claude_hook=false" in invoke("peek", identity)
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

            def fake_http(method, path):
                with socket.socket(socket.AF_UNIX) as conn:
                    conn.settimeout(3)
                    conn.connect(str(state / "b"))
                    conn.sendall(f"{method} {path} HTTP/1.0\r\nHost: fixture\r\n\r\n".encode())
                    with conn.makefile("rb") as stream:
                        header, body = stream.read().split(b"\r\n\r\n", 1)
                    assert b"200 OK" in header
                    return body

            def turns():
                return json.loads(fake_http("GET", "/probe"))

            if not native:
                assert turns()["openai"] and not turns()["anthropic"]
                message = "isolated fixture message\n東京"
                accepted = json.loads(invoke("message", identity, "--text", message))
                operation = accepted["operation_id"]
                assert accepted["state"] == "native_accepted", accepted
                assert accepted["native_session_id"] == thread and accepted["native_turn_id"]
                assert json.loads(invoke("message", identity, "--text", message,
                                         "--operation-id", operation)) == accepted
                invoke("message", identity, "--text", "changed input", "--operation-id", operation, expected=1)
                assert turns()["turnStarts"] == 1 and turns()["lastText"] == message
                fixtures.wait_for(lambda: json.loads(observation.read_text()).get("requests_available"),
                                  "joined native request stream")
                fake_http("POST", "/fixture/request?scope=other")
                assert json.loads(invoke("requests", identity)) == {"session": identity}
                fake_http("POST", "/fixture/request")
                fixtures.wait_for(lambda: json.loads(observation.read_text()).get("requests"),
                                  "exact native request projection")
                pending = json.loads(invoke("requests", identity))["requests"]
                assert pending[0]["native_session_id"] == thread and pending[0]["request_id"] == "29"
                print("structured request summaries fence events to the exact bound thread")

            daemon.kill()
            daemon.wait(timeout=5)
            daemon = start(log)
            assert json.loads((state / "owner.json").read_text()) == owner
            assert observed() == thread
            assert json.loads(invoke("ls", "--json"))[0]["id"] == identity
            if not native:
                assert json.loads(invoke("message", identity, "--status", "--operation-id", operation)) == accepted
                assert json.loads(invoke("message", identity, "--text", message,
                                         "--operation-id", operation)) == accepted
                assert turns()["turnStarts"] == 1 and turns()["lastText"] == message
                print("one native turn accepted across exact retries and supervisor replacement")
            invoke("kill", identity)
            assert json.loads(invoke("watch", identity, "--until", "completed", "--timeout", "4s"))["group"] == "completed"
            print("managed Codex worker and exact native thread survived owner restart")
            if not native:
                (root / "work/.fixture-drop-turn-reply").touch()
                identity = launch()
                state = root / "state/rust-v1" / identity.split("/")[1]
                observation = state / "observation.json"
                fixtures.wait_for(observed, "independent thread before deliberately lost response")
                lost = f"semantic:{int(time.time() * 1000)}:{'a' * 32}"
                invoke("message", identity, "--text", "lost reply", "--operation-id", lost, expected=1)
                unknown = json.loads(invoke("message", identity, "--status", "--operation-id", lost))
                assert unknown["state"] == "outcome_unknown", unknown
                invoke("message", identity, "--text", "lost reply", "--operation-id", lost, expected=1)
                assert turns()["turnStarts"] == 1
                invoke("kill", identity)
                invoke("watch", identity, "--until", "completed", "--timeout", "4s")
                print("lost native reply stays visibly uncertain and never reissues the turn")

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
