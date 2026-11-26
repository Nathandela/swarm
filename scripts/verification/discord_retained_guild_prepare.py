#!/usr/bin/env python3
"""Pinned Go connector prepare/rollback with a loopback-only guild and Codex."""
import hashlib
import http.server
import fcntl
import json
import os
from pathlib import Path
from queue import Queue
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
PUBLICATION = dict(output_id="plain-guild-final", sequence=1, text="One retained reply, verified and complete.", final=True)


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
                bits = (1 << 10) | ((1 << 16) if rest.read_history else 0)
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
            if rest.before_response is not None:
                rest.before_response(route)
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def do_POST(self):
            if not rest.publish:
                attempts.append(self.path)
                self.send_error(400)
                return
            assert self.headers["Authorization"] == "Bot synthetic-unused-credential"
            assert self.path == "/api/v10/channels/401/messages"
            body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            assert body["content"] == PUBLICATION["text"] and body["enforce_nonce"] is True
            assert body.get("message_reference") is None and body["allowed_mentions"]["parse"] == []
            assert not attempts, "a second POST attempted to replay the original nonce"
            attempts.append(body)
            rest.echo = dict(id="989", type=0, guild_id="300", channel_id="401", nonce=body["nonce"],
                             author=dict(id="100", bot=True), content=body["content"], attachments=[])
            # The synthetic Discord effect has completed; its REST response is lost.
            self.close_connection = True
            self.connection.shutdown(socket.SHUT_RDWR)

    rest = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    rest.history = None
    rest.read_history = True
    rest.change_starter = False
    rest.before_response = None
    rest.messages = messages
    rest.publish = False
    rest.echo = None
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


def lines_from(stream):
    lines = Queue()
    def read_output():
        for line in stream:
            lines.put(line)
    worker = Thread(target=read_output, daemon=True)
    worker.start()
    return lines, worker


def refused_bridge_tool(go, env, socket_path, auth_file, conversation, local):
    command = [go, "voice", "mcp", "--socket", socket_path, "--auth-file", auth_file,
               "--conversation", conversation]
    with subprocess.Popen(command, env=env | {"SWARM_SESSION_ID": local}, text=True,
                          stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE) as child:
        replies, output = lines_from(child.stdout)
        def send(method, params, id=None):
            message = dict(jsonrpc="2.0", method=method, params=params)
            if id is not None:
                message["id"] = id
            child.stdin.write(json.dumps(message) + "\n")
            child.stdin.flush()
        try:
            send("initialize", {"protocolVersion":"2024-11-05", "capabilities":{},
                                "clientInfo":{"name":"private bridge fixture", "version":"1"}}, 1)
            assert json.loads(replies.get(timeout=5))["id"] == 1
            send("notifications/initialized", {})
            send("tools/call", {"name":"swarm_discussion_history", "arguments":{}}, 2)
            answer = json.loads(replies.get(timeout=8))
            assert answer["id"] == 2 and answer["result"]["isError"], answer
            assert "manager action is unconfirmed" in json.dumps(answer["result"]), answer
        finally:
            child.stdin.close()
            child.wait(timeout=8)
            output.join(timeout=3)
            assert child.returncode == 0, child.stderr.read()


def tool_pages(value):
    if isinstance(value, str):
        try:
            return tool_pages(json.loads(value))
        except ValueError:
            return []
    if isinstance(value, dict):
        if "HighWater" in value and "Messages" in value:
            return [value]
        value = list(value.values())
    return [page for item in value for page in tool_pages(item)] if isinstance(value, list) else []


def manager_request(sock, credential, body):
    with socket.socket(socket.AF_UNIX) as peer:
        peer.settimeout(5)
        peer.connect(str(sock))
        peer.sendall((f"POST /manager/call HTTP/1.0\r\nHost: voice\r\nAuthorization: Bearer {credential}\r\n"
                      f"Content-Type: application/json\r\nContent-Length: {len(body)}\r\n\r\n").encode() + body)
        with peer.makefile("rb") as response:
            return json.loads(response.read().split(b"\r\n\r\n", 1)[1])


def interrupt_history_call(env, binding, turn, call):
    target = dict(binding=binding, native_input_id=turn["native_input_id"], native_turn_id=turn["native_turn_id"])
    wire = json.dumps(target, separators=(",", ":")).encode()
    receipt = owner(env, {"op":"manager_interrupt", "body_version":1, "session_id":binding["session"],
        "operation_id":"manager-interrupt:" + hashlib.sha256(wire).hexdigest(),
        "manager_interrupt_request":target}, caps=("manager-interrupt",))
    ack = receipt["manager_interrupt"]
    assert ack["native_turn_id"] == turn["native_turn_id"] and ack["native_input_id"] == turn["native_input_id"], receipt
    def closed():
        reply = owner(env, {"op":"manager_input_status", "body_version":1, "session_id":binding["session"],
            "manager_input_status_request":{"binding":binding, "nonce":turn["native_input_id"]}},
            caps=("manager-provenance",))
        current = reply.get("manager_input_status", {})
        return (current.get("state") == "completed" and current.get("reason") == "aborted"
                and current.get("native_turn_id") == turn["native_turn_id"])
    await_value(closed)
    args = json.dumps(call["arguments"], sort_keys=True, separators=(",", ":")).encode()
    proof = owner(env, {"op":"manager_resolve_call", "body_version":1, "session_id":binding["session"],
        "manager_call":{"binding":binding, "tool_name":call["name"], "arguments_hash":hashlib.sha256(args).hexdigest(),
                        "host_meta":call["meta"]}}, caps=("manager-provenance",))
    assert proof["op"] == "error", proof


