#!/usr/bin/env python3
"""Run the Rust dispatcher against installed Claude and a synthetic loopback model."""
import argparse
import base64
from contextlib import ExitStack
import hashlib
import http.server
import json
import os
import re
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import tempfile
import threading
import time

from native_fixture_sandbox import guarded, check_guards
from parity_runtime import fixtures
from rust_claude_worker_input import save


def tool_result(request, identifier):
    return next((item for row in request.get("messages", [])
                 if isinstance(row.get("content"), list) for item in row["content"]
                 if item.get("type") == "tool_result" and item.get("tool_use_id") == identifier), {})


def mcp_object(result):
    content = result.get("content", [])
    assert not result.get("is_error"), f"MCP tool returned an error: {str(content)[:512]}"
    text = content if isinstance(content, str) else next(
        (block.get("text", "") for block in content if block.get("type") == "text"), ""
    )
    try:
        return json.loads(text)
    except json.JSONDecodeError as error:
        raise AssertionError(f"MCP tool returned non-JSON text: {text[:512]}") from error


def wait_for_claude_worker_close(root, cwd, receipt):
    worker = root / "s/rust-v1" / receipt["session"].split("/")[-1]
    project = root / "h/.claude/projects" / re.sub(r"[^a-zA-Z0-9]", "-", str(cwd))
    deadline = time.monotonic() + 12
    while time.monotonic() < deadline:
        try:
            observation = json.loads((worker / "observation.json").read_text())
            conversation = observation.get("conversation_id")
            if conversation and observation["status"]["Turn"] == "idle":
                transcript = (project / f"{conversation}.jsonl").read_bytes()
                for line in transcript.rpartition(b"\n")[0].splitlines():
                    row = json.loads(line)
                    if (row.get("type") == "system" and row.get("subtype") == "turn_duration"
                            and row.get("sessionId") == conversation and row.get("cwd") == str(cwd)):
                        return
        except FileNotFoundError:
            pass
        time.sleep(0.05)
    raise AssertionError("the exact Claude worker never recorded native turn completion")


