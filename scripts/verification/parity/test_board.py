#!/usr/bin/env python3
"""Run Rust board launch and reattach after daemon SIGKILL on a real local PTY."""

import argparse
import base64
import copy
import fcntl
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


def witness(root, tmux=None, cycle=False, automations=False, form_grid=False, board_grid=False, handoff=False, touch=False, menu_grid=False, edit_rows=False, edit_grid=False, manager_options=False, manager_grid=False, context_options=False, context_grid=False, relocate_board=False, scheduled_enter=False, relocation_grid=False, static_grid=False, roster_motion=False, layout_options=False, managed=None, handoff_grid=False):
    env = p.environment(root)
    cwd = root / "work"
    binary = managed / "swarm" if managed else root / "bin/swarm"

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
        def hello():
            if result.poll() is not None:
                raise RuntimeError((root / "daemon-capture").read_text(errors="replace"))
            try:
                return owner_request(env["SWARM_DAEMON_SOCK"], "subscribe", None)
            except OSError:
                return None

        # The CLI can itself try to bring up an owner before the test daemon
        # has claimed its socket. Wait on the wire before running any CLI.
        p.wait_for(hello, "public owner hello")
        p.wait_for(roster, "public owner roster")
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
        p.wait_for(lambda: (root / "effects/size").exists(), "fixture worker raw terminal")

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
            board.wait_bytes("claude\x1b[0;38;5;60m ▸".encode())
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
            board.wait_bytes("claude\x1b[0;38;5;60m ▸".encode(), begin)
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
            board.send(b"\x1b[27u\x1b[27u\x1b[27u")
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
            board.wait_bytes("active · Fixture automation · 2099-01-01 01:00 · UTC (fixed)".encode())
            board.send(b"\x1b[<0;4;2M\x1b[<32;5;2M\x1b[<0;4;2m")
            board.send(b"\x1b[<0;4;2M\x1b[<0;4;2m")
            board.wait_bytes(b"Status")
            board.send(b"h")
            board.wait_bytes(b"No runs for this automation")

            def step(keys, landmark):
                start = len(board.capture)
                board.send(keys)
                board.wait_bytes(landmark, start)

            step(b"\x1b[D", b"Status")
            step(b"\x1b[27u", b"[New (n)]")
            step(b"v", b"No jobs in this view.")
            step(b"\r", b"No jobs in this view.")
            step(b"v", b"Fixture automation")
            step(b"\x1b[<0;2;24M\x1b[<0;2;24m", b"New automation")
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
            step(b"p", b"Edit automation")
            assert shown(created_id) == edited
            step(b"\x1b[27u", b"Status")
            resumed = cli("schedule", "resume", created_id)
            assert resumed.returncode == 0 and shown(created_id)["state"] == "active"
            manual = {"op":"schedule_board_manual", "schedule_id":created_id,
                      "intent":{"kind":"run", "previous":None}}
            owner_request(owner_socket, "schedule-board-manual-v1", manual, discard=True)
            def recorded_without_ack():
                runs = history_for(created_id)
                if not runs:
                    return None
                single, = runs
                rejected = [attempt["state"] for attempt in single.get("attempts", [])]
                return single if single["state"] == "exhausted" and rejected == ["rejected"] else None
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
            step(b"\t\r", b"Status")
            board.wait_bytes(f"Run recorded · {first['id']} · exhausted".encode(), offset)
            step(b"r", b"Run recorded")
            assert [run["id"] for run in history_for(created_id)] == [first["id"]]
            step(b"R", b"Run recorded")
            ids = {run["id"] for run in history_for(created_id)}
            assert first["id"] in ids and len(ids) == 2
            stale = {**manual, "intent":{"kind":"run", "previous":first["id"]}}
            assert owner_request(owner_socket, "schedule-board-manual-v1", stale)["op"] == "error"
            assert {run["id"] for run in history_for(created_id)} == ids
            assert not (root / "effects/worker").exists()
            step(b"h", "Open (⏎)".encode())
            step(b"\r", b"Automation run")
            step(b"\x1b[D", "Open (⏎)".encode())
            step(b"\x1b[D", b"Revised from board")
            step(b"p", "Status · paused".encode())
            assert json.loads(cli("schedule", "show", created_id).stdout)["state"] == "paused"
            schedule_db = root / "state/schedules/state.db"
            before_archive = schedule_db.read_bytes()
            step(b"xy", b"Archive Revised from board?")
            assert schedule_db.read_bytes() == before_archive
            step(b"\r", b"Archive Revised from board?")
            assert schedule_db.read_bytes() == before_archive
            step(b"\x1b[27u", b"Status")
            assert schedule_db.read_bytes() == before_archive
            step(b"x", b"Archive Revised from board?")
            assert schedule_db.read_bytes() == before_archive
            before_resize = len(board.capture)
            board.resize(24, 4)
            board.send(b"y")
            board.wait_bytes(b"[esc] [y]", before_resize)
            step(b"\x1b[<0;8;4M\x1b[<0;8;4m", b"[esc] [y]")
            assert schedule_db.read_bytes() == before_archive
            before_resize = len(board.capture)
            board.resize(40, 8)
            board.wait_bytes(b"[esc] [y]", before_resize)
            step(b"y", b"Status")
            board.resize(80, 24)
            assert shown(created_id)["state"] == "archived"
            assert shown(job)["state"] == "active"
            assert {run["id"] for run in history_for(created_id)} == ids

            archived_catalog = schedule_db.read_bytes()
            step(b"\x1b[27u", b"[Active (v)]")
            step(b"\r", "Status · archived".encode())
            step(b"h", "Open (⏎)".encode())
            run_id = history_for(created_id)[0]["id"]
            assert run_id in ids
            step(b"\r", f"Run · {run_id}".encode())
            step(b"\x1b[D", "Open (⏎)".encode())
            step(b"\x1b[D", "Status · archived".encode())
            step(b"\x1b[27u", b"[Active (v)]")
            step(b"v", b"[Archived (v)]")
            step(b"\x1b[<0;4;2M\x1b[<0;4;2m", "Status · active".encode())
            assert schedule_db.read_bytes() == archived_catalog

            daemon.terminate()
            daemon.wait(timeout=5)
            offline = cli("schedule", "list", "--json")
            assert offline.returncode == 0
            assert {row["id"] for row in json.loads(offline.stdout)} == {job, created_id}
            board.wait_bytes(b"daemon connection lost")
            board.send(b"u")
            board.wait_bytes(b"daemon disconnected")
            quit_board(board)
        except Exception:
            board.drain()
            print("automation fixture terminal tail:", repr(bytes(board.capture[-3200:])))
            raise
        finally:
            board.close()
        return

    def terminal(payload, fresh):
        board = p.Board([str(binary if managed else root / "bin/board")], root, env)
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
        except Exception:
            if managed:
                print(f"installed board exit={board.child.poll()}, terminal tail={board.capture[-1000:]!r}", file=sys.stderr)
            raise
        finally:
            board.close()

    if layout_options:
        daemon = start()
        capability = "context-guard-settings"
        has_context = capability in owner_request(env["SWARM_DAEMON_SOCK"], capability, None).get("capabilities", [])
        ids = []
        for name, repo in (("alpha", "repo-a"), ("zulu", "repo-z")):
            working = cwd / repo
            working.mkdir()
            created = cli("spawn", "--cli", "claude", "--name", name, "--dir", working,
                          "--prompt", "Offline layout fixture")
            assert created.returncode == 0, created.stderr
            identity = created.stdout.decode().strip()
            ids.append(identity)
            response = owner_request(env["SWARM_DAEMON_SOCK"], "subscribe",
                                     {"op":"set_tag", "session_id":identity, "tag":repo})
            assert response["op"] == "ok"
        layout = (root / "home/Library/Application Support/swarm/board-layout.json"
                  if sys.platform == "darwin" else root / "home/config/swarm/board-layout.json")

        def saved(group, order):
            if not layout.exists():
                return False
            observed = json.loads(layout.read_bytes())
            return (observed.get("grouping") == group and observed.get("ordering") == order
                    and observed.get("touch_back", False) is False)

        def open_form(board):
            offset = len(board.capture)
            board.send(b"o")
            board.wait_bytes(b"options", offset)
            if has_context:
                board.wait_bytes(b"threshold", offset)

        board = p.Board([str(root / "bin/board")], root, env)
        try:
            board.wait_bytes(b"zulu")
            right = b"\x1b[C"
            down = b"\x1b[B"
            open_form(board)
            board.send(right + down + right * 3 + b"\r")
            p.wait_for(lambda: saved("repo", "name"), "repository grouping and name order persisted")
            offset = len(board.capture)
            open_form(board)
            board.send(right + down + right + b"\r")
            p.wait_for(lambda: saved("tag", "arrival"), "tag grouping and arrival order persisted")
            open_form(board)
            board.send(right + down + right * 2 + b"\x1b[27u")
            board.wait_bytes(b"More (m)", offset)
            board.send(b"\x1b[27u")
            p.wait_for(lambda: (board.drain(), board.child.poll())[1] is not None, "exit after layout")
            assert saved("tag", "arrival") and termios.tcgetattr(board.master) == board.original_mode
        finally:
            board.close()
        board = p.Board([str(root / "bin/board")], root, env)
        try:
            board.wait_bytes(b"group: tag")
            board.wait_bytes(b"order: arrival")
            board.send(b"\x1b[27u")
            p.wait_for(lambda: (board.drain(), board.child.poll())[1] is not None, "exit after layout reload")
            assert termios.tcgetattr(board.master) == board.original_mode
        finally:
            board.close()
        for identity in ids:
            assert cli("kill", identity).returncode == 0
        daemon.terminate()
        daemon.wait(timeout=5)
        return

    if scheduled_enter:
        daemon = start()
        created = cli("spawn", "--cli", "agy", "--name", "scheduled-review", "--dir", cwd,
                      "--prompt", "Offline scheduled review fixture")
        assert created.returncode == 0, created.stderr
        identity = created.stdout.decode().strip()
        assert cli("send", identity, "--text", "finish").returncode == 0
        p.wait_for(lambda: next((row for row in (roster() or ([],))[0]
                   if row["id"] == identity and row["status"]["Process"] == "exited"), None),
                   "stopped scheduled-review fixture")
        daemon.terminate()
        daemon.wait(timeout=5)
        # Seed only the synthetic completed marker while the fixture owner is
        # stopped. Its actual shim and native conversation came from public spawn.
        local = identity.rsplit("/", 1)[-1]
        owned = root / "state" / local
        matches = [path for path in (owned / "meta.json", owned / "session.json") if path.is_file()]
        if not matches:
            matches = [path for path in (root / "state").rglob("session.json")
                       if path.parent.name == local]
        meta, = matches
        persisted = json.loads(meta.read_bytes())
        agent = persisted.get("agent_type", persisted.get("provider", {}).get("name"))
        assert persisted["id"] in (identity, local) and agent == "agy", (persisted["id"], agent)
        persisted.update(tag="automation", scheduled_completed=True, schedule_projected=True)
        meta.write_text(json.dumps(persisted))
        daemon = start()
        marked, = roster()[0]
        assert marked["id"] == identity and marked["tag"] == "automation" and marked["scheduled_completed"]
        board = p.Board([str(root / "bin/board")], root, env)
        try:
            board.wait_bytes(b"scheduled-review")
            board.wait_bytes("◆".encode())
            board.send(b"\r")
            board.wait_bytes(b"AGY fixture ready")
            launched = [json.loads(line) for line in (root / "effects/agy-launches").read_text().splitlines()]
            assert len(launched) == 2 and launched[1]["cwd"] == str(cwd), launched
            args = launched[1]["argv"]
            assert "--conversation" in args and args[args.index("--conversation") + 1] == "11111111-2222-4333-8444-555555555555", args
            offset = len(board.capture)
            board.send(b"\x11")
            board.wait_bytes(b"More (m)", offset)
            board.send(b"\x1b[27u")
            p.wait_for(lambda: (board.drain(), board.child.poll())[1] is not None, "quit after scheduled review")
            assert board.child.returncode == 0 and termios.tcgetattr(board.master) == board.original_mode
        finally:
            board.close()
        for row in (roster() or ([],))[0]:
            if row["status"]["Process"] == "running":
                assert cli("kill", row["id"]).returncode == 0
        daemon.terminate()
        daemon.wait(timeout=5)
        return

    if relocate_board:
        daemon = start()
        source = cwd / "source"
        neighbor = cwd / "neighbor"
        destination = cwd / "destination" / "qualified"
        for folder in (source, neighbor, destination):
            folder.mkdir(parents=True)
        identities = {}
        trails = {
            "original-agy": "11111111-2222-4333-8444-555555555555",
            "neighbor-agy": "22222222-3333-4444-8555-666666666666",
        }

        def sessions():
            current = roster()
            return current[0] if current else []

        def launch(folder, label):
            result = cli("spawn", "--cli", "agy", "--name", label, "--dir", folder,
                         "--prompt", "Offline classic fixture")
            assert result.returncode == 0, result.stderr
            return result.stdout.decode().strip()

        source_id = launch(source, "original-agy")
        identities["neighbor-agy"] = launch(neighbor, "neighbor-agy")
        identities["original-agy"] = source_id
        for label, identity in identities.items():
            assert cli("send", identity, "--text", "finish").returncode == 0
            try:
                p.wait_for(lambda: any(row["id"] == identity and row["status"]["Process"] == "exited"
                                     for row in sessions()), f"stopped {label} with native identity")
            except RuntimeError as error:
                raise AssertionError((sessions(), cli("peek", identity).stdout)) from error
            # Older Go list projections omit native conversation identity. The
            # provider argv assertion below independently verifies the captured ID.
            observed = next(row for row in sessions() if row["id"] == identity)
            assert observed.get("conversation_id") in (None, "", trails[label]), observed
        source.rename(cwd / "retired-checkout")
        effects = root / "effects/agy-launches"

        def argv():
            return [json.loads(line) for line in effects.read_text().splitlines()]

        def assert_launch(label, folder, index):
            expected = trails[label]
            entry = p.wait_for(lambda: argv()[index] if len(argv()) > index else None, f"native {label} resume launch")
            args = entry["argv"]
            assert entry["cwd"] == str(folder), entry
            assert "--conversation" in args and args[args.index("--conversation") + 1] == expected, entry
            return entry

        board = p.Board([str(root / "bin/board")], root, env)
        try:
            board.wait_bytes(b"original-agy")
            board.send(b"r")
            board.wait_bytes(b'original-agy" cannot be resumed')
            board.send(b"\x15" + os.fsencode(cwd / "missing-destination") + b"\r")
            board.wait_bytes(b"cannot access directory")
            assert len(argv()) == 2
            offset = len(board.capture)
            board.send(b"\x1b[27u")
            board.wait_bytes(b"More (m)", offset)
            board.send(b"\x1b[B")
            offset = len(board.capture)
            board.send(b"r")
            board.wait_bytes(b"AGY fixture ready", offset)
            assert_launch("neighbor-agy", neighbor, 2)
            assert len([entry for entry in argv() if entry["cwd"] == str(destination)]) == 0
            offset = len(board.capture)
            board.send(b"\x11")
            board.wait_bytes(b"More (m)", offset)
            resumed = p.wait_for(lambda: next((row["id"] for row in sessions()
                    if row["id"] not in identities.values() and row["status"]["Process"] == "running"), None),
                    "neighbor successor")
            board.send(b"\x1b[27u")
            p.wait_for(lambda: (board.drain(), board.child.poll())[1] is not None, "quit after neighbor resume")
            assert termios.tcgetattr(board.master) == board.original_mode
            board.close()
            assert cli("kill", resumed).returncode == 0
            p.wait_for(lambda: any(row["id"] == resumed and row["status"]["Process"] == "exited"
                                   for row in sessions()), "neighbor successor worker exit")
            # Delete only the isolated neighbor successor so the undisplaced
            # original is again the first addressable completed row.
            peer = env["SWARM_DAEMON_SOCK"]
            deleted = owner_request(peer, "subscribe", {"op":"delete", "session_id":resumed})
            assert deleted["op"] == "ok", deleted
            board = p.Board([str(root / "bin/board")], root, env)
            board.wait_bytes(b"original-agy")
            offset = len(board.capture)
            board.send(b"r")
            board.wait_bytes(b'original-agy" cannot be resumed', offset)
            board.resize(24, 4)
            board.wait_bytes("Resize to 32×8".encode(), offset)
            offset = len(board.capture)
            board.send(b"\x15" + os.fsencode(destination) + b"\r")
            board.wait_bytes(b"qualified", offset)
            assert len(argv()) == 3
            offset = len(board.capture)
            board.resize(80, 24)
            board.wait_bytes(b"Select a new one to relocate it.", offset)
            board.send(b"\x15" + os.fsencode(destination.parent / "qual") + b"\t\r")
            board.wait_bytes(b"AGY fixture ready", offset)
            assert_launch("original-agy", destination, 3)
            assert len(argv()) == 4
            offset = len(board.capture)
            board.send(b"\x11")
            board.wait_bytes(b"More (m)", offset)
            board.send(b"\x1b[27u")
            p.wait_for(lambda: (board.drain(), board.child.poll())[1] is not None, "quit after relocation")
            assert termios.tcgetattr(board.master) == board.original_mode
        finally:
            board.close()
        for row in sessions():
            if row["status"]["Process"] == "running":
                assert cli("kill", row["id"]).returncode == 0
        daemon.terminate()
        daemon.wait(timeout=5)
        return

    if context_options:
        daemon = start()
        socket_path = env["SWARM_DAEMON_SOCK"]
        capability = "context-guard-settings"
        hello = owner_request(socket_path, capability, None)
        supported = capability in hello.get("capabilities", [])
        board = p.Board([str(root / "bin/board")], root, env)

        def get():
            response = owner_request(socket_path, capability, {"op":"context_guard_get"})
            assert response["op"] == "context_guard_get", response
            return response["context_guard_settings"]

        def set_policy(revision, threshold):
            response = owner_request(socket_path, capability, {
                "op":"context_guard_set", "context_guard_set":{
                    "expected_revision":revision,
                    "auto_compact":{"enabled":True, "threshold_percent":threshold},
                },
            })
            assert response["op"] == "context_guard_set", response
            return response["context_guard_settings"]

        try:
            board.wait_bytes(b"More (m)")
            offset = len(board.capture)
            board.send(b"o")
            board.wait_bytes(b"manager prompt", offset)
            if not supported:
                assert b"Context guard: Codex only" not in board.capture[offset:]
                board.send(b"\x1b[B" * 4)
            else:
                before = get()
                assert before["schema_version"] == 1 and before["revision"] == 0
                assert before["auto_compact"] == {"enabled":False, "threshold_percent":80}
                board.wait_bytes(b"80%", offset)
                board.send(b"\x1b[B" * 4 + b" " + b"\x1b[B")
                board.wait_bytes("█%".encode(), offset)
                board.send(b"\x1525\r")
                board.wait_bytes(b"threshold must be an integer", offset)
                assert get() == before

                board.send(b"\x1592\r")
                staged = p.wait_for(lambda: (value if (value := get())["revision"] == 1 else None),
                                    "saved context-guard revision")
                assert staged["auto_compact"] == {"enabled":True, "threshold_percent":92}
                board.wait_bytes(b"More (m)", offset)
                offset = len(board.capture)
                board.send(b"o")
                board.wait_bytes(b"92%", offset)
                board.send(b"\x1b[B" * 5 + b"\x1575")
                board.wait_bytes(b"75", offset)
                changed = set_policy(1, 81)
                assert changed["revision"] == 2
                board.send(b"\r")
                board.wait_bytes(b"context guard settings changed elsewhere", offset)
                assert get() == changed
            offset = len(board.capture)
            board.send(b"\x1b[27u")
            board.wait_bytes(b"More (m)", offset)
            board.send(b"\x1b[27u")
            p.wait_for(lambda: (board.drain(), board.child.poll())[1] is not None,
                       "quit after Context Guard Options")
            assert board.child.returncode == 0
            assert termios.tcgetattr(board.master) == board.original_mode
            assert roster() == ([],)
        finally:
            board.close()
        daemon.terminate()
        daemon.wait(timeout=5)
        return

    if manager_options:
        daemon = start()
        destination = Path(env["SWARM_DAEMON_STATE"]) / "manager-settings" / "starting-prompt"
        board = p.Board([str(root / "bin/board")], root, env)
        expected = b"Honor logs. Then  inspect."
        try:
            board.wait_bytes(b"More (m)")
            board.send(b"o")
            board.wait_bytes(b"manager prompt")
            board.send(b"\x1b[B" * 3)
            board.wait_bytes(b"enter save prompt")
            board.send(b"\x1b[200~  Honor logs.\r\nThen \tinspect.\x01  \x1b[201~\r")
            board.wait_bytes(b"Manager prompt saved.")
            assert destination.read_bytes() == expected
            assert destination.stat().st_mode & 0o777 == 0o600

            board.send(b"\x15\x1b[200~" + b"X" * 1025 + b"\x1b[201~\r")
            board.wait_bytes(b"1024 bytes")
            assert destination.read_bytes() == expected
            offset = len(board.capture)
            board.send(b"\x1b[27u")
            board.wait_bytes(b"More (m)", offset)
            offset = len(board.capture)
            board.send(b"o")
            board.wait_bytes(expected, offset)
            board.send(b"\x1b[B" * 3)
            board.resize(24, 4)
            board.wait_bytes("Resize to 32×8".encode(), offset)
            board.send(b"\x15unsafe\r")
            assert destination.read_bytes() == expected
            offset = len(board.capture)
            board.send(b"\x1b[27u")
            board.wait_bytes(b"More (m)", offset)
            board.send(b"\x1b[27u")
            p.wait_for(lambda: (board.drain(), board.child.poll())[1] is not None,
                       "quit after manager prompt")
            assert board.child.returncode == 0
            assert termios.tcgetattr(board.master) == board.original_mode
            assert destination.read_bytes() == expected and roster() == ([],)
        finally:
            board.close()
        daemon.terminate()
        daemon.wait(timeout=5)
        return

    if edit_rows:
        daemon = start()
        launched = cli("spawn", "--cli", "claude", "--name", "edit-amber",
                       "--dir", cwd, "--prompt", "offline fixture")
        assert launched.returncode == 0, launched.stderr
        identity = launched.stdout.decode().strip()
        board = p.Board([str(root / "bin/board")], root, env)

        def session(**expected):
            current = roster()
            return bool(current and any(row["id"] == identity and all(row.get(key) == value for key, value in expected.items())
                                        for row in current[0]))

        def commit(keys, field, value):
            board.send(keys + b"\r")
            p.wait_for(lambda: session(**{field: value}), f"{field} persisted through board")

        try:
            board.wait_bytes(b"edit-amber")
            offset = len(board.capture)
            board.send(b"e")
            board.wait_bytes("█".encode(), offset)
            board.wait_bytes("type  ←→ move".encode(), offset)
            commit(b"\x15suffix\x1b[1;9Damber-", "name", "amber-suffix")
            board.wait_bytes(b"amber-suffix")
            offset = len(board.capture)
            board.send(b"t")
            board.wait_bytes(b"edit tag", offset)
            commit(b"fixture-tag", "tag", "fixture-tag")

            board.resize(24, 4)
            offset = len(board.capture)
            board.send(b"\x18")
            board.wait_bytes("Resize to 32×8".encode(), offset)
            board.send(b"y")
            assert any(row["id"] == identity and row["status"]["Process"] == "running" for row in roster()[0])
            board.send(b"n")
            board.wait_bytes(b"amber", offset)
            board.resize(80, 24)
            offset = len(board.capture)
            board.send(b"\x18")
            board.wait_bytes(b"kill? y/n", offset)
            board.send(b"n")
            board.wait_bytes(b"More (m)", offset)
            assert any(row["id"] == identity and row["status"]["Process"] == "running" for row in roster()[0])
            offset = len(board.capture)
            board.send(b"\x18")
            board.wait_bytes(b"kill? y/n", offset)
            board.send(b"y")
            p.wait_for(lambda: session(group="completed"), "kill retained target identity")
            board.wait_bytes(b"ended", offset)

            offset = len(board.capture)
            board.send(b"\x18")
            board.wait_bytes(b"delete? y/n", offset)
            board.send(b"y")
            p.wait_for(lambda: roster() == ([],), "deleted target disappeared from actual roster")
            board.send(b"\x1b[27u")
            p.wait_for(lambda: (board.drain(), board.child.poll())[1] is not None, "board exit after edit")
            assert board.child.returncode == 0
            assert termios.tcgetattr(board.master) == board.original_mode
        finally:
            board.close()
        daemon.terminate()
        daemon.wait(timeout=5)
        return

    if touch:
        daemon = start()
        (root / "effects/touch-native").touch()
        launched = cli("spawn", "--cli", "claude", "--name", "touch-amber",
                       "--dir", cwd, "--prompt", "offline fixture")
        assert launched.returncode == 0, launched.stderr
        identity = launched.stdout.decode().strip()
        logged = root / "effects/input"
        report = b"\x1b[?1000;1$y"
        p.wait_for(lambda: logged.exists() and logged.read_bytes() == report,
                   "emulator reply with no attachment")
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
            marker(press + release + b"disabled", report + press + release + b"disabled\r")
            offset = len(board.capture)
            board.send(b"\x11")
            board.wait_bytes(b"More (m)", offset)
            board.send(b"o")
            board.wait_bytes(b"CLI button", offset)
            if "context-guard-settings" in owner_request(
                env["SWARM_DAEMON_SOCK"], "context-guard-settings", None,
            ).get("capabilities", []):
                board.wait_bytes(b"threshold", offset)
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
            board.send(b"query mouse\r")
            wanted += b"query mouse\r" + report
            p.wait_for(lambda: logged.read_bytes() == wanted, "one attached emulator reply")
            board.wait_bytes(b"fixture queried", offset)
            emitted = board.capture[offset:]
            restore = emitted.find(b"\x1b[?1000h")
            query = emitted.find(b"\x1b[?1000$p")
            assert 0 <= restore < query, emitted
            board.wait_bytes(b"ctrl+q returns to swarm", offset)
            external = root / "effects/terminal-mouse-reply"
            if external.exists():
                physical = external.read_bytes()
                assert physical == report, physical
                pasted = b"\x1b[200~" + physical + b"\x1b[201~"
                board.send(pasted)
                wanted += pasted
                board.send(physical[:5])
                board.send(physical[5:])
                board.send(physical)
                wanted += physical  # The expected reply was consumed. This copy is unsolicited.
            marker(press + release + b"native after query",
                   wanted + press + release + b"native after query\r")
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
        if handoff_grid:
            handoff_grid_witness(root, cli, roster, start, tmux, env)
        elif relocation_grid:
            relocation_grid_witness(root, cli, roster, start, tmux)
        elif form_grid:
            launch_grid_witness(root, roster, start, tmux)
        elif board_grid or menu_grid or edit_grid or manager_grid or context_grid or static_grid or roster_motion:
            roster_grid_witness(root, cli, roster, start, tmux, menu_grid, edit_grid, manager_grid,
                                context_grid, static_grid, roster_motion)
        else:
            grid_witness(root, cli, roster, start, tmux)
        return

    def writer_held():
        with (managed / "writers.lock").open("rb") as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                return True
            return False

    def checked(*args):
        result = cli(*args)
        assert result.returncode == 0, (args, result.stderr)
        return result.stdout

    if managed:
        card = json.loads(checked("release-info"))
        generation = binary.resolve(strict=True)
        assert not writer_held()
        assert not list((managed / "effects").iterdir())
    (cwd / "qualified").mkdir()
    daemon = start()
    if managed:
        daemon_before = (daemon.pid, p.generation(daemon.pid))
        assert Path(p.executable(daemon.pid)).resolve() == generation
        assert writer_held()
    before = terminal(b"before", True)
    rows, = roster()
    assert len(rows) == 1 and rows[0]["name"] == "amber"
    assert Path(rows[0]["cwd"]) == cwd / "qualified"
    identity = rows[0]["id"]
    daemon.kill()
    daemon.wait(timeout=5)
    assert p.generation(before[0]) == before[1]
    if managed:
        # The old daemon is dead. The retained shim still owns the writer lease.
        assert writer_held()
        inflight = {name.name: name.read_text() for name in (managed / "inflight").iterdir()}
        assert len(inflight) == 1 and set(inflight.values()) == {generation.parent.name}
    daemon = start()
    if managed:
        daemon_after = (daemon.pid, p.generation(daemon.pid))
        assert daemon_after != daemon_before
        assert Path(p.executable(daemon.pid)).resolve() == generation
    rows, = roster()
    assert [row["id"] for row in rows] == [identity]
    assert terminal(b"after", False) == before
    assert (root / "effects/input").read_bytes() == b"before\rafter\r"
    assert cli("kill", identity).returncode == 0
    daemon.terminate()
    daemon.wait(timeout=5)
    if managed:
        p.wait_for(lambda: not writer_held(), "managed writer lease released")
        assert not list((managed / "inflight").iterdir())
        assert binary.resolve(strict=True) == generation
        assert (managed / "effects" / generation.parent.name).is_file()
        return {"platform": sys.platform, "machine": os.uname().machine,
                "launcher": str(binary), "generation": str(generation), "release": card,
                "binary_sha256": p.hash_file(generation),
                "daemon_before": daemon_before, "daemon_after": daemon_after,
                "worker": before, "session": identity, "writer_held_after_daemon_death": True,
                "writer_released_after_exit": True, "terminal_restored": True}


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
                                and (not settled or "● claude" in text or "◂ claude ▸" in text)) else None
        return p.wait_for(observed, f"{cols}x{rows} {fragments!r}")

    daemon = start()
    frames = []
    (root / "work/point").mkdir()
    (root / "work/paint").mkdir()
    far_typo = "nested-workspace-title-for-explicit-confirmation-before-creation-typo"
    for i in range(5):
        (root / "work" / (far_typo + str(i))).mkdir()
    for cols, rows in ((40, 8), (24, 4), (80, 24)):
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
        result = {"cols": cols, "rows": rows, "directory": directory, "agent": agent}
        if not compact_block:
            run("send-keys", "-t", "form:0.0", "Up", "Up", "Up", "C-u")
            run("send-keys", "-t", "form:0.0", "-l", "ponit")
            landmark(("ponit█",), cols, rows)
            run("send-keys", "-t", "form:0.0", "Enter")
            result["directory_typo"] = landmark(("does not exist",), cols, rows)
            assert not (root / "work/ponit").exists()
            assert roster() == ([],)
            run("send-keys", "-t", "form:0.0", "Right")
            result["directory_typo_corrected"] = landmark(("point█",), cols, rows)
            assert not (root / "work/ponit").exists()
            assert roster() == ([],)
            if cols == 40:
                run("send-keys", "-t", "form:0.0", "C-u")
                run("send-keys", "-t", "form:0.0", "-l", far_typo)
                landmark(("creation-typo█",), cols, rows)
                run("send-keys", "-t", "form:0.0", "Enter")
                result["directory_typo_unread"] = landmark(("pgup/down read",), cols, rows)
                run("send-keys", "-t", "form:0.0", "Enter", "PageDown", "PageDown", "PageDown")
                result["directory_typo_read"] = landmark(("▌ confirm · enter",), cols, rows)
                assert not (root / "work" / far_typo).exists() and roster() == ([],)
            if cols == 80:
                def launched_in(expected):
                    rows_now = (roster() or ([],))[0]
                    return next((row for row in rows_now if Path(row["cwd"]).resolve() == expected), None)

                run("send-keys", "-t", "form:0.0", "Enter")
                landmark(("fixture ready",), cols, rows)
                chosen = p.wait_for(lambda: launched_in(root / "work/point"), "corrected workdir launched")
                assert not (root / "work/ponit").exists()
                run("send-keys", "-t", "form:0.0", "C-q")
                landmark(("(n)", "more (m)"), cols, rows)
                run("send-keys", "-t", "form:0.0", "n", "C-u")
                run("send-keys", "-t", "form:0.0", "-l", "brand-new")
                landmark(("brand-new█",), cols, rows, settled=True)
                run("send-keys", "-t", "form:0.0", "Enter")
                landmark(("does not exist",), cols, rows)
                run("send-keys", "-t", "form:0.0", "Down", "Up", "Enter")
                landmark(("does not exist",), cols, rows)
                assert not (root / "work/brand-new").exists()
                run("send-keys", "-t", "form:0.0", "Enter")
                landmark(("fixture ready",), cols, rows)
                created = p.wait_for(lambda: launched_in(root / "work/brand-new"), "confirmed directory launched")
                assert (root / "work/brand-new").is_dir()
                for session in (chosen, created):
                    proc = subprocess.run([root / "bin/swarm", "kill", session["id"]],
                                          env=p.environment(root), capture_output=True, timeout=5)
                    assert proc.returncode == 0, proc.stderr
                result["workspace_action"] = {"selected": "point", "confirmed": "brand-new"}
        frames.append(result)
        run("kill-session", "-t", "form")
    opencode = root / "bin/opencode"
    opencode.write_text("#!/bin/sh\n[ \"$1\" = --version ] || exit 1\nexec /bin/echo 1.19.3\n")
    opencode.chmod(0o700)
    effects = root / "effects"
    (effects / "cache-probe").touch()
    probes = effects / "cache-probes"

    def probe_ids(event):
        if not probes.exists():
            return set()
        return {line.removeprefix(event + " ") for line in probes.read_text().splitlines()
                if line.startswith(event + " ")}

    run("new-session", "-d", "-s", "form", "-x", "80", "-y", "24",
        "-e", "ANTHROPIC_API_KEY=synthetic-form-key", "-c", root / "work", root / "bin/board")
    landmark(("more (m)",), 80, 24)
    p.wait_for(lambda: probe_ids("start"), "board prefetched adapter discovery before opening a form")
    run("send-keys", "-t", "form:0.0", "n")
    billing = landmark(("auth: anthropic_api_key from env (api billing)",), 80, 24, settled=True)

    run("send-keys", "-t", "form:0.0", "Escape")
    landmark(("more (m)",), 80, 24)
    baseline = probe_ids("start")
    barrier = effects / "form-cold"
    barrier.touch()
    try:
        run("send-keys", "-t", "form:0.0", "n")
        fresh = p.wait_for(lambda: probe_ids("start") - baseline, "reopened form refreshed provider detection")
        reopen = landmark(("auth: anthropic_api_key from env (api billing)",), 80, 24, settled=True)
        assert billing["cells"] == reopen["cells"]
        assert fresh - probe_ids("finish"), "cached frame must precede the fresh version result"
        run("send-keys", "-t", "form:0.0", "Down", "Down", "Down", "Left", "Left")
        busy = landmark(("status: busy-only (no idle signal)",), 80, 24)
        run("resize-window", "-t", "form:0", "-x", "40", "-y", "8")
        narrow_busy = landmark(("status: busy-only (no idle signal)",), 40, 8)
        run("send-keys", "-t", "form:0.0", "Right", "Right")
        narrow_billing = landmark(("anthropic_api_key from env",), 40, 8, settled=True)
        for capture in (billing, reopen, busy, narrow_busy, narrow_billing):
            assert b"synthetic-form-key" not in base64.b64decode(capture["ansi_base64"])
    finally:
        barrier.unlink()
        run("kill-session", "-t", "form")
    frames[-1].update(claude_billing=billing, reopened_cached=reopen, opencode_status=busy)
    frames[0].update(claude_billing=narrow_billing, opencode_status=narrow_busy)
    frames.sort(key=lambda frame: -frame["cols"])
    daemon.terminate()
    daemon.wait(timeout=5)
    p.write_json(root / "grid.json", {"form_frames": frames, "form_cwd": str(root / "work"), "first_divergence": None})


