#!/usr/bin/env python3
"""Exercise the Rust owner socket and PTY across daemon termination."""

import argparse
import json
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
from pathlib import Path

sys.dont_write_bytecode = True
from parity_ipc import p as fixtures


def exact(conn, size):
    data = bytearray()
    while len(data) < size:
        chunk = conn.recv(size - len(data))
        if not chunk:
            raise EOFError("owner socket closed")
        data.extend(chunk)
    return bytes(data)


def read_frame(conn):
    size, = struct.unpack(">I", exact(conn, 4))
    assert 1 <= size <= 1 << 20
    tag, = exact(conn, 1)
    return tag, exact(conn, size - 1)


def read_control(conn):
    tag, body = read_frame(conn)
    assert tag == 1
    return json.loads(body)


def send_frame(conn, tag, data):
    conn.sendall(struct.pack(">IB", len(data) + 1, tag) + data)


def control(conn, op, **fields):
    send_frame(conn, 1, json.dumps({"op": op, **fields}).encode())


def attach(path, identity):
    conn = socket.socket(socket.AF_UNIX)
    try:
        conn.settimeout(4)
        conn.connect(str(path))
        control(conn, "hello", endpoint_id="", protocol_version=1, capabilities=["attach"])
        hello = read_control(conn)
        assert hello["op"] == "hello" and hello["capabilities"] == ["attach"]
        address = {"endpoint_id": hello["endpoint_id"], "session_id": identity}
        control(conn, "attach", **address)
        lease = read_control(conn)
        assert lease["op"] == "lease", lease
        address["generation"] = lease["generation"]
        snapshot = bytearray()
        while len(snapshot) < lease["snapshot_len"]:
            tag, payload = read_frame(conn)
            assert tag == 4
            snapshot.extend(payload)
        assert len(snapshot) == lease["snapshot_len"]
        return conn, address, json.loads(snapshot)
    except BaseException:
        conn.close()
        raise


def screen_text(snapshot):
    text = []
    for row in snapshot["lines"]:
        assert sum(run["width"] for run in row["runs"]) == snapshot["cols"]
        text.append("".join(run["text"] for run in row["runs"]).rstrip())
    assert len(text) == snapshot["rows"]
    return "\n".join(text)