class Model(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def codex_reply(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        assert request["model"] == "fixture"
        self.server.codex_requests.append(request)
        instruction = "Start the exact enrolled worker follow-up."
        text = ("Codex scoped follow-up completed." if instruction in json.dumps(request.get("input",[]))
                else "Codex scoped worker fixture.")
        item = dict(type="message", id="msg_worker", role="assistant", status="completed",
                    content=[dict(type="output_text", text=text, annotations=[])])
        response = dict(id="resp_worker", object="response", model="fixture",
                        created_at=int(time.time()), status="in_progress", output=[])
        if self.server.missing_reply:
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            data = json.dumps(dict(type="response.created", response=response))
            self.wfile.write(f"event: response.created\ndata: {data}\n\n".encode())
            self.wfile.flush()
            (self.server.release.parent / ".fixture-worker-running").touch()
            self.server.worker_release.wait(timeout=40)
            self.close_connection = True
            return
        events = [
            ("response.created", dict(response=response)),
            ("response.output_item.added", dict(output_index=0, item=item)),
            ("response.output_text.delta", dict(item_id=item["id"], output_index=0, content_index=0, delta=text)),
            ("response.output_item.done", dict(output_index=0, item=item)),
            ("response.completed", dict(response=dict(response, status="completed", output=[item],
                usage=dict(input_tokens=10, output_tokens=7, total_tokens=17,
                           input_tokens_details=dict(cached_tokens=0), output_tokens_details=dict(reasoning_tokens=0))))),
        ]
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for kind, event in events:
            self.wfile.write(f"event: {kind}\ndata: {json.dumps(dict(event, type=kind))}\n\n".encode())

    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"data":[]}')

    def do_POST(self):
        if self.server.codex and self.path == "/v1/responses":
            self.codex_reply()
            return
        if "/messages" not in self.path:
            self.send_error(404)
            return
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        tools = [tool.get("name") for tool in request.get("tools", [])]
        content = json.dumps(request.get("messages", []))
        envelope = "[SWARM_MANAGER_INPUT_V1:" in content
        main = envelope and ("mcp__swarm_voice__publish" in tools or self.server.vm and "Bash" in tools)
        discover = main and self.server.vm and "mcp__swarm_voice__publish" not in tools \
            and "ToolSearch" in tools and "toolu_fixture_vm_discover" not in content
        if envelope:
            self.server.seen_tools.append(tools)
        wake = main and "Fixture worker retained across parking." in content
        forwarded = main and self.server.forwarded and "Visible forwarded fixture quote" in content
        data = b"a,b\n1,2\n"
        read_attachment = forwarded and "toolu_fixture_forwarded_read" not in content
        read_note = wake and "toolu_fixture_swarm_read_note" not in content
        spawn = main and self.server.vm and not discover and "toolu_fixture_vm_spawn" not in content
        if main and self.server.vm and not spawn and not discover:
            result = tool_result(request,"toolu_fixture_vm_spawn")
            report = json.dumps(result.get("content", ""))
            exact = not result.get("is_error") and re.search(r"ep-[0-9a-f]{8}/[0-9a-f]{24}", report)
            if not exact:
                self.server.vm_failure = report
                self.send_error(400)
                return
        structured = None
        if main and self.server.missing_reply:
            created = tool_result(request, "toolu_fixture_enrolled_create")
            if not created:
                structured = ("swarm_create", "toolu_fixture_enrolled_create", dict(action_id="native-enrolled-job",
                    agent="codex", cwd=str(self.server.approved), prompt="Wait for the synthetic structured worker fixture."))
            else:
                assert mcp_object(created)["state"] == "created", created
        elif main and self.server.enrolled:
            denied = tool_result(request,"toolu_fixture_off_project")
            created = tool_result(request,"toolu_fixture_enrolled_create")
            replay = tool_result(request,"toolu_fixture_enrolled_replay")
            status = tool_result(request,"toolu_fixture_enrolled_status")
            listed = tool_result(request,"toolu_fixture_enrolled_list")
            wrong = tool_result(request,"toolu_fixture_enrolled_wrong_instance")
            wrong_job = tool_result(request,"toolu_fixture_enrolled_wrong_job")
            exact_turn = tool_result(request,"toolu_fixture_enrolled_exact_turn")
            send = tool_result(request,"toolu_fixture_enrolled_send")
            send_replay = tool_result(request,"toolu_fixture_enrolled_send_replay")
            send_conflict = tool_result(request,"toolu_fixture_send_conflict")
            send_denied = tool_result(request,"toolu_fixture_send_revoked")
            send_status = [mcp_object(item) for i in range(12)
                           if (item := tool_result(request, f"toolu_fixture_send_status_{i}"))]
            follow_up = [mcp_object(item) for i in range(5)
                         if (item := tool_result(request, f"toolu_fixture_send_read_{i}"))]
            attempts = [tool_result(request,f"toolu_fixture_enrolled_read_{i}") for i in range(5)]
            reads = [mcp_object(item) for item in attempts if item]
            if reads and not self.server.codex:
                receipt = mcp_object(created)
                assert all(reads[-1][key] == receipt[key]
                           for key in ("session", "session_instance", "attempt_id", "job_id"))
                assert reads[0]["state"] == "pending", reads[0]
            after = tool_result(request,"toolu_fixture_enrolled_next")
            post_delete_status = tool_result(request,"toolu_fixture_deleted_status")
            post_delete_create = tool_result(request,"toolu_fixture_deleted_create")
            post_delete_read = tool_result(request,"toolu_fixture_deleted_read")
            arguments = dict(action_id="native-enrolled-job",agent="codex" if self.server.codex else "claude",
                             cwd=str(self.server.approved),prompt="Wait for the synthetic structured worker fixture.")
            if not denied:
                structured = ("swarm_create","toolu_fixture_off_project",
                              arguments | dict(cwd=str(self.server.unapproved)))
            elif not created:
                assert "Manager request was not confirmed." in json.dumps(denied)
                structured = ("swarm_create","toolu_fixture_enrolled_create",arguments)
            elif not replay:
                assert mcp_object(created)["state"] == "created", json.dumps(created)
                structured = ("swarm_create","toolu_fixture_enrolled_replay",arguments)
            elif not status:
                assert mcp_object(created) == mcp_object(replay)
                structured = ("swarm_status","toolu_fixture_enrolled_status",
                              dict(action_id=mcp_object(created)["action_id"]))
            elif not listed:
                assert mcp_object(created) == mcp_object(status)
                if self.server.deleted:
                    (self.server.release.parent/".fixture-enrolled-deleted").touch()
                structured = ("swarm_list","toolu_fixture_enrolled_list",dict(limit=1))
            elif self.server.deleted:
                if not post_delete_status:
                    structured = ("swarm_status","toolu_fixture_deleted_status",
                                 dict(action_id=mcp_object(created)["action_id"]))
                elif not post_delete_create:
                    structured = ("swarm_create","toolu_fixture_deleted_create", arguments | dict(action_id="late-action"))
                elif not post_delete_read:
                    receipt = mcp_object(created)
                    structured = ("swarm_read","toolu_fixture_deleted_read",dict(session=receipt["session"],
                                    session_instance=receipt["session_instance"]))
                else:
                    for denied_result in (listed,post_delete_status,post_delete_create,post_delete_read):
                        assert "Manager request was not confirmed." in json.dumps(denied_result)
                        assert "session_instance" not in json.dumps(denied_result)
                    (self.server.release.parent/".fixture-enrolled-deleted-checked").touch()
            elif not after:
                workers = mcp_object(listed)["workers"]
                assert len(workers) == 1 and workers[0]["session"] == mcp_object(created)["session"]
                structured = ("swarm_list","toolu_fixture_enrolled_next",
                              dict(limit=1,after_session_id=workers[0]["session"]))
            elif not wrong:
                assert mcp_object(after)["workers"] == []
                structured = ("swarm_read","toolu_fixture_enrolled_wrong_instance",
                    dict(session=mcp_object(created)["session"],session_instance="wrong-incarnation"))
            elif not wrong_job and self.server.codex:
                receipt = mcp_object(created)
                structured = ("swarm_read","toolu_fixture_enrolled_wrong_job",
                    dict(session=receipt["session"],session_instance=receipt["session_instance"],job_id="wrong-job"))
            elif not reads or reads[-1]["state"] == "pending":
                if self.server.codex:
                    assert "Manager request was not confirmed." in json.dumps(wrong_job)
                assert "Manager request was not confirmed." in json.dumps(wrong)
                assert len(reads) < len(attempts), "native enrolled worker never provided a scoped result"
                receipt = mcp_object(created)
                if reads and not self.server.codex:
                    self.server.claude_worker_pending.set()
                    wait_for_claude_worker_close(self.server.release.parent, self.server.approved, receipt)
                structured = ("swarm_read",f"toolu_fixture_enrolled_read_{len(reads)}",
                    dict(session=receipt["session"],session_instance=receipt["session_instance"],
                         source="native",job_id=receipt["job_id"],attempt_id=receipt["attempt_id"]))
            else:
                assert reads[-1]["state"] == "completed"
                if self.server.codex:
                    scoped = reads[-1]
                    assert scoped["source"] == "authorized_worker_turn"
                    assert scoped["job_id"] == mcp_object(created)["action_id"]
                    assert scoped["attempt_id"] == mcp_object(listed)["workers"][0]["tracking"]["attempt_id"]
                    assert scoped["evidence"]["text"] == "Codex scoped worker fixture."
                    assert re.fullmatch(r"[a-f0-9-]{36}", scoped["evidence"]["turn_id"])
                    if not exact_turn:
                        structured = ("swarm_read","toolu_fixture_enrolled_exact_turn",
                            dict(session=scoped["session"],session_instance=scoped["session_instance"],
                                 source="native",job_id=scoped["job_id"],attempt_id=scoped["attempt_id"],
                                 native_turn_id=scoped["evidence"]["turn_id"]))
                    elif self.server.codex_send:
                        assert mcp_object(exact_turn) == scoped
                        args = dict(session=scoped["session"], session_instance=scoped["session_instance"],
                                    action_id="stable-codex-follow-up",text="Start the exact enrolled worker follow-up.")
                        if not send:
                            structured = ("swarm_send","toolu_fixture_enrolled_send",args)
                        elif self.server.codex_send_revoked:
                            assert mcp_object(send)["state"] == "queued"
                            revoked = self.server.release.parent / ".fixture-enrolled-deleted"
                            deadline = time.monotonic() + 6
                            while not revoked.is_file() and time.monotonic() < deadline:
                                time.sleep(0.04)
                            assert revoked.is_file(), "effect-time connector witness was never called"
                            if not send_denied:
                                structured = ("swarm_status","toolu_fixture_send_revoked",
                                              dict(action_id=mcp_object(send)["action_id"]))
                            else:
                                assert "Manager request was not confirmed." in json.dumps(send_denied)
                                (self.server.release.parent / ".fixture-enrolled-deleted-checked").touch()
                        elif not send_replay:
                            assert mcp_object(send)["state"] == "queued"
                            structured = ("swarm_send","toolu_fixture_enrolled_send_replay",args)
                        elif not send_conflict:
                            structured = ("swarm_send","toolu_fixture_send_conflict",
                                          args | dict(text="This divergent action must never reach the worker."))
                        elif not send_status or send_status[-1]["state"] in ("queued","executing"):
                            assert "Manager request was not confirmed." in json.dumps(send_conflict)
                            assert mcp_object(send)["operation_id"] == mcp_object(send_replay)["operation_id"]
                            assert len(send_status) < 12
                            structured = ("swarm_status",f"toolu_fixture_send_status_{len(send_status)}",
                                          dict(action_id=mcp_object(send)["action_id"]))
                        elif not follow_up or follow_up[-1]["state"] == "pending":
                            assert send_status[-1]["state"] == "native_accepted", send_status[-1]
                            assert len(follow_up) < 5
                            receipt = send_status[-1]["receipt"]
                            structured = ("swarm_read",f"toolu_fixture_send_read_{len(follow_up)}",
                                dict(session=scoped["session"],session_instance=scoped["session_instance"],
                                     source="native",job_id=scoped["job_id"],attempt_id=scoped["attempt_id"],
                                     native_turn_id=receipt["native_turn_id"]))
                        else:
                            assert follow_up[-1]["evidence"]["text"] == "Codex scoped follow-up completed."
                            assert follow_up[-1]["evidence"]["turn_id"] != scoped["evidence"]["turn_id"]
                    else:
                        assert mcp_object(exact_turn) == scoped
                else:
                    assert reads[-1]["text"] == "Fixture"
                    assert reads[-1]["native_input_message_id"] and reads[-1]["native_result_message_id"]
        call_id = "toolu_fixture_parked_publish" if wake else "toolu_fixture_swarm_publish"
        may_publish = not forwarded or self.server.forwarded == "allowed"
        if forwarded and not read_attachment:
            received = base64.b64encode(data).decode() in content
            if self.server.forwarded == "allowed":
                assert received, "the model did not receive the authorized pinned bytes"
            else:
                assert not received and "Manager request was not confirmed." in content, \
                    "the changed forwarded receipt was not refused"
            self.server.forwarded_checked.write_text("classified")
        publish = main and not (self.server.deleted and post_delete_read or self.server.codex_send_revoked and send_denied) and not structured and not discover and not spawn and not read_note and not read_attachment and may_publish and call_id not in content
        tool = publish or spawn or discover or read_note or read_attachment or structured
        usage = dict(input_tokens=1, output_tokens=1)
        model = request.get("model")
        message = dict(id="msg_fixture", type="message", role="assistant", model=model,
                       usage=usage, content=[], stop_reason=None, stop_sequence=None)
        if not request.get("stream"):
            message.update(content=[dict(type="text", text="Fixture")], stop_reason="end_turn")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(message).encode())
            return
        if main:
            if self.headers.get("Authorization") != "Bearer synthetic-token":
                self.send_error(401)
                return
            self.server.main_requests.append((publish, content, tools))
        if tool:
            if structured:
                name, call, args = structured
            elif discover:
                name, call = "ToolSearch", "toolu_fixture_vm_discover"
                args = dict(query="select:mcp__swarm_voice__publish")
            elif spawn:
                name, call = "Bash", "toolu_fixture_vm_spawn"
                args = dict(command=self.server.spawn_command, description="Launch the selected synthetic worker")
            elif read_attachment:
                name, call = "swarm_read_attachment", "toolu_fixture_forwarded_read"
                args = dict(id="421", sha256=hashlib.sha256(data).hexdigest(), offset=0)
                if self.server.forwarded == "revoked":
                    self.server.revoke.write_text("changed")
            elif read_note:
                name, call, args = "swarm_read_note", "toolu_fixture_swarm_read_note", {}
            else:
                name, call = "publish", call_id
                text = "Restored worker note reached Claude." if wake else (
                    "Read the pinned forwarded file." if forwarded else "Bounded native Claude reply.")
                args = dict(output_id="native-parked" if wake else "native-claude",
                            sequence=1, final=not self.server.missing_reply, text=text)
            block = dict(type="tool_use", name=name if spawn or discover else f"mcp__swarm_voice__{name}", id=call, input={})
            delta = dict(type="input_json_delta", partial_json=json.dumps(args))
        else:
            block = dict(type="text", text="")
            delta = dict(type="text_delta", text="Fixture")
        events = [
            ("message_start", dict(message=message)),
            ("content_block_start", dict(index=0, content_block=block)),
            ("content_block_delta", dict(index=0, delta=delta)),
            ("content_block_stop", dict(index=0)),
            ("message_delta", dict(delta=dict(stop_reason="tool_use" if tool else "end_turn",
                                              stop_sequence=None), usage=usage)),
            ("message_stop", {}),
        ]
        if (self.server.enrolled and not self.server.deleted and not self.server.codex and not main
                and "Wait for the synthetic structured worker fixture." in content):
            assert self.server.claude_worker_pending.wait(12), "manager never observed a pending Claude result"
        if self.server.park and main and not wake and not tool:
            end = time.monotonic() + 12
            while not self.server.release.is_file() and time.monotonic() < end:
                time.sleep(0.04)
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for event, data in events:
            self.wfile.write(f"event: {event}\ndata: {json.dumps(dict(type=event, **data))}\n\n".encode())
            self.wfile.flush()