def handoff_grid_witness(root, cli, roster, start, tmux, env):
    env["ANTHROPIC_API_KEY"] = "fixture-handoff-only"
    (root / "home/.claude").mkdir(mode=0o700, exist_ok=True)
    (root / "effects/board-events").touch()
    daemon = start()
    launch = cli("spawn", "--cli", "claude", "--name", "amber", "--dir", root / "work",
                 "--prompt", "Fixture handoff form")
    assert launch.returncode == 0, launch.stderr
    identity = launch.stdout.decode().strip()
    p.wait_for(lambda: (root / "effects/size").exists(), "handoff source worker")
    assert cli("send", identity, "--text", "fixture permission").returncode == 0
    p.wait_for(lambda: any(row["id"] == identity and row["group"] == "needs_input"
                           for row in (roster() or ([],))[0]), "handoff source approval")
    server = [tmux, "-S", root / "tmux.sock", "-f", "/dev/null"]
    pane = "handoff:0.0"

    def run(*args):
        result = subprocess.run([*server, *args], env=env, cwd=root / "work",
                                capture_output=True, timeout=4)
        assert result.returncode == 0, (args, result.stderr)
        return result.stdout

    def key(*names):
        run("send-keys", "-t", pane, *names)

    run("new-session", "-d", "-s", "handoff-anchor", "-c", root / "work", "/bin/sleep", "30")

    def collect(binary):
        frames = []
        cols, rows = 118, 26
        run("new-session", "-d", "-s", "handoff", "-x", str(cols), "-y", str(rows),
            "-c", root / "work", root / "bin" / binary)

        def capture(label, *fragments, absent=()):
            text = None

            def observed():
                nonlocal text
                raw = run("capture-pane", "-pe", "-S", "0", "-E", str(rows - 1), "-t", pane)
                cells = resolve_cells(raw, cols, rows)
                text = ["".join(cell[0] for cell in line).rstrip() for line in cells]
                haystack = "\n".join(text)
                if all(fragment in haystack for fragment in fragments) and not any(
                    fragment in haystack for fragment in absent
                ):
                    fmt = "|".join(f"#{{{mode}}}" for mode in TMUX_MODES)
                    modes = run("display-message", "-p", "-t", pane, fmt).decode().strip().split("|")
                    return {"label":label, "cols":cols, "rows":rows, "text":text, "cells":cells,
                            "ansi_base64":base64.b64encode(raw).decode(),
                            "terminal_modes":dict(zip(TMUX_MODES, modes, strict=True))}
            try:
                frame = p.wait_for(observed, f"{binary}: {cols}×{rows} {label}: {fragments!r}")
            except RuntimeError:
                p.write_json(root / "capture-timeout.json", {
                    "arm":binary, "label":label, "last_rows":text, "prior_frames":frames,
                }, replace=False)
                raise
            frames.append(frame)

        capture("source", "amber", "approval")
        key("h")
        capture("opened", "▌ target      ◂ agy ▸", "not applicable - no supervisor exists",
                "the source may still be running and editing this checkout", "arrows change target")
        cols, rows = 40, 8
        run("resize-window", "-t", "handoff:0", "-x", str(cols), "-y", str(rows))
        capture("compact-target", "Handoff: amber → agy", "Session: amber")
        key("PageDown", "PageDown")
        capture("compact-target-scrolled", "▌ Confirm · enter", absent=("Session: amber",))
        key("Left")
        capture("compact-target-changed", "▌ target      ◂ claude ▸", "Session: amber")
        key("Right")
        cols, rows = 118, 26
        run("resize-window", "-t", "handoff:0", "-x", str(cols), "-y", str(rows))
        capture("next-target", "▌ target      ◂ agy ▸")
        key("Tab")
        capture("model-default", "▌ model       █ (agent default)", "←→ suggestions")
        key("Right")
        capture("model-suggestion", "▌ model       Orchestrator (Carbon)█")
        key("Down")
        capture("supervision", "▌ supervision not applicable - no supervisor exists",
                "arrows change supervision")
        key("Right", "Down")
        capture("method", "▌ method      ◂ hands-off ▸", "arrows change method")
        key("Right")
        capture("supervised-method", "supervision ◂ manual ▸", "▌ method      ◂ supervised ▸")
        key("Enter")
        capture("supervised-refusal", "resolve the permission request before handoff")
        key("Right")
        capture("hands-off-restored", "▌ method      ◂ hands-off ▸",
                absent=("resolve the permission request",))
        key("Enter")
        capture("review", "▌ method      ◂ hands-off ▸", "Orchestrator (Carbon) ◂ ▸",
                "cross-CLI handoff: agy will be told to read a transcript written by claude - y confirm, n cancel")
        key("Left", "Tab", "Enter")
        capture("review-modal", "▌ method      ◂ hands-off ▸", "Orchestrator (Carbon) ◂ ▸",
                "cross-CLI handoff:")
        cols, rows = 64, 14
        run("resize-window", "-t", "handoff:0", "-x", str(cols), "-y", str(rows))
        capture("narrow-review", "▌ method      ◂ hands-off ▸", "cross-CLI handoff:",
                "written by claude - y confirm, n cancel")
        cols, rows = 40, 8
        run("resize-window", "-t", "handoff:0", "-x", str(cols), "-y", str(rows))
        capture("compact-review", "Handoff: amber → agy", "Agent: claude", "↑↓/tab read")
        key("y")
        capture("compact-approval-blocked", "Handoff: amber → agy", "Agent: claude", "↑↓/tab read")
        key("End")
        capture("compact-read", "Handoff: amber → agy", "▌ Confirm · y", "n cancel")
        cols, rows = 24, 4
        run("resize-window", "-t", "handoff:0", "-x", str(cols), "-y", str(rows))
        capture("tiny-review", "Resize to 32×8", "Esc back / n deny")
        key("y")
        capture("tiny-approval-blocked", "Resize to 32×8", "Esc back / n deny")
        cols, rows = 118, 26
        run("resize-window", "-t", "handoff:0", "-x", str(cols), "-y", str(rows))
        capture("restored-review", "cross-CLI handoff:", "y confirm, n cancel")
        key("n")
        capture("negative-answer", "▌ method      ◂ hands-off ▸", "arrows change method",
                absent=("cross-CLI handoff:",))
        key("Enter")
        capture("renewed-review", "cross-CLI handoff:", "y confirm, n cancel")
        key("Escape")
        capture("cancelled-review", "▌ method      ◂ hands-off ▸", "arrows change method",
                absent=("cross-CLI handoff:",))
        key("Escape")
        capture("cancelled-form", "amber", "approval", absent=(" · handoff",))
        assert [row["id"] for row in roster()[0]] == [identity]
        run("kill-session", "-t", "handoff")
        return frames[1:-1]

    expected = collect("reference-board")
    actual = collect("board")
    differences, cursor_coordinates = [], []
    cursor = ("cursor_x", "cursor_y")
    for go, rust in zip(expected, actual, strict=True):
        assert go["label"] == rust["label"]
        mode_go, mode_rust = go["terminal_modes"], rust["terminal_modes"]
        if mode_go["cursor_flag"] == mode_rust["cursor_flag"] == "0":
            if any(mode_go[k] != mode_rust[k] for k in cursor):
                cursor_coordinates.append({"frame":go["label"],
                    "go":{k:mode_go[k] for k in cursor}, "rust":{k:mode_rust[k] for k in cursor}})
            go = {**go, "terminal_modes":{k:v for k,v in mode_go.items() if k not in cursor}}
            rust = {**rust, "terminal_modes":{k:v for k,v in mode_rust.items() if k not in cursor}}
        if first := compare_projection(go, rust):
            differences.append({"frame":go["label"], "difference":first})
    run("kill-session", "-t", "handoff-anchor")
    assert cli("kill", identity).returncode == 0
    daemon.terminate()
    daemon.wait(timeout=5)
    p.write_json(root / "grid.json", {"handoff_reference":expected, "handoff_candidate":actual,
        "fixture_id":identity, "fixture_cwd":str(root / "work"), "differences":differences,
        "cursor_coordinates":cursor_coordinates, "first_divergence":differences[0] if differences else None})


