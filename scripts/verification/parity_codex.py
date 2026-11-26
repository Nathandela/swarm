#!/usr/bin/env python3
"""Drive the Codex adapter with a disposable CLI, state root and PTY."""

import argparse
import http.server
import threading
import json
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import time

sys.dont_write_bytecode = True
from parity_runtime import attach, control, fixtures, read_control, read_frame


def model_fixture(root):
    replies = []
    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_GET(self):
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"data":[]}')

        def do_POST(self):
            size = int(self.headers.get("Content-Length", "0"))
            if size <= 0 or size > 2 << 20:
                self.send_error(400)
                return
            body = json.loads(self.rfile.read(size))
            if body.get("model") != "fixture":
                self.send_error(400, "fixture model required")
                return
            index = len(replies) + 1
            replies.append(index)
            ident = f"fixture-response-{index}"
            text = "Native loopback reply." if index == 1 else "Auxiliary fixture reply."
            item = {"type":"message","id":f"fixture-item-{index}","role":"assistant","status":"completed",
                    "content":[{"type":"output_text","text":text,"annotations":[]}]}
            response = {"id":ident,"object":"response","created_at":int(time.time()),"model":"fixture","status":"in_progress","output":[]}
            events = [
                ("response.created", {"response":response.copy()}),
                ("response.output_item.added", {"output_index":0,"item":item}),
                ("response.output_text.delta", {"item_id":item["id"],"output_index":0,"content_index":0,"delta":text}),
                ("response.output_item.done", {"output_index":0,"item":item}),
                ("response.completed", {"response":dict(response,status="completed",output=[item],
                    usage={"input_tokens":10,"output_tokens":4,"total_tokens":14,
                           "input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}})})
            ]
            self.send_response(200)
            self.send_header("Content-Type","text/event-stream")
            self.end_headers()
            try:
                for name, fields in events:
                    payload=json.dumps(dict(fields,type=name))
                    self.wfile.write(f"event: {name}\ndata: {payload}\n\n".encode())
                    self.wfile.flush()
            except (BrokenPipeError,ConnectionResetError):
                pass

    server=http.server.ThreadingHTTPServer(("127.0.0.1",0),Handler)
    server.daemon_threads=True
    threading.Thread(target=server.serve_forever,daemon=True).start()
    home=root/"home/.codex"
    (home/"config.toml").write_text(
        'model="fixture"\nmodel_provider="fixture"\napproval_policy="never"\nsandbox_mode="read-only"\n'
        'cli_auth_credentials_store="file"\n[model_providers.fixture]\nname="fixture"\nwire_api="responses"\n'
        f'base_url="http://127.0.0.1:{server.server_port}/v1"\nrequires_openai_auth=false\nsupports_websockets=false\n'
        f'[projects."{root}/work"]\ntrust_level="trusted"\n'
        '[tui]\nscreen_reader_detection_done=true\n[tui.model_availability_nux]\n"gpt-6.1-sol"=100\n')
    (home/"config.toml").chmod(0o600)
    (home/"auth.json").write_text(json.dumps({"auth_mode":"apikey","OPENAI_API_KEY":"offline-fixture-only"}))
    (home/"auth.json").chmod(0o600)
    return server,replies


def drive(root, native, guard, loopback, resume_check=False):
    model, replies = model_fixture(root) if loopback else (None, [])
    daemon = None
    previous = {}
    try:
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

        def launch(from_id=None, expected="launch"):
            route = Path(env["SWARM_DAEMON_SOCK"])
            with socket.socket(socket.AF_UNIX) as conn:
                conn.settimeout(16)
                conn.connect(str(route))
                control(conn, "hello", endpoint_id="", protocol_version=1)
                endpoint = read_control(conn)["endpoint_id"]
                options = {"resume_from":from_id} if from_id else {"sandbox":"read-only"}
                control(conn, "launch", endpoint_id=endpoint, launch={
                    "agent": "codex", "name": "codex fixture", "cwd": str(root / "work"),
                    "cols": 100, "rows": 30, "options": options,
                })
                launched = read_control(conn)
                assert launched["op"] == expected, launched
                return launched["session"]["id"] if expected=="launch" else launched

        if not native:
            (root / "work/.fixture-seeded-rollout").touch()
        with (root / "daemon.log").open("wb") as log:
            daemon = start(log)
            if not native:
                operation = invoke("spawn", "--new-operation-id").decode().strip()
                base = ("spawn", "--cli", "codex", "--dir", str(root / "work"), "--prompt", "none")
                for selector in ("OPENAI_BASE_URL", "OPENAI_ORGANIZATION", "OPENAI_PROJECT_ID",
                                 "CODEX_CLI_AUTH_CREDENTIALS_STORE", "CODEX_REFRESH_TOKEN_URL_OVERRIDE"):
                    wrong_origin = {selector: "unqualified-test-origin"}
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
                while True:
                    tag, payload = read_frame(connection)
                    if tag == 1:
                        assert json.loads(payload)["op"] == "detach"
                        break
            observation = state / "observation.json"

            def observed():
                if observation.exists():
                    return json.loads(observation.read_text()).get("conversation_id")
                return None

            if loopback:
                invoke("send",identity,"--text","Return the fixed local fixture response.")
            try:
                thread = fixtures.wait_for(observed, "exact app-server thread", timeout=10)
            except RuntimeError:
                if native:
                    (root/"effects/native-screen.txt").write_bytes(invoke("peek",identity))
                raise
            if not native:
                assert thread == "01997f00-face-7000-8000-00000000cde0"
                observed_row = json.loads(invoke("watch", identity, "--until", "ready_for_review", "--timeout", "4s"))
                assert observed_row["status"]["Turn"] == "idle"
            if loopback:
                fixtures.wait_for(lambda: replies and b"Native loopback reply." in invoke("peek",identity),"native local provider response",timeout=20)
                assert replies == [1], f"expected one model request, saw {len(replies)}"
                print("installed native CLI rendered the unique loopback Responses answer in one request")
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
                fixtures.wait_for(lambda: json.loads(observation.read_text()).get("requests_available"),
                                  "joined native request stream before dispatch")
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
                conflict = launch()
                conflict_state = root / "state/rust-v1" / conflict.split("/")[1]
                conflict_observation = conflict_state / "observation.json"
                fixtures.wait_for(lambda: conflict_observation.exists() and json.loads(conflict_observation.read_text()).get("requests_available"),
                                  "second joined thread for operation collision")
                invoke("message", conflict, "--text", message, "--operation-id", operation, expected=1)
                original = state
                state = conflict_state
                assert turns()["turnStarts"] == 0
                state = original
                invoke("kill", conflict)
                invoke("watch", conflict, "--until", "completed", "--timeout", "4s")
                print("one native turn accepted across exact retries and supervisor replacement; cross-session reuse refused")
            invoke("kill", identity)
            assert json.loads(invoke("watch", identity, "--until", "completed", "--timeout", "4s"))["group"] == "completed"
            print("managed Codex worker and exact native thread survived owner restart")
            if resume_check:
                fixtures.wait_for(lambda: fixtures.generation(owner["pid"]) is None,"old managed shim reaped")
                config=root/"home/.codex/config.toml"
                saved=config.read_bytes()
                config.write_bytes(saved.replace(b"127.0.0.1:",b"127.0.0.2:"))
                try:
                    assert launch(identity,"error").get("error_code")=="invalid_field"
                finally:
                    config.write_bytes(saved)
                rollouts = list((root/"home/.codex/sessions").rglob(f"*-{thread}.jsonl"))
                assert len(rollouts) == 1
                def native_turns():
                    events = map(json.loads, rollouts[0].read_text().splitlines())
                    return sum(row["type"]=="event_msg" and row["payload"]["type"]=="task_started" for row in events)
                assert native_turns() == 1
                resumed=launch(identity)
                resumed_state=root/"state/rust-v1"/resumed.split("/")[1]
                assert json.loads((resumed_state/"observation.json").read_text())["conversation_id"]==thread
                assert any(row["id"]==resumed and row["resumed_from"]==identity for row in json.loads(invoke("ls","--json")))
                fixtures.wait_for(lambda: b"Native loopback reply." in invoke("peek",resumed),"resumed native viewer displays prior reply",timeout=15)
                assert native_turns() == 1, "resuming must not replay the original user turn"
                assert launch(identity,"error").get("error_code")=="invalid_field"
                invoke("kill",resumed)
                invoke("watch",resumed,"--until","completed","--timeout","4s")
                print("real Codex resumed the exact prior rollout, rejected changed authority without replaying the original user turn")

            if not native:
                (root / "work/.fixture-drop-turn-reply").touch()
                identity = launch()
                state = root / "state/rust-v1" / identity.split("/")[1]
                observation = state / "observation.json"
                fixtures.wait_for(lambda: observation.exists() and json.loads(observation.read_text()).get("requests_available"),
                                  "joined thread before deliberately lost response")
                lost = f"semantic:{int(time.time() * 1000)}:{'a' * 32}"
                invoke("message", identity, "--text", "lost reply", "--operation-id", lost, expected=1)
                unknown = json.loads(invoke("message", identity, "--status", "--operation-id", lost))
                assert unknown["state"] == "outcome_unknown", unknown
                fake_http("POST", "/fixture/idle")
                daemon.kill()
                daemon.wait(timeout=5)
                daemon = start(log)
                assert json.loads(invoke("message", identity, "--status", "--operation-id", lost)) == unknown
                invoke("message", identity, "--text", "lost reply", "--operation-id", lost, expected=1)
                invoke("message", identity, "--text", "new operation behind uncertainty", expected=1)
                assert turns()["turnStarts"] == 1
                invoke("kill", identity)
                invoke("watch", identity, "--until", "completed", "--timeout", "4s")
                print("lost native reply stays visibly uncertain and never reissues the turn")
                (root / "work/.fixture-drop-turn-reply").unlink()
                (root / "work/.fixture-seeded-rollout").unlink()
                identity = launch()
                state = root / "state/rust-v1" / identity.split("/")[1]
                observation = state / "observation.json"
                fixtures.wait_for(observed, "unjoined native thread")
                invoke("message", identity, "--text", "unjoined", expected=1)
                assert turns()["turnStarts"] == 0
                invoke("kill", identity)
                invoke("watch", identity, "--until", "completed", "--timeout", "4s")
                print("native send refused without proven request-stream join")

    finally:
        if daemon and daemon.poll() is None:
            daemon.terminate()
            daemon.wait(timeout=4)
        if model:
            model.shutdown()
            model.server_close()
        cleanup = fixtures.reap(root, None, previous)
        print("cleanup", cleanup)
        if cleanup["remaining"]:
            raise RuntimeError(f"fixture processes survived cleanup at {root}")

def verify(candidate, provider, native, scratch, loopback, resume_check=False):
    root = Path(tempfile.mkdtemp(prefix="sc-", dir=scratch))
    success = False
    try:
        for name in ("bin", "home", "runtime", "tmp", "work", "state", "effects", "bin/parity"):
            (root / name).mkdir(mode=0o700)
        source = Path(__file__).resolve().parent
        for original, target in ((candidate, "swarm"), (provider, "codex"),
                                 (source / "parity_runtime.py", "parity_runtime.py"),
                                 (source / "parity_ipc.py", "parity_ipc.py"),
                                 (source / "parity/swarm-parity", "parity/swarm-parity"),
                                 (source / "parity/claude", "parity/claude"),
                                 (Path(__file__), "parity_codex.py")):
            dest = root / "bin" / target
            shutil.copyfile(original, dest)
            dest.chmod(0o700)
        if native:
            home = root / "home/.codex"
            home.mkdir(mode=0o700)
            (home / "config.toml").write_text(f'[projects."{root}/work"]\ntrust_level = "trusted"\n')
        guard = fixtures.guard(root)
        if sys.platform == "darwin":
            drive(root, native, guard, loopback, resume_check)
        else:
            command = [*guard, sys.executable, root / "bin/parity_codex.py", "--isolated-root", root]
            if native:
                command.append("--native")
            if loopback:
                command.append("--loopback-model")
            if resume_check:
                command.append("--resume-check")
            subprocess.run(command, cwd=root/"work",env=fixtures.environment(root),timeout=150,check=True)
        success = True
    finally:
        cleanup = fixtures.reap(root, None, {})
        if success and not cleanup["remaining"]:
            shutil.rmtree(root)
        else:
            print(f"retained fixture root {root}; cleanup {cleanup}", file=sys.stderr)
        if cleanup["remaining"]:
            raise RuntimeError("fixture processes survived cleanup")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", type=fixtures.binary_path)
    parser.add_argument("--provider", type=fixtures.binary_path)
    parser.add_argument("--native", action="store_true")
    parser.add_argument("--loopback-model",action="store_true",help="use the installed native CLI with a fixed local Responses fixture in a private Linux network namespace")
    parser.add_argument("--scratch", type=Path, default=Path("/tmp").resolve())
    parser.add_argument("--isolated-root", type=Path, help=argparse.SUPPRESS)
    parser.add_argument("--resume-check",action="store_true")
    args = parser.parse_args()
    if args.resume_check and not args.loopback_model:
        parser.error("--resume-check requires --loopback-model")
    if args.loopback_model and (not args.native or not sys.platform.startswith("linux")):
        parser.error("--loopback-model requires Linux and --native")
    if args.isolated_root and not args.candidate and Path(__file__).resolve().parent == args.isolated_root/"bin":
        drive(args.isolated_root, args.native, [], args.loopback_model,args.resume_check)
    elif args.candidate and args.provider and not args.isolated_root:
        verify(args.candidate, args.provider, args.native, args.scratch.resolve(), args.loopback_model,args.resume_check)
    else:
        parser.error("--candidate and --provider are required")