def drive(root, guard):
    daemon = None
    previous = {}
    try:
        binary = root / "bin/swarm"
        env = fixtures.environment(root)
        env["AWS_SECRET_ACCESS_KEY"] = "unrelated-environment-canary"
        env["ANTHROPIC_API_KEY"] = "provider-environment-canary"
        command = [*guard, binary]
        address = Path(env["SWARM_DAEMON_SOCK"])

        def invoke(*args, expected=0):
            result = subprocess.run([*command, *args], env=env, cwd=root / "work",
                                    capture_output=True, timeout=6)
            assert result.returncode == expected, (args, result.returncode, result.stderr)
            return result.stdout

        def delete(identity):
            with socket.socket(socket.AF_UNIX) as connection:
                connection.settimeout(8)
                connection.connect(str(address))
                control(connection, "hello", endpoint_id="", protocol_version=1)
                endpoint = read_control(connection)["endpoint_id"]
                control(connection, "delete", endpoint_id=endpoint, session_id=identity)
                return read_control(connection)

        def launch_daemon(log):
            return subprocess.Popen([*command, "daemon"], env=env, cwd=root / "work",
                                    stdout=log, stderr=log)

        def ready():
            out = subprocess.run([*command, "ls", "--json"], env=env, cwd=root / "work",
                                 capture_output=True, timeout=2)
            return out.returncode == 0

        def expect_output(connection, fragment):
            output = bytearray()
            while fragment not in output:
                tag, data = read_frame(connection)
                assert tag == 2
                output.extend(data)
                assert len(output) <= 1 << 20

        with (root / "daemon.log").open("wb") as log:
            daemon = launch_daemon(log)
            fixtures.wait_for(ready, "Rust owner socket")
            invoke("daemon", expected=1)
            alternate = subprocess.run([*command, "daemon"], env={**env,
                "SWARM_DAEMON_SOCK": str(root/"other.sock"),
                "SWARM_DAEMON_LOCK": str(root/"other.lock")}, cwd=root/"work",
                stdin=subprocess.DEVNULL, capture_output=True, timeout=3)
            assert alternate.returncode == 1 and not (root/"other.sock").exists(), alternate
            print("singleton contention refused including independent socket/lock overrides")
            with socket.socket(socket.AF_UNIX) as malformed:
                malformed.settimeout(4)
                malformed.connect(str(address))
                control(malformed, "hello", endpoint_id="", protocol_version=1)
                endpoint = read_control(malformed)["endpoint_id"]
                control(malformed, "launch", endpoint_id=endpoint, launch={
                    "agent": "claude", "cwd": str(root / "work"), "worktree": "true",
                })
                refusal = read_control(malformed)
                assert refusal["op"] == "error" and "invalid launch field type" in refusal["error"]
            assert json.loads(invoke("ls", "--json")) == []
            assert not (root / "effects/worker").exists()
            print("malformed worktree refused before reservation or worker launch")

            baseline = ("spawn", "--cli", "claude", "--name", "amber",
                        "--dir", str(root / "work"), "--prompt", "offline fixture")
            version_override = root / "effects/reported-version"
            for version in ("1.99.0", "2.0.0-preview", "malformed"):
                version_override.write_text(version)
                invoke(*baseline, expected=1)
                assert json.loads(invoke("ls", "--json")) == []
                assert not (root / "effects/worker").exists()
            version_override.unlink()
            print("unsupported Claude releases refused before a worker or receipt existed")

            operation = invoke("spawn", "--new-operation-id").decode().strip()
            launch_args = ("spawn", "--operation-id", operation, "--cli", "claude", "--name", "amber",
                           "--dir", str(root / "work"), "--prompt", "offline fixture")
            receipt = invoke(*launch_args)
            identity = receipt.decode().strip()
            assert invoke(*launch_args) == receipt
            operation_status = invoke("spawn", "--status", operation)
            status = json.loads(operation_status)
            assert status["session_id"] == identity and status["state"] == "applied", status
            local_id = identity.split("/")[1]
            state = root / "state/rust-v1" / local_id
            owner = json.loads((state / "owner.json").read_text())
            for file in (root / "state/rust-v1").rglob("*.json"):
                persisted = file.read_bytes()
                assert b"environment-canary" not in persisted
                assert b"offline fixture" not in persisted
            worker_path = root / "effects/worker"
            fixtures.wait_for(worker_path.exists, "fixture worker")
            worker = int(worker_path.read_text())
            descendant_path = root / "effects/child"
            fixtures.wait_for(descendant_path.exists, "TERM-resistant child")
            child_pid = int(descendant_path.read_text())
            child_birth = fixtures.generation(child_pid)
            assert child_birth is not None
            assert (root / "effects/env").read_text() == "False:provider-environment-canary"
            fixtures.owned(root, None, previous)
            assert owner["worker_pid"] == worker
            with socket.socket(socket.AF_UNIX) as unauthorized:
                unauthorized.settimeout(4)
                unauthorized.connect(str(state / "p"))
                send_frame(unauthorized, 1, json.dumps({
                    "type": "hello", "wire_version": 1, "token": "0" * 24,
                }).encode())
                assert unauthorized.recv(1) == b""
            assert json.loads(invoke("ls", "--json"))[0]["id"] == identity
            fixtures.wait_for(lambda: b"fixture ready" in invoke("peek", identity),
                              "initial screen")

            native = "e903fef7-3d8d-4ae3-b86c-d8631eaf90de"
            hook_file = root / "effects/hook-request"
            response_file = root / "effects/hook-response"
            hook_sequence = 0

            def fire(event, expected=0, **extra):
                nonlocal hook_sequence
                hook_sequence += 1
                request = {"sequence": hook_sequence, "event": event,
                           "payload": {"cwd": str(root / "work"), "session_id": native, **extra}}
                temp = hook_file.with_suffix(".tmp")
                temp.write_text(json.dumps(request))
                temp.replace(hook_file)

                def response():
                    if response_file.exists():
                        result = json.loads(response_file.read_text())
                        return result if result["sequence"] == hook_sequence else None
                    return None

                result = fixtures.wait_for(response, "configured Claude hook reply")
                assert result["exit"] == expected, result

            fire("PermissionRequest")
            assert json.loads(invoke("watch", identity, "--until", "needs_input", "--timeout", "4s"))["status"]["Interaction"] == "permission"
            fire("Notification", notification_type="unrecognized")
            assert json.loads(invoke("ls", "--json"))[0]["status"]["Interaction"] == "permission"
            fire("Stop", expected=1, session_id="a903fef7-3d8d-4ae3-b86c-d8631eaf90de")
            fire("Stop")
            assert json.loads(invoke("watch", identity, "--until", "ready_for_review", "--timeout", "4s"))["group"] == "ready_for_review"
            assert json.loads((state / "observation.json").read_text())["conversation_id"] == native
            print("injected hooks published typed status and write-once native conversation identity")

            first, fields, snapshot = attach(address, identity)
            with first:
                assert "fixture ready" in screen_text(snapshot)
                control(first, "resize", cols=91, rows=26, **fields)
                size = root / "effects/size"
                fixtures.wait_for(lambda: size.read_bytes() == b"91x26\n", "PTY resize")
                send_frame(first, 3, b"before restart\r")
                expect_output(first, b"fixture received before restart")
                assert b"fixture received before restart" in invoke("peek", identity)
                successor, next_fields, _ = attach(address, identity)
                with successor:
                    revoked = read_control(first)
                    assert revoked["op"] == "detach"
                    control(successor, "detach", **next_fields)
                    assert read_control(successor)["op"] == "detach"
            print("leases superseded and detached, peek preserved attachment, resize and input reached the PTY")

            with socket.socket(socket.AF_UNIX) as edit:
                edit.settimeout(4)
                edit.connect(str(address))
                control(edit, "hello", endpoint_id="", protocol_version=1)
                endpoint = read_control(edit)["endpoint_id"]
                for op, changes in [("rename", {"name": "fixture\x07 renamed"}), ("set_tag", {"tag": "  retained  "})]:
                    control(edit, op, endpoint_id=endpoint, session_id=identity, **changes)
                    assert read_control(edit)["op"] == "ok"
            labels = json.loads(invoke("ls","--json"))[0]
            assert labels["name"] == "fixture renamed" and labels["tag"] == "retained"
            daemon.kill()
            daemon.wait(timeout=4)
            assert worker in fixtures.table()
            assert owner["pid"] in fixtures.table()
            corrupt = state.parent / (".deleting-" + "0"*24)
            corrupt.mkdir()
            (corrupt / "session.json").write_bytes(b"incomplete fixture")
            owner_path = state / "owner.json"
            try:
                owner_path.write_text(json.dumps({**owner, "birth": owner["birth"] + 1}))
                daemon = launch_daemon(log)
                fixtures.wait_for(ready, "replacement rejecting mismatched shim")
                invoke("peek", identity, expected=1)
                invoke("kill", identity, expected=1)
                assert delete(identity)["op"] == "error" and state.is_dir()
                assert json.loads(invoke("ls", "--json"))[0]["status"]["Process"] == "lost"
                assert worker in fixtures.table()
                daemon.kill()
                daemon.wait(timeout=4)
            finally:
                owner_path.write_text(json.dumps(owner))
            print("mismatched birth token refused without signaling the worker")
            daemon = launch_daemon(log)
            fixtures.wait_for(ready, "replacement Rust daemon")
            assert (corrupt / "session.json").read_bytes() == b"incomplete fixture"
            assert json.loads((state / "owner.json").read_text()) == owner
            assert invoke("spawn", "--status", operation) == operation_status
            row = json.loads(invoke("ls","--json"))[0]
            assert row["name"] == labels["name"] and row["tag"] == labels["tag"]
            assert invoke(*launch_args) == receipt
            assert worker_path.read_text() == str(worker) + "\n"
            assert [row["id"] for row in json.loads(invoke("ls", "--json"))] == [identity]
            print("reliable operation status and replay preserved the single original worker")
            assert b"fixture received before restart" in invoke("peek", identity)
            second, _, snapshot = attach(address, identity)
            with second:
                assert "fixture received before restart" in screen_text(snapshot)
                send_frame(second, 3, b"after restart\r")
                expect_output(second, b"fixture received after restart")
            assert (root / "effects/input").read_bytes() == b"before restart\rafter restart\r"
            print("same shim PID and birth token, same worker PID, same screen after daemon SIGKILL")
            assert fixtures.generation(child_pid) == child_birth

            invoke("kill", identity)
            assert json.loads(invoke("watch", identity, "--until", "completed", "--timeout", "4s"))["group"] == "completed"
            def child_released():
                row = fixtures.table().get(child_pid)
                return row is None or row[2].startswith("Z") or fixtures.generation(child_pid) != child_birth
            fixtures.wait_for(child_released, "TERM-resistant child containment")
            invoke("kill", identity)
            print("explicit termination contained a TERM-resistant descendant before completion")
            # Crash after the namespace rename, before the deletion receipt was written.
            state.rename(state.with_name(".deleting-" + state.name))
            assert delete(identity)["op"] == "ok"
            assert delete(identity)["op"] == "ok"
            assert not state.exists() and json.loads(invoke("ls", "--json")) == []
            assert invoke("spawn", "--status", operation) == operation_status
            invoke(*launch_args, expected=1)
            assert json.loads(invoke("ls", "--json")) == []
            state.mkdir()
            try:
                assert delete(identity)["op"] == "error" and state.is_dir()
            finally:
                state.rmdir()
            print("confirmed deletion was repeatable; consumed operation could not recreate worker")

            repo = root / "work/repo"
            repo.mkdir()
            def git(cwd,*args):
                return subprocess.run([*guard,"git","-C",str(cwd),*args],env=env,
                                      stdin=subprocess.DEVNULL,capture_output=True,check=True,timeout=15).stdout.strip()
            git(repo,"init","-q")
            git(repo,"-c","user.name=fixture","-c","user.email=fixture@localhost",
                "commit","--allow-empty","-qm","fixture")
            head = git(repo,"rev-parse","HEAD")
            (repo / "uncommitted").write_text("primary only")
            launch = ["spawn","--cli","claude","--worktree","--name","Amber One",
                      "--dir",str(repo),"--prompt","isolated fixture"]
            first = invoke(*launch).decode().strip()
            second = invoke(*launch).decode().strip()
            expected = [repo/".swarm/worktrees/amber-one", repo/".swarm/worktrees"/("amber-one-"+second.split("/")[1])]
            for child,checkout in zip((first,second),expected):
                assert git(checkout,"rev-parse","HEAD")==head
                assert git(checkout,"symbolic-ref","--short","HEAD").decode()=="swarm/amber-one-"+child.split("/")[1]
                assert not (checkout/"uncommitted").exists()
            protected = invoke("spawn","--cli","claude","--dir",str(expected[0]),"--prompt","hold checkout").decode().strip()
            assert delete(first)["op"]=="error" and expected[0].is_dir()
            assert delete(protected)["op"]=="ok"
            assert delete(first)["op"]=="ok"
            assert not expected[0].exists() and expected[1].is_dir()
            assert delete(second)["op"]=="ok"
            assert (repo/"uncommitted").read_text()=="primary only" and git(repo,"rev-parse","HEAD")==head
            print("two real git worktrees kept independent HEADs; shared checkout was retained until released")

    finally:
        if daemon and daemon.poll() is None:
            daemon.terminate()
            daemon.wait(timeout=4)
        cleanup = fixtures.reap(root, None, previous)
        print("cleanup", cleanup)
        if cleanup["remaining"]:
            (root / "cleanup-unresolved").touch()
            raise RuntimeError(f"fixture processes survived cleanup at {root}")


