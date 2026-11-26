#!/usr/bin/env python3
"""Original Go managed personal room with an installed, loopback-only Codex."""
import base64
import email.parser
import email.policy
import hashlib
import http.server
import json
import shlex
import socket
import threading
import time
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



def activate_original(fixture, env, place):
    argv = [fixture, "activate", place, "original-room"]
    for retry in range(3):
        ready = subprocess.run(argv, env=env, capture_output=True, text=True, timeout=80)
        if ready.returncode == 0:
            return json.loads(ready.stdout)
        assert retry < 2 and "discussion manager could not become ready" in ready.stderr, ready.stderr
        time.sleep(1)
    raise AssertionError("original Go manager never became ready")


def discord_fixture(acknowledge, wake=False, expected_file=None, revoke=False, change_source=None):
    posts = []
    app_reads = 0
    messages = {
        id: dict(id=id, type=0, channel_id="400", author=dict(id="200", username="synthetic owner"),
                 attachments=[], content=text)
        for id, text in [("501", "Original settled source"),
                         ("701", "Use project former. Retained real native loopback input."),
                         ("702", "Switch to secondary. Second retained real native loopback input.")]
    }

    if wake:
        messages["702"]["content"] = "Continue after the retained VM file."
        messages["701"]["content"] = "offline original owner prompt"
        messages["701"]["author"]["username"] = "offline fixture"

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
            nonlocal app_reads
            assert self.headers["Authorization"] == "Bot synthetic-unused-credential"
            if self.path == "/api/v10/users/@me":
                app_reads += 1
                self.payload(dict(id="100", username="fixturebot", bot=True))
            elif self.path == "/api/v10/channels/400":
                actor = "300" if revoke and app_reads > 1 else "200"
                self.payload(dict(id="400", type=1, recipients=[dict(id=actor, bot=False)]))
            elif self.path == "/api/v10/channels/400/messages?limit=100":
                if wake:
                    self.payload([messages["701"], messages["501"]])
                else:
                    self.payload([messages["702"], messages["701"]] if posts else [messages["501"]])
            elif self.path.startswith("/api/v10/channels/400/messages/"):
                message = self.path.rsplit("/", 1)[1]
                change = change_source() if message == "701" and change_source else None
                if change == "delete":
                    self.send_error(404)
                elif change == "edit":
                    self.payload(messages[message] | {"content": "withdrawn original prompt",
                                 "edited_timestamp": "2026-11-26T00:00:00Z"})
                else:
                    self.payload(messages[message])
            else:
                self.send_error(404)

        def do_POST(self):
            assert self.path == "/api/v10/channels/400/messages"
            assert self.headers["Authorization"] == "Bot synthetic-unused-credential"
            raw = self.rfile.read(int(self.headers["Content-Length"]))
            attachments = []
            if wake and self.headers.get("Content-Type", "").startswith("multipart/form-data"):
                content_type = self.headers["Content-Type"].encode()
                envelope = b"Content-Type: " + content_type + b"\r\nMIME-Version: 1.0\r\n\r\n" + raw
                multipart = email.parser.BytesParser(policy=email.policy.default).parsebytes(envelope)
                assert multipart.is_multipart()
                parts = {part.get_param("name", header="content-disposition"): part
                         for part in multipart.iter_parts()}
                assert set(parts) == {"payload_json", "files[0]"}
                body = json.loads(parts["payload_json"].get_payload(decode=True))
                file_part = parts["files[0]"]
                assert file_part.get_filename() == "completion.csv"
                assert file_part.get_content_type() == "text/csv"
                assert file_part.get_payload(decode=True) == expected_file
                assert body["attachments"] == [{"id": 0, "filename": "completion.csv"}]
                assert body["content"] == "Verified retained worker file."
                attachments = [dict(id="987", filename="completion.csv", size=len(expected_file))]
            else:
                body = json.loads(raw)
                assert body["content"] == "Verified native final reply."
            assert body["enforce_nonce"] is True and body["allowed_mentions"] == {"parse": []}
            nonce = body["nonce"]
            assert len(nonce) == 24 and nonce not in posts
            posts.append(nonce)
            if acknowledge:
                self.payload(dict(id=str(899 + len(posts)), channel_id="400", author=dict(id="100"),
                                  content=body["content"], nonce=nonce, attachments=attachments))
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
                binding = activate_original(fixture, env, place)
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
                if vm and not os.environ.get("SWARM_DISCORD_CONTINUED_VM_TEXT"):
                    test_env["SWARM_TEST_DISCORD_ACTIVE_VM"] = "1"
                    run(rust_test, test_env, "retained_original_go_vm_inspection", "--nocapture")
                    assert posts == [] and calls == []
                    reopened = json.loads(run(fixture, env, "activate", place, "original-room"))
                    assert reopened.pop("generation") == binding.pop("generation") + 1
                    assert reopened == binding and auth.read_bytes() == original_credential
                    test_env["SWARM_TEST_DISCORD_ACTIVE_VM_CONVERSATION"] = binding["conversation_id"]
                    run(rust_test, test_env, "retained_original_go_vm_pending_file_stays_in_original_mailbox",
                        "--nocapture")
                    assert posts == [] and calls == []
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