def relocation_grid_witness(root, cli, roster, start, tmux):
    daemon = start()
    source = root / "work" / "source"
    source.mkdir()
    launch = cli("spawn", "--cli", "agy", "--name", "original-agy", "--dir", source,
                 "--prompt", "Offline classic fixture")
    assert launch.returncode == 0, launch.stderr
    identity = launch.stdout.decode().strip()
    assert cli("send", identity, "--text", "finish").returncode == 0
    p.wait_for(lambda: next((row for row in (roster() or ([],))[0]
               if row["id"] == identity and row["status"]["Process"] == "exited"), None),
               "stopped Antigravity source")
    source.rename(root / "work/retired-checkout")
    command = [tmux, "-S", root / "tmux.sock", "-f", "/dev/null"]

    def run(*args):
        result = subprocess.run([*command, *args], env=p.environment(root), cwd=root / "work",
                                capture_output=True, timeout=4)
        assert result.returncode == 0, (args, result.stderr)
        return result.stdout

    def collect(binary):
        frames = []
        for cols, rows in ((80, 24), (40, 8), (24, 4)):
            run("new-session", "-d", "-s", "resume", "-x", str(cols), "-y", str(rows),
                "-c", root / "work", root / "bin" / binary)

            def snapshot(relocating):
                raw = run("capture-pane", "-pe", "-S", "0", "-E", str(rows - 1), "-t", "resume:0.0")
                fmt = "|".join(f"#{{{name}}}" for name in TMUX_MODES)
                modes = dict(zip(TMUX_MODES, run("display-message", "-p", "-t", "resume:0.0", fmt).decode().strip().split("|"), strict=True))
                cells = resolve_cells(raw, cols, rows)
                text = "\n".join("".join(cell[0] for cell in line) for line in cells).lower()
                if relocating and ("▌ directory" not in text or modes["mouse_sgr_flag"] != "0"):
                    return None
                if not relocating and ("original-agy" not in text or modes["mouse_sgr_flag"] != "1"):
                    return None
                return {"ansi_base64":base64.b64encode(raw).decode(), "cells":cells,
                        "terminal_modes":modes}

            p.wait_for(lambda: snapshot(False), "original board row")
            run("send-keys", "-t", "resume:0.0", "r")
            frame = p.wait_for(lambda: snapshot(True), f"relocate {cols}×{rows}")
            frames.append({"cols":cols, "rows":rows, "relocation":frame})
            run("send-keys", "-t", "resume:0.0", "Escape")
            p.wait_for(lambda: snapshot(False), "cancelled relocation")
            run("kill-session", "-t", "resume")
        return frames

    expected, actual = collect("reference-board"), collect("board")
    differences = []
    for left, right in zip(expected, actual, strict=True):
        difference = compare_projection(left["relocation"], right["relocation"])
        if difference is not None:
            differences.append({"cols":left["cols"], "rows":left["rows"], "difference":difference})
    daemon.terminate()
    daemon.wait(timeout=5)
    p.write_json(root / "grid.json", {"relocation_reference":expected, "relocation_candidate":actual,
        "source":identity, "source_directory":str(source), "differences":differences,
        "first_divergence":differences[0] if differences else None})