def smoke(candidate, scratch):
    root = Path(tempfile.mkdtemp(prefix="st-", dir=scratch))
    remove_root = False
    try:
        for name in ("bin", "home", "runtime", "tmp", "work", "state", "effects"):
            (root / name).mkdir(mode=0o700)
        binary = root / "bin/swarm"
        for source, destination in ((candidate, binary), (fixtures.FIXTURE, root / "bin/claude-fixture")):
            shutil.copyfile(source, destination)
            destination.chmod(0o700)
        # A fixture worker owns a child in its process group that ignores TERM.
        claude = root / "bin/claude"
        claude.write_text("""#!/usr/bin/env python3
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

path = Path(__file__).resolve()
effects = path.parents[1] / "effects"
fixture = path.with_name("claude-fixture")
if "--version" in sys.argv and (effects / "reported-version").exists():
    print((effects / "reported-version").read_text())
    sys.exit(0)
if "--version" not in sys.argv:
    settings = json.loads(sys.argv[sys.argv.index("--settings") + 1])
    if os.fork() == 0:
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGHUP, signal.SIG_IGN)
        (effects / "child").write_text(str(os.getpid()))
        seen = 0
        while True:
            source = effects / "hook-request"
            if source.exists():
                request = json.loads(source.read_text())
                if request["sequence"] != seen:
                    seen = request["sequence"]
                    command = settings["hooks"][request["event"]][0]["hooks"][0]["command"]
                    result = subprocess.run(command, shell=True, input=json.dumps(request["payload"]),
                                            text=True, capture_output=True, timeout=5)
                    target = effects / "hook-response"
                    temp = target.with_suffix(".tmp")
                    temp.write_text(json.dumps({"sequence": seen, "exit": result.returncode}))
                    temp.replace(target)
            time.sleep(0.025)
    (effects / "env").write_text(
        str("AWS_SECRET_ACCESS_KEY" in os.environ) + ":" + os.environ.get("ANTHROPIC_API_KEY", "")
    )
os.execv(fixture, [str(fixture), *sys.argv[1:]])
""")
        claude.chmod(0o700)
        fixtures.table()
        guard = fixtures.guard(root)
        if sys.platform.startswith("linux"):
            # Keep both supervisor lifetimes and the surviving shim in one PID namespace.
            source = Path(__file__).resolve()
            driver = root / "bin/runtime_driver"
            shutil.copyfile(source, driver)
            shutil.copyfile(source.with_name("parity_ipc.py"), driver.with_name("parity_ipc.py"))
            parity = root / "bin/parity"
            parity.mkdir()
            shutil.copyfile(fixtures.__file__, parity / "swarm-parity")
            subprocess.run([*guard, "/usr/bin/python3", "-u", driver, "--isolated-root", root],
                           check=True, timeout=40)
        else:
            drive(root, guard)
        remove_root = True
    finally:
        if remove_root:
            shutil.rmtree(root)
        else:
            print(f"retained fixture root {root}", file=sys.stderr)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", type=fixtures.binary_path)
    parser.add_argument("--scratch", type=Path, default=Path("/tmp").resolve())
    parser.add_argument("--isolated-root", type=Path, help=argparse.SUPPRESS)
    args = parser.parse_args()
    if args.isolated_root and not args.candidate:
        drive(args.isolated_root, [])
    elif args.candidate and not args.isolated_root:
        smoke(args.candidate, args.scratch.resolve())
    else:
        parser.error("--candidate is required")
