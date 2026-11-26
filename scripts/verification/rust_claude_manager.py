#!/usr/bin/env python3
"""Run the Rust dispatcher against installed Claude and a synthetic loopback model."""
import argparse
import base64
import hashlib
import http.server
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import tempfile
import threading
import time

from native_fixture_sandbox import guarded, check_guards
from rust_claude_worker_input import save


class Model(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        if "/messages" not in self.path:
            self.send_error(404)
            return
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        tools = [tool.get("name") for tool in request.get("tools", [])]
        content = json.dumps(request.get("messages", []))
        main = "[SWARM_MANAGER_INPUT_V1:" in content and "mcp__swarm_voice__publish" in tools
        wake = main and "Fixture worker retained across parking." in content
        forwarded = main and self.server.forwarded and "Visible forwarded fixture quote" in content
        data = b"a,b\n1,2\n"
        read_attachment = forwarded and "toolu_fixture_forwarded_read" not in content
        read_note = wake and "toolu_fixture_swarm_read_note" not in content
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
        publish = main and not read_note and not read_attachment and may_publish and call_id not in content
        tool = publish or read_note or read_attachment
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
            if read_attachment:
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
                            sequence=1, final=True, text=text)
            block = dict(type="tool_use", name=f"mcp__swarm_voice__{name}", id=call, input={})
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


def verify(swarm, provider, test, root, server):
    server.release = root / ".fixture-release-turn"
    server.revoke = root / ".fixture-hide-forward"
    server.forwarded_checked = root / ".fixture-forwarded-checked"
    check_guards(root, [server.server_port])
    home, project, tools, state = [root / name for name in ("h", "work", "bin", "s")]
    place = state / "discord"
    for directory in (home, project, tools, state, home / ".claude", place):
        directory.mkdir(mode=0o700)
    native = tools / "native-claude"
    shutil.copyfile(provider, native)
    native.chmod(0o700)
    wrapper = tools / "claude"
    wrapper.write_text("#!/bin/sh\n" + f"export ANTHROPIC_BASE_URL=http://127.0.0.1:{server.server_port}\n"
                       + "export CLAUDE_SECURESTORAGE_CONFIG_DIR=" + shlex.quote(str(home / ".claude")) + "\n"
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
    save(home / ".claude.json", dict(hasCompletedOnboarding=True, lastOnboardingVersion=version,
         theme="dark", oauthAccount=dict(accountUuid="synthetic-account", organizationUuid="synthetic-org",
                                        emailAddress="synthetic@example.invalid"),
         projects={str(project): dict(hasTrustDialogAccepted=True)}))
    save(home / ".claude/.credentials.json", dict(claudeAiOauth=dict(accessToken="synthetic-token",
         refreshToken="synthetic-refresh", expiresAt=int(time.time() * 1000) + 3600000,
         scopes=["user:inference", "user:profile"], subscriptionType="max", rateLimitTier="default_claude_max_5x")))
    marker = root / "token"
    marker.write_text("unused synthetic Discord token")
    marker.chmod(0o600)
    save(place / ".rust-offline-owner.json", "claude-fixture")
    save(place / "config.json", dict(version=1, enabled=True, mode="personal", installation_id="claude-fixture",
         name="Claude Fixture", application_id="200", owner_id="300", dm_channel_id="400",
         token_file=str(marker), daemon_socket=str(endpoint), work_dir=str(project),
         manager_cli="claude", supervisor=dict(state_dir=str(state), lock=str(root/"lock"), log=str(root/"daemon.log"))))
    run = lambda argv: guarded([str(x) for x in argv], root, [server.server_port])
    with (root / "daemon.log").open("w+") as log:
        daemon = subprocess.Popen(run([swarm, "daemon"]), cwd=project, env=env, stdout=log, stderr=log)
        try:
            deadline = time.monotonic() + 5
            while not endpoint.exists():
                assert daemon.poll() is None and time.monotonic() < deadline, "isolated owner socket absent"
                time.sleep(0.05)
            subprocess.run(run([test, "discord::manager::tests::installed_claude_manager_producer",
                                "--ignored", "--nocapture"]), cwd=project, env=env, check=True, timeout=75 if server.park else 48)
            expected_posts = 0 if server.forwarded == "revoked" else (2 if server.park else 1)
            assert sum(1 for publish, _, _ in server.main_requests if publish) == expected_posts
            assert not server.forwarded or server.forwarded_checked.is_file()
            assert all("Bash" not in tools for _, _, tools in server.main_requests)
            print(json.dumps(dict(version=version, private_loopback=True, original_envelope=True,
                                  exact_main_mcp_tool=True, observed_reply=expected_posts > 0, disabled_shell_tools=True,
                                  parked_worker_return=server.park, forwarded_attachments=server.forwarded)))
        except Exception:
            log.flush()
            log.seek(0)
            print(log.read())
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
            def roster():
                response = subprocess.run(run([swarm, "ls", "--json"]), cwd=project, env=env,
                                          capture_output=True, text=True, timeout=4)
                response.check_returncode()
                return json.loads(response.stdout)
            try:
                if daemon.poll() is None:
                    for row in roster():
                        subprocess.run(run([swarm, "kill", row["id"]]), cwd=project,
                                       env=env, capture_output=True, timeout=5, check=True)
                    deadline = time.monotonic() + 8
                    while any(row["status"]["Process"] != "exited" for row in roster()):
                        assert time.monotonic() < deadline, "owned Claude shim exit was not observed"
                        time.sleep(0.05)
            finally:
                if daemon.poll() is None:
                    daemon.terminate()
                    daemon.wait(timeout=5)



if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for arg in ("swarm", "claude", "test"):
        parser.add_argument(arg, type=Path)
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--park", action="store_true")
    modes.add_argument("--forwarded", choices=("allowed", "revoked"))
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="clmgr-", dir="/tmp") as name, \
            http.server.ThreadingHTTPServer(("127.0.0.1", 0), Model) as server:
        server.main_requests = []
        server.park = args.park
        server.forwarded = args.forwarded
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            verify(args.swarm.resolve(), args.claude.resolve(), args.test.resolve(), Path(name).resolve(), server)
        finally:
            server.shutdown()
