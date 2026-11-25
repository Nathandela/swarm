#!/usr/bin/env python3
"""Rust supervisor probe for the OS-isolated hermes_native_admission.py harness."""
import argparse
import ctypes
import errno
import hashlib
import json
import os
from pathlib import Path
import socket
import struct
import subprocess
import sys
import time
from urllib.parse import urlsplit
from urllib.request import urlopen
from uuid import uuid4


def digest(value):
    payload = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
    return hashlib.sha256(payload.encode()).hexdigest()


def wait(label, probe, seconds=35):
    print("waiting:", label, flush=True)
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if result := probe():
            return result
        time.sleep(0.15)
    raise AssertionError("timed out: " + label)


class Peer:
    def __init__(self, path):
        self.socket = socket.socket(socket.AF_UNIX)
        self.socket.settimeout(90)
        self.socket.connect(str(path))
        self.stream = self.socket.makefile("rb")
        self.endpoint = ""
        hello = self.query("hello", protocol_version=1, capabilities=[
            "semantic-send", "semantic-target", "worker-discord-origin-v1",
        ])
        assert hello["op"] == "hello", hello
        assert "worker-discord-origin-v1" in hello["capabilities"], hello
        self.endpoint = hello["endpoint_id"]

    def close(self):
        self.stream.close()
        self.socket.close()

    def query(self, op, **request):
        payload = json.dumps(dict(op=op, endpoint_id=self.endpoint, **request)).encode()
        self.socket.sendall(struct.pack(">IB", len(payload) + 1, 1) + payload)
        header = self.stream.read(5)
        assert len(header) == 5, (op, header)
        size, tag = struct.unpack(">IB", header)
        assert tag == 1 and 2 <= size <= 1 << 20, (size, tag)
        return json.loads(self.stream.read(size - 1))

    def view(self, worker):
        return next(row for row in self.query("list")["sessions"] if row["id"] == worker)


def pid_active(pid_file):
    try:
        pid = int(pid_file.read_text().strip())
    except (FileNotFoundError, ValueError):
        return False
    if sys.platform == "darwin":
        # PROC_PIDTBSDINFO exposes pbi_status after the flags word.
        libproc = ctypes.CDLL("/usr/lib/libproc.dylib", use_errno=True)
        info = (ctypes.c_uint32 * 128)()
        size = libproc.proc_pidinfo(pid, 3, ctypes.c_uint64(0), info, ctypes.sizeof(info))
        return size >= 8 and info[1] != 5
    try:
        stat = Path(f"/proc/{pid}/stat").read_text()
    except FileNotFoundError:
        return False
    return stat.rsplit(") ", 1)[1][0] != "Z"