def macos_worker_command(root, state, executable, port):
    from macos_codex_fixture import Guard

    # A fixed rendezvous limits the sandbox to the single fixture worker's socket.
    endpoint = root / "fixture-codex.sock"
    guard = Guard(root, state, port, False, endpoints=[endpoint], codex_binary=executable)
    launcher = f"""
from pathlib import Path
import os, re, sys

args = sys.argv[1:]
executable = {str(executable)!r}
if args[:2] == ["app-server", "--listen"]:
    requested = Path(args[2].removeprefix("unix://"))
    if (not args[2].startswith("unix://") or requested.name != "b"
        or requested.parent.parent != Path({str(state / "rust-v1")!r})
        or not re.fullmatch("[0-9a-f]{{24}}", requested.parent.name)
        or requested.parent.resolve(strict=True) != requested.parent):
        raise ValueError("Codex requested a socket outside the fixture worker state")
    requested.symlink_to({str(endpoint)!r})
    args[2] = {f"unix://{endpoint}"!r}
os.execv(executable, [executable, *args])
"""
    return guard, [sys.executable, "-c", launcher]


def stop_fixture_sessions(roster, stop):
    failures = {}
    requested = set()
    deadline = time.monotonic() + 15
    while True:
        survivors = [row["id"] for row in roster() if row["status"]["Process"] != "exited"]
        if not survivors:
            return
        if time.monotonic() >= deadline:
            raise RuntimeError(f"Owned fixture sessions survived: {survivors}; stop errors: {failures}")
        for session in survivors:
            if session in requested:
                continue
            try:
                stop(session)
                requested.add(session)
            except (OSError, subprocess.SubprocessError) as error:
                failures[session] = (getattr(error, "stderr", None) or str(error))[:512]
        time.sleep(0.1)


