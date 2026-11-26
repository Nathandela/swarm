#!/usr/bin/env python3
"""Pinned Go connector prepare/rollback with a loopback-only guild and Codex."""
import hashlib
import http.server
import fcntl
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from threading import Thread

from discord_retained_native import activate_original, run
from parity_codex import model_fixture
from stopped_go_state import await_value, daemon, fixture_env, listed, owner


TEXT = "One certified plain guild follow-up."
INSTALL = "fixture-teammate"
OP = "da_" + hashlib.sha256(json.dumps([INSTALL, "701"], separators=(",", ":")).encode()).hexdigest()[:32]
REV = hashlib.sha256(json.dumps(["201", TEXT, "401", "300", "701", None, "", None],
                                separators=(",", ":")).encode()).hexdigest()


def serve_discord():
    permissions = [True]
    wait_for_capture = [False]
    attempts = []
    messages = {mid: dict(id=mid, type=0, channel_id="401", guild_id="300",
                           author=dict(id=actor, username=name), content=content, attachments=[],
                           mentions=[], mention_roles=[], mention_everyone=False)
                for mid, actor, name, content in [("701", "201", "fixture external", TEXT),
                    ("501", "200", "fixture owner", "offline replay witness"),
                    ("500", "200", "fixture owner", "fixture starter")]}

    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_GET(self):
            assert self.headers["Authorization"] == "Bot synthetic-unused-credential"
            route = self.path.removeprefix("/api/v10")
            if route == "/users/@me":
                data = dict(id="100", bot=True, username="fixture bot")
            elif route == "/channels/401":
                data = dict(id="401", guild_id="300", type=11, parent_id="399",
                            thread_metadata=dict(archived=False, locked=False))
            elif route == "/channels/399":
                data = dict(id="399", guild_id="300", type=0)
            elif route == "/channels/399/permissions/member/100":
                bits = (1 << 10) | (1 << 16)
                if permissions[0]:
                    bits |= (1 << 38)
                data = dict(permissions=str(bits))
            elif route.startswith("/channels/401/messages?"):
                data = [messages["501"]] if wait_for_capture[0] else [messages["701"], messages["501"]]
                wait_for_capture[0] = False
            elif route.startswith("/channels/401/messages/") and route.rsplit("/", 1)[1] in messages:
                data = messages[route.rsplit("/", 1)[1]]
            else:
                raise AssertionError(route)
            raw = json.dumps(data).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def do_POST(self):
            attempts.append(self.path)
            self.send_error(400)

    rest = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    Thread(target=rest.serve_forever, daemon=True).start()
    return rest, permissions, wait_for_capture, attempts


