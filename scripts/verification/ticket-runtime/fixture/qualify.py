"""Synthetic persisted state tests role binding, real native commands and Swarm MCP separately from OwnerLaunch."""

import hashlib
import http.server
import json
import os
import shutil
import socketserver
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path

from provider import Provider

HERE = Path(__file__).resolve().parent


def atomic(path, data):
    path.write_text(json.dumps(data))
    path.chmod(0o600)


RUNTIME = HERE.parent


class Controller(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        n = int(self.headers["Content-Length"])
        body = json.loads(self.rfile.read(n))
        if self.path == "/manager/call":
            assert self.headers["Authorization"] == "Bearer " + "a" * 64
            assert (
                body["conversation_id"] == self.server.conversation
                and body["call"]["name"] == "swarm_list"
            )
            self.server.receipts.append(
                ["manager", body["session_id"], body["call"]["name"]]
            )
            reply = {"data": {"fixture": "private-manager-result"}}
        elif self.path == "/worker/message":
            assert (
                self.headers["Authorization"] == "Bearer " + "b" * 64
                and body["text"] == "Synthetic result"
                and body["kind"] == "complete"
            )
            self.server.receipts.append(
                ["worker", self.headers["Swarm-Worker-Session"], body["kind"]]
            )
            reply = {"state": "queued", "id": body["id"]}
        else:
            self.send_error(404)
            return
        data = json.dumps(reply).encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


MANAGER_PROBE = """import json,socket
s=socket.socket(socket.AF_UNIX);s.connect('/caps/role.sock');f=s.makefile('rwb')
def send(v):
 f.write(json.dumps(dict(jsonrpc='2.0',**v)).encode()+b'\\n');f.flush()
def read():return json.loads(f.readline())
send(dict(id=1,method='initialize',params=dict(protocolVersion='2025-03-26',capabilities={},clientInfo=dict(name='test',version='1'))))
assert 'result' in read()
send(dict(method='notifications/initialized'))
send(dict(id=2,method='tools/call',params=dict(name='swarm_list',arguments={})))
res=read()
assert 'private-manager-result' in str(res),res
print('NATIVE_MCP_BRIDGE_PASS')
"""


def run(native, swarm, owner_rpc, native_rpc):
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix="st-") as td:
        root = Path(td)
        sessions = root / "s"
        install = root / "bin"
        space = root / "w"
        work = space / "assigned"
        for p in (
            sessions,
            install,
            space,
            work,
            root / "home",
            root / "run",
            root / "controller",
        ):
            p.mkdir()
        for n in ("codex", "enter.py", "bridge.py", "binding.py"):
            shutil.copy2(RUNTIME / n, install / n)
        atomic(
            install / "launcher.json",
            dict(
                native=str(native),
                native_sha256=hashlib.sha256(native.read_bytes()).hexdigest(),
                generation="fixture-1",
                sessions=str(sessions),
                swarm=str(swarm),
                workspaces=str(space),
                model="gpt-5",
            ),
        )
        socket = root / "controller/bridge.sock"
        auth = root / "controller/bridge.auth"
        auth.write_text("a" * 64)
        ctl = socketserver.ThreadingUnixStreamServer(str(socket), Controller)
        os.chmod(socket, 0o600)
        ctl.receipts = []
        ctl.conversation = "discord-teammate-" + "f" * 64
        resources = [ctl]
        providerpaths = {}
        for role, cmd in [
            ("dispatcher", "/usr/bin/python3 manager_check.py"),
            (
                "worker",
                "swarm teammate message --kind complete --text 'Synthetic result'",
            ),
        ]:
            dest = root / (role + ".sock")
            srv = socketserver.ThreadingUnixStreamServer(str(dest), Provider)
            srv.calls = 0
            srv.command = cmd
            resources.append(srv)
            providerpaths[role] = str(dest)
        for svc in resources:
            threading.Thread(target=svc.serve_forever, daemon=True).start()
        (work / "manager_check.py").write_text(MANAGER_PROBE)
        atomic(
            install / "assignment.json",
            dict(
                assignment="fixture-1",
                workspace=str(work),
                supervisor=str(sessions),
                generation="fixture-1",
                expires_at=int(time.time()) + 120,
                conversation=ctl.conversation,
                auth_file=str(auth),
                bridge_socket=str(socket),
                provider_sockets=providerpaths,
            ),
        )
        env = dict(
            HOME=str(root / "home"),
            PATH=str(install) + ":/usr/bin:/bin",
            SWARM_DAEMON_STATE=str(sessions),
            SWARM_DAEMON_SOCK=str(root / "ds"),
            SWARM_DAEMON_LOCK=str(root / "lock"),
            SWARM_DAEMON_LOG=str(root / "log"),
            XDG_RUNTIME_DIR=str(root / "run"),
            TMPDIR=str(root / "run"),
            TERM="xterm-256color",
            LANG="C.UTF-8",
        )
        # A real Swarm manager must refuse fixture-only credentials. Do not substitute an identity.
        daemon = subprocess.Popen(
            [swarm, "daemon"],
            cwd=work,
            env=env,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
        )
        try:
            for _ in range(120):
                if (root / "ds").is_socket():
                    break
                if daemon.poll() is not None:
                    raise RuntimeError(daemon.stderr.read().decode())
                time.sleep(0.025)
            attempt = subprocess.run(
                [
                    owner_rpc,
                    root / "ds",
                    work,
                    "dispatcher",
                    ctl.conversation,
                    socket,
                    auth,
                ],
                timeout=25,
                capture_output=True,
                text=True,
            )
            reason = "manager native account identity is unavailable; sign in to the selected native CLI configuration and reconcile the manager before launching"
            assert (
                attempt.returncode != 0
                and reason in attempt.stderr
                and not list(sessions.glob("*/shim-launch.json"))
            ), attempt.stderr
        finally:
            daemon.terminate()
            daemon.wait(timeout=5)
        passed = []
        try:
            # These are explicit fixture records without a provider login; no supervisor admission is represented.
            manager = "aaaaaaaaaaaaaaaa"
            for i, (role, session_id) in enumerate(
                (("dispatcher", manager), ("worker", "bbbbbbbbbbbbbbbb"))
            ):
                d = sessions / session_id
                d.mkdir()
                public = d / "codex.sock"
                uri = "unix://" + str(public)
                meta = {
                    "id": session_id,
                    "agent_type": "codex",
                    "role": "manager" if i == 0 else "",
                    "manager_native_cli": False,
                    "manager_conversation_id": ctl.conversation if i == 0 else "",
                    "manager_auth_file": str(auth) if i == 0 else "",
                    "manager_bridge_socket": str(socket) if i == 0 else "",
                    "spawn_intent": "delegate" if i else "",
                    "spawned_from": manager if i else "",
                }
                atomic(d / "meta.json", meta)
                atomic(
                    d / "shim-launch.json",
                    dict(
                        session_id=session_id,
                        session_dir=str(d),
                        native_provider="codex",
                        cwd=str(work),
                        argv=[str(install / "codex")],
                        backend_program=str(install / "codex"),
                        backend_required=(i == 0),
                        backend_socket_path=str(public),
                        env=(
                            [
                                "SWARM_TEAMMATE_WORKER_BRIDGE="
                                + str(socket)
                                + ":"
                                + "b" * 64
                            ]
                            if i
                            else []
                        ),
                    ),
                )
                backend = subprocess.Popen(
                    [
                        install / "codex",
                        "app-server",
                        "--listen",
                        uri,
                        "-c",
                        "sandbox_workspace_write.network_access=true",
                    ],
                    cwd=work,
                    env={"PATH": "/usr/bin:/bin"},
                    stderr=subprocess.PIPE,
                )
                try:
                    for _ in range(200):
                        if public.is_socket():
                            break
                        if backend.poll() is not None:
                            raise RuntimeError(backend.stderr.read().decode())
                        time.sleep(0.03)
                    rpc = subprocess.run(
                        [native_rpc, public, work],
                        timeout=35,
                        capture_output=True,
                        text=True,
                    )
                    events = [json.loads(line) for line in rpc.stdout.splitlines()]
                    cmd = [
                        x["item"]
                        for x in events
                        if x.get("item", {}).get("type") == "commandExecution"
                    ]
                    assert (
                        rpc.returncode == 0 and cmd and cmd[-1].get("exitCode") == 0
                    ), (
                        cmd[-1]["aggregatedOutput"] if cmd else rpc.stderr,
                        ctl.receipts,
                    )
                    assert ctl.receipts[-1] == (
                        ["worker", session_id, "complete"]
                        if i
                        else ["manager", session_id, "swarm_list"]
                    ), ctl.receipts
                    passed.append(
                        dict(
                            role=role,
                            id=session_id,
                            native_command_exit=cmd[-1]["exitCode"],
                            real_swarm_bridge=ctl.receipts[-1],
                        )
                    )
                finally:
                    backend.terminate()
                    backend.wait(timeout=6)
            print(
                json.dumps(
                    dict(
                        passed=passed,
                        fixture_records=True,
                        real_owner_launch=False,
                        owner_launch_refusal=reason,
                    ),
                    indent=2,
                )
            )
        finally:
            for srv in resources:
                srv.shutdown()
                srv.server_close()


if __name__ == "__main__":
    run(*(Path(arg).resolve() for arg in sys.argv[1:]))
