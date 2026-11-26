"""Witness original Go's OpenCode TUI and native SDK bridge through owner replacement."""
import hashlib
import http.client
import json
import os
from pathlib import Path
import socket
import struct
import subprocess
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from live_go_shim import attach, connect, snapshot
from stopped_go_state import daemon, disk, listed, owner, refused


class LocalHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("127.0.0.1", timeout=5)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(5)
        self.sock.connect(self.path)


def rehearse(go, rust, root, configured_env, wait):
    state = root / "state"
    env = configured_env | {"SWARM_DAEMON_SOCK": str(state / "daemon.sock"),
                            "SWARM_DAEMON_LOCK": str(state / "daemon.lock"),
                            "SWARM_DAEMON_REMOTE_SOCK": str(state / "remote.sock")}
    rust_env = env | {"SWARM_DAEMON_STOPPED_GO_COMPAT":"1", "SWARM_DAEMON_LIVE_GO_COMPAT":"1"}
    active = None

    def page(route="/snapshot?limit=30"):
        binding = disk(session / "opencode-binding.json")
        bridge = LocalHTTP(binding["socketPath"])
        try:
            bridge.request("GET", route, headers={"Authorization":"Bearer "+binding["token"]})
            response = bridge.getresponse()
            body = json.loads(response.read())
            assert response.status == 200, (response.status, body)
            return body
        finally:
            bridge.close()

    def done(snapshot, marker):
        rows = snapshot.get("messages", snapshot.get("history", []))
        return any(any(marker in part.get("text", "") for part in row["parts"]) for row in rows) and any(
            row["info"]["role"] == "assistant" and row["info"].get("finish") == "stop"
            and any("Deterministic OpenCode fixture response." in part.get("text", "") for part in row["parts"])
            for row in rows)

    def prompt(environment, marker, history):
        with connect(environment) as connection:
            attach(connection, active)
            send = connection[0]
            text = f"{marker}. Reply with the private fixture only.".encode()
            send(3, b"\x1b[200~" + text + b"\x1b[201~")
            wait(lambda: marker in "\n".join(snapshot(environment, active)["lines"]), timeout=8)
            send(3, b"\r")
            wait(lambda: done(history(), marker), timeout=30)

    def rust_history(limit=30, before=""):
        returned = owner(rust_env, {"op":"opencode_history","session_id":active,"limit":limit,"before":before},
                         caps=("opencode-history-v1",), timeout=12)
        assert returned["op"] == "opencode_history", returned
        return returned["history_result"]

    def process_birth(pid):
        if sys.platform == "darwin":
            from macos_codex_fixture import process_birth
            return process_birth(pid)
        return int(Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()[19])

    def custody():
        shim = disk(session / "meta.json")
        assert process_birth(shim["shim_pid"]) == shim["shim_start_time"]
        endpoint = session / "oc.sock"
        with socket.socket(socket.AF_UNIX) as client:
            client.connect(str(endpoint))
            if sys.platform == "darwin":
                from macos_codex_fixture import socket_peer
                pid, uid = socket_peer(client)
            else:
                pid, uid, _ = struct.unpack("3i",client.getsockopt(socket.SOL_SOCKET,socket.SO_PEERCRED,12))
            assert uid == os.geteuid()
        database = root / "data/opencode/opencode.db"
        nodes = [path.stat() for path in (endpoint,database)]
        return ((shim["shim_pid"],shim["shim_start_time"]), (pid,process_birth(pid)),
                tuple((node.st_dev,node.st_ino) for node in nodes))

    operation_id = f"semantic:{int(time.time()*1000)}:01020304050607080102030405060708"
    old_prompt = "GO_DURABLE_OPEN_CODE_OPERATION. Reply with the private fixture only."

    def go_message(*flags):
        done = subprocess.run([go,"message",active,*flags,"--operation-id",operation_id],
                              env=env,capture_output=True,text=True,timeout=40)
        assert done.returncode == 0, (done.stdout,done.stderr)
        return json.loads(done.stdout)

    def preserved():
        return {name:hashlib.sha256((session/name).read_bytes()).digest()
                for name in ("meta.json","shim-launch.json","opencode-binding.json","opencode-tui.json",
                             "opencode-tui.mjs","opencode-input-embargo.json")}

    try:
        with daemon(go, env, root / "go.log"):
            launch = {"agent":"opencode","name":"Original Go OpenCode",
                      "cwd":str(root/"project"),"rows":28,"cols":100,"options":{"model":"fixture/fixture","agent":"build"},
                      "initial_prompt":"GO_ORIGINAL_OPEN_CODE. Reply with the private fixture only.",
                      "env":[f"{key}={value}" for key,value in env.items()]}
            offered = owner(env, {"op":"launch","launch":launch}, timeout=55)
            assert offered["op"] == "launch", offered
            active = offered["session"]["id"]
            session = state / active.split("/")[-1]
            wait(lambda: disk(session / "meta.json")["opencode_binding"]["phase"] == "ready")
            wait(lambda: done(page(),"GO_ORIGINAL_OPEN_CODE"), timeout=30)
            wait(lambda: listed(env)[0]["status"]["Turn"] == "idle"
                 and page("/ready")["draftGuardSupported"], timeout=20)
            admission = go_message("--text",old_prompt)
            wait(lambda: done(page(),"GO_DURABLE_OPEN_CODE_OPERATION"), timeout=30)
            original = page()
            native_custody = custody()
            preserved_files = preserved()
            print("original Go OpenCode owns native ready root", original["rootID"], flush=True)
        metadata = session / "meta.json"
        saved = metadata.read_bytes()
        for field, value, reason in (
            ("phase", "uncertain", "unqualified original Go OpenCode"),
            ("operation_id", "00112233445566778899aabbccddeeff", "initial receipt is unproved"),
        ):
            forged = json.loads(saved)
            forged["opencode_binding"]["initial"][field] = value
            try:
                metadata.write_text(json.dumps(forged))
                refused(rust, rust_env, reason)
                assert custody() == native_custody
                assert page()["messages"] == original["messages"]
            finally:
                metadata.write_bytes(saved)

        with daemon(rust, rust_env, root/"rust.log"):
            assert listed(rust_env)[0]["id"] == active and custody() == native_custody
            refusal = owner(rust_env, {"op":"semantic_send","session_id":active,"operation_id":operation_id,
                                     "composer_send":{"text":old_prompt}}, caps=("semantic-send",))
            assert refusal["error_code"] == "op_not_implemented", refusal
            native_page = rust_history()
            assert native_page["native_session_id"] == original["rootID"]
            assert native_page["native_created_at_ms"] == original["nativeCreatedAtMS"]
            prompt(rust_env, "RUST_CONTINUED_OPEN_CODE", rust_history)
            small = rust_history(1)
            assert len(small["history"]) == 1 and small["next_cursor"]
            older = rust_history(1, small["next_cursor"])
            assert older["history"][0]["info"]["id"] != small["history"][0]["info"]["id"]
            assert preserved() == preserved_files and custody() == native_custody
            print("Rust kept native SDK history and Go-owned PTY", flush=True)
        with daemon(go, env, root/"go-restored.log"):
            receipt = go_message("--status")
            assert receipt["operation_id"] == admission["operation_id"]
            assert receipt["native_session_id"] == admission["native_session_id"]
            retry = go_message("--text",old_prompt)
            assert retry["operation_id"] == receipt["operation_id"]
            assert custody() == native_custody
            prompt(env, "GO_RESTORED_OPEN_CODE", page)
            current = page()
            assert all(current[key] == original[key] for key in ("rootID","nativeCreatedAtMS","backendGeneration"))
            assert all(done(current, marker) for marker in ("GO_ORIGINAL_OPEN_CODE","RUST_CONTINUED_OPEN_CODE","GO_RESTORED_OPEN_CODE"))
            assert sum(any(old_prompt == part.get("text") for part in item["parts"])
                       for item in current["messages"]) == 1
            print("pinned Go kept the original native conversation and replayed its receipt without input", flush=True)
    finally:
        if active:
            with daemon(go, env, root/"go-cleanup.log"):
                result = owner(env, {"op":"kill","session_id":active})
                assert result["op"] == "ok", result
                wait(lambda: (session/"exit.json").is_file(), timeout=20)