def witness(base, go, fixture, codex, rust_test=None, interrupt=False, prepare_only=False, queue_only=False,
            prepare_native=False, native_bridge=False, native_history=None, native_publication=None):
    paged = (native_history or "").startswith("pages")
    closed = native_history in ("closed", "pages-closed")
    prepare_in_rust = rust_test is not None and not paged and not native_publication
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
    if native_bridge:
        env["SWARM_NATIVE_FIXTURE_HOST_NETNS"] = os.environ["SWARM_NATIVE_FIXTURE_HOST_NETNS"]
    if interrupt:
        for namespace in ("net", "pid"):
            name = f"SWARM_NATIVE_FIXTURE_HOST_{namespace.upper()}NS"
            outer = int(os.environ[name])
            if outer <= 0 or os.stat(f"/proc/self/ns/{namespace}").st_ino == outer:
                raise RuntimeError(f"interruption requires an isolated {namespace} namespace")
            env[name] = str(outer)
    model_started = Event()
    release_model = Event()
    release_continuation = Event()
    release_next_page = Event()
    first_output = []
    tool_output = []
    def hold_model(items):
        if paged and len(calls) == 2:
            first_output.extend(item for item in items if item.get("type") == "function_call_output")
            assert release_next_page.wait(timeout=20), "fixture never released the continuation call"
            return "Read the preceding page."
        if (native_history or native_publication) and len(calls) == (3 if paged else 2):
            tool_output.extend(item for item in items if item.get("type") == "function_call_output"
                               and (not paged or item.get("call_id") == "fixture-call-2"))
            assert release_continuation.wait(timeout=25), "fixture never released the native continuation"
            return "Read the retained history."
        if TEXT not in json.dumps(items):
            raise AssertionError("the native request did not contain the captured guild prompt")
        model_started.set()
        assert release_model.wait(timeout=35), "fixture never released the original model request"
        return "Native loopback reply."
    def request_history(body):
        if len(calls) != 1 and not (paged and len(calls) == 2):
            return None
        names = [(tool.get("name"), child.get("name"))
                 for tool in body.get("tools", []) for child in tool.get("tools", [])
                 if child.get("name") == "swarm_discussion_history"]
        namespace, name = names[0]
        args = {"cursor":tool_pages(first_output)[0]["Cursor"]} if paged and len(calls) == 2 else {}
        return namespace, name, args
    def request_publication(body):
        if len(calls) != 1:
            return None
        namespace, name = [(tool.get("name"), child.get("name"))
                           for tool in body.get("tools", []) for child in tool.get("tools", [])
                           if child.get("name") == "publish"][0]
        args = PUBLICATION if native_publication != "recipient" else PUBLICATION | {"recipient_id":"202", "recipient_message_id":"701"}
        return namespace, name, args
    source, calls = model_fixture(base, False, answer_for_input=hold_model if interrupt else None,
                                 fixture_tool=request_history if native_history else request_publication if native_publication else None)
    rest, permissions, wait_for_capture, posts = serve_discord(queue_only)
    previous_reads = []
    if paged:
        def old_message(id):
            return dict(rest.messages["501"], id=str(id), content=f"Retained prior message {id} <&>.")
        first = [rest.messages[str(id)] for id in (701, 501, 500)] + [old_message(id) for id in range(499, 482, -1)]
        previous = [old_message(id) for id in range(482, 476, -1)]
        def history_route(path):
            if "before=" in path:
                previous_reads.append(path)
                return previous if "before=483" in path else 403
            return first
        rest.history = history_route
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
        await_value(lambda: manager_observed(
            start, lambda current: current["ready_for_input"] is True
            and current["turn"]["state"] in ("idle", "completed")))
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
                    child.stdin.close()
                except BrokenPipeError:
                    pass
                child.wait(timeout=15)
                output.join(timeout=5)
            assert child.returncode == 0, "".join(log)

    def bridge_custody(start, voice, voice_bytes, connector_bytes):
        with subprocess.Popen([rust_test, *rust_args], env=rust_env("native-bridge-hold"), text=True,
                              stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT) as child:
            lines, output = lines_from(child.stdout)
            def marker(expected):
                while line := lines.get(timeout=10):
                    if expected in line:
                        return
            try:
                marker("holding the original prepared controller bridge")
                sock = Path(start["bridge_socket"])
                auth = Path(start["auth_file"])
                binding = start["binding"]
                local = binding["session"].split("/", 1)[1]
                with sock.with_name("controller.lock").open("rb") as lock:
                    try:
                        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    except BlockingIOError:
                        pass
                    else:
                        raise AssertionError("original Go controller lease was available during Rust ownership")
                assert auth == voice.with_name("bridge.auth") and sock.stat().st_mode & 0o777 == 0o600
                request = json.dumps(dict(version=1, conversation_id=binding["conversation_id"], session_id=local,
                    call=dict(name="swarm_discussion_history", arguments={}))).encode()
                with socket.socket(socket.AF_UNIX) as peer:
                    peer.settimeout(3)
                    peer.connect(str(sock))
                    peer.sendall((f"POST /manager/call HTTP/1.0\r\nHost: voice\r\nAuthorization: Bearer {'0' * 64}\r\n"
                                  f"Content-Type: application/json\r\nContent-Length: {len(request)}\r\n\r\n").encode() + request)
                    assert b'"error":"unconfirmed"' in peer.makefile("rb").read()
                original = auth.with_name("bridge.auth.fixture-original")
                auth.rename(original)
                try:
                    auth.write_bytes(original.read_bytes())
                    auth.chmod(0o600)
                    refused_bridge_tool(go, env, sock, auth, binding["conversation_id"], local)
                finally:
                    original.replace(auth)
                refused_bridge_tool(go, env, sock, auth, binding["conversation_id"], "fixture-other-session")
                refused_bridge_tool(go, env, sock, auth, binding["conversation_id"], local)
                marker("withheld native input and original Go tool calls")
                assert calls == [] and posts == []
                stop_connector(place)
                child.stdin.write("continue\n")
                child.stdin.flush()
                marker("holding duplicate descriptor after controller release")
                assert child.poll() is None and not sock.exists()
                assert voice.read_bytes() == voice_bytes and (place / "discord.db").read_bytes() == connector_bytes
                assert runtime()["binding"] == start["binding"] and calls == [] and posts == []
                invoke("guild-advance", place, go_port, "accept")
                assert child.poll() is None and calls == [] and posts == []
                child.stdin.write("continue\n")
                child.stdin.flush()
                assert child.wait(timeout=10) == 0
            finally:
                if child.poll() is None:
                    child.terminate()
                    child.wait(timeout=8)
                output.join(timeout=3)

    def manager_observed(start, matches_turn):
        try:
            current = runtime()
        except KeyError:
            return False
        if not (current["binding"] == start["binding"] and matches_turn(current)):
            return False
        rows = owner(env, {"op":"list"}, caps=("manager-provenance", "message-origin"))["sessions"]
        return len([row for row in rows if row["id"] == start["binding"]["session"]
                    and row["role"] == "manager" and row["agent"] == "codex"
                    and row["manager_conversation_id"] == start["binding"]["conversation_id"]
                    and row["cwd"] == str(base / "work") and row["status"]["Process"] == "running"
                    and row["capabilities"]["session_instance"] == start["binding"]["session_instance"]]) == 1

    def running_history(start, voice, original_turn):
        connector = place / "discord.db"
        expected = json.loads(invoke("guild-advance", place, go_port, "history"))
        await_value(lambda: manager_observed(
            start, lambda current: current["ready_for_input"] is False
            and current["turn"]["state"] == "running"
            and current["turn"]["native_turn_id"] == original_turn["native_turn_id"]
            and current["turn"]["native_input_id"] == original_turn["native_input_id"]))
        exact_voice, exact_connector = voice.read_bytes(), connector.read_bytes()
        sock = Path(start["bridge_socket"])
        auth = Path(start["auth_file"])
        if native_history == "allow":
            with sock.with_name("controller.lock").open("rb") as lock:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                invoke("guild-mismatch-nonce", voice.parent, "701")
            try:
                inconsistent = voice.read_bytes()
                rust("history-refuse")
                assert voice.read_bytes() == inconsistent and connector.read_bytes() == exact_connector
            finally:
                voice.write_bytes(exact_voice)
            print("PASS a different Go-readable Voice nonce was refused unchanged", flush=True)
        phase = "history-watch" if native_history == "allow" else f"history-watch-{native_history}"
        final_read = Event()
        release_read = Event()
        permission_reads = []
        def delay_last_history_read(route):
            if route == "/channels/399/permissions/member/100":
                permission_reads.append(route)
                if len(permission_reads) == 3:
                    final_read.set()
                    assert release_read.wait(timeout=12), "fixture did not release the final REST GET"
        with subprocess.Popen([rust_test, *rust_args], env=rust_env(phase), text=True,
                              stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT) as child:
            lines, reader = lines_from(child.stdout)
            log = []
            def marker(phrase):
                while True:
                    line = lines.get(timeout=10)
                    log.append(line)
                    if phrase in line:
                        return
            try:
                marker("Serving original public-thread history")
                if native_history == "allow":
                    binding = start["binding"]
                    request = json.dumps(dict(version=1, conversation_id=binding["conversation_id"],
                        session_id=binding["session"].split("/", 1)[1],
                        call=dict(name="swarm_discussion_history", arguments={}, meta={})), separators=(",", ":")).encode()
                    refused = {"error":"unconfirmed"}
                    assert manager_request(sock, "0" * 64, request) == refused
                    assert manager_request(sock, auth.read_text(), request.replace(b'"version":1', b'"version":1,"version":1')) == refused
                    assert manager_request(sock, auth.read_text(), request.replace(b"swarm_discussion_history", b"publish")) == refused
                release_model.set()
                source = rest.messages["701"]
                original = source.copy()
                original_auth = None
                if native_history != "allow":
                    marker("holding supervisor-proven history call")
                    if native_history == "edit":
                        source["content"] = "Changed before Rust released the authorized page."
                        source["edited_timestamp"] = "2024-09-22T10:00:00Z"
                    elif native_history == "stop":
                        stop_connector(place)
                    elif native_history == "closed":
                        rest.before_response = delay_last_history_read
                    else:
                        original_auth = auth.with_name("bridge.auth.fixture-original")
                        auth.rename(original_auth)
                        auth.write_bytes(original_auth.read_bytes())
                        auth.chmod(0o600)
                    child.stdin.write("continue\n")
                    child.stdin.flush()
                if native_history == "closed":
                    assert final_read.wait(timeout=5), "no final history permission GET was held"
                    actual = json.loads((base / "state/rust-guild-history-call").read_bytes())
                    interrupt_history_call(env, start["binding"], original_turn, actual["call"])
                    release_read.set()
                    marker("Withheld the history response from the closed native call")
                    assert len(permission_reads) == 3 and not tool_output and len(calls) == 1
                    rest.before_response = None
                else:
                    await_value(lambda: tool_output)
                if native_history == "allow":
                    pages = tool_pages(tool_output)
                    assert pages == [expected], tool_output
                    actual = base / "state/rust-guild-history-call"
                    assert manager_request(sock, auth.read_text(), actual.read_bytes()) == refused
                    print("PASS malformed JSON, invalid bearer, write tool and exact native call replay disclosed no page", flush=True)
                elif native_history != "closed":
                    assert "manager action is unconfirmed" in json.dumps(tool_output), tool_output
                    assert not tool_pages(tool_output), tool_output
                    source.clear()
                    source.update(original)
                    if original_auth is not None:
                        original_auth.replace(auth)
                assert calls and len(calls) == (1 if native_history == "closed" else 2) and posts == []
                assert voice.read_bytes() == exact_voice and connector.read_bytes() == exact_connector
                if native_history != "stop":
                    stop_connector(place)
                assert child.wait(timeout=10) == 0 and not Path(start["bridge_socket"]).exists()
            finally:
                release_read.set()
                rest.before_response = None
                if child.poll() is None:
                    child.terminate()
                    child.wait(timeout=8)
                reader.join(timeout=3)
                if child.returncode != 0:
                    print("".join(log), end="", file=sys.stderr)
                while not lines.empty():
                    print(lines.get_nowait(), end="", file=sys.stderr)
        print(f"PASS real restricted Codex history ({native_history}) with both Go stores unchanged", flush=True)

    def running_paged_history(start, voice, original_turn):
        before = json.loads(invoke("guild-advance", place, go_port, "history"))
        after = json.loads(invoke("guild-advance", place, go_port, "history", before["Cursor"]))
        assert len(before["Messages"]) == 20 and before["LowWater"] == "483"
        assert after["Cursor"] == "" and [row["id"] for row in after["Messages"]] == list(map(str, range(477, 483)))
        await_value(lambda: manager_observed(start, lambda current: not current["ready_for_input"] and
            current["turn"]["state"] == "running" and current["turn"]["native_turn_id"] == original_turn["native_turn_id"]
            and current["turn"]["native_input_id"] == original_turn["native_input_id"]))
        connector = place / "discord.db"
        original_bytes = (voice.read_bytes(), connector.read_bytes())
        sock, auth = Path(start["bridge_socket"]), Path(start["auth_file"])
        phase = f"history-watch-{native_history}"
        read_pending, release_read = Event(), Event()
        permission_reads = []
        def delay_final_scope(path):
            if path == "/channels/399/permissions/member/100":
                permission_reads.append(path)
                if len(permission_reads) == 4:
                    read_pending.set()
                    assert release_read.wait(timeout=10), "fixture did not release the final page permission GET"
        with subprocess.Popen([rust_test, *rust_args], env=rust_env(phase), text=True,
                              stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT) as child:
            lines, reader = lines_from(child.stdout)
            log = []
            def marker(phrase):
                while True:
                    line = lines.get(timeout=10)
                    log.append(line)
                    if phrase in line:
                        return
            try:
                marker("Serving original public-thread history")
                release_model.set()
                await_value(lambda: first_output)
                assert tool_pages(first_output) == [before], first_output
                first_call = (base / "state/rust-guild-history-call").read_bytes()
                request = json.loads(first_call)
                refused = {"error":"unconfirmed"}
                assert manager_request(sock, auth.read_text(), first_call) == refused
                baseline = len(previous_reads)
                for args in ({"cursor":before["Cursor"] + "x"}, {"cursor":before["Cursor"],"source":"parent"},
                             {"cursor":before["Cursor"],"anchor":"501"}):
                    request["call"]["arguments"] = args
                    wire = json.dumps(request, separators=(",", ":")).encode()
                    assert manager_request(sock, auth.read_text(), wire) == refused
                assert len(previous_reads) == baseline
                release_next_page.set()
                if native_history != "pages":
                    marker("holding supervisor-proven history call")
                    if closed:
                        rest.before_response = delay_final_scope
                    else:
                        rest.read_history = False
                    child.stdin.write("continue\n")
                    child.stdin.flush()
                next_call = base / "state/rust-guild-history-next-call"
                if closed:
                    assert read_pending.wait(timeout=5), "the final scope read was not held"
                    interrupt_history_call(env, start["binding"], original_turn, json.loads(next_call.read_bytes())["call"])
                    release_read.set()
                    marker("Withheld the history response from the closed native call")
                    assert len(calls) == 2 and not tool_output and len(previous_reads) == baseline + 1
                else:
                    await_value(lambda: tool_output)
                    if native_history == "pages":
                        assert tool_pages(tool_output) == [after], tool_output
                        assert manager_request(sock, auth.read_text(), next_call.read_bytes()) == refused
                        assert len(previous_reads) == baseline + 1
                    else:
                        assert "manager action is unconfirmed" in json.dumps(tool_output), tool_output
                        assert not tool_pages(tool_output) and len(previous_reads) == baseline
                assert (voice.read_bytes(), connector.read_bytes()) == original_bytes and posts == []
                stop_connector(place)
                assert child.wait(timeout=10) == 0
            finally:
                rest.read_history = True
                rest.before_response = None
                release_read.set()
                release_next_page.set()
                if child.poll() is None:
                    child.terminate()
                    child.wait(timeout=8)
                reader.join(timeout=3)
                if child.returncode != 0:
                    print("".join(log), end="", file=sys.stderr)
                while not lines.empty():
                    print(lines.get_nowait(), end="", file=sys.stderr)
        print(f"PASS real restricted Codex history ({native_history}) with both Go stores unchanged", flush=True)

    def running_publication(start, voice, turn):
        connector = place / "discord.db"
        original = voice.read_bytes(), connector.read_bytes()
        original_ingress = pending()
        sock = Path(start["bridge_socket"])
        auth = Path(start["auth_file"])
        result = lambda: json.loads(invoke("guild-publication", voice.parent))
        advance = lambda phase, *args: invoke("guild-advance", place, go_port, f"publication-{phase}", *args)
        phase = native_publication
        assert phase is not None and calls and len(calls) == 1 and result() is None and posts == []
        if phase == "go":
            command = [fixture, "guild-advance", place, go_port, "publication-serve"]
            process_env = env
        else:
            command = [rust_test, *rust_args]
            process_env = rust_env(f"publish-{phase}")
            if phase == "stage":
                for probe in ("dry-run", "wrong-message"):
                    checked = subprocess.run(command, env=rust_env(f"publish-{probe}"), capture_output=True, timeout=10)
                    assert checked.returncode == 0 and (voice.read_bytes(), connector.read_bytes()) == original, checked.stdout + checked.stderr
                print("PASS explicit stage dry-run and wrong message ID made no write", flush=True)
                for damage in ("conversation-type", "logical-bytes", "journal-type", "snapshot-type"):
                    with sock.with_name("controller.lock").open("rb") as lock:
                        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                        invoke("guild-malform-timestamp", voice.parent, "701", damage)
                    damaged = voice.read_bytes()
                    try:
                        go_read = subprocess.run([fixture, "guild-publication", voice.parent], env=env, capture_output=True, timeout=5)
                        assert go_read.returncode != 0 and b"corrupt" in go_read.stderr, (damage, go_read.stderr)
                        for probe in ("malformed-dry-run", "malformed"):
                            denied = subprocess.run(command, env=rust_env(f"publish-{probe}"), capture_output=True, timeout=10)
                            reason = b"unqualified original guild Voice header or conversation" if damage == "conversation-type" else b"cannot reopen in Go"
                            assert denied.returncode == 0 and reason in denied.stderr and (voice.read_bytes(), connector.read_bytes()) == (damaged, original[1]), (damage, denied.stdout + denied.stderr)
                    finally:
                        voice.write_bytes(original[0])
                    print(f"PASS Go and Rust reject checksum-correct Voice damage ({damage}) without further writes", flush=True)
        with subprocess.Popen(command, env=process_env, text=True, stdin=subprocess.PIPE,
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT) as child:
            lines, reader = lines_from(child.stdout)
            log = []
            saved_token = None
            def collect(marker):
                while not lines.empty():
                    log.append(lines.get_nowait())
                return any(marker in line for line in log)
            try:
                if phase == "go":
                    await_value(lambda: sock.exists())
                elif phase == "observer":
                    await_value(lambda: collect("Serving original public-thread history"))
                else:
                    await_value(lambda: collect("Waiting for the original guild final publication call."))
                if phase == "stage":
                    with voice.open("rb") as pin:
                        fcntl.flock(pin, fcntl.LOCK_EX | fcntl.LOCK_NB)
                if phase != "go":
                    empty = dict(version=1, conversation_id=start["binding"]["conversation_id"],
                                 session_id=start["binding"]["session"].split("/", 1)[1],
                                 call=dict(name="publish", arguments=PUBLICATION, meta={}))
                    assert manager_request(sock, auth.read_text(), json.dumps(empty, separators=(",", ":")).encode()) == {"error":"unconfirmed"}
                release_model.set()
                if phase in ("recipient", "observer"):
                    await_value(lambda: tool_output)
                    assert "manager action is unconfirmed" in json.dumps(tool_output), tool_output
                    stop_connector(place)
                elif phase not in ("go", "stage"):
                    await_value(lambda: collect("holding supervisor-proven publication call"))
                    live_call = json.loads((base / "state/rust-guild-publish-call").read_bytes())["call"]
                    assert live_call["arguments"] == PUBLICATION
                    if phase == "stop":
                        stop_connector(place)
                    elif phase == "edit":
                        rest.messages["701"]["content"] = "Edited after genuine publish proof."
                    elif phase == "revoked":
                        permissions[0] = False
                    elif phase == "credential":
                        token = Path(json.loads((place / "config.json").read_bytes())["token_file"])
                        saved_token = token, token.read_bytes()
                        token.write_text("synthetic-replacement-token\n")
                    elif phase == "closed":
                        final_read, release_read = Event(), Event()
                        def hold_source_read(route):
                            if route == "/channels/401/messages/500":
                                final_read.set()
                                assert release_read.wait(timeout=8)
                        rest.before_response = hold_source_read
                    child.stdin.write("continue\n")
                    child.stdin.flush()
                    if phase == "closed":
                        try:
                            assert final_read.wait(timeout=5)
                            interrupt_history_call(env, start["binding"], turn, live_call)
                        finally:
                            rest.before_response = None
                            release_read.set()
                expected = {"before-commit":87, "after-commit":88}.get(phase, 0)
                assert child.wait(timeout=10) == expected, log
                if phase == "closed":
                    await_value(lambda: collect("Rechecked the complete original publication source."))
            finally:
                permissions[0] = True
                if saved_token is not None:
                    saved_token[0].write_bytes(saved_token[1])
                if child.poll() is None:
                    child.terminate()
                    child.wait(timeout=8)
                reader.join(timeout=3)
                if child.returncode not in (0, 87, 88):
                    print("".join(log), end="", file=sys.stderr)
                    while not lines.empty():
                        print(lines.get_nowait(), end="", file=sys.stderr)
        assert pending() == original_ingress and posts == []
        if phase != "go":
            assert original[1] == connector.read_bytes()
        if phase not in ("go", "stage", "after-commit"):
            assert original[0] == voice.read_bytes() and result() is None
            if phase != "closed":
                await_value(lambda: tool_output)
                assert PUBLICATION["text"] not in json.dumps(tool_output), tool_output
            print(f"PASS guild publish {phase} leaves both stores byte-identical and no POST", flush=True)
            return None

        saved = result()
        publication, permit, row = [saved[k] for k in ("publication", "permit", "admission")]
        conversation = row["conversation_id"]
        stable = dict(Conversation=conversation, Admission=row["sequence"], Turn=turn["native_turn_id"], Logical=PUBLICATION["output_id"])
        expected_id = f"publication:{row['sequence']}:" + hashlib.sha256(json.dumps(stable, separators=(",", ":")).encode()).hexdigest()
        assert publication["id"] == expected_id and publication["text"] == PUBLICATION["text"] and publication["final"] is True
        assert publication["text_hash"] == hashlib.sha256(PUBLICATION["text"].encode()).hexdigest()
        assert publication["discussion_source"] == {"admission_id":OP} and "exported" not in publication
        assert json.loads(publication["discussion_projects"]) == [str(base / "work")]
        assert publication["native_turn_id"] == permit["native_turn_id"] == turn["native_turn_id"]
        assert row["native_input_id"] == permit["native_input_id"] == turn["native_input_id"]
        assert row["request"]["manager_admission"]["nonce"] == turn["native_input_id"] and row["phase"] == "running"
        assert publication["speech_epoch"] == permit["epoch"] == saved["lastSpeechEpoch"] and permit["revoked"] is False
        assert publication["output_sequence"] == saved["lastOutput"]
        assert json.loads(advance("outbox", expected_id)) is None
        await_value(lambda: tool_output)
        if phase in ("go", "stage"):
            assert expected_id in json.dumps(tool_output), tool_output
        else:
            assert expected_id not in json.dumps(tool_output), tool_output
        assert len(calls) == 2 and posts == []
        if phase != "go":
            frozen = voice.read_bytes(), connector.read_bytes()
            refused = subprocess.run(command, env=process_env, capture_output=True, timeout=10)
            assert refused.returncode != 0 and (voice.read_bytes(), connector.read_bytes()) == frozen

        advance("export")
        exported = result()
        assert exported["publication"] == publication | {"exported":True}
        outbox = lambda: json.loads(advance("outbox", expected_id))
        prepared = outbox()
        nonce = hashlib.sha256(json.dumps([INSTALL, "original-room", expected_id, 0], separators=(",", ":")).encode()).hexdigest()[:24]
        assert prepared["ExportAck"] and prepared["Route"] == {"guild_id":"300", "channel_id":"401", "parent_id":"399", "message_id":""}, prepared
        assert prepared["Chunks"] == [{"id":nonce, "body":PUBLICATION["text"], "state":"prepared"}]
        assert prepared["Publication"]["AdmissionID"] == OP and prepared["Publication"]["ID"] == expected_id
        rest.publish = True
        advance("send-lost")
        uncertain = outbox()
        assert len(posts) == 1 and posts[0]["nonce"] == nonce and rest.echo is not None
        assert uncertain["Chunks"][0]["state"] == "uncertain" and uncertain["Chunks"][0]["id"] == nonce
        for echo in (rest.echo | {"author":{"id":"101", "bot":True}}, rest.echo | {"channel_id":"402"},
                     rest.echo | {"content":"not the original body"}, rest.echo | {"nonce":nonce[::-1]}):
            advance("gateway", json.dumps(echo))
            assert outbox() == uncertain
        advance("gateway", json.dumps(rest.echo))
        sent = outbox()
        assert sent["Chunks"][0] == {"id":nonce,"body":"","state":"sent","message_id":"989","gateway_seen":True}, sent
        advance("send")
        assert outbox() == sent and len(posts) == 1
        advance("compact", expected_id)
        receipt = json.loads(advance("receipt", expected_id))
        assert outbox() is None and receipt["DiscordMessageIDs"] == ["989"]
        assert receipt["BodyHash"] == prepared["BodyHash"] and receipt["Route"] == prepared["Route"]
        assert result() == exported and len(posts) == 1 and len(calls) == 2
        release_continuation.set()
        completed = await_value(lambda: native_input(turn["native_input_id"], "completed"))
        assert completed["native_turn_id"] == turn["native_turn_id"]
        advance("settle")
        settled = result()
        keys = ("sequence", "source_id", "operation_id", "native_input_id", "request", "native_turn_id")
        assert pending() == [] and settled["publication"] == exported["publication"]
        assert settled["admission"]["phase"] == "completed" and not settled["admission"]["settled_at"].startswith("0001")
        assert all(settled["admission"][key] == row[key] for key in keys)
        invoke("guild-record", place, "original-room", "701", "201", "fixture external")
        invoke("guild-advance", place, go_port, "advance")
        advance("send")
        assert result() == settled and json.loads(advance("receipt", expected_id)) == receipt
        assert pending() == [] and len(posts) == 1 and len(calls) == 2
        print(f"PASS guild publish {phase}: current original turn, one Voice receipt and one authenticated Go reply", flush=True)
        return {k: publication[k] for k in ("admission", "sequence", "speech_epoch", "output_sequence", "text_hash", "final", "speak", "discussion_source")}

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
                if not prepare_in_rust:
                    invoke("guild-record", place, "original-room", "701", "201", "fixture external")
                else:
                    wait_for_capture[0] = True
                    rust("capture")
                original, = pending()
                assert original["State"] == "prepared" and original["Admission"] is None
                if not prepare_in_rust:
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
                print(f"{'Rust' if prepare_in_rust else 'Go'} committed rendering-free sending with no Voice mutation or native call", flush=True)
                permissions[0] = False
                decline = subprocess.run([fixture, "guild-advance", place, go_port, "accept"],
                                         env=env, capture_output=True, timeout=35)
                assert decline.returncode != 0 and b"not authorized" in decline.stderr, decline.stderr
                assert pending() == [sending] and calls == [] and posts == []
                permissions[0] = True
                if queue_only and prepare_in_rust:
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
                        if native_bridge:
                            bridge_custody(start, voice, persisted, source_bytes)
                            print("PASS signed Go MCP reached the original Rust-held bridge; stop kept both ledgers unchanged and Go reacquired despite an open duplicate", flush=True)
                if not native_bridge:
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
                    if not prepare_in_rust:
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
                    if native_publication:
                        print("PASS Go prepared the original guild native tuple", flush=True)
                    elif paged:
                        print("PASS Go prepared the original full-page native tuple", flush=True)
                    else:
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
                    if prepare_native and prepare_in_rust:
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
                    if paged:
                        running_paged_history(start, voice, before)
                    elif native_history:
                        running_history(start, voice, before)
                    elif native_publication:
                        return running_publication(start, voice, before)
                invoke("guild-advance", place, go_port, "advance")
                assert pending() == []
                row = native(voice)
                assert row["source_cutover"] and row["operation_id"].startswith("semantic:")
                assert row["origin"]["revision"] == REV and row["origin"]["request_id"] == OP
                assert len(row["native_input_id"]) == 64 and row["request"]["manager_admission"]["nonce"] == row["native_input_id"]
                assert row["request"]["session"] == session and row["request"]["session_instance"] == instance
                expected_requests = (2 if closed else 3) if paged else 2 if native_history and not closed else 1
                await_value(lambda: len(calls) == expected_requests)
                native_proof = await_value(lambda: native_input(row["native_input_id"]))
                if interrupt:
                    keys = ("sequence", "source_id", "operation_id", "native_input_id", "request")
                    assert [row[k] for k in keys] == [interrupted[k] for k in keys]
                    assert row["phase"] == ("completed" if closed else "running")
                    assert row["native_turn_id"] == before["native_turn_id"]
                    assert native_proof["native_turn_id"] == before["native_turn_id"]
                semantic = owner(env, {"op":"semantic_status", "session_id":session,
                    "operation_id":row["operation_id"]}, caps=("semantic-send",))["semantic_receipt"]
                assert semantic["operation_id"] == row["operation_id"] and semantic["session"] == session
                invoke("guild-record", place, "original-room", "701", "201", "fixture external")
                assert pending() == []
                invoke("guild-advance", place, go_port, "advance")
                assert native(voice) == row and len(calls) == expected_requests and posts == []
                assert runtime()["binding"] == start["binding"] and voice.with_name("bridge.auth").read_bytes() == credential
                if interrupt:
                    release_model.set()
                    release_continuation.set()
                    completed = await_value(lambda: native_input(row["native_input_id"], "completed"))
                    assert completed["native_turn_id"] == before["native_turn_id"] and len(calls) == expected_requests and posts == []
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
        release_next_page.set()
        release_continuation.set()
        rest.shutdown()
        rest.server_close()
        source.shutdown()
        source.server_close()