def witness(base, go, fixture, codex, rust_test=None):
    for part in ("home", "home/.codex", "bin", "work", "state"):
        (base / part).mkdir(mode=0o700)
    (base / "bin/codex").symlink_to(codex)
    env = fixture_env(base, base / "state", base / "bin")
    env["TERM"] = "xterm-256color"
    source, calls = model_fixture(base, False)
    rest, permissions, wait_for_capture, posts = serve_discord()
    go_port = str(rest.server_port)
    place = base / "connector"
    invoke = lambda *args: run(fixture, env, *args)
    pending = lambda: (json.loads(invoke("ingress-state", place, "original-room"))["pending"] or [])
    native = lambda voice: json.loads(invoke("guild-native", voice.parent, "701"))
    def rust(phase):
        return run(rust_test, env | {"SWARM_TEST_DISCORD_ACTIVE_ROOT": str(base / "state"),
                                    "SWARM_TEST_DISCORD_REST_PORT": go_port,
                                    "SWARM_TEST_DISCORD_GUILD_PHASE": phase},
                   "native_go_retained_guild_preparation", "--ignored", "--nocapture")
    try:
        invoke("create", place, base / "state", "teammate", "true", base / "work")
        invoke("bind-thread", place, "original-room", "401", "399")
        invoke("ingress", place, "original-room", "501", "settle")
        with daemon(go, env, base / "go.log"):
            binding = activate_original(fixture, env, place)
            session, instance = binding["session_id"], binding["session_instance"]
            voice, = (place / "discussions").glob("*/voice.db")
            credential = voice.with_name("bridge.auth").read_bytes()
            before_voice = voice.read_bytes()
            probe = {"op":"manager_runtime", "session_id":session, "body_version":1,
                     "manager_runtime_request":{"session_instance":instance}}
            def runtime():
                return owner(env, probe, caps=("manager-provenance",))["manager_runtime"]
            start = runtime()
            assert not start.get("native_cli", False) and start["ready_for_input"], start
            assert start["binding"]["session"] == session
            try:
                if rust_test is None:
                    invoke("guild-record", place, "original-room", "701", "201", "fixture external")
                else:
                    wait_for_capture[0] = True
                    rust("capture")
                original, = pending()
                assert original["State"] == "prepared" and original["Admission"] is None
                if rust_test is None:
                    invoke("guild-advance", place, go_port, "stage")
                else:
                    connector = place / "discord.db"
                    before = connector.read_bytes()
                    permissions[0] = False
                    rust("refuse")
                    permissions[0] = True
                    assert connector.read_bytes() == before and voice.read_bytes() == before_voice
                    with Path(start["bridge_socket"]).with_name("controller.lock").open("rb") as lock:
                        fcntl.flock(lock, fcntl.LOCK_EX)
                        rust("refuse")
                    assert connector.read_bytes() == before and voice.read_bytes() == before_voice
                    config = place / "config.json"
                    enrolled = config.read_bytes()
                    try:
                        for field in (b'"OWNER_ID": "201"', b'"owner_id": "200"'):
                            config.write_bytes(enrolled.rstrip()[:-1] + b',\n  ' + field + b'\n}\n')
                            rust("refuse")
                            assert connector.read_bytes() == before and voice.read_bytes() == before_voice
                    finally:
                        config.write_bytes(enrolled)
                    rust("dry-run")
                    assert connector.read_bytes() == before and voice.read_bytes() == before_voice
                    rust("prepare")
                sending, = pending()
                a = sending["Admission"]
                assert sending["State"] == "sending" and a["OperationID"] == OP and a["Revision"] == REV
                assert sending["Sequence"] == original["Sequence"] == a["Sequence"]
                assert a["Authority"] == "external" and a["InvokerID"] == "201" and not a.get("ExpectReply")
                assert a["Text"] == "" and a["Route"] == original["Route"]
                assert before_voice == voice.read_bytes() and calls == [] and posts == []
                assert native(voice)["sequence"] == 0
                print(f"{'Rust' if rust_test else 'Go'} committed rendering-free sending with no Voice mutation or native call", flush=True)
                permissions[0] = False
                decline = subprocess.run([fixture, "guild-advance", place, go_port, "accept"],
                                         env=env, capture_output=True, timeout=35)
                assert decline.returncode != 0 and b"not authorized" in decline.stderr, decline.stderr
                assert pending() == [sending] and calls == [] and posts == []
                permissions[0] = True
                invoke("guild-advance", place, go_port, "accept")
                accepted, = pending()
                assert accepted["State"] == "accepted" and accepted["Admission"] == a
                row = native(voice)
                assert row["source_id"] == f"discussion:{OP}" and row["origin"]["revision"] == REV, row
                assert row["origin"]["guild_id"] == "300" and row["origin"]["kind"] == "external"
                assert not row.get("phase") and not row.get("native_input_id") and calls == [] and posts == []
                invoke("guild-advance", place, go_port, "advance")
                assert pending() == []
                row = native(voice)
                assert row["source_cutover"] and row["operation_id"].startswith("semantic:")
                assert row["origin"]["revision"] == REV and row["origin"]["request_id"] == OP
                assert len(row["native_input_id"]) == 64 and row["request"]["manager_admission"]["nonce"] == row["native_input_id"]
                assert row["request"]["session"] == session and row["request"]["session_instance"] == instance
                await_value(lambda: len(calls) == 1)
                native_proof = owner(env, {"op":"manager_input_status", "session_id":session, "body_version":1,
                    "manager_input_status_request":{"binding":start["binding"], "nonce":row["native_input_id"]}},
                    caps=("manager-provenance",))["manager_input_status"]
                assert native_proof["native_input_id"] == row["native_input_id"]
                assert native_proof["state"] in ("running", "completed")
                semantic = owner(env, {"op":"semantic_status", "session_id":session,
                    "operation_id":row["operation_id"]}, caps=("semantic-send",))["semantic_receipt"]
                assert semantic["operation_id"] == row["operation_id"] and semantic["session"] == session
                invoke("guild-record", place, "original-room", "701", "201", "fixture external")
                assert pending() == []
                invoke("guild-advance", place, go_port, "advance")
                assert native(voice) == row and len(calls) == 1 and posts == []
                assert runtime()["binding"] == start["binding"] and voice.with_name("bridge.auth").read_bytes() == credential
                print("PASS same Go sending operation produced one authenticated native nonce and no Discord posts", flush=True)
                sending["Admission"]["Discussion"].pop("supervisor_socket")
                return sending
            except Exception:
                print((base / "go.log").read_text()[-8000:], file=sys.stderr)
                raise
            finally:
                for original in listed(env):
                    if original["role"] == "manager":
                        owner(env, {"op": "kill", "session_id": original["id"]})
    finally:
        rest.shutdown()
        rest.server_close()
        source.shutdown()
        source.server_close()


if __name__ == "__main__":
    assert os.stat("/proc/self/ns/net").st_ino != int(os.environ["SWARM_NATIVE_FIXTURE_HOST_NETNS"])
    with tempfile.TemporaryDirectory(prefix="gp-") as root:
        first = witness(Path(root).resolve(), *sys.argv[1:4])
    if len(sys.argv) == 5:
        with tempfile.TemporaryDirectory(prefix="gp-") as root:
            assert first == witness(Path(root).resolve(), *sys.argv[1:]), "Rust differs from Go's sending row"
