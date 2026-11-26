#!/usr/bin/env python3
"""Exercise signed Go and Rust template replay, streaming, and dashboard saves in one private catalog."""
import argparse
import base64
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import struct
import subprocess
import tempfile

from test_board import owner_request, p


def send(conn, value):
    body = json.dumps(value).encode()
    conn.sendall(struct.pack(">IB", len(body) + 1, 1) + body)


def read(stream):
    size, = struct.unpack(">I", stream.read(4))
    assert 1 < size <= 1 << 20
    payload = stream.read(size)
    assert len(payload) == size and payload[0] == 1
    return json.loads(payload[1:])


def upload_template(path, target, name, prompt, *, complete=True, before_complete=None,
                    discard=False, busy=False, expired=False):
    with socket.socket(socket.AF_UNIX) as conn, conn.makefile("rb") as stream:
        conn.settimeout(23 if expired else 8)
        conn.connect(path)
        capability = "schedule-template-upload-v1"
        send(conn, {"op":"hello", "protocol_version":1, "capabilities":[capability]})
        hello = read(stream)
        assert capability in hello["capabilities"]
        endpoint = hello["endpoint_id"]
        if busy:
            error = read(stream)
            assert error["endpoint_id"] == endpoint and "another oversized" in error["error"], error
            return error
        text = name.encode() + prompt.encode()
        send(conn, dict(op="schedule_template_upload", endpoint_id=endpoint, name_bytes=len(name.encode()),
                        encoded_bytes=len(text), **target))
        assert read(stream) == {"op":"schedule_template_upload", "endpoint_id":endpoint, "ready":True}
        sequence = 0
        for start in range(0, len(text), 64 << 10):
            data = base64.b64encode(text[start:start + (64 << 10)]).decode()
            send(conn, dict(op="schedule_template_chunk", endpoint_id=endpoint, sequence=sequence, data=data))
            sequence += 1
        if before_complete is not None:
            before_complete()
        if complete:
            send(conn, dict(op="schedule_template_chunk", endpoint_id=endpoint, sequence=sequence, complete=True))
        conn.shutdown(socket.SHUT_WR)
        if expired:
            assert stream.read(1) == b"", "expired upload returned a reply instead of closing"
            return None
        if discard:
            assert conn.recv(1, socket.MSG_PEEK), "server never published the committed acknowledgement"
            return None
        reply = read(stream)
        assert reply["endpoint_id"] == endpoint
        return reply


def read_template_snapshot(path, after_header):
    with socket.socket(socket.AF_UNIX) as conn, conn.makefile("rb") as stream:
        conn.settimeout(8)
        conn.connect(path)
        send(conn, {"op": "hello", "protocol_version": 1,
                    "capabilities": ["schedule-catalog-v1"]})
        owner = read(stream)["endpoint_id"]
        send(conn, {"op": "schedule_templates", "endpoint_id": owner, "stream": True})
        assert read(stream) == {"op": "schedule_templates", "endpoint_id": owner, "stream": True}
        after_header()
        content = bytearray()
        for sequence in range(128):
            packet = read(stream)
            assert packet["op"] == "schedule_template_chunk"
            assert packet["endpoint_id"] == owner and packet["sequence"] == sequence
            if packet.get("complete") is True:
                return json.loads(content)
            content.extend(base64.b64decode(packet["data"], validate=True))
        raise AssertionError("fixture template snapshot never completed")