def verify(swarm, provider, test, root, server):
    server.release = root / ".fixture-release-turn"
    server.claude_worker_pending = threading.Event()
    server.revoke = root / ".fixture-hide-forward"
    server.forwarded_checked = root / ".fixture-forwarded-checked"
    check_guards(root, [server.server_port])
    home, project, tools, state = [root / name for name in ("h", "work", "bin", "s")]
    place = state / "discord"
    for directory in (home, project, tools, state, home / ".claude", place):
        directory.mkdir(mode=0o700)
    staged_swarm = tools / "swarm"
    shutil.copyfile(swarm, staged_swarm)
    staged_swarm.chmod(0o700)
    swarm = staged_swarm
    native = tools / "native-claude"
    shutil.copyfile(provider, native)
    native.chmod(0o700)
    wrapper = tools / "claude"
    provider_exec = root / ".fixture-provider-executed"
    wrapper.write_text("#!/bin/sh\n: >" + shlex.quote(str(provider_exec)) + "\n" + f"export ANTHROPIC_BASE_URL=http://127.0.0.1:{server.server_port}\n"
                       + "export CLAUDE_SECURESTORAGE_CONFIG_DIR=" + shlex.quote(str(home / ".claude")) + "\n"
                       + "export CLAUDE_CODE_TMPDIR=" + shlex.quote(str(root)) + " TMPDIR=" + shlex.quote(str(root)) + "\n"
                       + "export DISABLE_AUTOUPDATER=1 CLAUDE_CODE_MAX_RETRIES=0 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1\n"
                       + "exec " + shlex.quote(str(native)) + ' "$@"\n')
    wrapper.chmod(0o700)
    endpoint = root / "d"
    env = dict(PATH=f"{tools}:/usr/bin:/bin", HOME=str(home), TMPDIR=str(root),
               TERM="xterm-256color", LANG="en_US.UTF-8", DISABLE_AUTOUPDATER="1",
               CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC="1", CLAUDE_CODE_MAX_RETRIES="0",
               SWARM_DAEMON_STATE=str(state), SWARM_DAEMON_SOCK=str(endpoint),
               SWARM_DAEMON_LOCK=str(root / "lock"), SWARM_CLAUDE_MANAGER_FIXTURE_ROOT=str(root),
               SWARM_CLAUDE_MANAGER_FIXTURE_BINARY=str(swarm))
    if server.park:
        env["SWARM_CLAUDE_MANAGER_FIXTURE_PARK"] = "1"
    if server.forwarded:
        env["SWARM_CLAUDE_MANAGER_FIXTURE_FORWARDED"] = server.forwarded
    version = subprocess.check_output(guarded([str(wrapper), "--version"], root, [server.server_port]),
                                      env=env, text=True).split()[0]
    provider_exec.unlink()
    if server.project_fault:
        env["SWARM_CLAUDE_MANAGER_FIXTURE_PROJECT_FAULT"] = server.project_fault
    if server.project_fault == "before":
        (project/".claude").mkdir(mode=0o700)
        save(project/".claude/settings.local.json",dict(env=dict(ANTHROPIC_BASE_URL="http://127.0.0.1:3")))
    save(home / ".claude.json", dict(hasCompletedOnboarding=True, lastOnboardingVersion=version,
         theme="dark", oauthAccount=dict(accountUuid="synthetic-account", organizationUuid="synthetic-org",
                                        emailAddress="synthetic@example.invalid"),
         projects={str(project): dict(hasTrustDialogAccepted=True)}))
    save(home / ".claude/.credentials.json", dict(claudeAiOauth=dict(accessToken="synthetic-token",
         refreshToken="synthetic-refresh", expiresAt=int(time.time() * 1000) + 3600000,
         scopes=["user:inference", "user:profile"], subscriptionType="max", rateLimitTier="default_claude_max_5x")))
    access = {}
    codex_version = None
    mac_guard = None
    if server.vm:
        child = root / "child"
        child.mkdir(mode=0o700)
        document = child / "delegation.md"
        document.write_text("Wait for the synthetic worker fixture.")
        if server.worktree:
            git = lambda *args: subprocess.run(
                guarded(["/usr/bin/git", "-C", str(child), *args], root, [server.server_port]),
                env=env, check=True, capture_output=True)
            git("init", "-b", "main")
            git("add", "delegation.md")
            git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                "commit", "-m", "Prepare synthetic checkout")
            env["SWARM_CLAUDE_MANAGER_FIXTURE_WORKTREE"] = "1"
        command = shlex.join(["swarm", "spawn", "--cli", "claude",
                              "--worktree=" + ("true" if server.worktree else "false"),
                              "--dir", str(child), "--delegate", str(document)])
        server.spawn_command = command
        save(home / ".claude/settings.json", dict(permissions=dict(allow=["Bash(" + command + ")"])))
        access["project_access"] = "vm"
        env["SWARM_CLAUDE_MANAGER_FIXTURE_VM"] = "1"
        if server.vm_revoked:
            env["SWARM_CLAUDE_MANAGER_FIXTURE_VM_REVOKED"] = "1"
    if server.enrolled:
        server.approved = root / "approved"
        server.unapproved = root / "unapproved"
        server.approved.mkdir(mode=0o700)
        server.unapproved.mkdir(mode=0o700)
        profile_path = home / ".claude.json"
        profile = json.loads(profile_path.read_text())
        for directory in (server.approved, server.unapproved):
            profile["projects"][str(directory)] = dict(hasTrustDialogAccepted=True)
        save(profile_path, profile)
        if server.codex:
            env["SWARM_CLAUDE_MANAGER_FIXTURE_CODEX"] = "1"
            local_codex = tools / "native-codex"
            shutil.copyfile(server.codex, local_codex)
            local_codex.chmod(0o700)
            command = [str(local_codex)]
            if sys.platform == "darwin":
                mac_guard, command = macos_worker_command(root, state, local_codex, server.server_port)
            codex_wrapper = tools / "codex"
            codex_wrapper.write_text("#!/bin/sh\nexec " + shlex.join(command)
               + ' "$@" 2>>' + shlex.quote(str(root / "codex.log")) + "\n")
            codex_wrapper.chmod(0o700)
            native_home = home / ".codex"
            native_home.mkdir(mode=0o700)
            (native_home / "config.toml").write_text(
                'model="fixture"\nmodel_provider="fixture"\napproval_policy="never"\nsandbox_mode="read-only"\n'
                'cli_auth_credentials_store="file"\n[model_providers.fixture]\nname="fixture"\nwire_api="responses"\n'
                f'base_url="http://127.0.0.1:{server.server_port}/v1"\nrequires_openai_auth=false\nsupports_websockets=false\n'
                f'[projects."{server.approved}"]\ntrust_level="trusted"\n'
                '[tui]\nscreen_reader_detection_done=true\n[tui.model_availability_nux]\n"gpt-6.1-sol"=100\n')
            (native_home / "config.toml").chmod(0o600)
            save(native_home / "auth.json", dict(auth_mode="apikey", OPENAI_API_KEY="fixture-only"))
            codex_version = subprocess.check_output(
                guarded([str(codex_wrapper), "--version"], root, [server.server_port]),
                env=env, text=True, timeout=8).strip()
        access.update(project_access="enrolled", projects=dict(approved=str(server.approved)))
        env["SWARM_CLAUDE_MANAGER_FIXTURE_ENROLLED"] = "1"
        if server.missing_reply:
            env["SWARM_CLAUDE_MANAGER_FIXTURE_MISSING_REPLY"] = "1"
        if server.codex_send:
            env["SWARM_CLAUDE_MANAGER_FIXTURE_CODEX_SEND"] = "1"
        if server.codex_send_revoked and not server.replaced_issuer:
            env["SWARM_CLAUDE_MANAGER_FIXTURE_SEND_REVOKED"] = "1"
        if server.replaced_issuer:
            env["SWARM_CLAUDE_MANAGER_FIXTURE_REPLACED_ISSUER"] = str(root)
        if server.deleted:
            env["SWARM_CLAUDE_MANAGER_FIXTURE_DELETED"] = "1"
    marker = root / "token"
    marker.write_text("unused synthetic Discord token")
    marker.chmod(0o600)
    save(place / ".rust-offline-owner.json", "claude-fixture")
    save(place / "config.json", dict(version=1, enabled=True, mode="personal", installation_id="claude-fixture",
         name="Claude Fixture", application_id="200", owner_id="300", dm_channel_id="400",
         token_file=str(marker), daemon_socket=str(endpoint), work_dir=str(project),
         manager_cli="claude", **access, supervisor=dict(state_dir=str(state), lock=str(root/"lock"), log=str(root/"daemon.log"))))
    def run(argv):
        args = [str(x) for x in argv]
        if mac_guard:
            return ["/usr/bin/sandbox-exec", "-f", str(mac_guard.seatbelt), *args]
        return guarded(args, root, [server.server_port])

    with (root / "daemon.log").open("w+") as log, ExitStack() as cleanup:
        if mac_guard:
            cleanup.callback(mac_guard.close)
        daemon = subprocess.Popen(run([swarm, "daemon"]), cwd=project, env=env, stdout=log, stderr=log)
        try:
            deadline = time.monotonic() + 5
            while not endpoint.exists():
                assert daemon.poll() is None and time.monotonic() < deadline, "isolated owner socket absent"
                time.sleep(0.05)
            if server.vm:
                status = lambda: json.loads(subprocess.check_output(
                    run([swarm,"discord","status","--state-dir",str(place)]), cwd=project,env=env,text=True,timeout=8))
                assert status()["project_access"] == "enrolled", "unproved preview VM string was adopted"
                configured = subprocess.run(run([swarm,"discord","connect","--state-dir",str(place),
                    "--project-access","vm"]), cwd=project,env=env,capture_output=True,text=True,timeout=8)
                assert configured.returncode == 0, configured.stderr
                assert status()["project_access"] == "vm", "explicit verified CLI grant was not loaded"
            if server.enrolled:
                ordinary = subprocess.run(run([swarm,"spawn","--cli","claude","--worktree=false",
                                               "--dir",str(server.unapproved),"--prompt","Unrelated synthetic worker."]),
                                          cwd=project,env=env,capture_output=True,text=True,timeout=15)
                assert ordinary.returncode == 0, ordinary.stderr
            subprocess.run(run([test, "discord::manager::tests::installed_claude_manager_producer",
                                "--ignored", "--nocapture"]), cwd=project, env=env, check=True,
                           timeout=145 if server.codex else 75 if server.park else 48)
            if server.project_fault or server.vm_revoked:
                assert not server.seen_tools
                assert provider_exec.is_file() == (server.project_fault == "after" or server.vm_revoked)
            expected_posts = 0 if server.forwarded == "revoked" or server.deleted or server.codex_send_revoked or server.project_fault or server.vm_revoked else (2 if server.park else 1)
            assert sum(1 for publish, _, _ in server.main_requests if publish) == expected_posts
            if server.codex:
                assert server.codex_requests
                opening = ("Use this approved project for the delegated task. Report verified results only.\n\n"
                           "Wait for the synthetic structured worker fixture.")
                authored = []
                for path in (home / ".codex" / "sessions").rglob("*.jsonl"):
                    for raw in path.read_text().splitlines():
                        row = json.loads(raw)
                        item = row.get("payload", {}).get("item", {})
                        if row["type"] == "event_msg" and item.get("type") == "UserMessage":
                            authored.extend(part.get("text") for part in item["content"])
                assert authored.count(opening) == 1, "durable native opening was duplicated"
                if server.missing_reply:
                    assert authored == [opening], "the monitor submitted another native worker input"
                    proofs = [json.loads(path.read_text())["evidence"]
                              for path in state.glob("rust-v1/*/manager-proof.json")]
                    assert len(proofs) == 1 and len(proofs[0]["prepared"]) == len(proofs[0]["turns"]) == 1, \
                        "the monitor submitted another native manager input"
                if server.codex_send:
                    assert authored.count("Start the exact enrolled worker follow-up.") == (0 if server.codex_send_revoked else 1)
                    assert "This divergent action must never reach the worker." not in authored
            assert not (root / ".fixture-impostor-contacted").exists()
            assert not server.forwarded or server.forwarded_checked.is_file()
            assert all(("Bash" in tools) == server.vm for _, _, tools in server.main_requests)
            print(json.dumps(dict(version=version, private_loopback=True, original_envelope=bool(server.seen_tools),
                                  exact_main_mcp_tool=not server.project_fault and not server.vm_revoked, observed_reply=expected_posts > 0, disabled_shell_tools=not server.vm,
                                  vm_native_worker_launch=server.vm and not server.vm_revoked, vm_grant_revoked=server.vm_revoked, vm_worktree_files=server.worktree,
                                  enrolled_typed_launch=server.enrolled,
                                  missing_worker_reply=server.missing_reply,
                                  codex_version=codex_version,
                                  project_authority_fault=server.project_fault,
                                  parked_worker_return=server.park, forwarded_attachments=server.forwarded)))
        except Exception:
            if server.codex and (root / "codex.log").is_file():
                print((root / "codex.log").read_text())
            log.flush()
            log.seek(0)
            print(log.read())
            print("Provider tools at admitted native turns:", server.seen_tools)
            if server.vm:
                print("Synthetic VM launch result:", getattr(server,"vm_failure",None))
            listing = subprocess.run(run([swarm, "ls", "--json"]), cwd=project, env=env,
                                     capture_output=True, text=True, timeout=4).stdout
            print(listing)
            for row in json.loads(listing):
                outcome = subprocess.run(run([swarm, "peek", row["id"]]), cwd=project, env=env,
                                         capture_output=True, text=True, timeout=4)
                print(outcome.stdout, outcome.stderr)
            for evidence in state.glob("rust-v1/*/manager-proof.json"):
                print(evidence.read_text())
            raise
        finally:
            observed = {}
            def roster():
                response = subprocess.run(run([swarm, "ls", "--json"]), cwd=project, env=env,
                                          capture_output=True, text=True, timeout=4)
                response.check_returncode()
                return json.loads(response.stdout)
            try:
                fixtures.owned(root, None, observed)
                if daemon.poll() is None:
                    def stop(session_id):
                        subprocess.run(run([swarm, "kill", session_id]), cwd=project,
                                       env=env, capture_output=True, text=True, timeout=12, check=True)

                    stop_fixture_sessions(roster, stop)
            finally:
                try:
                    if daemon.poll() is None:
                        daemon.terminate()
                        daemon.wait(timeout=5)
                finally:
                    cleanup = fixtures.reap(root, None, observed)
                    assert not cleanup["remaining"], cleanup



