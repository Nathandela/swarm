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


def witness(root, tmux=None, cycle=False, automations=False, form_grid=False):
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
            daemon.terminate()
            daemon.wait(timeout=5)
            offline = cli("schedule", "list", "--json")
            assert offline.returncode == 0 and [row["id"] for row in json.loads(offline.stdout)] == [job]
            board.wait_bytes(b"daemon connection lost")
            board.send(b"u")
            board.wait_bytes(b"daemon disconnected")
            board.send(b"\x1b[27u\x1b[27u")
            def quit_catalog():
                board.drain()
                return board.child.poll() is not None
            p.wait_for(quit_catalog, "automation board quit")
            assert board.child.returncode == 0
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
                board.wait_bytes(b"2.1.100")
                board.send(b"\x15" + os.fsencode(cwd / "qual") + b"\t")
                board.wait_bytes(b"/qualified/")
                board.send(b"\x1b[Bamber\r")
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
        run("new-session", "-d", "-s", "form", "-x", str(cols), "-y", str(rows),
            "-c", root / "work", root / "bin/board")
        landmark(("swarm", "(n)", "no sessions"), cols, rows)
        run("send-keys", "-t", "form:0.0", "n")
        compact_block = cols < 32 or rows < 8
        directory = landmark(("resize to 32",) if compact_block else ("▌ directory",), cols, rows)
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
    p.write_json(root / "grid.json", {"form_frames": frames, "first_divergence": None})


def run(supervisor, client, tmux=None, evidence=None, cycle=False, automations=False, form_grid=False):
    root = Path(tempfile.mkdtemp(prefix="pb-", dir=Path("/tmp").resolve()))
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
        if automations:
            command.append("--automations")
        elif cycle:
            command.append("--cycle")
        elif tmux is not None:
            command.append(tmux)
            if form_grid:
                command.append("--form-grid")
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
    exercise = ("comparative launch-form grids" if form_grid else
                "local automation CLI and owner dashboard catalog/disconnect" if automations else
                "running-session cycling" if cycle else "board launch, attach and live reattach")
    print(f"{exercise} {'captured' if form_grid else 'passed'}; cleanup {cleanup}")


if __name__ == "__main__":
    if sys.argv[1:2] == ["--compare-grids"] and len(sys.argv) == 4:
        compare_grid_receipts(sys.argv[2], sys.argv[3])
    elif len(sys.argv) in (3, 4, 5) and sys.argv[1] == "_inside":
        mode = sys.argv[3] if len(sys.argv) >= 4 else None
        witness(Path(sys.argv[2]), Path(mode) if mode and not mode.startswith("--") else None,
                mode == "--cycle", mode == "--automations", len(sys.argv) == 5)
    else:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument("--supervisor", required=True, type=p.binary_path)
        parser.add_argument("--board", required=True, type=p.binary_path)
        modes = parser.add_mutually_exclusive_group()
        modes.add_argument("--tmux", type=p.binary_path)
        modes.add_argument("--automations", action="store_true", help="read the owner automation catalog from CLI and board")
        modes.add_argument("--cycle", action="store_true", help="drive two live sessions through next, previous and detach")
        parser.add_argument("--grid-evidence", type=Path)
        parser.add_argument("--form-grid", action="store_true", help="capture launch form across reference terminal sizes")
        args = parser.parse_args()
        if bool(args.tmux) != bool(args.grid_evidence):
            parser.error("--tmux and --grid-evidence are required together")
        if args.form_grid and not args.tmux:
            parser.error("--form-grid requires --tmux")
        run(args.supervisor, args.board, args.tmux, args.grid_evidence, args.cycle, args.automations,
            args.form_grid)
