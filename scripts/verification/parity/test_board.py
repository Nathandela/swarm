#!/usr/bin/env python3
"""Run Rust board launch and reattach after daemon SIGKILL on a real local PTY."""

import argparse
import base64
import copy
import importlib.util
from importlib.machinery import SourceFileLoader
import json
import os
import re
from pathlib import Path
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import termios
import time
import unicodedata

HERE = Path(__file__).resolve().parent
DRIVER = HERE / "swarm-parity"
spec = importlib.util.spec_from_loader("parity", SourceFileLoader("parity", str(DRIVER)))
p = importlib.util.module_from_spec(spec)
spec.loader.exec_module(p)


def owner_request(path, capability, body, *, discard=False):
    def read(conn, count):
        chunks = bytearray()
        while len(chunks) < count:
            data = conn.recv(count - len(chunks))
            assert data, "owner socket closed before a complete frame"
            chunks.extend(data)
        return chunks

    def receive(conn):
        length, = struct.unpack(">I", read(conn, 4))
        assert 1 <= length <= 1 << 20
        payload = read(conn, length)
        assert payload[0] == 1
        return json.loads(payload[1:])

    def send(conn, value):
        payload = json.dumps(value).encode()
        conn.sendall(struct.pack(">IB", len(payload) + 1, 1) + payload)

    with socket.socket(socket.AF_UNIX) as conn:
        conn.settimeout(5)
        conn.connect(path)
        send(conn, {"op":"hello", "protocol_version":1, "capabilities":[capability]})
        hello = receive(conn)
        assert hello["op"] == "hello"
        if body is None:
            return hello
        assert capability in hello["capabilities"]
        send(conn, {**body, "endpoint_id":hello["endpoint_id"]})
        if discard:
            conn.shutdown(socket.SHUT_WR)
            return None
        return receive(conn)


