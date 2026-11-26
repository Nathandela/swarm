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
import socket
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


def serve_discord(timestamps=False):
    permissions = [True]
    wait_for_capture = [False]
    attempts = []
    messages = {mid: dict(id=mid, type=0, channel_id="401", guild_id="300",
                           author=dict(id=actor, username=name), content=content, attachments=[],
                           mentions=[], mention_roles=[], mention_everyone=False)
                for mid, actor, name, content in [("701", "201", "fixture external", TEXT),
                    ("501", "200", "fixture owner", "offline replay witness"),
                    ("500", "200", "fixture owner", "fixture starter")]}
    if timestamps:
        messages["501"]["timestamp"] = "2024-09-21T13:14:15.456000+00:00"
        messages["500"]["timestamp"] = "2024-09-20T09:00:00Z"
        messages["500"]["content"] += " <&>"

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
                if rest.history is not None:
                    data = rest.history(route)
                    if isinstance(data, int):
                        self.send_error(data)
                        return
                else:
                    data = [messages["501"]] if wait_for_capture[0] else [messages["701"], messages["501"]]
                    wait_for_capture[0] = False
            elif route.startswith("/channels/401/messages/") and route.rsplit("/", 1)[1] in messages:
                data = messages[route.rsplit("/", 1)[1]]
            else:
                raise AssertionError(route)
            raw = json.dumps(data).encode()
            if route == "/channels/401/messages/500" and rest.change_starter:
                messages["500"]["content"] = "a changed starter during writer revalidation"
                rest.change_starter = False
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def do_POST(self):
            attempts.append(self.path)
            self.send_error(400)

    rest = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    rest.history = None
    rest.change_starter = False
    rest.messages = messages
    Thread(target=rest.serve_forever, daemon=True).start()
    return rest, permissions, wait_for_capture, attempts


def stop_connector(place):
    identity = hashlib.sha256(INSTALL.encode()).hexdigest()
    with socket.socket(socket.AF_UNIX) as stream:
        stream.settimeout(5)
        stream.connect(str(place / "bridge.sock"))
        stream.sendall((f"POST /installation-v1/{identity}/shutdown HTTP/1.0\r\n"
                        "Host: local\r\nContent-Length: 2\r\nContent-Type: application/json\r\n\r\n{}").encode())
        with stream.makefile("rb") as response:
            raw = response.read()
        assert raw.startswith(b"HTTP/1.0 200 OK\r\n")
        assert json.loads(raw.split(b"\r\n\r\n", 1)[1]) == {"stopped": True}


