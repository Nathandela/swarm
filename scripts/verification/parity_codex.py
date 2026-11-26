#!/usr/bin/env python3
"""Drive the Codex adapter with a disposable CLI, state root and PTY."""

import argparse
import http.server
import os
import fcntl
import pty
import select
import termios
import threading
import json
from pathlib import Path
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time

sys.dont_write_bytecode = True
from parity_runtime import attach, control, fixtures, read_control, read_frame


def model_fixture(root, alternate_check, guard_check=False, native_publication=False, fixture_tool=None):
    replies = []
    history=[]
    release_pressure=threading.Event()
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
            joined=json.dumps(body.get("input",[]))
            continuation="This session's context was automatically compacted" in joined
            pressure="A synthetic context pressure turn." in joined
            high=guard_check and pressure and not any(row["high"] for row in history)
            history.append(dict(path=self.path,continuation=continuation,high=high))
            if high:
                release_pressure.wait(timeout=8)
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
                    usage={"input_tokens":82_000 if high else 10,"output_tokens":4,"total_tokens":82_004 if high else 14,
                           "input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}})})
            ]
            selected = fixture_tool(body) if fixture_tool else None
            if native_publication and index % 2 == 1:
                names = [(tool.get("name"), child.get("name"))
                         for tool in body.get("tools",[]) for child in tool.get("tools",[])
                         if child.get("name") == "publish"]
                assert len(names) == 1, [(tool.get("type"),tool.get("name")) for tool in body.get("tools",[])]
                namespace, name = names[0]
                arguments = json.dumps(dict(output_id="retained-final",sequence=1,
                                            text="Verified native final reply.",final=True))
                selected = namespace, name, json.loads(arguments)
            if selected:
                namespace, name, arguments = selected
                arguments = json.dumps(arguments)
                item = dict(type="function_call",id=f"fixture-function-{index}",call_id=f"fixture-call-{index}",
                            name=name,arguments=arguments,status="completed")
                if namespace:
                    item["namespace"] = namespace
                events = [
                    ("response.created", {"response":response.copy()}),
                    ("response.output_item.added", {"output_index":0,"item":dict(item,arguments="",status="in_progress")}),
                    ("response.function_call_arguments.delta", {"item_id":item["id"],"output_index":0,"delta":arguments}),
                    ("response.function_call_arguments.done", {"item_id":item["id"],"output_index":0,"arguments":arguments}),
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
    home=root/"home"/("custom-codex" if alternate_check else ".codex")
    (home/"config.toml").write_text(
        'model="fixture"\nmodel_provider="fixture"\napproval_policy="never"\nsandbox_mode="read-only"\n'
        'cli_auth_credentials_store="file"\n[model_providers.fixture]\nname="fixture"\nwire_api="responses"\n'
        f'base_url="http://127.0.0.1:{server.server_port}/v1"\nrequires_openai_auth=false\nsupports_websockets=false\n'
        f'[projects."{root}/work"]\ntrust_level="trusted"\n'
        '[tui]\nscreen_reader_detection_done=true\n[tui.model_availability_nux]\n"gpt-6.1-sol"=100\n')
    if guard_check:
        path=home/"config.toml"
        path.write_text("model_context_window=100000\nmodel_auto_compact_token_limit=95000\n"+path.read_text())
    (home/"config.toml").chmod(0o600)
    (home/"auth.json").write_text(json.dumps({"auth_mode":"apikey","OPENAI_API_KEY":"offline-fixture-only"}))
    (home/"auth.json").chmod(0o600)
    server.history=history
    server.release_pressure=release_pressure
    return server,replies


def drive(root, native, guard, loopback, resume_check=False, history_check=False, alternate_check=False, reattach_check=False, guard_check=False):
    model, replies = model_fixture(root, alternate_check, guard_check) if loopback else (None, [])
    daemon = None
    previous = {}
    try:
        if sys.platform=="darwin" and model:
            from native_fixture_sandbox import check_guards, guarded
            ports=[model.server_port]
            check_guards(root,ports)
            guard=guarded([],root,ports)
        env = fixtures.environment(root)
        native_home = root/"home"/("custom-codex" if alternate_check else ".codex")
        if alternate_check:
            env["CODEX_HOME"]=str(native_home)
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

        def launch(from_id=None, expected="launch", overrides=None):
            route = Path(env["SWARM_DAEMON_SOCK"])
            with socket.socket(socket.AF_UNIX) as conn:
                conn.settimeout(16)
                conn.connect(str(route))
                control(conn, "hello", endpoint_id="", protocol_version=1)
                endpoint = read_control(conn)["endpoint_id"]
                options = {"resume_from":from_id} if from_id else {"sandbox":"read-only"}
                request = {"agent": "codex", "name": "codex fixture", "cwd": str(root / "work"),
                    "cols": 100, "rows": 30, "options": options}
                if guard_check and not from_id:
                    request["initial_prompt"]="Warm up the isolated context guard fixture."
                    request["options"]={}
                if overrides:
                    request["env"]=[f"{key}={value}" for key,value in {**env,**overrides}.items()]
                control(conn,"launch",endpoint_id=endpoint,launch=request)
                launched = read_control(conn)
                assert launched["op"] == expected, launched
                return launched["session"]["id"] if expected=="launch" else launched

        if not native:
            (root / "work/.fixture-seeded-rollout").touch()
        with (root / "daemon.log").open("wb") as log:
            daemon = start(log)
            if reattach_check:
                master, slave = pty.openpty()
                fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack("HHHH",30,100,0,0))
                standalone=subprocess.Popen([*guard,"/usr/bin/setsid","--ctty",root/"bin/codex","--no-daemon","--no-alt-screen","External Codex acquisition fixture."],
                    cwd=root/"work",env=env,stdin=slave,stdout=slave,stderr=slave,umask=0o077)
                os.close(slave)
                terminal=bytearray()
                def standalone_reply():
                    if select.select([master],[],[],0.05)[0]:
                        try:
                            terminal.extend(os.read(master,8192))
                        except OSError as exc:
                            raise RuntimeError(f"standalone fixture PTY closed: {terminal[-1600:]!r}") from exc
                    return b"Native loopback reply." in terminal
                try:
                    fixtures.wait_for(standalone_reply,"first reply from the external native CLI",timeout=20)
                finally:
                    if standalone.poll() is None:
                        standalone.terminate()
                        standalone.wait(timeout=5)
                    os.close(master)
                rollout, = list((native_home/"sessions").rglob("*.jsonl"))
                with rollout.open() as f:
                    external=json.loads(f.readline())["payload"]
                assert external["source"]=="cli"
                thread=external["id"]
                cmd=("reattach","--cli","codex","--session",thread)
                assert b"would reattach codex" in invoke(*cmd,"--dry-run")
                invoke(*cmd,expected=1)
                archive=native_home/"archived_sessions"
                archive.mkdir(mode=0o700,exist_ok=True)
                retired=archive/rollout.name
                rollout.rename(retired)
                assert b"unarchive before importing" in invoke(*cmd,"--all","--dry-run")
                invoke(*cmd,"--all","--take-over",expected=1)
                retired.rename(rollout)
                assert json.loads(invoke("ls","--json"))==[]
                imported=invoke(*cmd,"--take-over")
                identity=imported.decode().splitlines()[0].split(" as ")[1]
                row,=json.loads(invoke("ls","--json"))
                assert row["id"]==identity
                fixtures.wait_for(lambda:b"Native loopback reply." in invoke("peek",identity),
                    "Swarm viewer displays the reply from an external native rollout",timeout=15)
                state=root/"state/rust-v1"/identity.split("/")[1]
                assert json.loads((state/"observation.json").read_text())["conversation_id"]==thread
                with rollout.open() as f:
                    observed_turns=sum(x["type"]=="event_msg" and x["payload"]["type"]=="task_started" for x in map(json.loads,f))
                assert observed_turns==1
                invoke(*cmd,"--take-over",expected=1)
                invoke("kill",identity)
                invoke("watch",identity,"--until","completed","--timeout","4s")
                print("external native CLI stopped before reattach; dry-run, archive refusal, exact old thread, and duplicate writer fence passed")
                return
            if not native:
                operation = invoke("spawn", "--new-operation-id").decode().strip()
                base = ("spawn", "--cli", "codex", "--dir", str(root / "work"), "--prompt", "none")
                for selector in ("OPENAI_BASE_URL", "OPENAI_ORGANIZATION", "OPENAI_PROJECT_ID",
                                 "CODEX_CLI_AUTH_CREDENTIALS_STORE", "CODEX_REFRESH_TOKEN_URL_OVERRIDE"):
                    wrong_origin = {selector: "unqualified-test-origin"}
                    invoke(*base, expected=1, overrides=wrong_origin)
                    invoke(*base, "--operation-id", operation, expected=1, overrides=wrong_origin)
                assert json.loads(invoke("ls", "--json")) == []
            if alternate_check:
                retired=root/"home/swarm/logins/provider/codex/native"
                retired.mkdir(mode=0o700,parents=True)
                rejected=launch(expected="error",overrides={"CODEX_HOME":str(retired)})
                assert "retired Swarm-managed" in rejected["error"],rejected
                alias=root/"home/aliased-codex"
                alias.symlink_to(native_home)
                rejected=launch(expected="error",overrides={"CODEX_HOME":str(alias)})
                assert "physical private" in rejected["error"],rejected
            identity = launch()
            row = next(row for row in json.loads(invoke("ls", "--json")) if row["id"]==identity)
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

            if loopback and not guard_check:
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
                invoke("requests", identity, expected=1)
                fake_http("POST", "/fixture/request")
                invoke("requests", identity, expected=1)
                print("Codex keeps native requests in its own terminal")

            daemon.kill()
            daemon.wait(timeout=5)
            daemon = start(log)
            assert json.loads((state / "owner.json").read_text()) == owner
            assert observed() == thread
            assert any(row["id"]==identity for row in json.loads(invoke("ls", "--json")))
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
            if guard_check:
                def settings(operation, **payload):
                    with socket.socket(socket.AF_UNIX) as conn:
                        conn.settimeout(4)
                        conn.connect(env["SWARM_DAEMON_SOCK"])
                        control(conn,"hello",endpoint_id="",protocol_version=1,capabilities=["context-guard-settings"])
                        hello=read_control(conn)
                        control(conn,operation,endpoint_id=hello["endpoint_id"],**payload)
                        reply=read_control(conn)
                        assert reply["op"]==operation,reply
                        return reply["context_guard_settings"]
                def guard_view():
                    row=next(row for row in json.loads(invoke("ls","--json")) if row["id"]==identity)
                    return row.get("context_guard")
                guard=fixtures.wait_for(guard_view,"authenticated 0.151 Context Guard projection")
                assert guard["support"]=="automatic",guard
                fixtures.wait_for(lambda: json.loads(observation.read_text()).get("requests_available"),
                                  "joined before raising usage")
                current=settings("context_guard_get")
                settings("context_guard_set",context_guard_set={"expected_revision":current["revision"],
                    "auto_compact":{"enabled":True,"threshold_percent":80}})
                fixtures.wait_for(lambda:guard_view().get("phase")=="armed","revisioned auto policy")
                accepted=json.loads(invoke("message",identity,"--text","A synthetic context pressure turn."))
                assert accepted["state"]=="native_accepted",accepted
                try:
                    controller, exact, _ = attach(env["SWARM_DAEMON_SOCK"], identity)
                    with controller:
                        model.release_pressure.set()
                        occupied=fixtures.wait_for(lambda: guard_view() if guard_view().get("usage_percent",0)>=80 else None,
                            "exact native occupancy from 0.151")
                        print("native exact context occupancy:",occupied)
                        time.sleep(.8)
                        assert guard_view()["phase"]=="pending_idle"
                        assert not any(row["continuation"] for row in model.history)
                        control(controller,"detach",**exact)
                        while True:
                            tag, body=read_frame(controller)
                            if tag==1:
                                assert json.loads(body)["op"]=="detach"
                                break
                    fixtures.wait_for(lambda:guard_view().get("phase")=="latched","native compaction confirmed",timeout=15)
                    fixtures.wait_for(lambda:any(row["continuation"] for row in model.history),
                                      "native continuation reached only the loopback model",timeout=15)
                    daemon.kill()
                    daemon.wait(timeout=5)
                    daemon=start(log)
                    assert json.loads((state/"owner.json").read_text())==owner
                    fixtures.wait_for(lambda:guard_view() and guard_view().get("last_result")=="compacted",
                                      "guard projection survived owner restart")
                    assert sum(row["continuation"] for row in model.history)==1
                    print("controller lease fenced compaction; after detach native compact and single continuation survived owner restart")
                finally:
                    (root/"effects/context-guard-screen").write_bytes(invoke("peek",identity))
                    (root/"effects/context-guard-history.json").write_text(json.dumps(model.history))
                    (root/"effects/context-guard-view.json").write_text(json.dumps(guard_view()))
                invoke("kill",identity)
                invoke("watch",identity,"--until","completed","--timeout","4s")
                return
            if history_check:
                def history_page(limit=1, before=""):
                    with socket.socket(socket.AF_UNIX) as conn:
                        conn.settimeout(4)
                        conn.connect(env["SWARM_DAEMON_SOCK"])
                        control(conn,"hello",endpoint_id="",protocol_version=1,capabilities=["journal"])
                        hello=read_control(conn)
                        assert "journal" in hello["capabilities"]
                        control(conn,"interaction_history",endpoint_id=hello["endpoint_id"],session_id=identity,
                            history={"session":identity,"limit":limit,"before_item":before})
                        page=read_control(conn)
                        assert page["op"]=="interaction_history"
                        return page
                def latest():
                    page = history_page()
                    return page if page["journal"] and page["journal"][-1]["item"]["kind"]=="agent_message" else None
                current = fixtures.wait_for(latest, "durable native history")
                reply, = current["journal"]
                assert not current["history_floor"] and reply["item"]["kind"]=="agent_message"
                assert reply["item"]["text"]=="Native loopback reply."
                older = history_page(before=reply["item"]["item_id"])
                user, = older["journal"]
                assert older["history_floor"] and user["item"]["text"]=="Return the fixed local fixture response."
                assert user["cursor"] < reply["cursor"]
                missing = history_page(before="forged-item")
                assert missing["history_floor"] and missing["journal"] == []
                ctx = json.loads(invoke("--pilot"))["context"]
                pilot = ("pilot", "--context", ctx)
                seen = json.loads(invoke(*pilot,"view",identity))["worker_data"]
                assert seen["source"]=="structured_history" and seen["history"][-1]==reply
                watched = [json.loads(line) for line in invoke(*pilot,"watch",identity,
                           "--after",str(user["cursor"]),"--once","--timeout","2s").splitlines()]
                assert len(watched)==2 and watched[-1]["worker_data"]["item"]==reply["item"],watched
                invoke(*pilot,"exit")
                print("owner paginated the retained native turn; Pilot read and replayed the verified reply")
            invoke("kill", identity)
            assert json.loads(invoke("watch", identity, "--until", "completed", "--timeout", "4s"))["group"] == "completed"
            print("managed Codex worker and exact native thread survived owner restart")
            if resume_check:
                fixtures.wait_for(lambda: fixtures.generation(owner["pid"]) is None,"old managed shim reaped")
                config=native_home/"config.toml"
                saved=config.read_bytes()
                config.write_bytes(saved.replace(b"127.0.0.1:",b"127.0.0.2:"))
                try:
                    assert launch(identity,"error").get("error_code")=="invalid_field"
                finally:
                    config.write_bytes(saved)
                if sys.platform == "linux":
                    for name in ("config.toml", "requirements.toml", "managed_config.toml"):
                        policy = Path("/etc/codex") / name
                        policy.write_text('model="unqualified-fixture-policy"\n')
                        try:
                            rejected = launch(identity, "error")
                            assert rejected["error_code"] == "invalid_field" and "system configuration" in rejected["error"]
                        finally:
                            policy.unlink()
                rollouts = list((native_home/"sessions").rglob(f"*-{thread}.jsonl"))
                assert len(rollouts) == 1
                def native_turns():
                    events = map(json.loads, rollouts[0].read_text().splitlines())
                    return sum(row["type"]=="event_msg" and row["payload"]["type"]=="task_started" for row in events)
                assert native_turns() == 1
                if alternate_check:
                    ordinary=root/"home/.codex"
                    ordinary.mkdir(mode=0o700)
                    for name in ("auth.json","config.toml"):
                        shutil.copy2(native_home/name,ordinary/name)
                    rejected=launch(identity,"error",{"CODEX_HOME":""})
                    assert "origin or authority changed" in rejected["error"],rejected
                    ordinary_auth=json.loads((native_home/"auth.json").read_text())
                    alternate_auth={**ordinary_auth,"OPENAI_API_KEY":"distinct-synthetic-account"}
                    (native_home/"auth.json").write_text(json.dumps(alternate_auth))
                    try:
                        assert "origin or authority changed" in launch(identity,"error")["error"]
                    finally:
                        (native_home/"auth.json").write_text(json.dumps(ordinary_auth))
                shutil.copyfile(root/"bin/parity/swarm-parity",root/"bin/driver")
                board = fixtures.Board([*guard,str(root/"bin/swarm")], root, env)
                try:
                    board.wait_bytes(b"codex fixture")
                    first_frame=len(board.capture)
                    board.send(b"r")
                    def new_viewer():
                        return next((row["id"] for row in json.loads(invoke("ls","--json"))
                                     if row.get("resumed_from")==identity), None)
                    resumed=fixtures.wait_for(new_viewer,"keyboard board resume",timeout=18)
                    board.wait_bytes(b"OpenAI Codex",start=first_frame)
                finally:
                    (root/"effects/board-resume.capture").write_bytes(board.capture)
                    board.close()
                resumed_state=root/"state/rust-v1"/resumed.split("/")[1]
                assert json.loads((resumed_state/"observation.json").read_text())["conversation_id"]==thread
                assert any(row["id"]==resumed and row["resumed_from"]==identity for row in json.loads(invoke("ls","--json")))
                fixtures.wait_for(lambda: b"Native loopback reply." in invoke("peek",resumed),"resumed native viewer displays prior reply",timeout=15)
                assert native_turns() == 1, "resuming must not replay the original user turn"
                assert launch(identity,"error").get("error_code")=="invalid_field"
                if sys.platform == "linux":
                    # Replay the already-approved fixture plan through the real shim after policy changed.
                    denied = resumed_state.parent/"4f0000000000000000000000"
                    denied.mkdir(mode=0o700)
                    plan = json.loads((resumed_state/"session.json").read_text())
                    plan.update(id=denied.name, token="9f0000000000000000000000")
                    config = denied/"session.json"
                    config.write_text(json.dumps(plan))
                    config.chmod(0o600)
                    policy = Path("/etc/codex/requirements.toml")
                    policy.write_text('allowed_approval_policies=["never"]\n')
                    argv = json.dumps({"argv":["resume",thread]}).encode()
                    try:
                        result = subprocess.run([*command,"shim","--rust-session",denied],
                            input=struct.pack(">IB",len(argv)+1,1)+argv,
                            env=env,cwd=root/"work",capture_output=True,timeout=8)
                        assert result.returncode != 0 and not (denied/"owner.json").exists(), result
                        refused = json.loads((denied/"exit.json").read_text())
                        assert "system configuration" in refused["error"], refused
                    finally:
                        policy.unlink()
                        shutil.rmtree(denied)
                invoke("kill",resumed)
                invoke("watch",resumed,"--until","completed","--timeout","4s")
                if alternate_check:
                    args=("spawn","--cli","codex","--dir",str(root/"work"),"--name","alternate reliable","--prompt","A disposable reliable launch.")
                    operation=invoke("spawn","--new-operation-id").decode().strip()
                    invoke(*args,"--operation-id",operation,expected=1,overrides={"CODEX_HOME":str(root/"home/swarm/logins/provider/codex/native")})
                    operation=invoke("spawn","--new-operation-id").decode().strip()
                    reliable=invoke(*args,"--operation-id",operation).decode().strip()
                    reliable_meta=root/"state/rust-v1"/reliable.split("/")[1]/"session.json"
                    assert json.loads(reliable_meta.read_text())["codex_home"]["path"]==str(native_home)
                    assert invoke(*args,"--operation-id",operation).decode().strip()==reliable
                    invoke("kill",reliable)
                    invoke("watch",reliable,"--until","completed","--timeout","4s")
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
            model.release_pressure.set()
            model.shutdown()
            model.server_close()
        cleanup = fixtures.reap(root, None, previous)
        print("cleanup", cleanup)
        if cleanup["remaining"]:
            raise RuntimeError(f"fixture processes survived cleanup at {root}")