def witness(root, tmux=None, cycle=False, automations=False, form_grid=False, board_grid=False, handoff=False, touch=False):
    env = p.environment(root)
    cwd = root / "work"
    binary = root / "bin/swarm"

    def cli(*args):
        return subprocess.run([binary, *args], cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                              capture_output=True, timeout=5)

    def roster():
        command = cli("ls", "--json")
        return (json.loads(command.stdout),) if command.returncode == 0 else None

    def start():
        with (root / "daemon-capture").open("ab") as log:
            result = subprocess.Popen([binary, "daemon"], cwd=cwd, env=env,
                                      stdin=subprocess.DEVNULL, stdout=log, stderr=log)
        p.wait_for(roster, "public owner socket")
        return result

    if handoff:
        os.umask(0o077)
        env["ANTHROPIC_API_KEY"] = "fixture-handoff-only"
        (root / "home/.claude").mkdir(mode=0o700, exist_ok=True)
        daemon = start()
        (root / "effects/board-events").touch()
        (root / "effects/handoff-fixture").touch()
        src = cli("spawn", "--cli", "claude", "--name", "amber", "--dir", cwd,
                  "--prompt", "source for synthetic handoff")
        assert src.returncode == 0, src.stderr
        source = src.stdout.decode().strip()
        source_row, = roster()[0]
        project = root / "home/.claude/projects" / re.sub(r"[^a-zA-Z0-9]", "-", str(cwd))
        project.mkdir(mode=0o700, parents=True)
        conversation = "8bf8d92a-d8f0-4025-9e38-8c70c64c6dd1"
        transcript = project / (conversation + ".jsonl")
        transcript.write_text(json.dumps({"sessionId":conversation, "cwd":str(cwd),
                                          "timestamp":source_row["created_at"]}) + "\n")
        transcript.chmod(0o600)

        def hook(text, group):
            assert cli("send", source, "--text", text).returncode == 0
            def seen():
                rows = roster()
                return rows and any(row["id"] == source and row["group"] == group for row in rows[0])
            p.wait_for(seen, group)

        hook("fixture review", "ready_for_review")
        board = p.Board([str(root / "bin/board")], root, env)
        try:
            board.wait_bytes(b"amber")
            board.send(b"h")
            board.wait_bytes("● claude".encode())
            board.send(b"\r")
            log = root / "effects/input"
            p.wait_for(lambda: b"Prepare and supervise a handoff" in log.read_bytes(),
                       "supervised prompt on captured source")
            before_hook = len(board.capture)
            hook("fixture permission", "needs_input")
            board.wait_bytes(b"approval", before_hook)
            begin = len(board.capture)
            board.send(b"h")
            board.wait_bytes(b"hands-off", begin)
            board.wait_bytes("● claude".encode(), begin)
            hello = owner_request(env["SWARM_DAEMON_SOCK"], "hands-off-handoff", None)
            negotiated = "hands-off-handoff" in hello.get("capabilities", [])
            begin = len(board.capture)
            board.send(b"\r")
            if negotiated:
                def successor():
                    board.drain()
                    rows = roster()
                    return rows and len(rows[0]) == 2 and next(
                        (row for row in rows[0] if row["id"] != source), None)
                child = p.wait_for(successor, "hands-off successor")
                assert child["spawned_from"] == source.split("/", 1)[1]
                board.wait_bytes(b"fixture ready", begin)
                args = root / "effects/provider-argv"
                p.wait_for(lambda: str(transcript) in args.read_text()
                           or str(transcript).encode() in log.read_bytes(),
                           "owner-authored transcript pointer")
                back = len(board.capture)
                board.send(b"\x11")
                board.wait_bytes(b"amber", back)
                assert cli("kill", child["id"]).returncode == 0
            else:
                board.wait_bytes(b"daemon is too old for hands-off", begin)
                assert len(roster()[0]) == 1
                board.send(b"\x1b[27u")
            assert any(row["id"] == source and row["status"]["Process"] == "running"
                       for row in roster()[0])
            assert cli("kill", source).returncode == 0
            board.send(b"\x1b[27u\x1b[27u")
            p.wait_for(lambda: (board.drain(), board.child.poll())[1] is not None, "handoff board quit")
            assert board.child.returncode == 0
        except RuntimeError:
            print("handoff fixture terminal tail:", repr(bytes(board.capture[-1400:])))
            raise
        finally:
            board.close()
        daemon.terminate()
        daemon.wait(timeout=5)
        return

    if automations:
        daemon = start()
        listing = cli("schedule", "list", "--json")
        assert listing.returncode == 0 and json.loads(listing.stdout) is None, listing.stderr
        assert not (root / "state/schedules").exists()
        spec = root / "automation.json"
        spec.write_text(json.dumps({
            "name": "Fixture automation", "prompt": "Wait for synthetic instructions",
            "directory": str(cwd), "authority": "agent-default", "workspace": "directory",
            "agents": ["claude"], "calendar": {"kind":"once", "time_zone":"UTC",
                                               "time":"01:00", "date":"2099-01-01"},
        }))
        schedule = cli("schedule", "create", "--spec-file", spec)
        assert schedule.returncode == 0, schedule.stderr
        job = json.loads(schedule.stdout)["id"]
        listing = cli("schedule", "list", "--json")
        assert listing.returncode == 0 and [row["id"] for row in json.loads(listing.stdout)] == [job]
        assert json.loads(cli("schedule", "show", job).stdout)["id"] == job
        assert json.loads(cli("schedule", "history", job).stdout) is None
        owner_socket = env["SWARM_DAEMON_SOCK"]

        def shown(id):
            return json.loads(cli("schedule", "show", id).stdout)

        def history_for(id):
            return json.loads(cli("schedule", "history", id).stdout) or []

        original = shown(job)
        pause = {"op":"schedule_catalog_change", "change":{
            "kind":"state", "id":job, "revision":original["revision_id"],
            "expected":original["state"], "generation":original.get("lifecycle_generation", 0),
            "updated_at":original["updated_at"], "value":"paused",
        }}
        owner_request(owner_socket, "schedule-catalog-control-v1", pause, discard=True)
        p.wait_for(lambda: shown(job)["state"] == "paused", "lost pause ACK")
        resumed = cli("schedule", "resume", job)
        assert resumed.returncode == 0, resumed.stderr
        refusal = owner_request(owner_socket, "schedule-catalog-control-v1", pause)
        assert refusal["op"] == "error" and shown(job)["state"] == "active"

        def quit_board(board):
            board.send(b"\x1b[27u\x1b[27u")
            def exited():
                board.drain()
                return board.child.poll() is not None
            p.wait_for(exited, "automation board quit")
            assert board.child.returncode == 0

        board = p.Board([str(root / "bin/board")], root, env)
        try:
            board.wait_bytes(b"swarm")
            board.send(b"m")
            board.wait_bytes(b"More")
            board.send(b"a")
            board.wait_bytes(b"Fixture automation")
            board.send(b"\r")
            board.wait_bytes(b"Status")
            board.send(b"h")
            board.wait_bytes(b"No runs for this automation")

            def step(keys, landmark):
                start = len(board.capture)
                board.send(keys)
                board.wait_bytes(landmark, start)

            step(b"\x1b[D", b"Status")
            step(b"\x1b[D", b"n new")
            step(b"n", b"New automation")
            step(b"Created from board\x1b[B\x1b[B\x1b[200~first line\nsecond line\x1b[201~"
                 b"\x1b[B\x15claude\r", b"Status")
            jobs = json.loads(cli("schedule", "list", "--json").stdout)
            created, = [record for record in jobs if record["spec"]["name"] == "Created from board"]
            assert created["state"] == "paused"
            assert created["spec"]["prompt"] == "first line\nsecond line"
            created_id = created["id"]
            step(b"e", b"Edit automation")
            step(b"\x15Revised from board\r", b"Revised from board")
            edited = json.loads(cli("schedule", "show", created_id).stdout)
            assert edited["spec"]["name"] == "Revised from board"
            assert edited["revision_id"] != created["revision_id"]
            step(b"p", b"Status")
            assert json.loads(cli("schedule", "show", created_id).stdout)["state"] == "active"
            manual = {"op":"schedule_board_manual", "schedule_id":created_id,
                      "intent":{"kind":"run", "previous":None}}
            owner_request(owner_socket, "schedule-board-manual-v1", manual, discard=True)
            def recorded_without_ack():
                runs = history_for(created_id)
                if not runs:
                    return None
                single, = runs
                if single["state"] == "queued" or any(a["state"] == "unknown" for a in single.get("attempts", [])):
                    return None
                return single
            first = p.wait_for(recorded_without_ack, "durable outcome without manual run ACK")
            assert first["manual"] is True
            quit_board(board)
            board.close()
            daemon.kill()
            daemon.wait(timeout=5)
            daemon = start()
            board = p.Board([str(root / "bin/board")], root, env)
            board.wait_bytes(b"swarm")
            step(b"m", b"More")
            step(b"a", b"Revised from board")
            offset = len(board.capture)
            step(b"\x1b[F\r", b"Status")
            def restored_run():
                board.drain()
                frame = board.capture[offset:]
                return b"Run pending" in frame or b"Run recorded" in frame
            p.wait_for(restored_run, "durable run receipt after restart")
            step(b"r", b"Run recorded")
            assert [run["id"] for run in history_for(created_id)] == [first["id"]]
            step(b"R", b"Run recorded")
            ids = {run["id"] for run in history_for(created_id)}
            assert first["id"] in ids and len(ids) == 2
            stale = {**manual, "intent":{"kind":"run", "previous":first["id"]}}
            assert owner_request(owner_socket, "schedule-board-manual-v1", stale)["op"] == "error"
            assert {run["id"] for run in history_for(created_id)} == ids
            assert not (root / "effects/worker").exists()
            step(b"h", b"enter run")
            step(b"\r", b"Automation run")
            step(b"\x1b[D", b"enter run")
            step(b"\x1b[D", b"Revised from board")
            step(b"p", "Status · paused".encode())
            assert json.loads(cli("schedule", "show", created_id).stdout)["state"] == "paused"
            step(b"x", b"Archive automation?")
            step(b"n", b"Status")
            assert json.loads(cli("schedule", "show", created_id).stdout)["state"] == "paused"
            step(b"x", b"Archive automation?")
            step(b"y", b"Status")
            assert json.loads(cli("schedule", "show", created_id).stdout)["state"] == "archived"

            daemon.terminate()
            daemon.wait(timeout=5)
            offline = cli("schedule", "list", "--json")
            assert offline.returncode == 0
            assert {row["id"] for row in json.loads(offline.stdout)} == {job, created_id}
            board.wait_bytes(b"daemon connection lost")
            board.send(b"u")
            board.wait_bytes(b"daemon disconnected")
            quit_board(board)
        finally:
            board.close()
        return

    def terminal(payload, fresh):
        board = p.Board([str(root / "bin/board")], root, env)
        try:
            if fresh:
                board.wait_bytes(b"swarm")
                board.send(b"n")
                board.wait_bytes(b"new session")
                board.wait_bytes("● claude".encode())
                board.send(b"\x15" + os.fsencode(cwd / "qual") + b"\t")
                board.wait_bytes(b"/qualified/")
                board.send(b"\x1b[Btail\x1b[1;9Dprefix")
                board.wait_bytes("prefix█tail".encode())
                start = len(board.capture)
                board.send(b"\x1b[127;9u")
                board.wait_bytes("█tail".encode(), start)
                start = len(board.capture)
                board.send(b"\x1b[F")
                board.wait_bytes("tail█".encode(), start)
                start = len(board.capture)
                board.send(b"\x1b[H")
                board.wait_bytes("█tail".encode(), start)
                start = len(board.capture)
                board.send(b"\x1b[57351;9u")
                board.wait_bytes("tail█".encode(), start)
                board.resize(40, 8)
                board.send(b"\x15" + ("界" * 13 + "X").encode() + b"\x1b[D")
                board.wait_bytes(("界" * 12 + "█").encode())
                board.resize(80, 24)
                board.send(b"\x1b[F\x15amber\r")
                board.wait_bytes(b"fixture ready")
            else:
                board.wait_bytes(b"amber")
                board.send(b"\r")
                board.wait_bytes(b"fixture received before")
            worker = int((root / "effects/worker").read_text())
            birth = p.generation(worker)
            assert birth is not None
            board.send(payload + b"\r")
            board.wait_bytes(b"fixture received " + payload + b"\r\nfixture end")
            board.resize(91, 27)
            p.wait_for(lambda: (root / "effects/size").read_bytes() == b"91x26\n", "worker resize")
            detached = len(board.capture)
            board.send(b"\x11")
            board.wait_bytes(b"\x1b[?1049h", detached)
            board.wait_bytes(b"amber", detached)
            board.send(b"\x1b[27u")

            def quit_board():
                board.drain()
                return board.child.poll() is not None

            p.wait_for(quit_board, "board quit")
            assert board.child.returncode == 0
            assert termios.tcgetattr(board.master) == board.original_mode
            assert p.generation(worker) == birth
            return worker, birth
        finally:
            board.close()

    if touch:
        daemon = start()
        (root / "effects/touch-native").touch()
        launched = cli("spawn", "--cli", "claude", "--name", "touch-amber",
                       "--dir", cwd, "--prompt", "offline fixture")
        assert launched.returncode == 0, launched.stderr
        identity = launched.stdout.decode().strip()
        logged = root / "effects/input"
        board = p.Board([str(root / "bin/board")], root, env)
        try:
            board.wait_bytes(b"touch-amber")
            board.send(b"\r")
            board.wait_bytes(b"ctrl+q returns to swarm")
            assert b"[swarm (ctrl+q)]" not in board.capture

            def marker(data, expected):
                board.send(data + b"\r")
                try:
                    p.wait_for(lambda: logged.read_bytes() == expected, "exact attached worker bytes")
                except RuntimeError as error:
                    raise AssertionError((expected, logged.read_bytes(), board.capture[-900:])) from error

            press = b"\x1b[<0;3;24M"
            release = b"\x1b[<0;3;24m"
            native_press = b"\x1b[<0;3;3M"
            marker(press + release + b"disabled", press + release + b"disabled\r")
            offset = len(board.capture)
            board.send(b"\x11")
            board.wait_bytes(b"More (m)", offset)
            board.send(b"o")
            board.wait_bytes(b"CLI button", offset)
            board.send(b"\x1b[B\x1b[B\x1b[C")
            board.wait_bytes(b"[x] on", offset)
            offset = len(board.capture)
            board.send(b"\r")
            board.wait_bytes(b"More (m)", offset)
            offset = len(board.capture)
            board.send(b"\r")
            board.wait_bytes(b"[swarm (ctrl+q)]", offset)

            before = logged.read_bytes()
            wanted = before + native_press + release + b"native\r"
            marker(native_press + release + b"native", wanted)
            drag = b"\x1b[<32;3;5M"
            marker(press + drag + release + b"dragged", wanted + b"dragged\r")
            wanted = logged.read_bytes()
            offset = len(board.capture)
            board.send(press[:5])
            board.send(press[5:] + release[:6])
            board.send(release[6:])
            board.wait_bytes(b"More (m)", offset)
            assert logged.read_bytes() == wanted
            offset = len(board.capture)
            board.send(b"\r")
            board.wait_bytes(b"[swarm (ctrl+q)]", offset)
            offset = len(board.capture)
            marker(b"query mouse", wanted + b"query mouse\r")
            board.wait_bytes(b"fixture queried", offset)
            emitted = board.capture[offset:]
            restore = emitted.find(b"\x1b[?1000h")
            query = emitted.find(b"\x1b[?1000$p")
            assert 0 <= restore < query, emitted
            board.wait_bytes(b"ctrl+q returns to swarm", offset)
            # The Go shim's VT answers DECRQM itself. A bare PTY transports the
            # terminal query but has no outer emulator to return another reply.
            board.send(press + release + b"native after query\r")
            normal = wanted + b"query mouse\r"
            native = press + release + b"native after query\r"
            p.wait_for(lambda: logged.read_bytes() in (normal + native, normal + b"\x1b[?1000;1$y" + native),
                       "native mouse delivery after ownership release")
            offset = len(board.capture)
            board.send(b"\x11")
            board.wait_bytes(b"More (m)", offset)
            board.send(b"\x1b[27u")
            p.wait_for(lambda: (board.drain(), board.child.poll())[1] is not None, "quit after touch detach")
            assert board.child.returncode == 0
            assert termios.tcgetattr(board.master) == board.original_mode
        finally:
            board.close()
        assert cli("kill", identity).returncode == 0
        deleted = owner_request(env["SWARM_DAEMON_SOCK"], "subscribe",
                                {"op": "delete", "session_id": identity})
        assert deleted["op"] != "error", deleted
        (root / "effects/touch-native").unlink()
        launched = cli("spawn", "--cli", "claude", "--name", "touch-plain",
                       "--dir", cwd, "--prompt", "offline fixture")
        assert launched.returncode == 0, launched.stderr
        identity = launched.stdout.decode().strip()
        board = p.Board([str(root / "bin/board")], root, env)
        try:
            board.wait_bytes(b"touch-plain")
            board.send(b"\r")
            board.wait_bytes(b"[swarm (ctrl+q)]")
            before = logged.read_bytes()
            marker(native_press + b"plain", before + b"plain\r")
            offset = len(board.capture)
            board.send(press)
            board.resize(30, 2)
            p.wait_for(lambda: (root / "effects/size").read_bytes() == b"30x2\n", "footer released on resize")
            board.resize(80, 24)
            board.wait_bytes(b"[swarm (ctrl+q)]", offset)
            marker(release + b"resized", before + b"plain\rresized\r")
            offset = len(board.capture)
            board.send(press + release)
            board.wait_bytes(b"More (m)", offset)
            board.send(b"\x1b[27u")
            p.wait_for(lambda: (board.drain(), board.child.poll())[1] is not None, "quit after plain touch detach")
            assert board.child.returncode == 0
            assert termios.tcgetattr(board.master) == board.original_mode
        finally:
            board.close()
        assert cli("kill", identity).returncode == 0
        daemon.terminate()
        daemon.wait(timeout=5)
        return

    if cycle:
        daemon = start()
        sessions = []
        for name in ("amber", "violet"):
            launch = cli("spawn", "--cli", "claude", "--name", name,
                         "--dir", cwd, "--prompt", "offline fixture")
            assert launch.returncode == 0, launch.stderr
            sessions.append(launch.stdout.decode().strip())
        board = p.Board([str(root / "bin/board")], root, env)
        try:
            board.wait_bytes(b"violet")
            board.send(b"\r")
            board.wait_bytes(b"fixture ready")

            def identify(marker):
                board.send(marker + b"\r")
                board.wait_bytes(b"fixture received " + marker)
                def owners():
                    return [identity for identity in sessions
                            if b"fixture received " + marker in cli("peek", identity).stdout]
                found = p.wait_for(owners, "worker echo in the owning session")
                assert len(found) == 1, found
                return found[0]

            first = identify(b"first-only")
            offset = len(board.capture)
            board.send(b"\x0e")
            board.wait_bytes(b"fixture ready", offset)
            second = identify(b"second-only")
            assert first != second
            offset = len(board.capture)
            board.send(b"\x1b[112;5u")
            board.wait_bytes(b"first-only", offset)
            assert identify(b"wrapped-back") == first
            offset = len(board.capture)
            board.send(b"\x10")
            board.wait_bytes(b"second-only", offset)
            offset = len(board.capture)
            board.send(b"\x11")
            board.wait_bytes(b"violet", offset)
            offset = len(board.capture)
            board.send(b"\r")
            board.wait_bytes(b"second-only", offset)
            assert identify(b"last-selection") == second
            assert (root / "effects/input").read_bytes() == b"first-only\rsecond-only\rwrapped-back\rlast-selection\r"
            offset = len(board.capture)
            board.send(b"\x11")
            board.wait_bytes(b"violet", offset)
            board.send(b"\x1b[27u")
            p.wait_for(lambda: (board.drain(), board.child.poll())[1] is not None, "board quit after cycling")
            assert board.child.returncode == 0
            assert termios.tcgetattr(board.master) == board.original_mode
        finally:
            board.close()
        for identity in sessions:
            assert cli("kill", identity).returncode == 0
        daemon.terminate()
        daemon.wait(timeout=5)
        return

    if tmux is not None:
        if form_grid:
            launch_grid_witness(root, roster, start, tmux)
        elif board_grid:
            roster_grid_witness(root, cli, roster, start, tmux)
        else:
            grid_witness(root, cli, roster, start, tmux)
        return

    (cwd / "qualified").mkdir()
    daemon = start()
    before = terminal(b"before", True)
    rows, = roster()
    assert len(rows) == 1 and rows[0]["name"] == "amber"
    assert Path(rows[0]["cwd"]) == cwd / "qualified"
    identity = rows[0]["id"]
    daemon.kill()
    daemon.wait(timeout=5)
    assert p.generation(before[0]) == before[1]
    daemon = start()
    rows, = roster()
    assert [row["id"] for row in rows] == [identity]
    assert terminal(b"after", False) == before
    assert (root / "effects/input").read_bytes() == b"before\rafter\r"
    assert cli("kill", identity).returncode == 0
    daemon.terminate()
    daemon.wait(timeout=5)


