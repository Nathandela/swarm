#!/usr/bin/env python3
"""Original Go managed personal room with an installed, loopback-only Codex."""
import http.server
import json
import socket
import threading
import os
from pathlib import Path
import subprocess
import sys
import tempfile

from parity_codex import model_fixture
from stopped_go_state import await_value, daemon, fixture_env, listed, owner


def run(bin, env, *args):
    result = subprocess.run([bin, *map(str, args)], env=env, capture_output=True,
                            text=True, timeout=92)
    assert result.returncode == 0, (args[0], result.returncode, result.stderr)
    return result.stdout



def discord_fixture(acknowledge):
    posts = []
    messages = {
        id: dict(id=id, type=0, channel_id="400", author=dict(id="200", username="synthetic owner"),
                 attachments=[], content=text)
        for id, text in [("501", "Original settled source"),
                         ("701", "Use project former. Retained real native loopback input."),
                         ("702", "Switch to secondary. Second retained real native loopback input.")]
    }

    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def payload(self, body):
            raw = json.dumps(body).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def do_GET(self):
            assert self.headers["Authorization"] == "Bot synthetic-unused-credential"
            if self.path == "/api/v10/users/@me":
                self.payload(dict(id="100", username="fixturebot", bot=True))
            elif self.path == "/api/v10/channels/400":
                self.payload(dict(id="400", type=1, recipients=[dict(id="200", bot=False)]))
            elif self.path == "/api/v10/channels/400/messages?limit=100":
                self.payload([messages["702"], messages["701"]] if posts else [messages["501"]])
            elif self.path.startswith("/api/v10/channels/400/messages/"):
                self.payload(messages[self.path.rsplit("/", 1)[1]])
            else:
                self.send_error(404)

        def do_POST(self):
            assert self.path == "/api/v10/channels/400/messages"
            assert self.headers["Authorization"] == "Bot synthetic-unused-credential"
            body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            assert body["enforce_nonce"] is True and body["allowed_mentions"] == {"parse": []}
            assert body["content"] == "Verified native final reply."
            nonce = body["nonce"]
            assert len(nonce) == 24 and nonce not in posts
            posts.append(nonce)
            if acknowledge:
                self.payload(dict(id=str(899 + len(posts)), channel_id="400", author=dict(id="100"),
                                  content=body["content"], nonce=nonce, attachments=[]))
            else:
                self.close_connection = True
                self.connection.shutdown(socket.SHUT_RDWR)

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, posts

