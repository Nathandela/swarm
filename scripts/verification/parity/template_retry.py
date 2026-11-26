#!/usr/bin/env python3
"""Use one private catalog across an unacknowledged Rust create and Go deletion."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

from test_board import owner_request, p


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
            board.send(b"\x1b[27u\x1b[27u\x1b[27u")

            def exited():
                board.drain()
                return board.child.poll() is not None

            p.wait_for(exited, "large template board quit")
            assert board.child.returncode == 0
        finally:
            board.close()
    finally:
        if owner.poll() is None:
            owner.terminate()
            owner.wait(timeout=8)
        result = p.reap(root, None, {})
        assert not result["remaining"], result
    print("Go reopened and deleted the Rust template. Exact retry after restart made no write.")
    print("The board read an over-frame Go template and saved another without changing it.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("rust", type=Path)
    parser.add_argument("go", type=Path)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="tp-", dir=os.environ.get("TMPDIR", "/tmp")) as tmp:
        run(Path(tmp).resolve(), args.rust.resolve(strict=True), args.go.resolve(strict=True))