def run(root, rust, go):
    for name in ("bin", "home", "runtime", "tmp", "work", "effects", "state"):
        (root / name).mkdir(mode=0o700)
    staged_rust, staged_go = root / "bin/swarm-rust", root / "bin/swarm-go"
    shutil.copy2(rust, staged_rust)
    shutil.copy2(go, staged_go)
    shutil.copy2(p.__file__, root / "bin/driver")
    rust, go = staged_rust, staged_go
    env = p.environment(root)
    guard = p.guard(root)
    work = root / "work"
    socket = env["SWARM_DAEMON_SOCK"]
    database = root / "state/schedules/state.db"
    prompt = root / "instructions"
    prompt.write_text("original Go template")

    def cli(binary, *args, check=True):
        return subprocess.run([*guard, binary, "schedule", "templates", *args], cwd=work,
                              env=env, stdin=subprocess.DEVNULL, capture_output=True,
                              timeout=10, check=check)

    def daemon():
        with (root / "daemon.log").open("ab") as log:
            return subprocess.Popen([*guard, rust, "daemon"], cwd=work, env=env,
                                    stdin=subprocess.DEVNULL, stdout=log, stderr=log)

    def ready(child):
        if child.poll() is not None:
            raise RuntimeError((root / "daemon.log").read_text())
        try:
            return owner_request(socket, "subscribe", None)
        except OSError:
            return None

    original = json.loads(cli(go, "create", "Existing Go template", prompt).stdout)
    request = {"op": "schedule_template_change", "change": {
        "kind": "save", "create_id": "01M8C694P5EA2PJDV268PQ74XW",
        "name": "Unacknowledged template", "prompt": "First line\nSecond line",
    }}
    capability = "schedule-catalog-control-v1"
    template_id = request["change"]["create_id"]
    owner = daemon()
    try:
        p.wait_for(lambda: ready(owner), "first private Rust owner")
        owner_request(socket, capability, request, discard=True)

        def stored():
            result = cli(rust, "show", template_id, check=False)
            return json.loads(result.stdout) if result.returncode == 0 else None

        created = p.wait_for(stored, "durable create after a discarded response")
        assert created["prompt"] == request["change"]["prompt"]
        owner.terminate()
        owner.wait(timeout=8)
        assert json.loads(cli(go, "show", template_id).stdout) == created
        cli(go, "delete", template_id)
        deleted = database.read_bytes()

        owner = daemon()
        p.wait_for(lambda: ready(owner), "replacement private Rust owner")
        response = owner_request(socket, capability, request)
        assert response["op"] == "error" and "was deleted" in response["error"], response
        assert database.read_bytes() == deleted
        assert cli(go, "show", template_id, check=False).returncode != 0
        assert json.loads(cli(go, "show", original["id"]).stdout) == original

        big_prompt = "🙂a\t\n" * 200_000 + "Legacy checked tail"
        assert len(big_prompt.encode()) > 1 << 20
        prompt.write_text(big_prompt)
        large = json.loads(cli(go, "create", "Large retained Go template", prompt).stdout)

        def replace_after_snapshot():
            prompt.write_text("Go updated this template during the stream")
            cli(go, "edit", large["id"], "Renamed during read", prompt)

        snapshot = read_template_snapshot(socket, replace_after_snapshot)
        original_streamed, = [item for item in snapshot if item["id"] == large["id"]]
        assert original_streamed == large
        assert json.loads(cli(go, "show", large["id"]).stdout)["name"] == "Renamed during read"
        prompt.write_text(big_prompt)
        cli(go, "edit", large["id"], large["name"], prompt)
        unchanged = database.read_bytes()
        board = p.Board([*guard, str(rust)], root, env)
        try:
            def step(keys, landmark):
                board.drain()
                start = len(board.capture)
                board.send(keys)
                board.wait_bytes(landmark, start)

            board.wait_bytes(b"swarm")
            step(b"m", b"More")
            step(b"a", b"Automations")
            step(b"t", b"Large retained Go template")
            step(b"\x1b[B\r", b"Personal templates / Edit")
            step(b"\t", b"Legacy checked tail")
            step(b"\x1b[27u", b"Automations / Personal templates")
            assert database.read_bytes() == unchanged
            step(b"n", b"Personal templates / Edit")
            step(b"A small template\tSmall instructions\r", b"Automations / Personal templates")
            assert json.loads(cli(go, "show", large["id"]).stdout)["prompt"] == big_prompt
            all_templates = json.loads(cli(go, "list").stdout)
            small, = [item for item in all_templates if item["name"] == "A small template"]
            assert small["prompt"] == "Small instructions"
            step(b"\x1b[B\x1b[B\r", b"Personal templates / Edit")
            step(b"!\r", b"Automations / Personal templates")
            edited = json.loads(cli(go, "show", large["id"]).stdout)
            assert edited["name"] == large["name"] + "!" and edited["prompt"] == big_prompt
            assert edited["created_at"] == large["created_at"]
            step(b"\x1b[27u", b"Automations")
            step(b"n", b"New automation")
            step(b"\x14", b"Large retained Go template!")
            step(b"\x1b[B\x1b[B\r", b"New automation")
            step(b"\x05", b"Personal templates / Edit")
            step(b" Copy\r", b"New automation")
            copied, = [row for row in json.loads(cli(go, "list").stdout) if row["name"] == edited["name"] + " Copy"]
            assert copied["id"] != edited["id"] and copied["prompt"] == big_prompt
            board.send(b"\x1b[27u\x1b[27u\x1b[27u")

            def exited():
                board.drain()
                return board.child.poll() is not None

            p.wait_for(exited, "large template board quit")
            assert board.child.returncode == 0
        finally:
            board.close()

        uploaded_id = "01M8C694P5EA2PJDV268PQ74XY"
        target = {"create_id":uploaded_id}
        snapshot = database.read_bytes()
        error = upload_template(socket, target, "Truncated", big_prompt, complete=False,
                                before_complete=lambda: upload_template(socket, target, "Busy", big_prompt, busy=True))
        assert error["op"] == "error" and database.read_bytes() == snapshot, error
        changed = []
        def competing_edit():
            prompt.write_text("Go edit won during the Rust upload")
            cli(go, "edit", edited["id"], "Go won", prompt)
            changed.append(database.read_bytes())
        cas = {"id":edited["id"], "expected_updated_at":edited["updated_at"]}
        stale = upload_template(socket, cas, "Stale Rust edit", big_prompt, before_complete=competing_edit)
        assert stale["op"] == "error" and "changed; refresh" in stale["error"], stale
        assert database.read_bytes() == changed[0]
        upload_template(socket, target, "Lost oversized acknowledgement", big_prompt, discard=True)
        stored = json.loads(cli(go, "show", uploaded_id).stdout)
        assert stored["prompt"] == big_prompt
        committed = database.read_bytes()
        owner.terminate()
        owner.wait(timeout=8)
        owner = daemon()
        p.wait_for(lambda: ready(owner), "Rust owner after lost oversized acknowledgement")
        duplicate = upload_template(socket, target, "Lost oversized acknowledgement", big_prompt)["saved"]
        assert {key:duplicate[key] for key in ("id", "created_at", "updated_at")} == {
            key:stored[key] for key in ("id", "created_at", "updated_at")}
        fingerprint = hashlib.sha256(json.dumps([stored["name"], stored["prompt"]],
            ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()
        assert set(duplicate) == {"id", "created_at", "updated_at", "fingerprint"}
        assert duplicate["fingerprint"] == fingerprint and database.read_bytes() == committed

        for lock_path, mode, suffix in ((database.with_name("state.lock"), fcntl.LOCK_EX, "Z"),
                                        (database, fcntl.LOCK_SH, "V")):
            expired_id = "01M8C694P5EA2PJDV268PQ74X" + suffix
            retry = {"create_id":expired_id}
            before = database.read_bytes()
            with lock_path.open("rb") as blocking:
                fcntl.flock(blocking, mode | fcntl.LOCK_NB)
                upload_template(socket, retry, "Expired while locked", big_prompt, expired=True)
                assert database.read_bytes() == before
                probe = upload_template(socket, retry, "Released upload permit", big_prompt, complete=False)
                assert probe["op"] == "error" and "another oversized" not in probe["error"], probe
                assert database.read_bytes() == before
            assert database.read_bytes() == before
            recovered = upload_template(socket, retry, "Fresh after release", big_prompt)["saved"]
            assert recovered["id"] == expired_id
            assert json.loads(cli(go, "show", expired_id).stdout)["name"] == "Fresh after release"
    finally:
        if owner.poll() is None:
            owner.terminate()
            owner.wait(timeout=8)
        result = p.reap(root, None, {})
        assert not result["remaining"], result
    print("Go reopened and deleted the Rust template. Exact retry after restart made no write.")
    print("The board read an over-frame Go template and saved another without changing it.")
    print("A Go edit during a Rust stream preserved the original snapshot and the later edit.")
    print("The dashboard edited and copied an over-frame legacy Go template; signed Go reopened both.")
    print("Truncation and concurrent Go edits caused no Rust write; a lost oversized acknowledgement replayed exactly once.")
    print("Expired schedule and Bolt lock waits released the upload permit without a late write; fresh saves succeeded.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("rust", type=Path)
    parser.add_argument("go", type=Path)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="tp-", dir=os.environ.get("TMPDIR", "/tmp")) as tmp:
        run(Path(tmp).resolve(), args.rust.resolve(strict=True), args.go.resolve(strict=True))
