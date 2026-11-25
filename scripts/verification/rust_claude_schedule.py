#!/usr/bin/env python3
"""Run the Rust scheduled Claude route with a private native binary and loopback inference."""
import http.server
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading
import time

from native_fixture_sandbox import guarded, check_guards

SUMMARY = "Synthetic scheduled Claude completed."
MARKER = "SCHEDULE_CLAUDE_OPENING_WITNESS"
COMMAND = '"$SWARM_SCHEDULE_BIN" schedule complete --summary "' + SUMMARY + '"'
# This executable paints a modal immediately after it receives the bracketed paste.
# Receipt of any subsequent Enter is an exact, independently recorded failure.
LATE_MODAL = r"""
import os
from pathlib import Path
import sys
import tty
if "--version" in sys.argv:
    print("2.1.329 (Claude Code)")
    sys.exit(0)
tty.setraw(0)
cols, rows = os.get_terminal_size(0)
rule = "─" * cols
os.write(1, f"\x1b[?1049h\x1b[?2004h\x1b[{rows-3};1H{rule}\x1b[{rows-2};1H❯  \x1b[{rows-1};1H{rule}\x1b[{rows-2};3H".encode())
data = b""
while b"\x1b[201~" not in data:
    chunk = os.read(0, 4096)
    if not chunk:
        sys.exit(1)
    data += chunk
    if len(data) > 1 << 20:
        sys.exit(1)
assert data.startswith(b"\x1b[200~")
os.write(1, "\x1b[2J\x1b[5;1HYes, I trust this folder\x1b[6;1H❯ No, exit".encode())
Path("late-modal-painted").touch()
while True:
    received = os.read(0, 4096)
    if not received:
        sys.exit(0)
    if b"\r" in received:
        Path("late-modal-received-enter").touch()
"""



class Model(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        if "/messages" not in self.path:
            self.send_error(404)
            return
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        messages = json.dumps(body.get("messages", []))
        primary = MARKER in messages
        tool = primary and "tool_result" not in messages
        if primary:
            self.server.turns.append("tool" if tool else "answer")
        model = body.get("model")
        text = SUMMARY + " The assistant also recorded this closing detail."
        if not body.get("stream"):
            data = dict(id="msg_aux", type="message", role="assistant", model=model,
                        content=[dict(type="text", text="complete")], stop_reason="end_turn",
                        usage=dict(input_tokens=1, output_tokens=1))
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(data).encode())
            return
        if primary and not tool:
            self.server.waiting_to_close.set()
            if not self.server.close_turn.wait(15):
                self.send_error(504)
                return
        if tool:
            content = dict(type="tool_use", id="toolu_schedule_fixture", name="Bash", input={})
            delta = dict(type="input_json_delta", partial_json=json.dumps(
                dict(command=COMMAND, description="Record synthetic scheduled completion")))
        else:
            content = dict(type="text", text="")
            delta = dict(type="text_delta", text=text)
        events = [
            ("message_start", dict(message=dict(id="msg_schedule_" + str(len(self.server.turns)),
             type="message", role="assistant", model=model, content=[],
             stop_reason=None, stop_sequence=None, usage=dict(input_tokens=1, output_tokens=0)))),
            ("content_block_start", dict(index=0, content_block=content)),
            ("content_block_delta", dict(index=0, delta=delta)),
            ("content_block_stop", dict(index=0)),
            ("message_delta", dict(delta=dict(stop_reason="tool_use" if tool else "end_turn",
                                              stop_sequence=None), usage=dict(output_tokens=1))),
            ("message_stop", {}),
        ]
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for event, fields in events:
            self.wfile.write(f"event: {event}\ndata: {json.dumps(dict(type=event, **fields))}\n\n".encode())