def verify(candidate, provider, native, scratch, loopback, resume_check=False, history_check=False, alternate_check=False, reattach_check=False, guard_check=False):
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
            home = root/"home"/("custom-codex" if alternate_check else ".codex")
            home.mkdir(mode=0o700)
            (home / "config.toml").write_text(f'[projects."{root}/work"]\ntrust_level = "trusted"\n')
        guard = fixtures.guard(root)
        if sys.platform == "darwin":
            drive(root, native, guard, loopback, resume_check, history_check, alternate_check, reattach_check, guard_check)
        else:
            if resume_check:
                system = root/"system-policy"
                system.mkdir(mode=0o700)
                split = guard.index("--")
                guard[split:split] = ["--bind", str(system), "/etc/codex"]
            command = [*guard, sys.executable, root / "bin/parity_codex.py", "--isolated-root", root]
            if native:
                command.append("--native")
            if loopback:
                command.append("--loopback-model")
            if resume_check:
                command.append("--resume-check")
            if history_check:
                command.append("--history-check")
            if alternate_check:
                command.append("--alternate-check")
            if reattach_check:
                command.append("--reattach-check")
            if guard_check:
                command.append("--context-guard-check")
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
    parser.add_argument("--loopback-model",action="store_true",help="use the installed native CLI with an OS-guarded local Responses fixture")
    parser.add_argument("--scratch", type=Path, default=Path("/tmp").resolve())
    parser.add_argument("--isolated-root", type=Path, help=argparse.SUPPRESS)
    parser.add_argument("--resume-check",action="store_true")
    parser.add_argument("--history-check",action="store_true")
    parser.add_argument("--alternate-check",action="store_true")
    parser.add_argument("--reattach-check",action="store_true")
    parser.add_argument("--context-guard-check",action="store_true")
    args = parser.parse_args()
    if args.context_guard_check and not args.loopback_model:
        parser.error("--context-guard-check requires --loopback-model")
    if args.reattach_check and (not args.loopback_model or sys.platform!="linux"):
        parser.error("--reattach-check requires private Linux loopback inference")
    if args.alternate_check and not args.resume_check:
        parser.error("--alternate-check requires --resume-check")
    if args.resume_check and not args.loopback_model:
        parser.error("--resume-check requires --loopback-model")
    if args.history_check and not args.loopback_model:
        parser.error("--history-check requires --loopback-model")
    if args.loopback_model and (not args.native or sys.platform not in ("linux","darwin")):
        parser.error("--loopback-model requires Linux or macOS and --native")
    if args.isolated_root and not args.candidate and Path(__file__).resolve().parent == args.isolated_root/"bin":
        drive(args.isolated_root, args.native, [], args.loopback_model,args.resume_check,args.history_check,args.alternate_check,args.reattach_check,args.context_guard_check)
    elif args.candidate and args.provider and not args.isolated_root:
        verify(args.candidate, args.provider, args.native, args.scratch.resolve(), args.loopback_model,args.resume_check,args.history_check,args.alternate_check,args.reattach_check,args.context_guard_check)
    else:
        parser.error("--candidate and --provider are required")