def witness(base, go, fixture, codex, rust_test=None, interrupt=False, prepare_only=False, queue_only=False, prepare_native=False):
    for part in ("home", "home/.codex", "bin", "work", "state"):
        (base / part).mkdir(mode=0o700)
    (base / "bin/codex").symlink_to(codex)
    if prepare_native:
        settings = base / "state/manager-settings"
        settings.mkdir(mode=0o700)
        (settings / "starting-prompt").write_text("Retained manager settings <&>")
        (settings / "starting-prompt").chmod(0o600)
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
    rest, permissions, wait_for_capture, posts = serve_discord(queue_only)
    go_port = str(rest.server_port)
    place = base / "connector"
    invoke = lambda *args: run(fixture, env, *args)
    pending = lambda: (json.loads(invoke("ingress-state", place, "original-room"))["pending"] or [])
    native = lambda voice: json.loads(invoke("guild-native", voice.parent, "701"))
    def rust_env(phase, evidence=None):
        test_env = env | {"SWARM_TEST_DISCORD_ACTIVE_ROOT": str(base / "state"),
                          "SWARM_TEST_DISCORD_REST_PORT": go_port,
                          "SWARM_TEST_DISCORD_GUILD_PHASE": phase}
        if evidence is not None:
            test_env["SWARM_TEST_DISCORD_VOICE_EVIDENCE"] = json.dumps(evidence)
        return test_env

    rust_args = ["native_go_retained_guild_preparation", "--ignored", "--nocapture"]

    def rust(phase, evidence=None):
        test_env = rust_env(phase, evidence)
        exit_code = {"prepare-and-exit":75, "voice-storage-and-exit":76, "admit-before":77,
                     "admit-after":78, "native-prepare-before":79, "native-prepare-after":80}.get(phase)
        if exit_code is not None:
            child = subprocess.run([rust_test, *rust_args], env=test_env, capture_output=True, timeout=25)
            assert child.returncode == exit_code, child.stderr
            return
        return run(rust_test, test_env, *rust_args)
    def blocked_admission(voice, action, phase, marker="holding writer manager_runtime reply"):
        with subprocess.Popen([rust_test, *rust_args], env=rust_env(phase), text=True,
                              stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT) as child:
            waiting = Event()
            log = []
            def read_output():
                for line in child.stdout:
                    log.append(line)
                    if marker in line:
                        waiting.set()
            output = Thread(target=read_output)
            output.start()
            try:
                assert waiting.wait(timeout=10), "".join(log)
                with voice.open("rb") as ledger:
                    try:
                        fcntl.flock(ledger, fcntl.LOCK_SH | fcntl.LOCK_NB)
                    except BlockingIOError:
                        pass
                    else:
                        raise AssertionError("the blocked admission did not hold the Voice writer")
                action()
            finally:
                try:
                    child.stdin.write("continue\n")
                    child.stdin.flush()
                except BrokenPipeError:
                    pass
                child.stdin.close()
                child.wait(timeout=15)
                output.join(timeout=5)
            assert child.returncode == 0, "".join(log)
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
            if prepare_only:
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
                    rust("prepare-and-exit" if prepare_only else "prepare")
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
                if queue_only and rust_test:
                    connector = place / "discord.db"
                    source_bytes = connector.read_bytes()
                    before_queue = voice.read_bytes()
                    if prepare_native:
                        rust("native-prepare-refuse")
                        assert voice.read_bytes() == before_queue and connector.read_bytes() == source_bytes
                    rust("admit-dry-run")
                    permissions[0] = False
                    rust("admit-refuse")
                    permissions[0] = True
                    with Path(start["bridge_socket"]).with_name("controller.lock").open("rb") as lock:
                        fcntl.flock(lock, fcntl.LOCK_EX)
                        rust("admit-refuse")
                    starter = rest.messages["500"]["content"]
                    rest.change_starter = True
                    try:
                        rust("admit-refuse")
                        assert not rest.change_starter
                    finally:
                        rest.messages["500"]["content"] = starter
                    assert connector.read_bytes() == source_bytes and voice.read_bytes() == before_queue
                    blocked_admission(voice, lambda: stop_connector(place), "admit-delayed-stop")
                    assert connector.read_bytes() == source_bytes and voice.read_bytes() == before_queue
                    newest = rest.messages["701"]
                    original_message = newest.copy()
                    def edit_source():
                        newest["content"] = "source edited while the supervisor reply was held"
                        newest["edited_timestamp"] = "2024-09-22T10:00:00Z"
                    try:
                        blocked_admission(voice, edit_source, "admit-delayed-edit")
                    finally:
                        newest.clear()
                        newest.update(original_message)
                    assert connector.read_bytes() == source_bytes and voice.read_bytes() == before_queue
                    print("PASS stop and a source edit during the final manager_runtime reply left Voice unchanged", flush=True)
                    blocked_admission(voice, lambda: stop_connector(place), "admit-stop-at-commit", "holding staged Voice commit")
                    assert connector.read_bytes() == source_bytes and voice.read_bytes() == before_queue
                    print("PASS acknowledged local stop before the commit gate discarded the staged Voice write", flush=True)
                    rust("admit-before")
                    assert voice.read_bytes() == before_queue and native(voice)["sequence"] == 0
                    assert connector.read_bytes() == source_bytes and pending() == [sending]
                    rust("admit-after")
                    queued = native(voice)
                    assert queued["sequence"] > 0 and not queued.get("phase") and not queued.get("native_input_id")
                    after_voice = voice.read_bytes()
                    rust("admit")
                    assert voice.read_bytes() == after_voice and native(voice) == queued
                    assert connector.read_bytes() == source_bytes and pending() == [sending]
                    assert calls == [] and posts == []
                    if prepare_native:
                        rust("dry-run")
                        permissions[0] = False
                        rust("native-prepare-refuse")
                        permissions[0] = True
                        with Path(start["bridge_socket"]).with_name("controller.lock").open("rb") as lock:
                            fcntl.flock(lock, fcntl.LOCK_EX)
                            rust("native-prepare-refuse")
                        assert voice.read_bytes() == after_voice and connector.read_bytes() == source_bytes
                        rust("native-prepare-before")
                        assert voice.read_bytes() == after_voice and native(voice) == queued
                        rust("native-prepare-after")
                        prepared = native(voice)
                        request_bytes = invoke("guild-native-request", voice.parent, "701")
                        persisted = voice.read_bytes()
                        prompt = settings / "starting-prompt"
                        original_setting = prompt.read_bytes()
                        try:
                            prompt.write_text("changed after the original native request was prepared")
                            rust("native-prepare-refuse")
                            assert voice.read_bytes() == persisted and native(voice) == prepared
                        finally:
                            prompt.write_bytes(original_setting)
                        for prefix in ("0", "+"):
                            lock_path = Path(start["bridge_socket"]).with_name("controller.lock")
                            with lock_path.open("rb") as lock:
                                fcntl.flock(lock, fcntl.LOCK_EX)
                                invoke("guild-malform-timestamp", voice.parent, "701", prefix)
                            try:
                                malformed_row = prepared | {"operation_id": prepared["operation_id"].replace("semantic:", f"semantic:{prefix}", 1)}
                                assert native(voice) == malformed_row
                                malformed = voice.read_bytes()
                                rust("native-prepare-refuse")
                                assert voice.read_bytes() == malformed and connector.read_bytes() == source_bytes
                            finally:
                                voice.write_bytes(persisted)
                        print("PASS Go's replay parser and Rust reject noncanonical timestamps without a retry write", flush=True)
                        rust("prepare")
                        assert voice.read_bytes() == persisted and native(voice) == prepared
                        assert connector.read_bytes() == source_bytes and pending() == [sending]
                        assert calls == [] and posts == []
                invoke("guild-advance", place, go_port, "accept")
                accepted, = pending()
                assert accepted["State"] == "accepted" and accepted["Admission"] == a
                row = native(voice)
                assert row["source_id"] == f"discussion:{OP}" and row["origin"]["revision"] == REV, row
                assert row["origin"]["guild_id"] == "300" and row["origin"]["kind"] == "external"
                if not (prepare_native and rust_test):
                    assert not row.get("phase") and not row.get("native_input_id")
                assert calls == [] and posts == []
                if prepare_only:
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
                if prepare_native:
                    if not rust_test:
                        invoke("guild-advance", place, go_port, "prepare-native")
                        prepared = native(voice)
                        request_bytes = invoke("guild-native-request", voice.parent, "701")
                    assert prepared == native(voice) and pending() == [accepted]
                    assert prepared["phase"] == "prepared" and not prepared.get("source_cutover")
                    assert not prepared.get("reply_check_after") and prepared["request"]["manager_admission"]["binding"] == start["binding"]
                    nonce = prepared["native_input_id"]
                    operation = prepared["operation_id"]
                    assert native_input(nonce) is None and calls == [] and posts == []
                    status = owner(env, {"op":"semantic_status", "session_id":session, "operation_id":operation}, caps=("semantic-send",))
                    assert status["op"] == "error", status
                    comparable = request_bytes.replace(str(base / "work"), "/fixture/work").replace(nonce, "fixture-nonce")
                    for key, value in start["binding"].items():
                        if key != "provider":
                            comparable = comparable.replace(json.dumps(value), json.dumps(f"fixture-{key}"))
                    if not rust_test:
                        return comparable
                    assert request_bytes == invoke("guild-native-request", voice.parent, "701")
                    print("PASS Rust prepared one unsent Go tuple across commit interruption and repeat", flush=True)
                if queue_only and not prepare_native:
                    assert not row.get("reply_check_after") and not row.get("operation_id") and not row.get("request")
                    assert row["input_text"].count("Earlier Discord messages for context (JSON)") == 1
                    assert row.pop("conversation_id") == binding["conversation_id"]
                    assert row.pop("project") == str(base / "work")
                    assert runtime()["binding"] == start["binding"] and voice.with_name("bridge.auth").read_bytes() == credential
                    if rust_test:
                        assert queued == native(voice), (queued, native(voice))
                        print("PASS interrupted and repeated Rust Voice admission reopens once in signed Go, before nonce allocation", flush=True)
                    return row
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
                    if prepare_native:
                        assert interrupted["operation_id"] == prepared["operation_id"] and interrupted["native_input_id"] == prepared["native_input_id"]
                        assert request_bytes == invoke("guild-native-request", voice.parent, "701")
                        executing = voice.read_bytes()
                        connector = place / "discord.db"
                        actual = connector.read_bytes()
                        try:
                            connector.write_bytes(source_bytes)
                            rust("native-prepare-refuse")
                            assert voice.read_bytes() == executing and connector.read_bytes() == source_bytes
                        finally:
                            connector.write_bytes(actual)
                        print("PASS stale sending connector cannot renew the executing Go native tuple", flush=True)
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
                if prepare_native:
                    assert request_bytes == invoke("guild-native-request", voice.parent, "701")
                    return comparable
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
            witness(Path(root).resolve(), *sys.argv[1:5], prepare_only=True)
    elif len(sys.argv) == 6 and sys.argv[5] == "--queue-only":
        with tempfile.TemporaryDirectory(prefix="gp-") as root:
            first = witness(Path(root).resolve(), *sys.argv[1:4], queue_only=True)
        with tempfile.TemporaryDirectory(prefix="gp-") as root:
            second = witness(Path(root).resolve(), *sys.argv[1:5], queue_only=True)
        assert first == second, (first, second)
        print("PASS Rust and Go matched the authenticated starter/history prompt byte-for-byte and omitted reply wait", flush=True)
    elif len(sys.argv) == 6 and sys.argv[5] == "--native-prepare-rollback":
        with tempfile.TemporaryDirectory(prefix="gp-") as root:
            first = witness(Path(root).resolve(), *sys.argv[1:4], queue_only=True, prepare_native=True)
        with tempfile.TemporaryDirectory(prefix="gp-") as root:
            second = witness(Path(root).resolve(), *sys.argv[1:5], queue_only=True, prepare_native=True, interrupt=True)
        assert first == second, (first, second)
        print("PASS exact native request rendering and one Go-owned rollback send", flush=True)
    else:
        with tempfile.TemporaryDirectory(prefix="gp-") as root:
            first = witness(Path(root).resolve(), *sys.argv[1:4])
        if len(sys.argv) == 5:
            with tempfile.TemporaryDirectory(prefix="gp-") as root:
                assert first == witness(Path(root).resolve(), *sys.argv[1:]), "Rust differs from Go's sending row"
