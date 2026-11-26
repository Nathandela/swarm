#!/usr/bin/env python3
"""Original Go managed personal room with an installed, loopback-only Codex."""
import base64
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



def discord_fixture(acknowledge, wake=False):
    posts = []
    messages = {
        id: dict(id=id, type=0, channel_id="400", author=dict(id="200", username="synthetic owner"),
                 attachments=[], content=text)
        for id, text in [("501", "Original settled source"),
                         ("701", "Use project former. Retained real native loopback input."),
                         ("702", "Switch to secondary. Second retained real native loopback input.")]
    }

    if wake:
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


def admit_vm_worker_wake(go, fixture, codex, rust_test, base):
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
    waiting = threading.Event()
    release = threading.Event()
    selected = set()
    lock = threading.Lock()
    problems = []
    capture = []
    tool_outputs = []
    file_calls = []
    file_sha = hashlib.sha256(original_bytes).hexdigest()
    file_id = hashlib.sha256(f"completion.csv\0{file_sha}".encode()).hexdigest()

    def choose(body):
        observed = json.dumps(body["input"], ensure_ascii=False)
        tool_outputs.extend(item["output"] for item in body["input"]
                            if item.get("type") == "function_call_output")
        if "Worker message and identity" in observed:
            capture.append(observed)
            waiting.set()
            if not release.wait(timeout=60) or len(file_calls) >= 3:
                return None
            names = [(tool.get("name"), child.get("name"))
                     for tool in body.get("tools", []) for child in tool.get("tools", [])
                     if child.get("name") == "publish"]
            assert len(names) == 1, names
            namespace, name = names[0]
            selected_files = [[file_id, file_id], ["0" * 64], [file_id]][len(file_calls)]
            file_calls.append(selected_files)
            return namespace, name, dict(output_id="retained-worker-file", sequence=1,
                                         text="Verified retained worker file.", final=True,
                                         file_ids=selected_files)
        with lock:
            if "rollover-worker-file-fixture" in observed and "offline original owner prompt" not in observed and "worker" not in selected and "spawn" in selected:
                selected.add("worker")
                command = f"swarm teammate message --kind complete --file {shlex.quote(str(worker_file))}"
            elif "offline original owner prompt" in observed and "spawn" not in selected:
                selected.add("spawn")
                command = f"swarm spawn --cli codex --dir {shlex.quote(str(project))} --prompt rollover-worker-file-fixture"
            else:
                return None
        for tool in body["tools"]:
            if tool.get("name") in ("exec_command", "shell_command"):
                field = "cmd" if tool["name"] == "exec_command" else "command"
                return None, tool["name"], {field: command}
        problems.append([tool.get("name") for tool in body["tools"]])
        return None

    model, _ = model_fixture(base, False, fixture_tool=choose)
    discord, posts = discord_fixture(True, wake=True)
    try:
        place = base / "connector"
        run(fixture, env, "create", place, base / "state", "personal", "true", project)
        run(fixture, env, "project-access", place, "vm")
        run(fixture, env, "bind", place, "original-room", "400")
        run(fixture, env, "ingress", place, "original-room", "501", "settle")
        with daemon(go, env, base / "go.log"):
            try:
                activate = [fixture, "activate", place, "original-room"]
                for retry in range(3):
                    ready = subprocess.run(activate, env=env, capture_output=True, text=True, timeout=80)
                    if ready.returncode == 0:
                        break
                    assert (retry < 2 and "discussion manager could not become ready" in ready.stderr), ready.stderr
                    time.sleep(1)
                initial = json.loads(ready.stdout)
                assert initial["native_cli"] is True
                ctl = subprocess.Popen([fixture, "vm-controller-run", place, "original-room", "701", stop],
                                       env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
                try:
                    deadline = time.monotonic() + 45
                    while not waiting.wait(0.2) and ctl.poll() is None and not problems:
                        if time.monotonic() > deadline:
                            break
                    assert waiting.is_set(), (selected, problems, model.history, listed(env),
                                              tool_outputs[-4:],
                                              ctl.stderr.read() if ctl.poll() is not None else "running")
                    stop.touch()
                    _stdout, stderr = ctl.communicate(timeout=15)
                    assert ctl.returncode == 0, stderr
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
                    stager = subprocess.Popen([rust_test, "retained_original_go_stages_native_vm_file_call", "--nocapture"],
                                              env=rust_env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
                    try:
                        await_value(lambda: (base / "state/rust-wake-ready").exists() or stager.poll() is not None)
                        assert stager.poll() is None, stager.communicate()
                        release.set()
                        output, errors = stager.communicate(timeout=32)
                        assert stager.returncode == 0, (output, errors, tool_outputs)
                        assert len(file_calls) == 3
                        assert posts == []
                        states = json.loads(run(fixture, env, "publication-phases", place, "original-room"))
                        assert len(states) == 1 and states[0].startswith("prepared true "), states
                        staged, = json.loads(run(fixture, env, "vm-read-staged-publication", place, "original-room"))
                        assert base64.b64decode(staged) == original_bytes
                        voice_staged, = json.loads(run(fixture, env, "vm-validate-staged-wake", ledger.parent, base / "state"))
                        assert voice_staged == staged
                    finally:
                        if stager.poll() is None:
                            stager.terminate()
                            stager.communicate(timeout=10)
                finally:
                    release.set()
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
    if os.environ.get("SWARM_DISCORD_ADMITTED_WAKE") == "1":
        with tempfile.TemporaryDirectory(prefix="dn-admit-", dir="/tmp") as directory:
            admit_vm_worker_wake(go, fixture, codex, rust_test, Path(directory))
        print("PASS original Go admitted native worker wake fixture")
        sys.exit(0)
    for acknowledge in (True, False):
        with tempfile.TemporaryDirectory(prefix="dn-", dir="/tmp") as directory:
            exercise(go, fixture, codex, rust_test, Path(directory), acknowledge)
    with tempfile.TemporaryDirectory(prefix="dn-vm-", dir="/tmp") as directory:
        exercise(go, fixture, codex, rust_test, Path(directory), True, vm=True)
    print("PASS retained Go manager: real Codex through Rust Gateway, authenticated final, and acknowledged or uncertain downgrade")
