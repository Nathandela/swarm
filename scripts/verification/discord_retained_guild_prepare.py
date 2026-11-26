#!/usr/bin/env python3
"""Pinned Go connector prepare/rollback with a loopback-only guild and Codex."""
import hashlib
import http.server
import fcntl
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
from threading import Event, Thread

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


def witness(base, go, fixture, codex, rust_test=None, interrupt=False, prepare_native=False):
    for part in ("home", "home/.codex", "bin", "work", "state"):
        (base / part).mkdir(mode=0o700)
    (base / "bin/codex").symlink_to(codex)
    env = fixture_env(base, base / "state", base / "bin")
    env["TERM"] = "xterm-256color"
    if interrupt:
        for namespace in ("net", "pid"):
            name = f"SWARM_NATIVE_FIXTURE_HOST_{namespace.upper()}NS"
            outer = int(os.environ[name])
            if outer <= 0 or os.stat(f"/proc/self/ns/{namespace}").st_ino == outer:
                raise RuntimeError(f"interruption requires an isolated {namespace} namespace")
            env[name] = str(outer)
    model_started = Event()
    release_model = Event()
    def hold_model(items):
        if TEXT not in json.dumps(items):
            raise AssertionError("the native request did not contain the captured guild prompt")
        model_started.set()
        assert release_model.wait(timeout=35), "fixture never released the original model request"
        return "Native loopback reply."
    source, calls = model_fixture(base, False, answer_for_input=hold_model if interrupt else None)
    rest, permissions, wait_for_capture, posts = serve_discord()
    go_port = str(rest.server_port)
    place = base / "connector"
    invoke = lambda *args: run(fixture, env, *args)
    pending = lambda: (json.loads(invoke("ingress-state", place, "original-room"))["pending"] or [])
    native = lambda voice: json.loads(invoke("guild-native", voice.parent, "701"))
    def rust(phase, evidence=None):
        test_env = env | {"SWARM_TEST_DISCORD_ACTIVE_ROOT": str(base / "state"),
                          "SWARM_TEST_DISCORD_REST_PORT": go_port,
                          "SWARM_TEST_DISCORD_GUILD_PHASE": phase}
        if evidence is not None:
            test_env["SWARM_TEST_DISCORD_VOICE_EVIDENCE"] = json.dumps(evidence)
        args = ["native_go_retained_guild_preparation", "--ignored", "--nocapture"]
        if phase in ("prepare-and-exit", "voice-storage-and-exit"):
            child = subprocess.run([rust_test, *args], env=test_env, capture_output=True, timeout=20)
            assert child.returncode == (75 if phase == "prepare-and-exit" else 76), child.stderr
            return
        return run(rust_test, test_env, *args)
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
            if prepare_native:
                voice_copy = base / "voice-copy" / "voice.db"
                voice_copy.parent.mkdir(mode=0o700)
                shutil.copy2(voice, voice_copy)
            probe = {"op":"manager_runtime", "session_id":session, "body_version":1,
                     "manager_runtime_request":{"session_instance":instance}}
            def runtime():
                return owner(env, probe, caps=("manager-provenance",))["manager_runtime"]
            start = runtime()
            assert not start.get("native_cli", False) and start["ready_for_input"], start
            assert start["binding"]["session"] == session
            def native_input(nonce, state=None):
                report = owner(env, {"op":"manager_input_status", "session_id":session, "body_version":1,
                    "manager_input_status_request":{"binding":start["binding"], "nonce":nonce}},
                    caps=("manager-provenance",)).get("manager_input_status")
                if report and report["native_input_id"] == nonce and report["state"] in ("running", "completed"):
                    if state is None or report["state"] == state:
                        return report
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
                    copy = base / "malformed"
                    copy.mkdir(mode=0o700)
                    shutil.copy2(config, copy / "config.json")
                    copied = copy / "discord.db"
                    for field in ("ParentAnchor", "Starter", "StartupNoticeID", "InputNoticeID"):
                        shutil.copy2(connector, copied)
                        invoke("guild-damage-binding", copy, field)
                        malformed = copied.read_bytes()
                        negative = subprocess.run([fixture, "ingress-state", copy, "original-room"],
                                                  env=env, capture_output=True, timeout=5)
                        assert negative.returncode != 0 and b"cannot unmarshal object" in negative.stderr, negative.stderr
                        backup = connector.with_suffix(".witness-original")
                        connector.rename(backup)
                        try:
                            shutil.copy2(copied, connector)
                            for phase in ("malformed-binding-dry-run", "malformed-binding"):
                                rust(phase)
                                assert connector.read_bytes() == malformed and voice.read_bytes() == before_voice
                        finally:
                            backup.replace(connector)
                    assert connector.read_bytes() == before
                    rust("dry-run")
                    assert connector.read_bytes() == before and voice.read_bytes() == before_voice
                    rust("prepare-and-exit" if prepare_native else "prepare")
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
                if prepare_native:
                    invoke("guild-advance", place, go_port, "prepare-native")
                    prepared = native(voice)
                    assert pending() == [accepted]
                    assert prepared["phase"] == "prepared" and not prepared.get("source_cutover")
                    assert prepared["origin"] == row["origin"] and prepared["source_id"] == row["source_id"]
                    assert prepared["input_text"] == row["input_text"] and "Earlier Discord messages for context (JSON)" in prepared["input_text"]
                    assert not prepared.get("reply_check_after") and prepared["reply_tracked"]
                    assert prepared["operation_id"].startswith("semantic:") and len(prepared["native_input_id"]) == 64
                    request = prepared["request"]
                    assert request["manager_admission"]["binding"] == start["binding"]
                    assert request["manager_admission"]["nonce"] == prepared["native_input_id"]
                    assert runtime()["binding"] == start["binding"] and voice.with_name("bridge.auth").read_bytes() == credential
                    assert calls == [] and posts == [] and native_input(prepared["native_input_id"]) is None
                    receipt = owner(env, {"op":"semantic_status", "session_id":session,
                        "operation_id":prepared["operation_id"]}, caps=("semantic-send",))
                    assert receipt["op"] == "error", receipt
                    print("PASS Go reopened after Rust's connector commit and persisted its exact native preparation without sending", flush=True)
                    original_stores = (voice.read_bytes(), (place / "discord.db").read_bytes())
                    evidence = {"origin": prepared["origin"], "binding": a["Discussion"]}
                    unstaged = voice_copy.read_bytes()
                    with tempfile.TemporaryDirectory(prefix="gp-outside-", dir=base.parent) as outside:
                        outside = Path(outside)
                        backup = base / "voice-copy-real"
                        voice_copy.parent.rename(backup)
                        try:
                            foreign = outside / "voice.db"
                            shutil.copy2(backup / "voice.db", foreign)
                            voice_copy.parent.symlink_to(outside, target_is_directory=True)
                            rust("voice-storage-refuse-alias", evidence)
                            assert foreign.read_bytes() == unstaged and native(foreign)["sequence"] == 0
                        finally:
                            if voice_copy.parent.is_symlink():
                                voice_copy.parent.unlink()
                            backup.rename(voice_copy.parent)
                    print("PASS Rust refused an aliased Voice copy outside the fixture without writes", flush=True)
                    rust("voice-storage-and-exit", evidence)
                    assert voice_copy.read_bytes() == unstaged and native(voice_copy)["sequence"] == 0
                    print("PASS Go reopened the disposable Voice ledger after an exit before commit", flush=True)
                    rust("voice-storage", evidence)
                    assert native(voice_copy) == prepared
                    assert original_stores == (voice.read_bytes(), (place / "discord.db").read_bytes())
                    assert calls == [] and posts == []
                    print("PASS Rust's existing storage stages the exact Go tuple in a copy that Go reopens; personal rendering differs", flush=True)
                    return prepared
                if interrupt:
                    marker = base / "withheld-ack"
                    log = base / "interrupted-owner.log"
                    args = [fixture, "guild-advance", place, go_port, "advance", marker]
                    own_pid_ns = str(os.stat("/proc/self/ns/pid").st_ino)
                    refused = subprocess.run(args, env=env | {"SWARM_NATIVE_FIXTURE_HOST_PIDNS": own_pid_ns},
                                             capture_output=True, timeout=5)
                    assert refused.returncode != 0 and b"voice: invalid input or transition" in refused.stderr
                    assert not marker.exists() and pending() == [accepted] and calls == [] and posts == []
                    print("PASS acknowledgment withholding refused a matching PID namespace before native submission", flush=True)
                    with log.open("wb") as output:
                        controller = subprocess.Popen(args, env=env, stdout=output, stderr=subprocess.STDOUT)
                        controller_fd = os.pidfd_open(controller.pid)
                        try:
                            await_value(lambda: marker.exists() and marker.stat().st_size > 0 and model_started.is_set())
                            receipt = json.loads(marker.read_bytes())
                            if receipt["pid"] != controller.pid:
                                raise AssertionError("the withheld receipt belongs to a different process")
                            operation, nonce = receipt["operation"], receipt["nonce"]
                            before = await_value(lambda: native_input(nonce, "running"))
                            if len(calls) != 1 or posts or controller.poll() is not None:
                                raise AssertionError("the original effect or live controller could not be verified")
                            signal.pidfd_send_signal(controller_fd, signal.SIGKILL)
                            assert controller.wait(timeout=5) == -signal.SIGKILL
                        except Exception:
                            print(log.read_text(), file=sys.stderr)
                            raise
                        finally:
                            try:
                                if controller.poll() is None:
                                    signal.pidfd_send_signal(controller_fd, signal.SIGTERM)
                                    controller.wait(timeout=5)
                            finally:
                                os.close(controller_fd)
                    assert pending() == [accepted]
                    interrupted = native(voice)
                    assert interrupted["phase"] == "executing" and interrupted["source_cutover"]
                    assert (interrupted["operation_id"], interrupted["native_input_id"]) == (operation, nonce)
                    assert runtime()["binding"] == start["binding"]
                invoke("guild-advance", place, go_port, "advance")
                assert pending() == []
                row = native(voice)
                assert row["source_cutover"] and row["operation_id"].startswith("semantic:")
                assert row["origin"]["revision"] == REV and row["origin"]["request_id"] == OP
                assert len(row["native_input_id"]) == 64 and row["request"]["manager_admission"]["nonce"] == row["native_input_id"]
                assert row["request"]["session"] == session and row["request"]["session_instance"] == instance
                await_value(lambda: len(calls) == 1)
                native_proof = await_value(lambda: native_input(row["native_input_id"]))
                if interrupt:
                    keys = ("sequence", "source_id", "operation_id", "native_input_id", "request")
                    assert [row[k] for k in keys] == [interrupted[k] for k in keys]
                    assert row["phase"] == "running" and row["native_turn_id"] == before["native_turn_id"]
                    assert native_proof["native_turn_id"] == before["native_turn_id"]
                semantic = owner(env, {"op":"semantic_status", "session_id":session,
                    "operation_id":row["operation_id"]}, caps=("semantic-send",))["semantic_receipt"]
                assert semantic["operation_id"] == row["operation_id"] and semantic["session"] == session
                invoke("guild-record", place, "original-room", "701", "201", "fixture external")
                assert pending() == []
                invoke("guild-advance", place, go_port, "advance")
                assert native(voice) == row and len(calls) == 1 and posts == []
                assert runtime()["binding"] == start["binding"] and voice.with_name("bridge.auth").read_bytes() == credential
                if interrupt:
                    release_model.set()
                    completed = await_value(lambda: native_input(row["native_input_id"], "completed"))
                    assert completed["native_turn_id"] == before["native_turn_id"] and len(calls) == 1 and posts == []
                    print("PASS Go recovered the exact executing operation after SIGKILL at the withheld native acknowledgment", flush=True)
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
        release_model.set()
        rest.shutdown()
        rest.server_close()
        source.shutdown()
        source.server_close()


if __name__ == "__main__":
    assert os.stat("/proc/self/ns/net").st_ino != int(os.environ["SWARM_NATIVE_FIXTURE_HOST_NETNS"])
    if len(sys.argv) == 6 and sys.argv[5] == "--interrupt-after-native":
        with tempfile.TemporaryDirectory(prefix="gp-") as root:
            witness(Path(root).resolve(), *sys.argv[1:5], interrupt=True)
    elif len(sys.argv) == 6 and sys.argv[5] == "--prepare-native-only":
        with tempfile.TemporaryDirectory(prefix="gp-") as root:
            witness(Path(root).resolve(), *sys.argv[1:5], prepare_native=True)
    else:
        with tempfile.TemporaryDirectory(prefix="gp-") as root:
            first = witness(Path(root).resolve(), *sys.argv[1:4])
        if len(sys.argv) == 5:
            with tempfile.TemporaryDirectory(prefix="gp-") as root:
                assert first == witness(Path(root).resolve(), *sys.argv[1:]), "Rust differs from Go's sending row"