if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for arg in ("swarm", "claude", "test"):
        parser.add_argument(arg, type=Path)
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--park", action="store_true")
    modes.add_argument("--vm", action="store_true")
    modes.add_argument("--vm-worktree", action="store_true")
    modes.add_argument("--vm-revoked", action="store_true")
    modes.add_argument("--enrolled", action="store_true")
    modes.add_argument("--enrolled-codex", type=Path)
    modes.add_argument("--enrolled-codex-missing-reply", type=Path)
    modes.add_argument("--enrolled-codex-send", type=Path)
    modes.add_argument("--enrolled-codex-send-revoked", type=Path)
    modes.add_argument("--enrolled-deleted", action="store_true")
    modes.add_argument("--project-fault", choices=("before","after"))
    modes.add_argument("--forwarded", choices=("allowed", "revoked"))
    modes.add_argument("--enrolled-codex-replaced-issuer", type=Path)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="cm-", dir="/tmp") as name, \
            http.server.ThreadingHTTPServer(("127.0.0.1", 0), Model) as server:
        server.main_requests = []
        server.seen_tools = []
        server.park = args.park
        server.vm = args.vm or args.vm_worktree or args.vm_revoked
        server.vm_revoked = args.vm_revoked
        server.worktree = args.vm_worktree
        server.replaced_issuer = args.enrolled_codex_replaced_issuer is not None
        server.missing_reply = args.enrolled_codex_missing_reply is not None
        server.worker_release = threading.Event()
        server.codex = args.enrolled_codex or args.enrolled_codex_missing_reply or args.enrolled_codex_send or args.enrolled_codex_send_revoked or args.enrolled_codex_replaced_issuer
        server.codex_send_revoked = args.enrolled_codex_send_revoked is not None or server.replaced_issuer
        server.codex_send = bool(args.enrolled_codex_send or server.codex_send_revoked)
        server.enrolled = bool(args.enrolled or args.enrolled_deleted or server.codex)
        server.codex_requests = []
        server.deleted = args.enrolled_deleted
        server.project_fault = args.project_fault
        server.forwarded = args.forwarded
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            verify(args.swarm.resolve(), args.claude.resolve(), args.test.resolve(), Path(name).resolve(), server)
        finally:
            server.worker_release.set()
            server.shutdown()
