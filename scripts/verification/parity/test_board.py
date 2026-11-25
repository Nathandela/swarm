#!/usr/bin/env python3
"""Run Rust board launch and reattach after daemon SIGKILL on a real local PTY."""

import argparse
import base64
import importlib.util
from importlib.machinery import SourceFileLoader
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import termios
import time

HERE = Path(__file__).resolve().parent
DRIVER = HERE / "swarm-parity"
spec = importlib.util.spec_from_loader("parity", SourceFileLoader("parity", str(DRIVER)))
p = importlib.util.module_from_spec(spec)
spec.loader.exec_module(p)


def witness(root, tmux=None):
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

    def terminal(payload, fresh):
        board = p.Board([str(root / "bin/board")], root, env)
        try:
            if fresh:
                board.wait_bytes(b"swarm")
                board.send(b"n")
                board.wait_bytes(b"New session")
                board.send(b"\x1b[B\x1b[Bamber\r")
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

    if tmux is not None:
        grid_witness(root, cli, roster, start, tmux)
        return

    daemon = start()
    before = terminal(b"before", True)
    rows, = roster()
    assert len(rows) == 1 and rows[0]["name"] == "amber"
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
        # -e retains cell attributes. -N preserves trailing cells. Exclude the Swarm chrome row.
        return send_tmux("capture-pane", "-peN", "-S", "0", "-E", "22", "-t", "board:0.0")

    def observed(fragment):
        return p.wait_for(lambda: screen if fragment in (screen := grid()) else None,
                          repr(fragment))

    def capture():
        cells = observed(b"grid end")
        fmt = "|".join(f"#{{{name}}}" for name in TMUX_MODES)
        modes = send_tmux("display-message", "-p", "-t", "board:0.0", fmt).decode().strip().split("|")
        return {"cells_ansi_base64": base64.b64encode(cells).decode(), "width": 80, "rows": 23,
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
    daemon.kill()
    daemon.wait(timeout=5)
    assert p.generation(worker) == birth
    send_tmux("kill-session", "-t", "board")
    daemon = start()
    rows, = roster()
    assert [row["id"] for row in rows] == [identity]
    open_board()
    after = capture()
    send_tmux("send-keys", "-t", "board:0.0", "-l", "after")
    send_tmux("send-keys", "-t", "board:0.0", "Enter")
    observed(b"fixture received after")
    assert p.generation(worker) == birth
    assert (root / "effects/input").read_bytes() == b"before\rafter\r"
    send_tmux("kill-session", "-t", "board")
    assert cli("kill", identity).returncode == 0
    daemon.terminate()
    daemon.wait(timeout=5)
    p.write_json(root / "grid.json", {"before": before, "after": after})


def run(supervisor, client, tmux=None, evidence=None):
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
        if tmux is not None:
            command.append(tmux)
        child = subprocess.Popen(command, cwd=root / "work", env=p.environment(root),
                                 start_new_session=True)
        driver = (child.pid, p.generation(child.pid))
        until = time.monotonic() + 30
        while child.poll() is None:
            p.owned(root, driver, previous)
            if time.monotonic() >= until:
                raise RuntimeError("board qualification timed out")
            time.sleep(0.05)
        if child.returncode != 0:
            raise RuntimeError(f"board qualification exited {child.returncode}")
        if evidence is not None:
            receipt = json.loads((root / "grid.json").read_bytes())
            receipt["tmux"] = subprocess.check_output([tmux, "-V"], text=True).strip()
            receipt["board_sha256"] = p.hash_file(client)
            receipt["supervisor_sha256"] = p.hash_file(supervisor)
            p.write_json(evidence, receipt, replace=False)
            if receipt["before"] != receipt["after"]:
                raise RuntimeError(f"terminal grid or mode changed; captured in {evidence}")
    finally:
        cleanup = p.reap(root, driver, previous)
        if cleanup["remaining"]:
            raise RuntimeError(f"fixture cleanup has survivors in {root}: {cleanup}")
        if child is not None:
            child.wait(timeout=2)
        shutil.rmtree(root)
    print(f"board launch, attach and live reattach passed; cleanup {cleanup}")


if __name__ == "__main__":
    if len(sys.argv) in (3, 4) and sys.argv[1] == "_inside":
        witness(Path(sys.argv[2]), Path(sys.argv[3]) if len(sys.argv) == 4 else None)
    else:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument("--supervisor", required=True, type=p.binary_path)
        parser.add_argument("--board", required=True, type=p.binary_path)
        parser.add_argument("--tmux", type=p.binary_path)
        parser.add_argument("--grid-evidence", type=Path)
        args = parser.parse_args()
        if bool(args.tmux) != bool(args.grid_evidence):
            parser.error("--tmux and --grid-evidence are required together")
        run(args.supervisor, args.board, args.tmux, args.grid_evidence)