def exercise(go, fixture, codex, rust_test, base, acknowledge, vm=False):
    assert os.environ.get("SWARM_NATIVE_FIXTURE_OS_ISOLATED") == "1"
    assert os.stat("/proc/self/ns/net").st_ino != int(os.environ["SWARM_NATIVE_FIXTURE_HOST_NETNS"])
    for part in ("home", "home/.codex", "bin", "work", "state"):
        (base / part).mkdir(mode=0o700)
    (base / "bin/codex").symlink_to(codex)
    env = fixture_env(base, base / "state", base / "bin")
    env["PATH"] += ":/usr/sbin:/sbin"
    env["TERM"] = "xterm-256color"
    source, calls = model_fixture(base, False, native_publication=True)
    discord, posts = discord_fixture(acknowledge)
    try:
        place = base / "connector"
        run(fixture, env, "create", place, base / "state", "personal", "true", base / "work")
        if vm:
            run(fixture, env, "project-access", place, "vm")
        if acknowledge and not vm:
            run(fixture, env, "project", place, base / "work")
            (base / "secondary").mkdir(mode=0o700)
            run(fixture, env, "project-add", place, "secondary", base / "secondary")
        run(fixture, env, "bind", place, "original-room", "400")
        assert run(fixture, env, "ingress", place, "original-room", "501", "settle").strip() == "1"
        with daemon(go, env, base / "go.log"):
            try:
                binding = json.loads(run(fixture, env, "activate", place, "original-room"))
                session = binding["session_id"]
                row, = [r for r in listed(env) if r["id"] == session]
                assert row["role"] == "manager" and row["status"]["Process"] == "running"
                assert row["manager_conversation_id"] == binding["conversation_id"]
                voice, = (place / "discussions").glob("*/voice.db")
                auth = voice.parent / "bridge.auth"
                assert auth.stat().st_mode & 0o777 == 0o600
                original_credential = auth.read_bytes()
                assert calls == []
                test_env = env | {"SWARM_TEST_DISCORD_ACTIVE_ROOT": str(base / "state"),
                                  "SWARM_TEST_DISCORD_ACTIVE_FIXTURE": fixture,
                                  "SWARM_TEST_DISCORD_REST_PORT": str(discord.server_port)}
                if vm:
                    assert binding["native_cli"] is True
                    test_env["SWARM_TEST_DISCORD_ACTIVE_VM"] = "1"
                    run(rust_test, test_env, "retained_original_go_vm_inspection", "--nocapture")
                    assert posts == [] and calls == []
                    reopened = json.loads(run(fixture, env, "activate", place, "original-room"))
                    assert reopened.pop("generation") == binding.pop("generation") + 1
                    assert reopened == binding and auth.read_bytes() == original_credential
                    return
                if not acknowledge:
                    test_env["SWARM_TEST_DISCORD_LOST_PUBLICATION_ACK"] = "1"
                run(rust_test, test_env, "retained_original_go_native_manager_roundtrip", "--nocapture")
                inference_calls = 4 if acknowledge else 2
                await_value(lambda: len(calls) >= inference_calls)
                reopened = json.loads(run(fixture, env, "activate", place, "original-room"))
                # Opening the Go voice database advances its controller generation.
                # The supervisor process, PTY, account, native observation and session
                # incarnations must all remain the originals.
                assert reopened.pop("generation") == binding.pop("generation") + 1
                assert reopened == binding
                assert auth.read_bytes() == original_credential
                assert len(calls) == inference_calls
                states = json.loads(run(fixture, env, "publication-phases", place, "original-room"))
                assert len(states) == (2 if acknowledge else 1)
                nonces = set()
                for state in states:
                    phase, export, nonce = state.split()
                    assert (phase, export) == ("sent" if acknowledge else "uncertain", "true")
                    assert len(nonce) == 24 and nonce not in nonces
                    nonces.add(nonce)
                assert set(posts) == nonces and len(posts) == len(nonces)
                assert run(fixture, env, "ingress", place, "original-room", "701", "replay").strip() == "0"
                if acknowledge:
                    assert run(fixture, env, "ingress", place, "original-room", "702", "replay").strip() == "0"
            except Exception:
                print("Pinned Go supervisor log:", (base / "go.log").read_text()[-8000:], file=sys.stderr)
                for row in listed(env):
                    print({k: row.get(k) for k in ("agent", "role", "status", "conversation_id")}, file=sys.stderr)
                raise
            finally:
                for row in listed(env):
                    if row["role"] == "manager":
                        owner(env, {"op": "kill", "session_id": row["id"]})
    finally:
        discord.shutdown()
        discord.server_close()
        source.shutdown()
        source.server_close()


if __name__ == "__main__":
    go, fixture, codex, rust_test = (str(Path(arg).resolve(strict=True)) for arg in sys.argv[1:])
    for acknowledge in (True, False):
        with tempfile.TemporaryDirectory(prefix="dn-", dir="/tmp") as directory:
            exercise(go, fixture, codex, rust_test, Path(directory), acknowledge)
    with tempfile.TemporaryDirectory(prefix="dn-vm-", dir="/tmp") as directory:
        exercise(go, fixture, codex, rust_test, Path(directory), True, vm=True)
    print("PASS retained Go manager: real Codex through Rust Gateway, authenticated final, and acknowledged or uncertain downgrade")