if __name__ == "__main__":
    assert os.stat("/proc/self/ns/net").st_ino != int(os.environ["SWARM_NATIVE_FIXTURE_HOST_NETNS"])
    if len(sys.argv) == 6 and sys.argv[5] == "--native-publication-compare":
        evidence = []
        for phase in ("go", "stage"):
            with tempfile.TemporaryDirectory(prefix="gp-") as root:
                evidence.append(witness(Path(root).resolve(), *sys.argv[1:5], queue_only=True, prepare_native=True,
                                        interrupt=True, native_publication=phase))
        assert evidence[0] == evidence[1], evidence
        print("PASS Go and Rust staged the same scoped final publication fields", flush=True)
    elif len(sys.argv) == 6 and sys.argv[5].startswith("--native-publication-"):
        phase = sys.argv[5].removeprefix("--native-publication-")
        assert phase in ("go", "stage", "before-commit", "after-commit", "stop", "edit", "closed", "recipient", "observer", "revoked", "credential")
        with tempfile.TemporaryDirectory(prefix="gp-") as root:
            witness(Path(root).resolve(), *sys.argv[1:5], queue_only=True, prepare_native=True, interrupt=True,
                    native_publication=phase)
    elif len(sys.argv) == 6 and sys.argv[5] == "--interrupt-after-native":
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
    elif len(sys.argv) == 6 and sys.argv[5] in ("--native-history-pages", "--native-history-pages-revoked", "--native-history-pages-closed"):
        with tempfile.TemporaryDirectory(prefix="gp-") as root:
            witness(Path(root).resolve(), *sys.argv[1:5], queue_only=True, prepare_native=True, interrupt=True,
                    native_history=sys.argv[5].removeprefix("--native-history-"))
    elif len(sys.argv) == 6 and (sys.argv[5] in ("--native-prepare-rollback", "--native-bridge-custody")
                                 or sys.argv[5].startswith("--native-history")):
        history = {"--native-history":"allow", "--native-history-edit":"edit", "--native-history-stop":"stop",
                   "--native-history-credential":"credential", "--native-history-closed":"closed"}.get(sys.argv[5])
        assert history or sys.argv[5] in ("--native-prepare-rollback", "--native-bridge-custody")
        with tempfile.TemporaryDirectory(prefix="gp-") as root:
            first = witness(Path(root).resolve(), *sys.argv[1:4], queue_only=True, prepare_native=True)
        with tempfile.TemporaryDirectory(prefix="gp-") as root:
            second = witness(Path(root).resolve(), *sys.argv[1:5], queue_only=True, prepare_native=True, interrupt=True,
                             native_bridge=sys.argv[5] == "--native-bridge-custody",
                             native_history=history)
        assert first == second, (first, second)
        print("PASS exact native request rendering and one Go-owned rollback send", flush=True)
    else:
        with tempfile.TemporaryDirectory(prefix="gp-") as root:
            first = witness(Path(root).resolve(), *sys.argv[1:4])
        if len(sys.argv) == 5:
            with tempfile.TemporaryDirectory(prefix="gp-") as root:
                assert first == witness(Path(root).resolve(), *sys.argv[1:]), "Rust differs from Go's sending row"