def admit_vm_worker_wake(go, fixture, codex, rust_test, base, mode):
    assert os.environ.get("SWARM_NATIVE_FIXTURE_OS_ISOLATED") == "1"
    assert os.stat("/proc/self/ns/net").st_ino != int(os.environ["SWARM_NATIVE_FIXTURE_HOST_NETNS"])
    for part in ("home", "home/.codex", "bin", "work", "state"):
        (base / part).mkdir(mode=0o700)
    (base / "bin/codex").symlink_to(codex)
    (base / "bin/codex-linux-sandbox").symlink_to(codex)
    (base / "bin/swarm").symlink_to(go)
    env = fixture_env(base, base / "state", base / "bin")
    env["PATH"] += ":/usr/sbin:/sbin"
    env["TERM"] = "xterm-256color"
    project = base / "work"
    original_bytes = b"fixture,value\noriginal,1\n"
    worker_file = project / "completion.csv"
    worker_file.write_bytes(original_bytes)
    stop = base / "stop-original-controller"
    parent_fence = base / "original-parent-admitted"
    pending_ready = threading.Event()
    allow_pending = threading.Event()
    waiting = threading.Event()
    release = threading.Event()
    worker_release = threading.Event()
    selected = set()
    lock = threading.Lock()
    capture = []
    tool_outputs = []
    file_calls = []
    moved_project = base / "work-original"
    file_sha = hashlib.sha256(original_bytes).hexdigest()
    file_id = hashlib.sha256(f"completion.csv\0{file_sha}".encode()).hexdigest()

    restart = mode.startswith("ordinary-restart")
    lost_ack = mode == "ordinary-restart-lost"
    live = mode.startswith("ordinary-live")
    foreground_ready = threading.Event()
    foreground_release = threading.Event()
    worker_wake_requests = []
    mode_fence = mode.removeprefix("ordinary-fence-") if mode.startswith("ordinary-fence-") else ""
    fresh = mode == "ordinary-fresh" or bool(mode_fence) or live or restart
    queued = fresh or mode in ("pending-file", "prepare-queued", "prepare-queued-crash",
                               "prepare-malformed", "prepare-aged")

    def choose(body):
        observed = json.dumps(body["input"], ensure_ascii=False)
        tool_outputs.extend(item["output"] for item in body["input"]
                            if item.get("type") == "function_call_output")
        next_owner = "Continue after the retained VM file." in observed
        original_reply = (live and not next_owner and "offline original owner prompt" in observed
                          and "spawn" in selected
                          and any(item.get("type") == "function_call_output" for item in body["input"]))
        if original_reply or ((mode == "ordinary-mixed" or live) and next_owner):
            role = "parent-reply" if original_reply else "text"
            names = [(tool.get("name"), child.get("name"))
                     for tool in body.get("tools", []) for child in tool.get("tools", [])
                     if child.get("name") == "publish"]
            with lock:
                if names and role not in selected:
                    selected.add(role)
                else:
                    names = []
            if names:
                if live and next_owner:
                    foreground_ready.set()
                    assert foreground_release.wait(timeout=40)
                namespace, name = names[0]
                reply = "Verified native final reply."
                return namespace, name, dict(output_id=role, sequence=1, text=reply, final=True)
        if "Worker message and identity" in observed:
            worker_wake_requests.append(observed)
            names = [(tool.get("name"), child.get("name"))
                     for tool in body.get("tools", []) for child in tool.get("tools", [])
                     if child.get("name") == "publish"]
            if not names:
                return None
            assert len(names) == 1, names
            capture.append(observed)
            waiting.set()
            attempts = [[file_id, file_id], ["0" * 64], [file_id]]
            if mode == "ordinary-replace":
                attempts.insert(0,[file_id])
            if not release.wait(timeout=60) or len(file_calls) >= len(attempts):
                return None
            if mode == "ordinary-replace" and len(file_calls) == 1:
                assert moved_project.is_dir() and posts == []
                assert (place / "discord.db").read_bytes() == before_crash[0]
                assert not intent.exists()
                assert any("unconfirmed" in str(item) for item in tool_outputs)
                project.rmdir()
                moved_project.rename(project)
            namespace, name = names[0]
            selected_files = attempts[len(file_calls)]
            file_calls.append(selected_files)
            return namespace, name, dict(output_id="retained-worker-file", sequence=1,
                                         text="Verified retained worker file.", final=True,
                                         file_ids=selected_files)
        shell = next((tool["name"] for tool in body.get("tools", [])
                      if tool.get("name") in ("exec_command", "shell_command")), None)
        if shell is None:
            return None
        is_worker = "rollover-worker-file-fixture" in observed and "offline original owner prompt" not in observed
        command = None
        with lock:
            if is_worker and "worker" not in selected and "spawn" in selected:
                selected.add("worker")
                command = f"swarm teammate message --kind complete --file {shlex.quote(str(worker_file))}"
            elif "offline original owner prompt" in observed and "spawn" not in selected:
                selected.add("spawn")
                command = f"swarm spawn --cli codex --dir {shlex.quote(str(project))} --prompt rollover-worker-file-fixture"
        if command is None:
            if is_worker:
                worker_release.wait(timeout=90)
            return None
        if queued and is_worker:
            pending_ready.set()
            assert allow_pending.wait(timeout=90)
        field = "cmd" if shell == "exec_command" else "command"
        return None, shell, {field: command}

    source_change = threading.Event()
    model, _ = model_fixture(base, False, fixture_tool=choose)
    discord, posts = discord_fixture(mode != "uncertain" and not lost_ack, wake=True,
                                     change_source=lambda: (mode_fence or "edit") if source_change.is_set() else None,
                                     expected_file=original_bytes, revoke=mode == "revoked")
    try:
        place = base / "connector"
        run(fixture, env, "create", place, base / "state", "personal", "true", project)
        run(fixture, env, "project-access", place, "vm")
        run(fixture, env, "bind", place, "original-room", "400")
        run(fixture, env, "ingress", place, "original-room", "501", "settle")
        with daemon(go, env, base / "go.log"):
            try:
                initial = activate_original(fixture, env, place)
                assert initial["native_cli"] is True
                if live:
                    live_env = env | {"SWARM_TEST_DISCORD_ADMITTED_FILE_WAKE_ROOT": str(base / "state"),
                        "SWARM_TEST_DISCORD_REST_PORT": str(discord.server_port),
                        "SWARM_TEST_DISCORD_WAKE_DELIVERY": mode}
                    with subprocess.Popen([rust_test, "retained_original_go_stages_native_vm_file_call",
                            "--nocapture"], env=live_env, stdout=subprocess.PIPE,
                            stderr=subprocess.STDOUT, text=True) as test:
                        await_value(lambda: foreground_ready.is_set() or test.poll() is not None)
                        assert foreground_ready.is_set(), test.communicate(timeout=5)
                        assert pending_ready.is_set() or pending_ready.wait(8)
                        allow_pending.set()
                        await_value(lambda: any('"state":"queued"' in str(item) for item in tool_outputs))
                        assert test.poll() is None and len(posts) == 1 and worker_wake_requests == []
                        worker_file.write_bytes(b"fixture,value\nchanged,2\n")
                        if mode == "ordinary-live-edit":
                            source_change.set()
                        release.set()
                        foreground_release.set()
                        output, _ = test.communicate(timeout=55)
                        assert test.returncode == 0, (output, model.history, tool_outputs[-5:])
                    if mode == "ordinary-live-edit":
                        assert len(posts) == 2 and file_calls == [] and worker_wake_requests == []
                        ledger, = (place / "discussions").glob("*/voice.db")
                        run(fixture, env, "vm-verify-queued-file", place, "original-room", ledger.parent, "701")
                    else:
                        assert len(posts) == 3 and len(file_calls) == 3
                        ledger, = (place / "discussions").glob("*/voice.db")
                        payload, = json.loads(run(fixture, env, "vm-validate-staged-wake", ledger.parent, base / "state"))
                        assert base64.b64decode(payload) == original_bytes
                    reopened = activate_original(fixture, env, place)
                    assert reopened.pop("generation") > initial.pop("generation")
                    assert reopened == initial
                    return
                go_args = [fixture,"vm-controller-run",place,"original-room","701",stop,parent_fence]
                if fresh:
                    go_args.append("completed-parent")
                ctl = subprocess.Popen(go_args,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
                try:
                    started = pending_ready if queued else waiting
                    deadline = time.monotonic() + 80
                    while not started.wait(0.2):
                        if time.monotonic() > deadline or (
                            ctl.poll() is not None and not (fresh and parent_fence.exists())
                        ):
                            break
                    assert started.is_set(), (selected, model.history, listed(env),
                                              tool_outputs[-4:],
                                              ctl.stderr.read() if ctl.poll() is not None else "running")
                    await_value(lambda: parent_fence.exists() or ctl.poll() is not None)
                    if fresh:
                        assert parent_fence.exists()
                    else:
                        assert ctl.poll() is None, ctl.communicate()
                        stop.touch()
                    _stdout, stderr = ctl.communicate(timeout=15)
                    assert ctl.returncode == 0, stderr
                    if queued:
                        ledger, = (place / "discussions").glob("*/voice.db")
                        original_ledgers = ((place / "discord.db").read_bytes(),ledger.read_bytes())
                        allow_pending.set()
                        await_value(lambda: any('"state":"queued"' in str(item) for item in tool_outputs))
                        worker_file.write_bytes(b"fixture,value\nchanged,2\n")
                        witness = json.loads(run(fixture,env,"vm-verify-queued-file",place,"original-room",ledger.parent,"701"))
                        note = witness["Note"]
                        assert note["State"] == "queued" and note["Kind"] == "complete"
                        assert note["Discussion"] == initial["conversation_id"] and note["Project"] == str(project)
                        row, = [row for row in listed(env) if row["id"] == note["Session"]]
                        assert row["worker_instance"] == note["Instance"]
                        assert row["worker_discussion"] == note["Discussion"]
                        assert row["worker_attempt"] == note["Attempt"]
                        assert row["worker_parent_input_hash"] == note["ParentInputHash"]
                        assert row["spawned_from"] == note["Parent"]
                        assert list(map(base64.b64decode,witness["Files"])) == [original_bytes]
                        queued_env = env | {"SWARM_TEST_DISCORD_QUEUED_FILE_ROOT":str(base / "state")}
                        run(rust_test,queued_env,"native_go_queued_file_owner_inspection_is_read_only","--nocapture")
                        assert ((place / "discord.db").read_bytes(),ledger.read_bytes()) == original_ledgers
                        assert posts == []
                        if fresh:
                            rust_env = env | {"SWARM_TEST_DISCORD_ADMITTED_FILE_WAKE_ROOT":str(base / "state"),
                                "SWARM_TEST_DISCORD_REST_PORT":str(discord.server_port), "SWARM_TEST_DISCORD_WAKE_DELIVERY":mode}
                            release.set()
                            receipt = None
                            if restart:
                                test_name = "retained_original_go_stages_native_vm_file_call"
                                fault = subprocess.run([rust_test, test_name, "--nocapture"],
                                    env=rust_env | {"SWARM_TEST_DISCORD_QUEUE_EXIT_AFTER_MAILBOX": "1"},
                                    capture_output=True, text=True, timeout=30)
                                assert fault.returncode == 88, (fault.stdout, fault.stderr)
                                receipt = json.loads(run(fixture, env, "vm-read-prepared-queued-wake",
                                    ledger.parent, base / "state", note["Discussion"], note["Session"],
                                    note["Instance"], note["Attempt"], note["ID"], "ingested"))
                                assert list(map(base64.b64decode, receipt["Files"])) == [original_bytes]
                                assert posts == [] and worker_wake_requests == []
                            if mode.startswith("ordinary-restart-corrupt-"):
                                run(rust_test, rust_env | {"SWARM_TEST_DISCORD_REJECT_CORRUPT_WAKE": "1"},
                                    "retained_original_go_stages_native_vm_file_call", "--nocapture")
                                after = json.loads(run(fixture, env, "vm-read-prepared-queued-wake",
                                    ledger.parent, base / "state", note["Discussion"], note["Session"],
                                    note["Instance"], note["Attempt"], note["ID"], "ingested"))
                                assert after == receipt
                                missing = owner(env, {
                                    "op": "semantic_status", "session_id": initial["session_id"],
                                    "operation_id": receipt["Operation"],
                                }, caps=("semantic-send",))
                                assert missing["op"] == "error" and missing["error"] == (
                                    "semantic receipt: semantic operation does not belong to this session"), missing
                                assert posts == [] and file_calls == [] and worker_wake_requests == []
                                reopened = activate_original(fixture, env, place)
                                assert reopened.pop("generation") > initial.pop("generation")
                                assert reopened == initial
                                return
                            if mode_fence:
                                rust_env["SWARM_TEST_DISCORD_DISPATCH_BLOCK_RUNTIME"] = "1"
                                blocked = base / "state/rust-dispatch-runtime-blocked"
                                resume = base / "state/rust-dispatch-runtime-resume"
                                args = [rust_test, "retained_original_go_stages_native_vm_file_call", "--nocapture"]
                                with subprocess.Popen(args, env=rust_env, stdout=subprocess.PIPE,
                                                      stderr=subprocess.STDOUT, text=True) as test:
                                    await_value(lambda: blocked.exists() or test.poll() is not None)
                                    assert blocked.exists(), test.communicate(timeout=5)
                                    if mode_fence != "stop":
                                        source_change.set()
                                        resume.touch()
                                    output, _ = test.communicate(timeout=25)
                                    assert test.returncode == 0, output
                                receipt = json.loads(run(fixture, env, "vm-read-prepared-queued-wake",
                                    ledger.parent, base / "state", note["Discussion"], note["Session"],
                                    note["Instance"], note["Attempt"], note["ID"], "ingested"))
                                assert receipt["Operation"].startswith("semantic:")
                                missing = owner(env, {
                                    "op": "semantic_status", "session_id": initial["session_id"],
                                    "operation_id": receipt["Operation"],
                                }, caps=("semantic-send",))
                                assert missing["op"] == "error" and missing["error"] == (
                                    "semantic receipt: semantic operation does not belong to this session"), missing
                                assert list(map(base64.b64decode, receipt["Files"])) == [original_bytes]
                                assert posts == [] and file_calls == []
                                assert worker_wake_requests == []
                                assert (place / "discord.db").read_bytes() == original_ledgers[0]
                                reopened = activate_original(fixture, env, place)
                                assert reopened.pop("generation") > initial.pop("generation")
                                assert reopened == initial
                                print(f"{mode}: source/cancel fence preserved original Go operation, zero supervisor semantic sends, native turns and POSTs")
                                return
                            try:
                                run(rust_test,rust_env,"retained_original_go_stages_native_vm_file_call","--nocapture")
                            except AssertionError as error:
                                raise AssertionError((error.args,file_calls,model.history,tool_outputs[-5:])) from error
                            if receipt:
                                native = owner(env, {
                                    "op": "semantic_status", "session_id": initial["session_id"],
                                    "operation_id": receipt["Operation"],
                                }, caps=("semantic-send",))
                                observed = native.get("semantic_receipt", {})
                                assert observed["operation_id"] == receipt["Operation"]
                                assert observed["state"] in ("admitted", "submitted", "completed"), observed
                                assert observed["session_instance"] == initial["session_instance"], observed
                            if lost_ack:
                                assert len(posts) == 1 and len(file_calls) == 3
                                phases = json.loads(run(fixture, env, "publication-phases", place, "original-room"))
                                assert len(phases) == 1 and phases[0].startswith("uncertain true ")
                                assert phases[0].split()[-1] == posts[0]
                                run(rust_test, rust_env | {"SWARM_TEST_DISCORD_EXPECT_WAKE_HOLD": "1"},
                                    "retained_original_go_stages_native_vm_file_call", "--nocapture")
                                assert len(posts) == 1
                                run(fixture, env, "vm-resume-publication", place, "original-room", "uncertain")
                                assert json.loads(run(fixture, env, "publication-phases", place, "original-room")) == phases
                                assert len(posts) == 1
                                return
                            states = json.loads(run(fixture,env,"publication-phases",place,"original-room"))
                            assert len(file_calls) == 3 and len(states) == 1 and states[0].startswith("sent true ")
                            assert posts == [states[0].split()[-1]]
                            payload, = json.loads(run(fixture,env,"vm-validate-staged-wake",ledger.parent,base/"state"))
                            assert base64.b64decode(payload) == original_bytes
                            reopened = activate_original(fixture,env,place)
                            assert reopened.pop("generation") > initial.pop("generation")
                            assert reopened == initial
                            run(fixture,env,"vm-resume-publication",place,"original-room","sent")
                            assert json.loads(run(fixture,env,"publication-phases",place,"original-room")) == states
                            assert posts == [states[0].split()[-1]]
                        elif mode != "pending-file":
                            queued_env["SWARM_TEST_DISCORD_REST_PORT"] = str(discord.server_port)
                            rust_args = [rust_test, "native_go_queued_file_prepares_old_scheduler_receipt", "--nocapture"]
                            verify_args = ("vm-read-prepared-queued-wake",ledger.parent,base/"state",
                                           note["Discussion"],note["Session"],note["Instance"],note["Attempt"],note["ID"])
                            prior = None
                            if mode in ("prepare-queued-crash", "prepare-malformed", "prepare-aged"):
                                fault_env = queued_env | {"SWARM_TEST_DISCORD_QUEUE_EXIT_AFTER_VOICE":"1"}
                                fault = subprocess.run(rust_args,env=fault_env,capture_output=True,text=True,timeout=30)
                                assert fault.returncode == 87, (fault.stdout,fault.stderr)
                                prior=json.loads(run(fixture,env,*verify_args,"pending"))
                                if mode in ("prepare-malformed", "prepare-aged"):
                                    replacement = "semantic:1:" + "a" * 32 if mode == "prepare-aged" else "semantic:0:" + "a" * 32
                                    run(fixture,env,"vm-replace-prepared-queued-operation",ledger.parent,
                                        note["Discussion"],note["Session"],note["Instance"],note["Attempt"],note["ID"],replacement)
                                    prior["Operation"] = replacement
                                voice_after_prepare = ledger.read_bytes()
                            if mode == "prepare-malformed":
                                queued_env["SWARM_TEST_DISCORD_INVALID_QUEUED_OPERATION"] = "1"
                                old_mailbox = (base/"state/dispatcher-mail.db").read_bytes()
                            run(rust_test,queued_env,*rust_args[1:])
                            if mode == "prepare-malformed":
                                assert (base/"state/dispatcher-mail.db").read_bytes() == old_mailbox
                                assert ledger.read_bytes() == voice_after_prepare
                                assert (place/"discord.db").read_bytes() == original_ledgers[0]
                                assert posts == []
                                return
                            prepared=json.loads(run(fixture,env,*verify_args,"ingested"))
                            assert prepared["Operation"].startswith("semantic:")
                            assert list(map(base64.b64decode,prepared["Files"])) == [original_bytes]
                            if prior is not None:
                                assert prior == prepared and ledger.read_bytes() == voice_after_prepare
                            assert (place / "discord.db").read_bytes() == original_ledgers[0]
                            assert posts == []
                        return
                    assert "completion.csv" in capture[0]
                    assert len([row for row in listed(env) if row.get("role") == "manager"]) == 1
                    worker_file.write_bytes(b"fixture,value\nchanged,2\n")
                    assert worker_file.read_bytes() != original_bytes
                    ledger, = (place / "discussions").glob("*/voice.db")
                    stored, = json.loads(run(fixture, env, "vm-read-admitted-wake", ledger.parent, base / "state"))
                    assert base64.b64decode(stored) == original_bytes
                    rust_env = env | {"SWARM_TEST_DISCORD_ADMITTED_FILE_WAKE_ROOT": str(base / "state")}
                    run(rust_test, rust_env, "retained_original_go_admitted_file_wake_is_read_only",
                        "--nocapture")
                    restored, = json.loads(run(fixture, env, "vm-read-admitted-wake", ledger.parent, base / "state"))
                    assert restored == stored
                    rust_env["SWARM_TEST_DISCORD_REST_PORT"] = str(discord.server_port)
                    fault = mode in ("before-pin", "after-pin", "after-voice", "after-outbox")
                    if fault:
                        rust_env["SWARM_TEST_DISCORD_WAKE_FAULT"] = mode
                    elif mode != "prepared":
                        rust_env["SWARM_TEST_DISCORD_WAKE_DELIVERY"] = mode
                    intent = place / ".rust-vm-file-intent-v1.json"
                    connector = place / "discord.db"
                    before_crash = (connector.read_bytes(), ledger.read_bytes())
                    stager = subprocess.Popen([rust_test, "retained_original_go_stages_native_vm_file_call", "--nocapture"],
                                              env=rust_env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
                    try:
                        await_value(lambda: (base / "state/rust-wake-ready").exists() or stager.poll() is not None)
                        assert stager.poll() is None, stager.communicate()
                        if mode == "ordinary-replace":
                            original_checkout = project.stat().st_ino
                            project.rename(moved_project)
                            project.mkdir(mode=0o700)
                            assert project.stat().st_ino != original_checkout
                        release.set()
                        output, errors = stager.communicate(timeout=32)
                        if fault:
                            assert stager.returncode == 86, (output, errors, tool_outputs)
                            proposal = json.loads(intent.read_text())
                            publication = proposal["request"]["publication"]
                            assert proposal["request"]["source"]["files"][0]["id"] == file_id
                            assert len(proposal["nonce"]) == 24
                            old_pin = lambda: run(fixture, env, "vm-pin-count", base / "state",
                                                  publication, file_id).strip()
                            assert old_pin() == ("0" if mode == "before-pin" else "1")
                            state_after_fault = (connector.read_bytes(), ledger.read_bytes())
                            assert (state_after_fault[0] != before_crash[0]) == (mode == "after-outbox")
                            assert (state_after_fault[1] != before_crash[1]) == (mode in ("after-voice", "after-outbox"))
                            replay = rust_env.copy()
                            del replay["SWARM_TEST_DISCORD_WAKE_FAULT"]
                            replay["SWARM_TEST_DISCORD_WAKE_RECOVER"] = mode
                            run(rust_test, replay, "retained_original_go_stages_native_vm_file_call", "--nocapture")
                            assert posts == []
                            assert old_pin() == ("1" if mode in ("after-voice", "after-outbox") else "0")
                            assert intent.exists() == (mode == "after-voice")
                            assert (connector.read_bytes(), ledger.read_bytes()) == state_after_fault
                            if mode == "after-outbox":
                                old = json.loads(run(fixture,env,"vm-read-staged-publication",place,"original-room","prepared"))
                                assert len(old) == 1 and base64.b64decode(old[0]) == original_bytes
                            return
                        assert stager.returncode == 0, (output, errors, tool_outputs)
                        assert not intent.exists()
                        assert len(file_calls) == (4 if mode == "ordinary-replace" else 3)
                        states = json.loads(run(fixture, env, "publication-phases", place, "original-room"))
                        phase = {"revoked": "prepared", "ordinary-sent": "sent", "ordinary-mixed": "sent", "ordinary-replace": "sent"}.get(mode, mode)
                        count = 2 if mode == "ordinary-mixed" else 1
                        assert len(states) == count and all(item.startswith(f"{phase} true ") for item in states), states
                        nonces = [item.split()[-1] for item in states]
                        nonce = nonces[0]
                        if mode == "ordinary-mixed":
                            assert "text" in selected
                            assert len(set(posts)) == 2 and set(posts) == set(nonces)
                        else:
                            assert posts == ([] if phase == "prepared" else [nonce])
                        staged = json.loads(run(fixture, env, "vm-read-staged-publication", place, "original-room", phase))
                        if phase == "sent":
                            assert staged is None
                        else:
                            assert len(staged) == 1 and base64.b64decode(staged[0]) == original_bytes
                        voice_staged, = json.loads(run(fixture, env, "vm-validate-staged-wake", ledger.parent, base / "state"))
                        assert base64.b64decode(voice_staged) == original_bytes
                        if mode == "uncertain":
                            replay = rust_env | {"SWARM_TEST_DISCORD_WAKE_REPLAY": "1"}
                            run(rust_test, replay, "retained_original_go_stages_native_vm_file_call", "--nocapture")
                            assert posts == [nonce]
                            assert json.loads(run(fixture, env, "publication-phases", place, "original-room")) == states
                        if phase in ("sent", "uncertain"):
                            reopened = json.loads(run(fixture, env, "activate", place, "original-room"))
                            assert reopened.pop("generation") >= initial.pop("generation") + 1
                            assert reopened == initial
                            run(fixture, env, "vm-resume-publication", place, "original-room", phase)
                            assert json.loads(run(fixture, env, "publication-phases", place, "original-room")) == states
                            if mode == "ordinary-mixed":
                                assert len(set(posts)) == 2 and set(posts) == set(nonces)
                                assert run(fixture, env, "ingress", place, "original-room", "702", "replay").strip() == "0"
                            else:
                                assert posts == [nonce]
                    finally:
                        if moved_project.exists():
                            project.rmdir()
                            moved_project.rename(project)
                        if stager.poll() is None:
                            stager.terminate()
                            stager.communicate(timeout=10)
                finally:
                    release.set()
                    worker_release.set()
                    allow_pending.set()
                    if ctl.poll() is None:
                        ctl.terminate()
                        _stdout, stderr = ctl.communicate(timeout=10)
                        if stderr:
                            print(stderr, file=sys.stderr)
            finally:
                for row in listed(env):
                    owner(env, {"op": "kill", "session_id": row["id"]})
    finally:
        discord.shutdown()
        discord.server_close()
        model.shutdown()
        model.server_close()


if __name__ == "__main__":
    go, fixture, codex, rust_test = (str(Path(arg).resolve(strict=True)) for arg in sys.argv[1:])
    if modes := os.environ.get("SWARM_DISCORD_ADMITTED_WAKE"):
        for mode in ("prepared", "sent", "uncertain", "revoked") if modes == "1" else modes.split(","):
            assert mode in ("prepared", "sent", "uncertain", "revoked", "before-pin", "after-pin", "after-voice", "after-outbox", "ordinary-sent", "ordinary-mixed", "ordinary-replace", "pending-file", "prepare-queued", "prepare-queued-crash", "prepare-malformed", "prepare-aged", "ordinary-fresh", "ordinary-fence-edit", "ordinary-fence-delete", "ordinary-fence-stop", "ordinary-live", "ordinary-live-edit", "ordinary-restart", "ordinary-restart-lost",
                            "ordinary-restart-corrupt-admission", "ordinary-restart-corrupt-native_input_id")
            with tempfile.TemporaryDirectory(prefix="dn-admit-", dir="/tmp") as directory:
                admit_vm_worker_wake(go, fixture, codex, rust_test, Path(directory), mode)
            if mode == "pending-file":
                print("PASS original Go queued native file and exact owner actor fixture")
            else:
                print(f"PASS original Go admitted native worker wake fixture ({mode})")
        sys.exit(0)
    if os.environ.get("SWARM_DISCORD_CONTINUED_VM_TEXT"):
        with tempfile.TemporaryDirectory(prefix="dn-vm-text-", dir="/tmp") as directory:
            exercise(go, fixture, codex, rust_test, Path(directory), True, vm=True)
        print("PASS retained original Go VM manager continues text across Rust service restart")
        sys.exit(0)
    for acknowledge in (True, False):
        with tempfile.TemporaryDirectory(prefix="dn-", dir="/tmp") as directory:
            exercise(go, fixture, codex, rust_test, Path(directory), acknowledge)
    with tempfile.TemporaryDirectory(prefix="dn-vm-", dir="/tmp") as directory:
        exercise(go, fixture, codex, rust_test, Path(directory), True, vm=True)
    print("PASS retained Go manager: real Codex through Rust Gateway, authenticated final, and acknowledged or uncertain downgrade")