TMUX_MODES = ("cursor_x", "cursor_y", "cursor_flag", "alternate_on", "insert_flag",
              "wrap_flag", "keypad_cursor_flag", "keypad_flag", "mouse_standard_flag",
              "mouse_button_flag", "mouse_all_flag", "mouse_utf8_flag", "mouse_sgr_flag")


PALETTE = ("000000", "800000", "008000", "808000", "000080", "800080", "008080", "c0c0c0",
           "808080", "ff0000", "00ff00", "ffff00", "0000ff", "ff00ff", "00ffff", "ffffff")
SGR = re.compile(r"\x1b\[([0-9;]*)m")


def indexed_rgb(index):
    if index < 16:
        return PALETTE[index]
    if index >= 232:
        gray = 8 + (index - 232) * 10
        return f"{gray:02x}" * 3
    color = index - 16
    def channel(channel_index):
        return 0 if channel_index == 0 else 55 + 40 * channel_index
    return "".join(f"{channel(value):02x}" for value in (color // 36, color // 6 % 6, color % 6))


def resolve_cells(raw, width, height):
    """Expand tmux's ANSI cell serialization including uninitialized default cells."""
    blank = [" ", 1, [], None, None]
    scope = set()
    colors = [None, None]
    lines = raw.decode().split("\n")[:-1]
    assert len(lines) == height
    grid = []
    for line in lines:
        cells = []
        tokens = SGR.split(line)
        for i, token in enumerate(tokens):
            if i % 2 == 0:
                if "\x1b" in token:
                    raise ValueError("unexpected control in tmux cell capture")
                for ch in token:
                    if ch == "\t":
                        cells.extend([[" ", 1, sorted(scope), *colors] for _ in range(8 - len(cells) % 8)])
                        continue
                    if unicodedata.combining(ch):
                        base = cells[-2] if cells[-1][1] == 0 else cells[-1]
                        base[0] += ch
                        continue
                    columns = 2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1
                    cells.append([ch, columns, sorted(scope), *colors])
                    if columns == 2:
                        cells.append(["", 0, sorted(scope), *colors])
                continue
            operations = list(map(int, token.split(";"))) if token else [0]
            while operations:
                op = operations.pop(0)
                if op == 0:
                    scope.clear()
                    colors[:] = [None, None]
                elif op in (1, 2, 3, 4, 5, 7, 8, 9):
                    scope.add(op)
                elif op == 22:
                    scope.difference_update({1, 2})
                elif op in (23, 24, 25, 27, 28, 29):
                    scope.discard(op - 20)
                elif op in (38, 48):
                    mode = operations.pop(0)
                    channel = int(op == 48)
                    if mode == 5:
                        colors[channel] = indexed_rgb(operations.pop(0))
                    elif mode == 2:
                        colors[channel] = "".join(f"{operations.pop(0):02x}" for _ in range(3))
                    else:
                        raise ValueError("unexpected color mode in tmux cell capture")
                elif op in (39, 49):
                    colors[int(op == 49)] = None
                elif 30 <= op <= 37 or 40 <= op <= 47 or 90 <= op <= 97 or 100 <= op <= 107:
                    offset = op % 10 + (8 if op >= 90 else 0)
                    colors[int(op // 10 in (4, 10))] = indexed_rgb(offset)
                else:
                    raise ValueError(f"unsupported SGR {op} in tmux cell capture")
        assert len(cells) <= width
        grid.append(cells + [blank] * (width - len(cells)))
    return grid


def compare_projection(before, after):
    if before["terminal_modes"] != after["terminal_modes"]:
        return {"before_modes": before["terminal_modes"], "after_modes": after["terminal_modes"]}
    for y, (left, right) in enumerate(zip(before["cells"], after["cells"], strict=True)):
        for x, (a, b) in enumerate(zip(left, right, strict=True)):
            if a != b:
                return {"first_cell": [x, y], "before_cell": a, "after_cell": b}
    return None


def compare_grid_receipts(reference_path, candidate_path):
    go = json.loads(Path(reference_path).read_bytes())
    rust = json.loads(Path(candidate_path).read_bytes())
    expected = go["before"]
    for capture in (expected, go["after"], rust["before"], rust["after"]):
        if capture["width"] != 80 or capture["rows"] != 23 or len(capture["cells"]) != 23 or any(len(row) != 80 for row in capture["cells"]):
            raise ValueError("fixture geometry changed")
    if expected["cells"][1][4][0] != "e\u0301":
        raise ValueError("the reference live grid must contain the combining sequence")
    old_snapshot = copy.deepcopy(expected)
    old_snapshot["cells"][1][4][0] = "e"
    for label, left, right in (("pinned Go snapshot", old_snapshot, go["after"]),
                               ("Rust live", expected, rust["before"]),
                               ("Rust reattach", expected, rust["after"])):
        difference = compare_projection(left, right)
        if difference:
            raise ValueError(f"{label}: {difference!r}")
    print("All 1,840 Rust live and reattached cells and observed modes matched the Go live grid. "
          "The pinned Go reattach lost only U+0301 at (4,1), as recorded.")


def grid_witness(root, cli, roster, start, tmux):
    env = p.environment(root)
    command = [tmux, "-S", root / "tmux.sock", "-f", "/dev/null"]
    cwd = root / "work"

    def send_tmux(*args):
        result = subprocess.run([*command, *args], env=env, cwd=cwd,
                                capture_output=True, timeout=4)
        assert result.returncode == 0, (args, result.stderr)
        return result.stdout

    def grid():
        # -e retains cell attributes. Resolution fills trailing cells. Exclude Swarm's chrome row.
        return send_tmux("capture-pane", "-pe", "-S", "0", "-E", "22", "-t", "board:0.0")

    def observed(fragment):
        return p.wait_for(lambda: screen if fragment in (screen := grid()) else None,
                          repr(fragment))

    def capture():
        raw = observed(b"grid end")
        fmt = "|".join(f"#{{{name}}}" for name in TMUX_MODES)
        modes = send_tmux("display-message", "-p", "-t", "board:0.0", fmt).decode().strip().split("|")
        cells = resolve_cells(raw, 80, 23)
        assert cells[1][2][:2] == ["年", 2] and cells[1][3][1] == 0
        return {"cells_ansi_base64": base64.b64encode(raw).decode(), "cells": cells,
                "width": 80, "rows": 23,
                "terminal_modes": dict(zip(TMUX_MODES, modes, strict=True))}

    def open_board():
        send_tmux("new-session", "-d", "-s", "board", "-x", "80", "-y", "24",
                  "-c", cwd, root / "bin/board")
        observed(b"amber")
        send_tmux("send-keys", "-t", "board:0.0", "Enter")

    (root / "effects/grid").touch()
    daemon = start()
    launched = cli("spawn", "--cli", "claude", "--name", "amber", "--dir", cwd,
                   "--prompt", "offline fixture")
    assert launched.returncode == 0, launched.stderr
    identity = launched.stdout.decode().strip()
    open_board()
    observed(b"fixture ready")
    worker = int((root / "effects/worker").read_text())
    birth = p.generation(worker)
    assert birth is not None
    send_tmux("send-keys", "-t", "board:0.0", "-l", "before")
    send_tmux("send-keys", "-t", "board:0.0", "Enter")
    before = capture()
    snapshot = cli("peek", identity)
    assert snapshot.returncode == 0
    before_snapshot = snapshot.stdout.decode()
    daemon.kill()
    daemon.wait(timeout=5)
    assert p.generation(worker) == birth
    send_tmux("kill-session", "-t", "board")
    daemon = start()
    rows, = roster()
    assert [row["id"] for row in rows] == [identity]
    open_board()
    after = capture()
    receipt = {"before": before, "after": after, "snapshot_before_daemon_death": before_snapshot,
               "first_divergence": compare_projection(before, after)}
    p.write_json(root / "grid.json", receipt)
    # tmux wraps paste only while its resolved pane has DEC bracketed-paste mode enabled.
    send_tmux("set-buffer", "after")
    send_tmux("paste-buffer", "-p", "-t", "board:0.0")
    send_tmux("send-keys", "-t", "board:0.0", "Enter")
    observed(b"fixture received after")
    assert p.generation(worker) == birth
    worker_input = (root / "effects/input").read_bytes()
    assert worker_input == b"before\r\x1b[200~after\x1b[201~\r"
    receipt["worker_input_hex"] = worker_input.hex()
    receipt["worker_identity_unchanged"] = True
    p.write_json(root / "grid.json", receipt)
    send_tmux("kill-session", "-t", "board")
    assert cli("kill", identity).returncode == 0
    daemon.terminate()
    daemon.wait(timeout=5)


def launch_grid_witness(root, roster, start, tmux):
    """Resolve launch layout in a real outer terminal at the reference breakpoints."""
    command = [tmux, "-S", root / "tmux.sock", "-f", "/dev/null"]
    def run(*argv):
        proc = subprocess.run([*command, *argv], env=p.environment(root), cwd=root / "work",
                              capture_output=True, timeout=4)
        assert proc.returncode == 0, (argv, proc.stderr)
        return proc.stdout

    def frame(cols, rows):
        raw = run("capture-pane", "-pe", "-S", "0", "-E", str(rows - 1), "-t", "form:0.0")
        return {"ansi_base64": base64.b64encode(raw).decode(),
                "cells": resolve_cells(raw, cols, rows)}

    def landmark(fragments, cols, rows, *, settled=False):
        def observed():
            snapshot = frame(cols, rows)
            text = "\n".join("".join(cell[0] for cell in row) for row in snapshot["cells"]).lower()
            return snapshot if (any(fragment in text for fragment in fragments)
                                and (not settled or "claude" in text)) else None
        return p.wait_for(observed, f"{cols}x{rows} {fragments!r}")

    daemon = start()
    frames = []
    for cols, rows in ((80, 24), (40, 8), (24, 4)):
        barrier = root / "effects/form-cold"
        barrier.touch()
        run("new-session", "-d", "-s", "form", "-x", str(cols), "-y", str(rows),
            "-c", root / "work", root / "bin/board")
        landmark(("swarm", "(n)", "no sessions"), cols, rows)
        run("send-keys", "-t", "form:0.0", "n")
        compact_block = cols < 32 or rows < 8
        directory = landmark(("resize to 32",) if compact_block else ("▌ directory",), cols, rows)
        barrier.unlink()
        run("send-keys", "-t", "form:0.0", "Down", "Down", "Down")
        agent = landmark(("▌ agent",), cols, rows, settled=True)
        if compact_block:
            run("send-keys", "-t", "form:0.0", "Enter")
            landmark(("resize to 32",), cols, rows)
            assert roster() == ([],), "compact launch unexpectedly created a session"
        frames.append({"cols": cols, "rows": rows, "directory": directory, "agent": agent})
        run("kill-session", "-t", "form")
    daemon.terminate()
    daemon.wait(timeout=5)
    p.write_json(root / "grid.json", {"form_frames": frames, "form_cwd": str(root / "work"), "first_divergence": None})


def roster_grid_witness(root, cli, roster, start, tmux):
    (root / "effects/board-events").touch()
    daemon = start()
    ids = {}
    for name in ("amber", "teal", "violet", "slate"):
        session = cli("spawn", "--cli", "claude", "--name", name, "--dir", root / "work",
                      "--prompt", "offline board fixture")
        assert session.returncode == 0, session.stderr
        ids[name] = session.stdout.decode().strip()
    for name, text, event in (("amber", "fixture permission", "PermissionRequest"),
                               ("teal", "fixture busy", "UserPromptSubmit"),
                               ("violet", "fixture review", "Stop")):
        sent = cli("send", ids[name], "--text", text)
        assert sent.returncode == 0, sent.stderr
        receipt = root / f"effects/event-{event}"
        status = p.wait_for(lambda: receipt.read_text() if receipt.exists() else "",
                            f"fixture hook {event}")
        assert status == "0", (name, event, status)
    assert cli("kill", ids["slate"]).returncode == 0
    reply = owner_request(str(root / "d.sock"), "subscribe", {
        "op":"set_tag", "session_id":ids["teal"], "tag":"fixture",
    })
    assert reply["op"] == "ok", reply

    def grouped():
        rows, = roster()
        groups = {row["name"]:row["group"] for row in rows}
        expected = {"amber":"needs_input", "teal":"working",
                    "violet":"ready_for_review", "slate":"completed"}
        return rows if groups == expected else None

    rows = p.wait_for(grouped, "mixed public roster")
    state = [{key: row[key] for key in ("name", "group", "status") if key in row} for row in rows]
    command = [tmux, "-S", root / "tmux.sock", "-f", "/dev/null"]
    def run(*argv):
        proc = subprocess.run([*command, *argv], env=p.environment(root), cwd=root / "work",
                              capture_output=True, timeout=4)
        assert proc.returncode == 0, (argv, proc.stderr)
        return proc.stdout

    def snapshot(cols, height):
        raw = run("capture-pane", "-pe", "-S", "0", "-E", str(height - 1), "-t", "roster:0.0")
        fmt = "|".join(f"#{{{mode}}}" for mode in TMUX_MODES)
        modes = run("display-message", "-p", "-t", "roster:0.0", fmt).decode().strip().split("|")
        return {"ansi_base64": base64.b64encode(raw).decode(), "cells": resolve_cells(raw, cols, height),
                "terminal_modes": dict(zip(TMUX_MODES, modes, strict=True))}

    def capture(cols, height, fragment):
        def observed():
            frame = snapshot(cols, height)
            text = "\n".join("".join(cell[0] for cell in row) for row in frame["cells"]).lower()
            if fragment in ("more", "options"):
                return frame if fragment in text.splitlines()[0].strip() else None
            if fragment.startswith("selected:"):
                label = fragment.split(":", 1)[1]
                return frame if any(line.startswith("▌ ") and label in line for line in text.splitlines()) else None
            return frame if fragment in text else None
        return p.wait_for(observed, f"{cols}x{height} {fragment}")

    def key(*names):
        run("send-keys", "-t", "roster:0.0", *names)

    frames = []
    for cols, height in ((120, 24), (80, 24), (40, 8), (24, 4)):
        run("new-session", "-d", "-s", "roster", "-x", str(cols), "-y", str(height),
            "-c", root / "work", root / "bin/board")
        initial = capture(cols, height, "amber")
        key("Left")
        folded = capture(cols, height, "▌ needs input")
        key("Right")
        expanded = capture(cols, height, "amber")
        case = {"cols":cols, "rows":height, "initial":initial, "folded":folded, "expanded":expanded}
        key("Down")
        case["down"] = capture(cols, height, "selected:teal")
        key("Up")
        case["up"] = capture(cols, height, "selected:amber")
        key("g")
        case["attention"] = capture(cols, height, "selected:violet")
        key("g")
        capture(cols, height, "selected:amber")
        if cols == 40:
            key("End")
            case["bottom"] = capture(cols, height, "selected:slate")
            run("send-keys", "-H", "-t", "roster:0.0", *(f"{byte:02x}" for byte in b"\x1b[<64;10;4M"))
            case["wheel"] = capture(cols, height, "show (")
            key("Home")
            capture(cols, height, "selected:amber")
        if cols == 80:
            run("resize-window", "-t", "roster:0", "-x", "40", "-y", "8")
            case["resized"] = capture(40, 8, "approval")
            run("resize-window", "-t", "roster:0", "-x", str(cols), "-y", str(height))
            capture(cols, height, "group: ")
        if cols >= 40:
            key("m")
            case["more"] = capture(cols, height, "more")
            key("o")
            case["options"] = capture(cols, height, "options")
        frames.append(case)
        run("kill-session", "-t", "roster")

    for name in ("amber", "teal", "violet"):
        assert cli("kill", ids[name]).returncode == 0
    daemon.terminate()
    daemon.wait(timeout=5)
    p.write_json(root / "grid.json", {"roster_frames":frames, "roster":state,
                                      "form_cwd":str(root / "work"), "first_divergence":None})


def compare_rosters(reference, candidate):
    ref, rust = (json.loads(Path(path).read_bytes()) for path in (reference, candidate))
    assert ref["tmux"] == rust["tmux"] and ref["form_cwd"] == rust["form_cwd"]
    assert {s["name"]:s["group"] for s in ref["roster"]} == {s["name"]:s["group"] for s in rust["roster"]}
    mismatches = 0
    for left, right in zip(ref["roster_frames"], rust["roster_frames"], strict=True):
        assert left.keys() == right.keys() and (left["cols"], left["rows"]) == (right["cols"], right["rows"])
        for frame in (key for key in left if key not in ("cols", "rows")):
            delta = []
            for y, (expected, observed) in enumerate(zip(left[frame]["cells"], right[frame]["cells"], strict=True)):
                for x, (a, b) in enumerate(zip(expected, observed, strict=True)):
                    if a != b:
                        delta.append((x, y, a, b))
            print(f"{left['cols']}×{left['rows']} {frame}: {len(delta)} differing cells")
            if delta:
                print(f"  first difference {delta[0]}")
            for mode, expected in left[frame]["terminal_modes"].items():
                actual = right[frame]["terminal_modes"][mode]
                if expected != actual:
                    print(f"  {mode}: {expected} → {actual}")
                    mismatches += 1
            mismatches += len(delta)
    if mismatches:
        raise SystemExit(1)


def compare_forms(reference, candidate):
    ref, rust = (json.loads(Path(path).read_bytes()) for path in (reference, candidate))
    assert ref["tmux"] == rust["tmux"], "form receipts use different terminal engines"
    assert ref["form_cwd"] == rust["form_cwd"], "form receipts use different workspace paths"
    expected_sizes = [(80, 24), (40, 8), (24, 4)]
    assert [(f["cols"], f["rows"]) for f in ref["form_frames"]] == expected_sizes
    mismatches = 0
    for r, c in zip(ref["form_frames"], rust["form_frames"], strict=True):
        assert (r["cols"], r["rows"]) == (c["cols"], c["rows"])
        for landmark in ("directory", "agent"):
            count = 0
            for y, (rrow, crow) in enumerate(zip(r[landmark]["cells"], c[landmark]["cells"], strict=True)):
                for x, (expected, actual) in enumerate(zip(rrow, crow, strict=True)):
                    if expected != actual:
                        if count < 3:
                            print(f"{r['cols']}×{r['rows']} {landmark} cell {x},{y} expected {expected!r} got {actual!r}")
                        count += 1
            print(f"{r['cols']}×{r['rows']} {landmark} — {count} cell mismatches")
            mismatches += count
    if mismatches:
        raise SystemExit(1)


def run(supervisor, client, tmux=None, evidence=None, cycle=False, automations=False, form_grid=False,
        form_root=None, board_grid=False, handoff=False, touch=False):
    scratch = Path("/tmp").resolve()
    if form_root is None:
        root = Path(tempfile.mkdtemp(prefix="pb-", dir=scratch))
    else:
        root = form_root.resolve()
        if not (form_grid or board_grid) or root.parent != scratch:
            raise ValueError("form root must be a new direct child of /tmp")
        root.mkdir(mode=0o700)
    child = driver = None
    previous = {}
    try:
        for name in ("bin", "tmp", "home", "work", "runtime", "effects", "state"):
            (root / name).mkdir(mode=0o700)
        for source, name in ((supervisor, "swarm"), (client, "board"), (DRIVER, "driver"),
                             (DRIVER, "swarm-parity"), (p.FIXTURE, "claude"),
                             (Path(__file__).resolve(), "test_board.py")):
            dest = root / "bin" / name
            shutil.copyfile(source, dest)
            dest.chmod(0o700)
        p.table()
        command = [*p.guard(root), sys.executable, root / "bin/test_board.py", "_inside", root]
        if handoff:
            command.append("--handoff-board")
        elif automations:
            command.append("--automations")
        elif cycle:
            command.append("--cycle")
        elif touch:
            command.append("--touch-back")
        elif tmux is not None:
            command.append(tmux)
            if form_grid:
                command.append("--form-grid")
            elif board_grid:
                command.append("--board-grid")
        child = subprocess.Popen(command, cwd=root / "work", env=p.environment(root),
                                 start_new_session=True)
        driver = (child.pid, p.generation(child.pid))
        until = time.monotonic() + 30
        while child.poll() is None:
            p.owned(root, driver, previous)
            if time.monotonic() >= until:
                raise RuntimeError("board qualification timed out")
            time.sleep(0.05)
        captured = root / "grid.json"
        if evidence is not None and captured.exists():
            receipt = json.loads(captured.read_bytes())
            receipt["tmux"] = subprocess.check_output([tmux, "-V"], text=True).strip()
            receipt["board_sha256"] = p.hash_file(client)
            receipt["supervisor_sha256"] = p.hash_file(supervisor)
            p.write_json(evidence, receipt, replace=False)
            if receipt["first_divergence"] is not None:
                raise RuntimeError(f"terminal grid or mode changed; captured in {evidence}")
        if child.returncode != 0:
            raise RuntimeError(f"board qualification exited {child.returncode}")
    finally:
        cleanup = p.reap(root, driver, previous)
        if cleanup["remaining"]:
            raise RuntimeError(f"fixture cleanup has survivors in {root}: {cleanup}")
        if child is not None:
            child.wait(timeout=2)
        shutil.rmtree(root)
    exercise = ("attached CLI button, native mouse and terminal restoration" if touch else
                "supervised and hands-off handoff" if handoff else
                "comparative roster grids" if board_grid else
                "comparative launch-form grids" if form_grid else
                "local automation CLI and owner dashboard catalog/disconnect" if automations else
                "running-session cycling" if cycle else "board launch, attach and live reattach")
    print(f"{exercise} {'captured' if form_grid or board_grid else 'passed'}; cleanup {cleanup}")


if __name__ == "__main__":
    if sys.argv[1:2] == ["--compare-rosters"] and len(sys.argv) == 4:
        compare_rosters(sys.argv[2], sys.argv[3])
    elif sys.argv[1:2] == ["--compare-forms"] and len(sys.argv) == 4:
        compare_forms(sys.argv[2], sys.argv[3])
    elif sys.argv[1:2] == ["--compare-grids"] and len(sys.argv) == 4:
        compare_grid_receipts(sys.argv[2], sys.argv[3])
    elif len(sys.argv) in (3, 4, 5) and sys.argv[1] == "_inside":
        mode = sys.argv[3] if len(sys.argv) >= 4 else None
        witness(Path(sys.argv[2]), Path(mode) if mode and not mode.startswith("--") else None,
                mode == "--cycle", mode == "--automations", sys.argv[-1] == "--form-grid",
                sys.argv[-1] == "--board-grid", mode == "--handoff-board", mode == "--touch-back")
    else:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument("--supervisor", required=True, type=p.binary_path)
        parser.add_argument("--board", required=True, type=p.binary_path)
        modes = parser.add_mutually_exclusive_group()
        modes.add_argument("--tmux", type=p.binary_path)
        modes.add_argument("--automations", action="store_true", help="read the owner automation catalog from CLI and board")
        modes.add_argument("--handoff-board", action="store_true", help="exercise supervised and capability-gated hands-off handoff")
        modes.add_argument("--touch-back", action="store_true",
                           help="drive the Options toggle and attached native mouse and footer gestures")
        modes.add_argument("--cycle", action="store_true", help="drive two live sessions through next, previous and detach")
        parser.add_argument("--grid-evidence", type=Path)
        parser.add_argument("--form-root", type=Path, help="reuse the same new /tmp path in sequential form arms")
        grids = parser.add_mutually_exclusive_group()
        grids.add_argument("--form-grid", action="store_true", help="capture launch form across reference terminal sizes")
        grids.add_argument("--board-grid", action="store_true", help="capture populated roster across reference terminal sizes")
        args = parser.parse_args()
        if bool(args.tmux) != bool(args.grid_evidence):
            parser.error("--tmux and --grid-evidence are required together")
        if (args.form_grid or args.board_grid) and not args.tmux:
            parser.error("--form-grid requires --tmux")
        run(args.supervisor, args.board, args.tmux, args.grid_evidence, args.cycle, args.automations,
            args.form_grid, args.form_root, args.board_grid, args.handoff_board, args.touch_back)
