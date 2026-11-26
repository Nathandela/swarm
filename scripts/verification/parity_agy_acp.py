#!/usr/bin/env python3
"""Compare scheduled Antigravity ACP admission, permission and completion on Go and Rust."""
from contextlib import contextmanager
import json
import fcntl
import os
import pty
import select
import shlex
import shutil
import signal
import termios
import socket
import struct
import time
from pathlib import Path
import subprocess
import sys
import tempfile

from parity_agy import wait_for

FIXTURE = r'''
import json, os, subprocess, sys, time
from pathlib import Path

args = sys.argv[1:]
if args == ["--version"]:
    print(Path(__file__).with_suffix(".version").read_text().strip())
    raise SystemExit(0)
if args == ["--help"]:
    raise SystemExit(0)
assert args == ["acp", "--stdio"], args
assert not any(name.startswith("SWARM_") for name in os.environ)
def read():
    return json.loads(sys.stdin.readline())
def send(content):
    print(json.dumps({"jsonrpc":"2.0",**content}), flush=True)
def result(req, value):
    send({"id":req["id"],"result":value})
def permission(id, conversation):
    send({"id":id,"method":"session/request_permission","params":{
        "sessionId":conversation, "toolCall":{"title":"fixture permission"},
        "options":[{"name":"Allow once","kind":"allow_once","optionId":"allow"}],
    }})
def update(conversation, kind, text=""):
    send({"method":"session/update","params":{"sessionId":conversation,
          "update":{"sessionUpdate":kind,"content":{"type":"text","text":text}}}})

init = read()
assert init["method"] == "initialize" and init["params"]["protocolVersion"] == 1
other = "22222222-2222-4222-8222-222222222222"
ours = "11111111-1111-4111-8111-111111111111"
permission(12, other)
assert read()["result"]["outcome"]["outcome"] == "cancelled"
result(init, {"protocolVersion":1})
new = read()
assert new["method"] == "session/new"
assert Path(new["params"]["cwd"]).resolve() == Path.cwd().resolve()
bridge, = new["params"]["mcpServers"]
assert bridge["name"] == "swarm_schedule" and bridge["args"] == ["schedule-receipt-bridge"]
assert not any(env["name"] in ("SWARM_DAEMON_SOCK","SWARM_DAEMON_STATE") for env in bridge["env"])
result(new, {"sessionId":ours})
prompt = read()
assert prompt["method"] == "session/prompt" and prompt["params"]["sessionId"] == ours
content, = prompt["params"]["prompt"]
assert content["type"] == "text" and "Fixture opening.\nExact 東京" in content["text"]
with Path("prompts").open("a") as out:
    print(json.dumps(content["text"]), file=out)

permission(13, other)
assert read()["result"]["outcome"]["outcome"] == "cancelled"
if Path("challenge-fixture").exists():
    deadline = time.monotonic() + 8
    while not Path("show-permission").exists():
        assert time.monotonic() < deadline
        time.sleep(.05)
    permission(14, ours)
    answer = read()
    assert answer["id"] == 14 and answer["result"]["outcome"] == {
        "outcome":"selected", "optionId":"allow",
    }
    Path("first-permission-approved").touch()
    permission(15, ours)
    stale = read()
    assert stale["id"] == 15 and stale["result"]["outcome"]["outcome"] == "cancelled"
else:
    permission(14, ours)
    owner = read()
    assert owner["id"] == 14 and owner["result"]["outcome"]["outcome"] == "cancelled"

complete = {"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
    "name":"complete","arguments":{"summary":"Fixture assistant result","links":[]},
}}
receipt = subprocess.run([bridge["command"],*bridge["args"]], capture_output=True, text=True,
                    input=json.dumps(complete) + "\n",
                    env={**os.environ,**{row["name"]:row["value"] for row in bridge["env"]}},
                    timeout=8)
assert receipt.returncode == 0 and not json.loads(receipt.stdout)["result"]["isError"]
Path("mcp-receipt-confirmed").touch()
update(ours, "agent_message_chunk", "Fixture assistant result")
update(ours, "tool_call")
update(other, "agent_message_chunk", "Fixture assistant result")
deadline = time.monotonic()+15
while not Path("permit-final").exists():
    assert time.monotonic() < deadline
    time.sleep(.05)
if Path("permit-final").read_text() == "valid":
    update(ours, "agent_message_chunk", "Fixture assistant")
    update(ours, "agent_message_chunk", " result")
result(prompt, {"stopReason":"end_turn"})
'''


