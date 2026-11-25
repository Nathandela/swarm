#!/usr/bin/env python3
"""Compare scheduled Antigravity ACP admission, permission and completion on Go and Rust."""
import json
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


def verify(binary, *, allow=True, version="1.5.12", authority="agent-default"):
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
                peek = cli("peek", sid, ok=False)
                pending = rows()[0]["status"]["Interaction"] == "permission" or state["state"] == "needs-input"
                return pending and peek.returncode == 0 and "fixture permission" in peek.stdout

            wait_for(has_permission, "pending human permission")
            cli("send", sid, "--text", "0")
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
        verify(binary, allow=False)
        verify(binary, version="1.5.11")
        verify(binary, authority="observe")
    print("Scheduled ACP version and authority refusal, explicit permission and final assistant proof passed.")