def roster_grid_witness(root, cli, roster, start, tmux, menu_grid=False, edit_grid=False, manager_grid=False, context_grid=False,
                        static_grid=False, roster_motion=False):
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
        if (edit_grid or static_grid) and name == "teal":
            # A review row makes the edit compositor independent of the
            # animation phase. The ordinary roster witness retains Working.
            text, event = "fixture review", "Stop"
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
        if edit_grid or static_grid:
            expected["teal"] = "ready_for_review"
        return rows if groups == expected else None

    rows = p.wait_for(grouped, "mixed public roster")
    state = [{key: row[key] for key in ("name", "group", "status") if key in row} for row in rows]
    command = [tmux, "-S", root / "tmux.sock", "-f", "/dev/null"]
    def run(*argv):
        proc = subprocess.run([*command, *argv], env=p.environment(root), cwd=root / "work",
                              capture_output=True, timeout=4)
        assert proc.returncode == 0, (argv, proc.stderr)
        return proc.stdout

    def snapshot(cols, height, pane="roster:0.0"):
        raw = run("capture-pane", "-pe", "-S", "0", "-E", str(height - 1), "-t", pane)
        fmt = "|".join(f"#{{{mode}}}" for mode in TMUX_MODES)
        modes = run("display-message", "-p", "-t", pane, fmt).decode().strip().split("|")
        return {"ansi_base64": base64.b64encode(raw).decode(), "cells": resolve_cells(raw, cols, height),
                "terminal_modes": dict(zip(TMUX_MODES, modes, strict=True))}

    capture_arm = None

    def capture(cols, height, fragment, *, menu=False, form=False):
        wait_label = f"{cols}x{height} {fragment}"
        last_frame = None

        def observed():
            nonlocal last_frame
            frame = snapshot(cols, height)
            last_frame = frame
            text = "\n".join("".join(cell[0] for cell in row) for row in frame["cells"]).lower()
            if form and frame["terminal_modes"]["mouse_sgr_flag"] != "0":
                return None
            if menu:
                footer = text.splitlines()[-1].rstrip()
                expected = "[down (↓)]" if cols == 24 else "[back (esc)] [up (↑)] [down (↓)]"
                if not text.startswith("more · ") or footer != expected:
                    return None
            if fragment in ("more", "options"):
                return frame if fragment in text.splitlines()[0].strip() else None
            if fragment.startswith("selected:"):
                label = fragment.split(":", 1)[1]
                return frame if any(line.startswith("▌ ") and label in line for line in text.splitlines()) else None
            return frame if fragment in text else None
        try:
            return p.wait_for(observed, wait_label)
        except RuntimeError as error:
            if static_grid and str(error) == f"deadline waiting for {wait_label}":
                p.write_json(root / "capture-timeout.json", {
                    "kind":"roster_capture_timeout", "arm":capture_arm,
                    "wait_label":wait_label, "frame":last_frame,
                }, replace=False)
            raise

    def key(*names):
        run("send-keys", "-t", "roster:0.0", *names)

    def close_fixture():
        for name in ("amber", "teal", "violet"):
            assert cli("kill", ids[name]).returncode == 0
        daemon.terminate()
        daemon.wait(timeout=5)

    if roster_motion:
        cols, height = 120, 24
        run("new-session", "-d", "-s", "roster", "-x", str(cols), "-y", str(height),
            "-c", root / "work", root / "bin/board")
        capture(cols, height, "teal")
        frames, observed = [], set()
        spinner = set("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

        def observe():
            frame = snapshot(cols, height)
            teal = next(line for line in frame["cells"] if "teal" in "".join(c[0] for c in line))
            glyph = teal[2][0]
            assert glyph in spinner and teal[2][3] == "5fd7af", teal
            elapsed = "".join(cell[0] for cell in teal[74:80]).strip()
            assert elapsed.endswith("s") and elapsed[:-1].isdigit(), elapsed
            state = (glyph, int(elapsed[:-1]))
            if state not in observed:
                observed.add(state)
                frames.append({"phase":state[0], "elapsed_seconds":state[1], "actual":frame})
            return len({glyph for glyph, _ in observed}) >= 4 and len({age for _, age in observed}) >= 2

        p.wait_for(observe, "native spinner changes and elapsed clock advances while idle")
        assert all(next_frame["elapsed_seconds"] >= frame["elapsed_seconds"]
                   for frame, next_frame in zip(frames, frames[1:]))
        run("resize-window", "-t", "roster:0", "-x", "40", "-y", "8")
        capture(40, 8, "teal")
        assert cli("send", ids["teal"], "--text", "fixture permission").returncode == 0
        caption = "teal needs input (approval)"
        banner = capture(40, 8, caption)
        assert caption in "".join(c[0] for c in banner["cells"][1])
        def dismissed():
            frame = snapshot(40, 8)
            text = "\n".join("".join(c[0] for c in row) for row in frame["cells"])
            return frame if caption not in text and "approval" in text else None
        settled = p.wait_for(dismissed, "attention banner expires while approval remains in the roster")
        assert any(row["id"] == ids["teal"] and row["group"] == "needs_input" for row in roster()[0])
        run("kill-session", "-t", "roster")
        close_fixture()
        p.write_json(root / "grid.json", {"motion":frames, "cols":cols, "rows":height,
                                         "banner_shown":banner, "banner_expired":settled,
                                         "first_divergence":None})
        return

    if menu_grid or edit_grid or manager_grid or context_grid or static_grid:
        # Keep the isolated server alive across closing one comparison arm's
        # last pane and opening the next. tmux 3.2a can still be exiting then.
        run("new-session", "-d", "-s", "roster-anchor", "-c", root / "work", "/bin/sleep", "30")
        def click(x, y):
            press = f"\x1b[<0;{x};{y}M\x1b[<0;{x};{y}m".encode()
            run("send-keys", "-H", "-t", "roster:0.0", *(f"{byte:02x}" for byte in press))

        def button(grid, text):
            for y, row in enumerate(grid["cells"], 1):
                line = "".join(cell[0] for cell in row)
                if text in line:
                    return line.index(text) + 1, y
            raise AssertionError(f"Missing {text} in the captured menu")

        def collect_edits(binary):
            frames = []
            for cols, height in ((80, 24), (40, 8), (24, 4)):
                run("new-session", "-d", "-s", "roster", "-x", str(cols), "-y", str(height),
                    "-c", root / "work", root / "bin" / binary)
                capture(cols, height, "selected:amber")
                key("e")
                rename = capture(cols, height, "amber█", form=True)
                key("C-u")
                capture(cols, height, "● █" if cols == 80 else "rename  █", form=True)
                run("send-keys", "-l", "-t", "roster:0.0", "界界X")
                capture(cols, height, "界界x█", form=True)
                key("Left")
                edited = capture(cols, height, "界界█x", form=True)
                key("Escape")
                capture(cols, height, "selected:amber")
                key("t")
                tag = capture(cols, height, "[█]" if cols == 80 else "tag  █", form=True)
                key("Escape")
                capture(cols, height, "selected:amber")
                key("C-x")
                confirm = capture(cols, height, "kill? y/n" if cols == 80 else (
                    "kill this session?" if cols == 40 else "resize to 32"), form=True)
                key("n")
                capture(cols, height, "selected:amber")
                frames.append({"cols":cols, "rows":height, "rename":rename,
                               "edited":edited, "tag":tag, "confirm":confirm})
                run("kill-session", "-t", "roster")
            return frames

        def collect_manager(binary):
            frames = []
            prompt = "Review fixture artifacts."
            prompt_file = root / "state/manager-settings/starting-prompt"
            prompt_file.parent.mkdir(mode=0o700, exist_ok=True)
            if not prompt_file.exists():
                prompt_file.touch(mode=0o600)
                prompt_file.write_text(prompt)
            for cols, height in ((40, 8), (24, 4)):
                run("new-session", "-d", "-s", "roster", "-x", str(cols), "-y", str(height),
                    "-c", root / "work", root / "bin" / binary)
                capture(cols, height, "selected:amber")
                key("o")
                capture(cols, height, "options" if cols == 40 else "resize to 32", form=True)
                key("Down", "Down", "Down")
                focused = capture(cols, height, "▌ manager prompt", form=True)
                key("Escape")
                capture(cols, height, "selected:amber")
                frames.append({"cols":cols, "rows":height, "manager":focused})
                run("kill-session", "-t", "roster")
            assert prompt_file.read_text() == prompt
            return frames

        def collect_context(binary):
            frames = []
            for cols, height in ((120, 24), (80, 24), (40, 8), (24, 4)):
                run("new-session", "-d", "-s", "roster", "-x", "80", "-y", "24",
                    "-c", root / "work", root / "bin" / binary)
                capture(80, 24, "selected:amber")
                key("o")
                capture(80, 24, "threshold", form=True)
                run("resize-window", "-t", "roster:0", "-x", str(cols), "-y", str(height))
                capture(cols, height, "resize to 32" if cols == 24 else "options", form=True)
                key("Down", "Down", "Down")
                selected_manager = capture(cols, height, "▌ manager prompt", form=True)
                manager = selected_manager if cols >= 80 else None
                key("Down")
                compact = capture(cols, height, "▌ auto compact", form=True)
                key("Down")
                threshold = capture(cols, height, "▌ threshold", form=True)
                key("Escape")
                capture(cols, height, "selected:amber")
                frames.append({"cols":cols, "rows":height, "manager":manager,
                               "compact":compact, "threshold":threshold})
                run("kill-session", "-t", "roster")
            return frames

        def collect_static(binary):
            nonlocal capture_arm
            capture_arm = "reference" if binary == "reference-board" else "candidate"
            frames = []
            for cols, height in ((80, 24), (40, 8), (24, 4)):
                run("new-session", "-d", "-s", "roster", "-x", str(cols), "-y", str(height),
                    "-c", root / "work", root / "bin" / binary)
                initial = capture(cols, height, "selected:amber")
                key("Left")
                folded = capture(cols, height, "▌ needs input")
                key("Right")
                expanded = capture(cols, height, "selected:amber")
                key("Down")
                down = capture(cols, height, "selected:violet")
                key("Up")
                up = capture(cols, height, "selected:amber")
                key("g")
                attention = capture(cols, height, "selected:violet")
                key("End")
                bottom = capture(cols, height, "selected:slate")
                frames.append({"cols":cols, "rows":height, "initial":initial, "folded":folded,
                               "expanded":expanded, "down":down, "up":up, "attention":attention,
                               "bottom":bottom})
                run("kill-session", "-t", "roster")
            return frames

        def collect_wide_pair():
            cols, height = 120, 24
            windows = (("reference-roster", "reference-board"), ("candidate-roster", "board"))
            for session, binary in windows:
                run("new-session", "-d", "-s", session, "-x", str(cols), "-y", str(height),
                    "-c", root / "work", root / "bin" / binary)
            scenes = (
                ("initial", None, "amber"),
                ("folded", "Left", "needs input"),
                ("expanded", "Right", "amber"),
                ("down", "Down", "violet"),
                ("up", "Up", "amber"),
                ("attention", "g", "violet"),
                ("bottom", "End", "slate"),
            )
            arms = [{"cols":cols, "rows":height}, {"cols":cols, "rows":height}]

            def sampled(frame, selected):
                ages = []
                seen = False
                for cells in frame["cells"]:
                    text = "".join(c[0] for c in cells).lower()
                    if text.startswith("▌ ") and selected in text:
                        seen = True
                    candidate_age = "".join(c[0] for c in cells[74:80]).strip()
                    if candidate_age and candidate_age[:-1].isdigit():
                        ages.append(candidate_age)
                return ages if seen and ages else None

            for label, key_name, selected in scenes:
                if key_name is not None:
                    for session, _ in windows:
                        run("send-keys", "-t", f"{session}:0.0", key_name)

                def settled():
                    frames = tuple(snapshot(cols, height, f"{session}:0.0") for session, _ in windows)
                    ages = tuple(sampled(frame, selected) for frame in frames)
                    # Capture from a shared real owner in the same elapsed
                    # interval. Compare the original age cells as emitted.
                    if ages[0] and ages[0] == ages[1]:
                        return frames
                    if all(ages):
                        # The pinned board can leave idle age cells unchanged
                        # until an input event. This unbound key leaves selection
                        # and data unchanged and schedules a fresh view.
                        for session, _ in windows:
                            run("send-keys", "-t", f"{session}:0.0", "~")
                    return None

                for arm, frame in zip(arms, p.wait_for(settled, f"120×24 shared age interval for {label}"), strict=True):
                    arm[label] = frame
            for session, _ in windows:
                run("kill-session", "-t", session)
            return arms

        def collect_menu(binary):
            frames = []
            for cols, height in ((120, 24), (80, 24), (40, 8), (24, 4)):
                run("new-session", "-d", "-s", "roster", "-x", str(cols), "-y", str(height),
                    "-c", root / "work", root / "bin" / binary)
                capture(cols, height, "selected:amber")
                key("m")
                initial = capture(cols, height, "selected:open (", menu=True)
                click(*button(initial, "[Down (↓)]"))
                after_down = capture(cols, height, "selected:rename (", menu=True)
                # A press outside the action's rectangle cannot activate a command.
                _, action_row = button(after_down, "▌ [Rename (e)]")
                click(cols, action_row)
                outside = capture(cols, height, "selected:rename (", menu=True)
                click(*button(after_down, "[Up (↑)]"))
                after_up = capture(cols, height, "selected:open (", menu=True)
                resized = None
                if (cols, height) == (80, 24):
                    key("Down")
                    capture(cols, height, "selected:rename (", menu=True)
                    run("resize-window", "-t", "roster:0", "-x", "24", "-y", "4")
                    resized = capture(24, 4, "selected:rename (", menu=True)
                    click(*button(resized, "[Back (esc)]"))
                    capture(24, 4, "selected:amber")
                else:
                    click(*button(after_up, "[Back (esc)]"))
                    capture(cols, height, "selected:amber")
                frames.append({"cols":cols, "rows":height, "more":initial, "down":after_down,
                               "up":after_up, "outside":outside, "resized":resized})
                run("kill-session", "-t", "roster")
            return frames

        collect = (collect_static if static_grid else collect_context if context_grid else collect_manager if manager_grid
                   else collect_edits if edit_grid else collect_menu)
        expected = collect("reference-board")
        actual = collect("board")
        if static_grid:
            left, right = collect_wide_pair()
            expected.insert(0, left)
            actual.insert(0, right)
        differences, coordinates, retired_rows = [], [], []
        for left, right in zip(expected, actual, strict=True):
            for view in (key for key in left if key not in ("cols", "rows")):
                if left[view] is None:
                    assert right[view] is None
                    continue
                if static_grid:
                    go, rust = left[view], right[view]
                    modes_go, modes_rust = go["terminal_modes"], rust["terminal_modes"]
                    keys = {"cursor_x", "cursor_y"}
                    if any(modes_go[k] != modes_rust[k] for k in keys):
                        coordinates.append({"cols":left["cols"],"frame":view,
                                            "go":{k:modes_go[k] for k in sorted(keys)},
                                            "rust":{k:modes_rust[k] for k in sorted(keys)}})
                    # A hidden paint cursor's coordinates are reported separately.
                    # Visible cursors retain all coordinate assertions.
                    if modes_go["cursor_flag"] == modes_rust["cursor_flag"] == "0":
                        go = {**go, "terminal_modes":{k:v for k,v in modes_go.items() if k not in keys}}
                        rust = {**rust, "terminal_modes":{k:v for k,v in modes_rust.items() if k not in keys}}
                    first = compare_projection(go, rust)
                elif context_grid and left["cols"] >= 80:
                    go, rust = left[view], right[view]
                    modes_go, modes_rust = go["terminal_modes"], rust["terminal_modes"]
                    cursor = ("cursor_x", "cursor_y")
                    if any(modes_go[k] != modes_rust[k] for k in cursor):
                        coordinates.append({"cols":left["cols"],"frame":view,
                                            "go":{k:modes_go[k] for k in cursor},
                                            "rust":{k:modes_rust[k] for k in cursor}})
                    first = next(({"mode":key,"go":value,"rust":modes_rust[key]}
                                  for key, value in modes_go.items()
                                  if value != modes_rust[key]
                                  and not (key in cursor and modes_go["cursor_flag"] == modes_rust["cursor_flag"] == "0")),
                                 None)
                    # Only the complete, named former login row is intentionally
                    # absent. Record that difference without changing either arm.
                    for y, (expected_row, observed_row) in enumerate(zip(go["cells"], rust["cells"], strict=True)):
                        if expected_row == observed_row:
                            continue
                        text = "".join(c[0] for c in expected_row).split()
                        if text == ["logins", "Provider", "logins", "→"] and all(
                            cell == [" ", 1, [], None, None] for cell in observed_row
                        ):
                            retired_rows.append({"cols":left["cols"],"frame":view,"row":y,"control":"Provider logins →"})
                        elif first is None:
                            first = next({"x":x,"y":y,"go":before,"rust":after}
                                         for x, (before, after) in enumerate(zip(expected_row, observed_row, strict=True))
                                         if before != after)
                else:
                    first = compare_projection(left[view], right[view])
                if first is not None:
                    differences.append({"cols":left["cols"], "rows":left["rows"], "frame":view,
                                        "difference":first})
        run("kill-session", "-t", "roster-anchor")
        close_fixture()
        prefix = "static" if static_grid else "context" if context_grid else "manager" if manager_grid else "edit" if edit_grid else "menu"
        p.write_json(root / "grid.json", {f"{prefix}_reference":expected, f"{prefix}_candidate":actual,
            "fixture_ids":ids, "fixture_cwd":str(root / "work"), "first_divergence":differences[0] if differences else None,
            "differences":differences, "cursor_coordinates":coordinates, "retired_rows":retired_rows})
        return

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

    close_fixture()
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
        assert r.keys() == c.keys()
        assert r.get("workspace_action") == c.get("workspace_action")
        for landmark in (key for key in r if key not in ("cols", "rows", "workspace_action")):
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
        form_root=None, board_grid=False, handoff=False, touch=False, terminal_reply=None, reference_board=None, edit_rows=False, edit_grid=False, manager_options=False, manager_grid=False, context_options=False, context_grid=False, relocate_board=False, scheduled_enter=False, relocation_grid=False, static_grid=False, roster_motion=False, layout_options=False, scratch_path=Path("/tmp"), handoff_grid=False):
    scratch = scratch_path.resolve(strict=True)
    if form_root is None:
        root = Path(tempfile.mkdtemp(prefix="pb-", dir=scratch))
    else:
        root = form_root.resolve()
        if not (form_grid or board_grid) or root.parent != scratch:
            raise ValueError("form root must be a new direct child of the selected scratch directory")
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
        if relocate_board or scheduled_enter or relocation_grid or handoff_grid:
            target = root / "bin/agy"
            shutil.copyfile(HERE / "agy", target)
            target.chmod(0o700)
        if terminal_reply is not None:
            shutil.copyfile(terminal_reply, root / "effects/terminal-mouse-reply")
        if reference_board is not None:
            target = root / "bin/reference-board"
            shutil.copyfile(reference_board, target)
            target.chmod(0o700)
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
        elif edit_rows:
            command.append("--edit-rows")
        elif manager_options:
            command.append("--manager-options")
        elif context_options:
            command.append("--context-options")
        elif relocate_board:
            command.append("--relocation-board")
        elif scheduled_enter:
            command.append("--scheduled-enter")
        elif layout_options:
            command.append("--layout-options")
        elif tmux is not None:
            command.append(tmux)
            if form_grid:
                command.append("--form-grid")
            elif board_grid:
                command.append("--board-grid")
            elif roster_motion:
                command.append("--roster-motion")
            elif reference_board is not None:
                command.append("--handoff-grid" if handoff_grid else "--static-grid" if static_grid else "--relocation-grid" if relocation_grid else "--context-grid" if context_grid else "--manager-grid" if manager_grid
                               else "--edit-grid" if edit_grid else "--menu-grid")
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
        diagnostic = not captured.exists()
        if diagnostic:
            captured = root / "capture-timeout.json"
        if evidence is not None and captured.exists():
            receipt = json.loads(captured.read_bytes())
            receipt["tmux"] = subprocess.check_output([tmux, "-V"], text=True).strip()
            receipt["board_sha256"] = p.hash_file(client)
            receipt["supervisor_sha256"] = p.hash_file(supervisor)
            if reference_board is not None:
                receipt["reference_board_sha256"] = p.hash_file(reference_board)
            p.write_json(evidence, receipt, replace=False)
            if diagnostic:
                print(f"roster capture timeout retained in {evidence}")
            else:
                if receipt.get("retired_rows"):
                    print(f"{len(receipt['retired_rows'])} retired Provider logins rows recorded in {evidence}")
                if receipt.get("cursor_coordinates"):
                    print(f"{len(receipt['cursor_coordinates'])} hidden cursor coordinate differences recorded in {evidence}")
                if receipt["first_divergence"] is not None:
                    raise RuntimeError(f"terminal grid or mode changed; captured in {evidence}")
        if child.returncode != 0:
            raise RuntimeError(f"board qualification exited {child.returncode}")
    finally:
        if tmux is not None:
            try:
                subprocess.run([tmux, "-S", root / "tmux.sock", "kill-server"],
                               env=p.environment(root), stdout=subprocess.DEVNULL,
                               stderr=subprocess.DEVNULL, timeout=2, check=False)
            except (OSError, subprocess.TimeoutExpired):
                pass  # The independent process reaper still checks every fixture process.
        cleanup = p.reap(root, driver, previous)
        if cleanup["remaining"]:
            raise RuntimeError(f"fixture cleanup has survivors in {root}: {cleanup}")
        if child is not None:
            child.wait(timeout=2)
        shutil.rmtree(root)
    exercise = ("retained Options grouping, ordering and persistence" if layout_options else
                "native scheduled review reopening through Enter" if scheduled_enter else
                "captured Antigravity resume and relocation" if relocate_board else
                "capability-gated Codex Context Guard Options and revision fencing" if context_options else
                "owner manager prompt persistence and validation through Options" if manager_options else
                "rename, tag, cancel, kill and delete through public board" if edit_rows else
                "attached CLI button, native mouse and terminal restoration" if touch else
                "supervised and hands-off handoff" if handoff else
                "working spinner and elapsed-clock motion" if roster_motion else
                "paired deterministic roster cells" if static_grid else
                "paired handoff form grids" if handoff_grid else
                "paired native Antigravity relocation grids" if relocation_grid else
                "paired compact Context Guard settings grids" if context_grid else
                "paired compact manager prompt grids" if manager_grid else
                "paired edit and confirmation grids" if edit_grid else
                "paired More menu grids" if reference_board else
                "comparative roster grids" if board_grid else
                "comparative launch-form grids" if form_grid else
                "local automation CLI and owner dashboard catalog/disconnect" if automations else
                "running-session cycling" if cycle else "board launch, attach and live reattach")
    print(f"{exercise} {'captured' if form_grid or board_grid or reference_board or roster_motion else 'passed'}; cleanup {cleanup}")


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
                sys.argv[-1] == "--board-grid", mode == "--handoff-board", mode == "--touch-back",
                sys.argv[-1] == "--menu-grid", mode == "--edit-rows", sys.argv[-1] == "--edit-grid", mode == "--manager-options", sys.argv[-1] == "--manager-grid", mode == "--context-options", sys.argv[-1] == "--context-grid", mode == "--relocation-board", mode == "--scheduled-enter", sys.argv[-1] == "--relocation-grid", sys.argv[-1] == "--static-grid", sys.argv[-1] == "--roster-motion", mode == "--layout-options", handoff_grid=sys.argv[-1] == "--handoff-grid")
    else:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument("--supervisor", required=True, type=p.binary_path)
        parser.add_argument("--board", required=True, type=p.binary_path)
        modes = parser.add_mutually_exclusive_group()
        modes.add_argument("--tmux", type=p.binary_path)
        modes.add_argument("--automations", action="store_true", help="read the owner automation catalog from CLI and board")
        modes.add_argument("--handoff-board", action="store_true", help="exercise supervised and capability-gated hands-off handoff")
        modes.add_argument("--layout-options", action="store_true",
                           help="verify Options grouping and ordering across public board restarts")
        modes.add_argument("--scheduled-enter", action="store_true",
                           help="open a stopped synthetic scheduled review through the real native resume path")
        modes.add_argument("--relocation-board", action="store_true",
                           help="exercise captured Antigravity relocation, cancellation and wrong-row fences")
        modes.add_argument("--context-options", action="store_true",
                           help="check capability negotiation, owner Context Guard validation and stale revision refusal")
        modes.add_argument("--manager-options", action="store_true",
                           help="edit the owner-shared manager prompt and verify save/refusal")
        modes.add_argument("--edit-rows", action="store_true",
                           help="exercise inline edits and lifecycle confirmation against actual owner state")
        modes.add_argument("--touch-back", action="store_true",
                           help="drive the Options toggle and attached native mouse and footer gestures")
        modes.add_argument("--cycle", action="store_true", help="drive two live sessions through next, previous and detach")
        parser.add_argument("--terminal-mouse-reply", type=Path,
                            help="inject a physical terminal's binary mouse mode report into the attached PTY")
        parser.add_argument("--grid-evidence", type=Path)
        parser.add_argument("--form-root", type=Path, help="reuse the same new scratch path in sequential form arms")
        parser.add_argument("--scratch", type=Path, default=Path("/tmp"), help="fixture parent directory")
        grids = parser.add_mutually_exclusive_group()
        grids.add_argument("--form-grid", action="store_true", help="capture launch form across reference terminal sizes")
        grids.add_argument("--reference-board", type=p.binary_path,
                           help="compare the More menu with this board using the same public owner and IDs")
        grids.add_argument("--roster-motion", action="store_true",
                           help="verify live working animation and age in the full width roster")
        grids.add_argument("--board-grid", action="store_true", help="capture populated roster across reference terminal sizes")
        paired = parser.add_mutually_exclusive_group()
        paired.add_argument("--static-grid", action="store_true",
                            help="compare deterministic ordinary roster cells with --reference-board")
        paired.add_argument("--handoff-grid", action="store_true",
                            help="compare cross-CLI handoff form and consent with --reference-board")
        paired.add_argument("--relocation-grid", action="store_true",
                            help="compare Antigravity relocation with --reference-board")
        paired.add_argument("--context-grid", action="store_true",
                            help="compare compact Context Guard settings with --reference-board")
        paired.add_argument("--manager-grid", action="store_true",
                            help="compare compact manager-prompt focus with --reference-board")
        paired.add_argument("--edit-grid", action="store_true",
                           help="compare edit and confirmation panels with --reference-board")
        args = parser.parse_args()
        if bool(args.tmux) != bool(args.grid_evidence):
            parser.error("--tmux and --grid-evidence are required together")
        if (args.form_grid or args.board_grid or args.reference_board or args.roster_motion) and not args.tmux:
            parser.error("grid checks require --tmux")
        if (args.edit_grid or args.manager_grid or args.context_grid or args.relocation_grid or args.static_grid or args.handoff_grid) and not args.reference_board:
            parser.error("paired grid checks require --reference-board")
        if args.terminal_mouse_reply and not args.touch_back:
            parser.error("--terminal-mouse-reply requires --touch-back")
        run(args.supervisor, args.board, args.tmux, args.grid_evidence, args.cycle, args.automations,
            args.form_grid, args.form_root, args.board_grid, args.handoff_board, args.touch_back,
            args.terminal_mouse_reply, args.reference_board, args.edit_rows, args.edit_grid, args.manager_options, args.manager_grid, args.context_options, args.context_grid, args.relocation_board, args.scheduled_enter, args.relocation_grid, args.static_grid, args.roster_motion, args.layout_options, args.scratch, args.handoff_grid)