def verify(binary, *, allow=True, version="1.5.12", authority="agent-default", approval="", protected=True):
    with tempfile.TemporaryDirectory(prefix="aca-", dir=Path("/tmp").resolve()) as tmp:
        root = Path(tmp)
        home, tools, work, state = (root / key for key in ("home", "bin", "work", "s"))
        for path in (home, tools, work, state):
            path.mkdir(mode=0o700)
        agy = tools / "agy"
        agy.write_text(f"#!{sys.executable}\n" + FIXTURE)
        agy.chmod(0o700)
        agy.with_suffix(".version").write_text(version)
        sock = state / "d"
        env = {"HOME": str(home), "PATH": f"{tools}:/usr/bin:/bin", "SHELL":"/bin/sh",
               "TERM":"xterm-256color", "TMPDIR": str(root), "SWARM_DAEMON_STATE": str(state),
               "SWARM_DAEMON_SOCK":str(sock), "SWARM_DAEMON_REMOTE_SOCK":"",
               "SWARM_DAEMON_LOCK":str(root / "lock"), "SWARM_DAEMON_LOG":str(root / "daemon.log")}

        def cli(*args, ok=True, stdin=None):
            result = subprocess.run([binary,*args], cwd=work, env=env, input=stdin, text=True,
                                    capture_output=True, timeout=15)
            if ok:
                assert result.returncode == 0, (args, result.stderr, result.stdout)
            return result

        def schedule(*args, **kwargs):
            return cli("schedule", *args, **kwargs)

        def rows():
            return json.loads(cli("ls", "--json").stdout) or []

        def plain_peek():
            with (root / "peek-only").open("w") as display:
                subprocess.run([binary,"peek",sid],cwd=work,env=env,stdout=display,
                               stderr=subprocess.DEVNULL,timeout=5)
            return (root / "peek-only").read_text()

        def unverified_peer(action):
            # A new protocol-speaking child has the same live parent and a real stdout pipe.
            program = """
import json, socket, struct, sys
sock, sid, action = sys.argv[1:]
with socket.socket(socket.AF_UNIX) as conn:
    conn.settimeout(5)
    conn.connect(sock)
    with conn.makefile("rwb") as stream:
        def write(**body):
            frame = bytes([1]) + json.dumps(body).encode()
            stream.write(struct.pack(">I", len(frame)) + frame)
            stream.flush()

        def read():
            size, = struct.unpack(">I", stream.read(4))
            frame = stream.read(size)
            assert frame[0] == 1
            return json.loads(frame[1:])

        write(op="hello", protocol_version=1, capabilities=["agy-permission-cli-v1"])
        endpoint = read()["endpoint_id"]
        context = {"endpoint_id": endpoint, "session_id": sid}
        if action == "peek":
            write(op="terminal_subscribe", permission_route="pipe", **context)
            assert read()["op"] == "ok"
            snapshot = read()["terminal"]
            assert not snapshot.get("agy_permission"), snapshot
            write(op="agy_permission_peeked", request="unoffered", **context)
        else:
            write(op="send_input", send_input={"text":"1","submit":True}, **context)
        response = read()
        assert response["op"] == "error", response
"""
            result = subprocess.run(
                [sys.executable, "-c", program, sock, sid, action],
                cwd=work, env=env, capture_output=True, text=True, timeout=8)
            assert result.returncode == 0, (action, result.stdout, result.stderr)

        @contextmanager
        def protocol_attach():
            with socket.socket(socket.AF_UNIX) as conn:
                conn.settimeout(5)
                conn.connect(str(sock))
                with conn.makefile("rwb") as stream:
                    def frame(tag, body):
                        stream.write(struct.pack(">I", len(body) + 1) + bytes([tag]) + body)
                        stream.flush()
                    def receive():
                        length = struct.unpack(">I", stream.read(4))[0]
                        data = stream.read(length)
                        return data[0], data[1:]
                    def control(body):
                        frame(1,json.dumps(body).encode())
                    control({"op":"hello","protocol_version":1,"capabilities":["attach","agy-permission-v1"]})
                    _, welcome = receive()
                    hello = json.loads(welcome)
                    assert "agy-permission-v1" in hello["capabilities"]
                    control({"op":"attach","endpoint_id":hello["endpoint_id"],"session_id":sid})
                    _, leased = receive()
                    lease = json.loads(leased)
                    assert lease["op"] == "lease"
                    remaining = lease["snapshot_len"]
                    while remaining:
                        tag, chunk = receive()
                        assert tag == 4
                        remaining -= len(chunk)
                    def offer():
                        while True:
                            tag, data = receive()
                            if tag == 1:
                                event = json.loads(data)
                                if event["op"] == "agy_permission" and event["request"]:
                                    return event
                    def decision(op, menu, choice=None):
                        control({"op":op,"request":menu["request"],"display":menu["display"],
                                 "generation":menu["generation"],"endpoint_id":hello["endpoint_id"],
                                 "session_id":sid,"choice":choice})
                    yield offer, decision, frame

        class Board:
            def __enter__(self):
                self.master, slave = pty.openpty()
                fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 32, 124, 0, 0))
                self.child = subprocess.Popen([binary],cwd=work,env=env,stdin=slave,stdout=slave,stderr=slave)
                os.close(slave)
                self.buffer = bytearray()
                return self

            def read(self, text):
                def found():
                    if select.select([self.master],[],[],.05)[0]:
                        self.buffer.extend(os.read(self.master,8192))
                    return text in self.buffer
                return wait_for(found,f"real board display {text!r}")

            def send(self, text):
                os.write(self.master,text)

            def __exit__(self,*_):
                self.child.terminate()
                self.child.wait(timeout=5)
                os.close(self.master)

        with (root / "daemon-log").open("wb") as log:
            daemon = subprocess.Popen([binary, "daemon"], cwd=work, env=env, stderr=log)
        sid = None
        try:
            wait_for(lambda: cli("ls", "--json", ok=False).returncode == 0, "owner socket")
            created = schedule("create", "--name", "ACP fixture", "--dir", work,
                "--agents", "agy", "--authority", authority, "--workspace", "directory",
                "--cron", "0 0 31 12 *", "--timezone", "UTC", "--prompt-file", "-",
                stdin="Fixture opening.\nExact 東京", ok=authority == "agent-default")
            if created.returncode != 0:
                assert authority != "agent-default"
                assert "agent-default" in created.stderr or "configured agent permissions" in created.stderr
                assert not rows() and not (work / "prompts").exists()
                return
            job = json.loads(created.stdout)
            if approval:
                (work / "challenge-fixture").touch()
            schedule("run", job["id"])
            def history():
                return (json.loads(schedule("history", job["id"]).stdout) or [None])[0]
            def tick():
                schedule("tick")
                return history()

            if version != "1.5.12" or authority != "agent-default":
                final = wait_for(lambda: (current if (current := tick())["state"] != "working" else None),
                                 "unqualified route refusal")
                assert final["state"] == "exhausted", final
                attempt, = final["attempts"]
                reason = "1.5.12 or newer" if version != "1.5.12" else "requires configured agent permissions"
                assert attempt["state"] == "rejected" and reason in attempt["detail"], attempt
                assert not rows() and not (work / "prompts").exists()
                return
            def get_sid():
                found = rows()
                return found[0]["id"] if len(found) == 1 else None

            sid = wait_for(get_sid, "ACP session")
            def has_permission():
                state = tick()
                screen = plain_peek()
                pending = rows()[0]["status"]["Interaction"] == "permission" or state["state"] == "needs-input"
                return pending and "fixture permission" in screen

            expected_menu = "Enter an option number, or 0 to cancel."
            def open_prompt():
                def prompted():
                    tick()
                    return (work / "prompts").exists()
                wait_for(prompted,"ACP prompt before first permission")

            if approval == "board":
                open_prompt()
                with Board() as board:
                    board.read(b"ACP fixture")
                    board.send(b"\r")
                    time.sleep(.2)
                    board.send(b"1\r")  # Before the first ACP request is displayed.
                    (work / "show-permission").touch()
                    wait_for(has_permission, "pending human permission")
                    board.read(expected_menu.encode())
                    time.sleep(.15)
                    board.send(b"\r")   # A late Enter cannot approve the next empty draft.
                    board.send(b"\x1b[200~unrelated\r1\r\x1b[201~")
                    time.sleep(.15)
                    assert not (work / "first-permission-approved").exists()
                    assert cli("send",sid,"--text","1",ok=False).returncode
                    board.send(b"1\r")
                    wait_for(lambda:(work/"first-permission-approved").exists(),"fresh numbered board approval")
                wait_for(has_permission,"consecutive permission")
                assert cli("send",sid,"--text","1",ok=False).returncode
                cli("send",sid,"--text","0")
            elif approval == "wire":
                open_prompt()
                (work / "show-permission").touch()
                wait_for(has_permission, "pending wire permission")
                with protocol_attach() as (offer, decision, frame):
                    old = offer()
                    decision("agy_permission_reply", old, 1)  # No displayed ACK.
                    frame(3,b"1\r")
                    time.sleep(.1)
                    assert not (work/"first-permission-approved").exists()
                    with protocol_attach() as (new_offer, new_decision, _):
                        first = new_offer()
                        assert first["request"] == old["request"] and first["generation"] != old["generation"]
                        new_decision("agy_permission_displayed", old)
                        new_decision("agy_permission_reply", old, 1)
                        stale_display = old | {"generation":first["generation"]}
                        new_decision("agy_permission_displayed", stale_display)
                        new_decision("agy_permission_reply", stale_display, 1)
                        time.sleep(.1)
                        assert not (work/"first-permission-approved").exists()
                        new_decision("agy_permission_displayed", first)
                        new_decision("agy_permission_reply", first, 1)
                        wait_for(lambda:(work/"first-permission-approved").exists(),"current acknowledged request")
                        second = new_offer()
                        assert second["request"] != first["request"]
                        new_decision("agy_permission_displayed", first)
                        new_decision("agy_permission_reply", first, 1)
                        time.sleep(.1)
                        assert not (work/"mcp-receipt-confirmed").exists()
                        new_decision("agy_permission_displayed", second)
                        new_decision("agy_permission_reply", second, 0)
            elif approval == "tty":
                open_prompt()
                (work / "show-permission").touch()
                wait_for(has_permission, "pending foreground permission")
                pid, fd = pty.fork()
                if pid == 0:
                    os.chdir(work)
                    os.execve("/bin/sh",["/bin/sh","-i"],env | {"PS1":"ACP-SHELL> "})
                def shell_line(command, expected):
                    os.write(fd,(command+"\n").encode())
                    output = bytearray()
                    def visible():
                        if select.select([fd],[],[],.05)[0]:
                            output.extend(os.read(fd,8192))
                        return expected in output
                    wait_for(visible,f"foreground shell output {expected!r}")
                try:
                    shell_line(shlex.join([str(binary),"peek",sid]),expected_menu.encode())
                    shell_line(shlex.join([str(binary),"send",sid,"--text","1"])+" && printf 'APPROVED\\n'","APPROVED\r\n".encode())
                    wait_for(lambda:(work/"first-permission-approved").exists(),"fresh foreground CLI approval")
                finally:
                    os.close(fd)
                    try:
                        os.kill(pid,signal.SIGTERM)
                    except ProcessLookupError:
                        pass
                    os.waitpid(pid,0)
                wait_for(has_permission,"consecutive permission")
                assert cli("send",sid,"--text","1",ok=False).returncode
                cli("send",sid,"--text","0")
            elif approval == "pipe":
                open_prompt()
                (work / "show-permission").touch()
                wait_for(has_permission, "pending human permission")
                if protected:
                    assert cli("send",sid,"--text","1",ok=False).returncode
                    cli("peek",sid,"--lines","1")
                    assert cli("send",sid,"--text","1",ok=False).returncode
                    # A prestarted process retains its original birth even after exec.
                    release = work / "release-cli"
                    prestarted = subprocess.Popen(
                        ["/bin/sh","-c",'while [ ! -e "$1" ]; do sleep .02; done; shift; exec "$@"',
                         "prestarted", release, binary, "send",sid,"--text","1"],
                        cwd=work,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
                if protected:
                    unverified_peer("peek")
                    copied_cli = tools / "copied-swarm"
                    shutil.copy2(binary, copied_cli)
                    peek = subprocess.run([copied_cli, "peek", sid], cwd=work, env=env,
                                          capture_output=True, text=True, timeout=8, check=True)
                    snapshot = peek.stdout
                else:
                    snapshot = cli("peek", sid).stdout
                assert expected_menu in snapshot and "followed by" not in snapshot
                if protected:
                    unverified_peer("send")
                    release.touch()
                    prestarted.communicate(timeout=8)
                    assert prestarted.returncode, "prestarted job used a later peek acknowledgment"
                    assert not (work/"first-permission-approved").exists()
                    different = subprocess.run(
                        [sys.executable,"-c","import subprocess,sys; sys.exit(subprocess.call(sys.argv[1:]))",
                         binary,"send",sid,"--text","1"],cwd=work,env=env,capture_output=True,timeout=8)
                    assert different.returncode, "unrelated parent used the observer's grant"
                    assert cli("send",sid,"--text","1","--no-submit",ok=False).returncode
                    assert cli("send",sid,"--key","enter",ok=False).returncode
                cli("send", sid, "--text", "1")
                wait_for(lambda:(work/"first-permission-approved").exists(),"fresh pipe approval")
                wait_for(has_permission,"consecutive permission")
                if protected:
                    assert cli("send",sid,"--text","1",ok=False).returncode
                cli("send",sid,"--text","0")
            else:
                wait_for(has_permission, "pending human permission")
                assert expected_menu in plain_peek()
                cli("send",sid,"--text","0")
            wait_for(lambda: (work / "mcp-receipt-confirmed").exists(), "real scoped MCP bridge receipt")
            assert tick()["state"] != "ready", "transport receipt projected before final assistant proof"
            (work / "permit-final").write_text("valid" if allow else "invalid")
            expected = "ready" if allow else "incomplete"
            completed = wait_for(lambda: (current if (current := tick())["state"] == expected else None),
                                 f"terminal schedule result {expected}")
            assert completed["completion"]["summary"] == "Fixture assistant result"
            assert len((work / "prompts").read_text().splitlines()) == 1
        except Exception:
            print(f"Daemon output for {binary}: {(root / 'daemon-log').read_text()}", file=sys.stderr)
            raise
        finally:
            if sid:
                cli("kill", sid, ok=False)
            daemon.terminate()
            try:
                daemon.wait(timeout=5)
            except subprocess.TimeoutExpired:
                daemon.kill()
                daemon.wait()


if __name__ == "__main__":
    for binary in (Path(path).resolve(strict=True) for path in sys.argv[1:]):
        print(binary, flush=True)
        verify(binary)
        rust = subprocess.check_output([binary, "version"], text=True).strip().endswith("(Rust)")
        verify(binary, approval="pipe", protected=rust)
        if rust:
            verify(binary, approval="board")
            verify(binary, approval="wire")
            verify(binary, approval="tty")
        verify(binary, allow=False)
        verify(binary, version="1.5.11")
        verify(binary, authority="observe")
    print("Scheduled ACP version and authority refusal, explicit permission and final assistant proof passed.")