def verify(swarm, installed, root):
    home, work, state = [root / name for name in ("home", "work", "state")]
    for path in [home, work, state, home / ".claude", root / "bin"]:
        path.mkdir(mode=0o700)
    claude = root / "bin" / "claude"
    shutil.copyfile(installed, claude)
    claude.chmod(0o700)
    with http.server.ThreadingHTTPServer(("127.0.0.1", 0), Model) as model:
        model.turns = []
        model.waiting_to_close = threading.Event()
        model.close_turn = threading.Event()
        ports = [model.server_port]
        threading.Thread(target=model.serve_forever, daemon=True).start()
        try:
            check_guards(root, ports)
            env = dict(PATH=f"{root / 'bin'}:/usr/bin:/bin", HOME=str(home),
                       TMPDIR=str(root), TERM="xterm-256color", LANG="en_US.UTF-8",
                       ANTHROPIC_API_KEY="fixture-schedule",
                       SWARM_DAEMON_STATE=str(state), SWARM_DAEMON_SOCK=str(root / "d.sock"),
                       SWARM_DAEMON_LOCK=str(root / "d.lock"))

            def run(*args, stdin=None, check=True):
                result = subprocess.run([swarm, *args], env=env, capture_output=True,
                                        text=True, input=stdin, timeout=10)
                if check and result.returncode != 0:
                    raise AssertionError((args, result.stdout, result.stderr))
                return result

            version = subprocess.check_output(guarded([str(claude), "--version"], root, ports),
                                              env=env, text=True).split()[0]
            (home / ".claude.json").write_text(json.dumps(dict(
                hasCompletedOnboarding=True, lastOnboardingVersion=version, theme="dark",
                customApiKeyResponses=dict(approved=[env["ANTHROPIC_API_KEY"]], rejected=[]),
                projects={str(work): dict(hasTrustDialogAccepted=True)})))
            (home / ".claude" / "settings.json").write_text(json.dumps(dict(
                env=dict(ANTHROPIC_BASE_URL=f"http://127.0.0.1:{model.server_port}",
                         CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC="1", DISABLE_AUTOUPDATER="1",
                         CLAUDE_CODE_MAX_RETRIES="0", CLAUDE_CODE_TMPDIR=str(root)),
                permissions=dict(disableAutoMode="disable", allow=["Bash"]))))
            with (root / "daemon.log").open("w+") as log:
                daemon = subprocess.Popen(guarded([swarm, "daemon"], root, ports), env=env,
                                          stdout=log, stderr=log, start_new_session=True)
                try:
                    def until(check, timeout=20):
                        end = time.monotonic() + timeout
                        while time.monotonic() < end:
                            if value := check():
                                return value
                            assert daemon.poll() is None
                            time.sleep(0.1)
                        raise AssertionError("timed out waiting for native schedule evidence")

                    until((root / "d.sock").is_socket)
                    assert run("schedule", "complete", "--summary", SUMMARY, check=False).returncode
                    job = json.loads(run("schedule", "create", "--name", "Claude fixture",
                                         "--dir", str(work), "--workspace", "directory", "--authority",
                                         "agent-default", "--agents", "claude", "--weekly", "Monday",
                                         "--time", "09:00", "--prompt-file", "-", stdin=MARKER).stdout)
                    job_id = job["id"]
                    operation = "01K6EZMTP3SKR5SWS68SGQH74A"
                    occurrence = run("schedule", "run", job_id, "--operation-id", operation).stdout.strip()
                    assert run("schedule", "run", job_id, "--operation-id", operation).stdout.strip() == occurrence

                    def completed():
                        run("schedule", "tick")
                        rows = json.loads(run("schedule", "history", job_id).stdout)
                        assert len(rows) == 1 and len(rows[0]["attempts"]) == 1, rows
                        if rows[0]["state"] == "ready":
                            return rows[0]
                        if rows[0]["state"] in ("incomplete", "failed"):
                            raise AssertionError(rows[0])
                        return None

                    def receipt_waiting():
                        run("schedule", "tick")
                        return model.waiting_to_close.is_set()

                    until(receipt_waiting)
                    waiting = json.loads(run("schedule", "history", job_id).stdout)[0]
                    assert waiting["state"] == "working" and waiting["completion"]["summary"] == SUMMARY, waiting
                    model.close_turn.set()
                    result = until(completed)
                    attempt = result["attempts"][0]
                    local = attempt["session_id"].split("/")[-1]
                    session = next(state.rglob(f"{local}/session.json")).parent
                    finish = json.loads((session / "schedule-finish.json").read_text())
                    meta = json.loads((session / "session.json").read_text())
                    assert finish["conversation_id"] == meta["schedule_claude_conversation"]
                    assert "assistant also recorded" in finish["text"]
                    assert meta["schedule_projected"] is True
                    assert model.turns == ["tool", "answer"], model.turns
                    untrusted = root / "untrusted"
                    untrusted.mkdir(mode=0o700)
                    untrusted_job = json.loads(run(
                        "schedule", "create", "--name", "No folder trust", "--dir", str(untrusted),
                        "--workspace", "directory", "--authority", "agent-default", "--agents",
                        "claude", "--weekly", "Monday", "--time", "09:00", "--prompt-file", "-",
                        stdin=MARKER).stdout)["id"]
                    run("schedule", "run", untrusted_job)

                    def refused_gate():
                        run("schedule", "tick")
                        row = json.loads(run("schedule", "history", untrusted_job).stdout)[0]
                        return row if row["state"] == "needs-input" else None

                    waiting_gate = until(refused_gate)
                    assert not waiting_gate["attempts"][0].get("prompt_started")
                    assert model.turns == ["tool", "answer"], model.turns
                    unusable = root / "unrepresentable"
                    unusable.mkdir(mode=0o700)
                    for control in ("\t", "\r"):
                        rejected = json.loads(run(
                            "schedule", "create", "--name", "Unrepresentable prompt", "--dir", str(unusable),
                            "--workspace", "directory", "--authority", "agent-default", "--agents",
                            "claude,agy", "--weekly", "Monday", "--time", "09:00", "--prompt-file", "-",
                            stdin="before" + control + "after").stdout)["id"]
                        run("schedule", "run", rejected)
                        refused = json.loads(run("schedule", "history", rejected).stdout)[0]
                        failure = refused["attempts"][0]
                        assert refused["state"] == "exhausted" and failure["state"] == "rejected", refused
                        assert "cannot preserve tabs, carriage returns" in failure["detail"], failure
                        assert not failure.get("session_id") and not failure.get("prompt_started"), failure
                        assert [attempt["agent"] for attempt in refused["attempts"]] == ["claude", "agy"]

                    # Existing workers own copied binaries and are stopped before replacing
                    # the scratch executor path with the deterministic modal fixture.
                    for row in json.loads(run("ls", "--json").stdout):
                        run("kill", row["id"])
                    until(lambda: all(row["status"]["Process"] == "exited"
                                      for row in json.loads(run("ls", "--json").stdout)))
                    claude.rename(claude.with_name("claude-installed"))
                    claude.write_text(f"#!{sys.executable}\n" + LATE_MODAL)
                    claude.chmod(0o700)
                    late = root / "late"
                    late.mkdir(mode=0o700)
                    late_job = json.loads(run(
                        "schedule", "create", "--name", "Late modal", "--dir", str(late),
                        "--workspace", "directory", "--authority", "agent-default", "--agents",
                        "claude", "--weekly", "Monday", "--time", "09:00", "--prompt-file", "-",
                        stdin=MARKER).stdout)["id"]
                    run("schedule", "run", late_job)

                    def late_modal():
                        run("schedule", "tick")
                        row = json.loads(run("schedule", "history", late_job).stdout)[0]
                        return row if row["state"] == "attention" else None

                    closed = until(late_modal)
                    assert closed["attempts"][0]["prompt_started"]
                    assert not closed["attempts"][0].get("prompt_delivered")
                    assert (late / "late-modal-painted").exists()
                    assert not (late / "late-modal-received-enter").exists()
                    print(json.dumps(dict(version=version, synthetic_inference=True,
                                           completed=True, native_closing_message=True,
                                           replayed_one_occurrence=True, trust_gate_left_untouched=True,
                                           receipt_alone_was_working=True, control_prompt_refused=True,
                                           injected_late_modal_untouched=True, model_requests=model.turns)))
                except Exception:
                    log.flush()
                    log.seek(0)
                    print(log.read(), file=sys.stderr)
                    for row in json.loads(run("ls", "--json").stdout):
                        print(run("peek", row["id"], check=False).stdout, file=sys.stderr)
                    for path in state.rglob("observation.json"):
                        print(json.loads(path.read_text())["status"], file=sys.stderr)
                    raise
                finally:
                    model.close_turn.set()
                    if daemon.poll() is None:
                        try:
                            for row in json.loads(run("ls", "--json").stdout):
                                run("kill", row["id"], check=False)
                        finally:
                            daemon.terminate()
                            daemon.wait(timeout=5)
        finally:
            model.shutdown()


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: rust_claude_schedule.py /path/to/rust/swarm /path/to/installed/claude")
    with tempfile.TemporaryDirectory(prefix="sccl-", dir="/tmp") as temp:
        verify(str(Path(sys.argv[1]).resolve()), Path(sys.argv[2]).resolve(), Path(temp).resolve())