def run(root, swarm, stats):
    assert urlsplit(stats).hostname == "127.0.0.1", "fixture stats must be loopback"
    outside = root.parent / ("hermes-denied-" + uuid4().hex)
    try:
        with outside.open("x"):
            pass
    except OSError as error:
        assert error.errno in (errno.EACCES, errno.EPERM, errno.EROFS), error
    else:
        outside.unlink()
        raise AssertionError("fixture is missing its OS write guard")

    os.umask(0o077)
    state = root / "sw"
    state.mkdir()
    sock = state / "d"
    discord = state / "discord"
    discord.mkdir()
    install, app, actor, channel, source = "hermes-rust-fixture", "123", "124", "125", "126"
    (discord / "config.json").write_text(json.dumps({
        "version": 1, "enabled": True, "installation_id": install,
        "name": "Hermes fixture", "mode": "personal", "manager_cli": "codex",
        "application_id": app, "owner_id": actor, "dm_channel_id": channel,
        "token_file": str(root / "unused"), "daemon_socket": str(sock), "work_dir": str(root),
    }))
    (discord / ".rust-offline-owner.json").write_text(json.dumps(install))
    env = dict(os.environ, SWARM_DAEMON_STATE=str(state), SWARM_DAEMON_SOCK=str(sock))
    daemon, peer, workers = None, None, []

    def start():
        nonlocal daemon, peer
        with (state / "daemon.log").open("ab") as log:
            daemon = subprocess.Popen([str(swarm), "daemon"], env=env, cwd=root,
                                      stdout=log, stderr=log, stdin=subprocess.DEVNULL)
        def connect():
            assert daemon.poll() is None, (daemon.returncode, str(state / "daemon.log"))
            try:
                return Peer(sock)
            except (FileNotFoundError, ConnectionRefusedError):
                return None
        peer = wait("Rust supervisor", connect)

    def stop():
        nonlocal daemon, peer
        if peer:
            peer.close()
            peer = None
        if daemon and daemon.poll() is None:
            daemon.terminate()
            try:
                daemon.wait(timeout=5)
            except subprocess.TimeoutExpired:
                daemon.kill()
                daemon.wait(timeout=5)
        daemon = None

    def launch(name, cwd, resumed_from=None):
        options = {"toolsets": "terminal,delegation"}
        if resumed_from:
            options["resume_from"] = resumed_from
        args = {"agent": "hermes", "name": name, "cwd": str(cwd), "options": options,
                "env": [f"{key}={value}" for key, value in env.items()], "cols": 160, "rows": 45}
        response = peer.query("launch", launch=args)
        assert response["op"] == "launch", response
        row = response["session"]
        assert row["provider_mode"] == "shared-native", row
        worker = row["id"]
        workers.append(worker)
        path = state / "rust-v1" / worker.split("/", 1)[1] / "hermes-input.json"
        descriptor = json.loads(path.read_bytes())
        assert descriptor["binding"]["directory"] == str(cwd), descriptor
        assert row["conversation_id"] == descriptor["binding"]["session_id"], row
        return worker, descriptor

    def send(worker, text, attributed=False):
        target = peer.query("semantic_target", session_id=worker)
        assert target["op"] == "semantic_target", target
        operation = f"semantic:{time.time_ns() // 1000000}:{uuid4().hex}"
        composer = {"session": worker, "session_instance": target["semantic_target_instance"],
                    "text": text}
        if attributed:
            revision = digest([actor, text, channel, "", source, None, "", None])
            request = "da_" + digest([install, source])[:32]
            composer["origin"] = {"kind": "owner_direct", "installation_id": install,
                                  "request_id": request, "actor_id": actor, "channel_id": channel,
                                  "message_id": source, "revision": revision}
            (discord / "rust-text-v1.json").write_text(json.dumps({
                "version": 2, "identity": [install, app, actor, channel, str(sock)],
                "consent_floor": "120", "history_cursor": source,
                "messages": {source: {"text": text, "state": "sending", "worker_operation": operation,
                                     "route": {"guild_id": "", "channel_id": channel, "message_id": source},
                                     "author": {"id": actor}, "authority": "owner_direct",
                                     "supervisor_socket": str(sock), "revision": revision, "operation_id": request}},
                "worker_inputs": {operation: {"source_id": source, "provider": "hermes",
                                              "composer": composer}},
            }))
            wrong = {**composer, "origin": {**composer["origin"], "installation_id": "other"}}
            assert peer.query("semantic_send", session_id=worker, operation_id=operation,
                              body_version=1, composer_send=wrong)["op"] == "error"
        for _ in range(2):
            sent = peer.query("semantic_send", session_id=worker, operation_id=operation,
                              body_version=1, composer_send=composer)
            assert sent["op"] == "ok", sent
        status = peer.query("semantic_status", session_id=worker, operation_id=operation)
        receipt = status["semantic_receipt"]
        assert receipt["state"] == "submitted" and receipt["operation_id"] == operation, status
        assert receipt["session"] == worker and receipt["native_admission"]["input_id"] == operation, receipt
        assert receipt.get("origin") == composer.get("origin"), receipt
        return receipt["native_admission"]["status"]

    try:
        start()
        cwd_a, cwd_b = root / "workspace-a", root / "workspace-b"
        cwd_a.mkdir()
        cwd_b.mkdir()
        a, first = launch("A", cwd_a)
        b, second = launch("B", cwd_b)
        ab, bb = first["binding"], second["binding"]
        assert all(ab[key] == bb[key] for key in ("owner_pid", "owner_birth", "instance_id", "authority_epoch"))
        assert ab["session_id"] != bb["session_id"]
        first_receipt = send(a, "SWARM_BACKGROUND_A", attributed=True)
        second_receipt = send(b, "SWARM_BACKGROUND_B")
        wait("both native background jobs", lambda: pid_active(root / "a.pid") and pid_active(root / "b.pid"))
        stop()
        start()
        assert peer.view(a)["conversation_id"] == ab["session_id"]
        assert peer.view(b)["conversation_id"] == bb["session_id"]
        assert peer.query("delete", session_id=a)["op"] == "error"
        assert peer.query("kill", session_id=a)["error_code"] == "outcome_unknown"
        wait("A loses viewer and retains workspace", lambda: peer.view(a)["status"].get("NativeWorkUnknown"))
        wait("A background stop", lambda: not pid_active(root / "a.pid"))
        assert pid_active(root / "b.pid"), "A stop affected sibling B"
        assert cwd_a.exists() and peer.query("delete", session_id=a)["op"] == "error"
        resumed, successor = launch("A-resumed", cwd_a, a)
        assert successor["binding"] == ab
        denied = peer.query("kill", session_id=a)
        assert "successor" in denied["error"], denied
        send(resumed, "SWARM_PERMISSION_A")
        wait("Hermes observes its native approval",
             lambda: peer.view(resumed)["status"]["Interaction"] == "permission")
        assert (root / "protected").exists(), "native approval authority was bypassed"
        with urlopen(stats, timeout=5) as response:
            main_calls = json.load(response)["main_calls"]
        report = {"supervisor": "rust", "two_sessions_one_owner": True,
                  "connector_cutover": True, "wrong_installation_refused": True,
                  "native_admission_statuses": [first_receipt, second_receipt],
                  "daemon_restart_retained": True, "sibling_job_survived": True,
                  "delete_refused": True, "exact_resume": True,
                  "native_approval_preserved": True, "loopback_model_calls": main_calls}
        (root / "result.json").write_text(json.dumps(report, indent=2) + "\n")
    finally:
        if peer:
            for worker in reversed(workers):
                try:
                    peer.query("kill", session_id=worker)
                except (OSError, AssertionError, ValueError):
                    break
        stop()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--swarm", type=Path, required=True)
    parser.add_argument("--stats", required=True)
    options = parser.parse_args()
    run(options.root.resolve(), options.swarm.resolve(), options.stats)
