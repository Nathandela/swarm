"""Two scratch supervisors with an actual Discord manager, child worker and unrelated row."""

from contextlib import ExitStack, contextmanager
from hashlib import sha256
import json
from pathlib import Path
import shutil
import shlex
import socket
import subprocess
import sys
import tempfile
import time

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from stopped_go_state import await_value, daemon, fixture_env, owner, private

CAPS = [
    "manager-native-cli",
    "owner-launch",
    "manager-launch",
    "manager-provenance",
    "semantic-send",
    "message-origin",
]


def delegate(root, env, binary, place, launch, manager_id, discussion, operation):
    def wire(op, **fields):
        return owner(env, {"op": op, **fields}, caps=CAPS, timeout=45)

    def digest(value):
        return sha256(
            json.dumps(value, separators=(",", ":"), ensure_ascii=False).encode()
        ).hexdigest()

    def projected():
        rows = wire("list").get("sessions") or []
        return next(
            (
                row
                for row in rows
                if row["id"] == manager_id
                and row.get("capabilities", {}).get("session_instance")
            ),
            None,
        )

    instance = await_value(projected)["capabilities"]["session_instance"]

    def native():
        return wire(
            "manager_runtime",
            session_id=manager_id,
            body_version=1,
            manager_runtime_request={"session_instance": instance},
        ).get("manager_runtime", {})

    status = await_value(lambda: s if (s := native()).get("ready_for_input") else None)
    binding = status["binding"]
    external = status["turn"]["external_input"]
    text = "Open a worker for the linked board fixture."
    prompt = (
        "Send messages to this discussion through publish. Use swarm dispatcher-guide before delegating. "
        "Use swarm spawn to delegate work in the VM, and inspect the exact worker with swarm peek. "
        "Ask workers to use swarm teammate for needs-input and completion notes.\n\n"
        + text
    )
    source_id = "401"
    request_id = "da_" + digest(["fixture", source_id])[:32]
    revision = digest(["300", text, "400", "", source_id, None, None, None])
    discussion_id = discussion.removeprefix("discord-teammate-")
    nonce = digest([discussion_id, request_id, revision, binding, external])
    composer = {
        "session": manager_id,
        "session_instance": instance,
        "text": prompt,
        "manager_admission": {
            "binding": binding,
            "expected_external_input": external,
            "nonce": nonce,
        },
        "origin": {
            "kind": "owner_direct",
            "installation_id": "fixture",
            "request_id": request_id,
            "actor_id": "300",
            "requester_id": "300",
            "channel_id": "400",
            "message_id": source_id,
            "discussion_id": discussion_id,
            "revision": revision,
        },
    }
    semantic = f"semantic:{time.time_ns() // 1_000_000}:{2:032x}"
    # This synthetic Discord source and cutover are consumed by the real manager and shim.
    ledger = place / "rust-text-v1.json"
    data = json.loads(ledger.read_text())
    data["messages"][source_id] = {
        "operation_id": request_id,
        "revision": revision,
        "text": text,
        "route": {"guild_id": "", "channel_id": "400", "message_id": source_id},
        "author": {"id": "300"},
        "authority": "owner_direct",
        "supervisor_socket": env["SWARM_DAEMON_SOCK"],
        "state": "sending",
        "native_input_id": nonce,
        "native_binding": binding,
        "edited_at": None,
    }
    data["manager"] = {
        "generation": 1,
        "launch": launch,
        "launch_operation": operation,
        "session": manager_id,
        "binding": binding,
        "input": {"operation": semantic, "source_id": source_id, "composer": composer},
    }
    private(ledger, data)
    sent = wire(
        "semantic_send",
        session_id=manager_id,
        body_version=1,
        operation_id=semantic,
        composer_send=composer,
    )
    assert sent["op"] == "ok", sent
    await_value(lambda: native().get("turn", {}).get("native_input_id") == nonce)
    document = root / "delegation.md"
    document.write_text("# Fixture delegation\nInspect the named local workspace.\n")
    spawn_env = dict(
        env,
        SWARM_SESSION_ID=manager_id.split("/", 1)[1],
        SWARM_DISPATCHER_SPAWN_PROOF=(root / "spawn-proof").read_text(),
    )
    cmd = [
        binary,
        "spawn",
        "--cli",
        "codex",
        "--dir",
        launch["cwd"],
        "--worktree=false",
        "--name",
        "Fixture delegated worker",
        "--delegate",
        document,
        "--operation-id",
        f"launch:{time.time_ns() // 1_000_000}:{3:032x}",
    ]
    worker = subprocess.run(
        cmd,
        env=spawn_env,
        cwd=launch["cwd"],
        text=True,
        capture_output=True,
        timeout=45,
        check=True,
    )
    return worker.stdout.strip()


@contextmanager
def linked_owners(root, local_binary, remote_binary, fake_codex):
    root = Path(root).resolve()
    home, tools, local_state, remote_state, work = [
        root / n for n in ("home", "bin", "local", "remote", "work")
    ]
    for directory in (home, tools, local_state, remote_state, work):
        directory.mkdir(mode=0o700, exist_ok=True)
    shutil.copy2(Path(__file__).with_name("claude"), tools / "claude")
    codex = tools / "codex"
    codex.write_text(
        "#!/bin/sh\n"
        "if [ \"$1\" = --version ]; then echo 'codex-cli 0.167.0'; exit 0; fi\n"
        "umask 077\n"
        'if [ -n "$SWARM_DISPATCHER_SPAWN_PROOF" ]; then printf %s "$SWARM_DISPATCHER_SPAWN_PROOF" > '
        + shlex.quote(str(root / "spawn-proof"))
        + "; fi\nexec "
        + shlex.quote(str(Path(fake_codex).resolve(strict=True)))
        + ' "$@"\n'
    )
    codex.chmod(0o700)
    native_home = home / ".codex"
    native_home.mkdir(mode=0o700)
    private(
        native_home / "auth.json",
        {"auth_mode": "chatgpt", "tokens": {"account_id": "board-fixture"}},
    )
    local_env = fixture_env(root, local_state, tools)
    remote_env = fixture_env(root, remote_state, tools)
    place = remote_state / "discord"
    bridge = place / "manager-v1"
    bridge.mkdir(mode=0o700, parents=True)
    place.chmod(0o700)
    origin = remote_env["SWARM_DAEMON_SOCK"]
    cfg = {
        "version": 1,
        "enabled": True,
        "mode": "personal",
        "name": "Fixture dispatcher",
        "installation_id": "fixture",
        "owner_id": "300",
        "application_id": "200",
        "dm_channel_id": "400",
        "manager_cli": "codex",
        "work_dir": str(work),
        "daemon_socket": origin,
        "token_file": str(root / "unused-token"),
        "supervisor": {
            "state_dir": str(remote_state),
            "lock": remote_env["SWARM_DAEMON_LOCK"],
            "log": str(remote_state / "log"),
        },
    }
    remote_go = "(Rust)" not in subprocess.check_output(
        [remote_binary, "version"], env=remote_env, text=True
    )
    cfg["project_access"] = "vm"
    private(place / "config.json", cfg)
    private(place / ".rust-offline-owner.json", "fixture")
    if not remote_go:
        token = Path(cfg["token_file"])
        token.write_text("offline-linked-fixture")
        token.chmod(0o600)
        result = subprocess.run(
            [remote_binary, "discord", "connect", "--state-dir", str(place), "--project-access", "vm"],
            env=remote_env, text=True, capture_output=True, timeout=10,
        )
        assert result.returncode == 0, result.stderr
        assert json.loads(result.stdout)["project_access"] == "vm"
    platform = (
        (home / "Library/Application Support")
        if sys.platform == "darwin"
        else Path(local_env["XDG_CONFIG_HOME"])
    )
    slot = platform / "swarm/installed-discord"
    slot.mkdir(parents=True, mode=0o700)
    private(slot / "discord-active.json", str(place))
    auth = bridge / "bridge.auth"
    auth.write_text("a" * 64)
    auth.chmod(0o600)
    address = bridge / "bridge.sock"
    coordinate = json.dumps(["fixture", "", "400"], separators=(",", ":")).encode()
    discussion = "discord-teammate-dt_" + sha256(coordinate).hexdigest()[:32]
    launch = {
        "agent": "codex",
        "name": "Fixture dispatcher",
        "cwd": str(work),
        "options": {},
        "env": None,
        "cols": 120,
        "rows": 32,
        "initial_prompt": "",
        "manager": {
            "conversation_id": discussion,
            "native_cli": True,
            "bridge_socket": str(address),
            "auth_file": str(auth),
        },
    }
    operation = f"launch:{time.time_ns() // 1_000_000}:{1:032x}"
    private(
        place / "rust-text-v1.json",
        {
            "version": 3,
            "identity": ["fixture", "200", "300", "400", origin],
            "messages": {},
            "outbox": {},
            "manager": {
                "generation": 1,
                "launch": launch,
                "launch_operation": operation,
            },
        },
    )

    def spawn(env, name):
        spec = {
            "agent": "claude",
            "name": name,
            "cwd": str(work),
            "cols": 80,
            "rows": 24,
        }
        reply = owner(env, {"op": "launch", "launch": spec}, timeout=45)
        assert reply["op"] == "launch", reply
        return reply["session"]["id"]

    with ExitStack() as resources:
        listener = resources.enter_context(socket.socket(socket.AF_UNIX))
        listener.bind(str(address))
        address.chmod(0o600)
        listener.listen()
        resources.enter_context(daemon(local_binary, local_env, root / "local.log"))
        resources.enter_context(daemon(remote_binary, remote_env, root / "remote.log"))
        try:
            opened = owner(
                remote_env,
                {"op": "owner_launch", "operation_id": operation, "launch": launch},
                caps=CAPS,
                timeout=45,
            )
            assert (
                opened["op"] == "launch" and opened["session"]["role"] == "manager"
            ), opened
            manager = opened["session"]["id"]
            worker = delegate(
                root,
                remote_env,
                remote_binary,
                place,
                launch,
                manager,
                discussion,
                operation,
            )
            unrelated = spawn(remote_env, "Unrelated remote row")
            local = spawn(local_env, "Local row")
            yield dict(
                local_env=local_env,
                remote_env=remote_env,
                manager=manager,
                worker=worker,
                unrelated=unrelated,
                local=local,
                slot=slot,
                place=place,
                work=work,
            )
        finally:
            for env in (remote_env, local_env):

                def active():
                    rows = owner(env, {"op": "list"}).get("sessions") or []
                    return [
                        row["id"]
                        for row in rows
                        if row["status"]["Process"] == "running"
                    ]

                for sid in active():
                    owner(env, {"op": "kill", "session_id": sid})
                await_value(lambda: not active())


if __name__ == "__main__":
    if len(sys.argv) != 4:
        sys.exit(
            "usage: linked_owners.py LOCAL_OWNER REMOTE_OWNER PINNED_MANAGER_FIXTURE"
        )
    with (
        tempfile.TemporaryDirectory(prefix="lb-", dir=Path("/tmp").resolve()) as tmp,
        linked_owners(Path(tmp), *(Path(s).resolve() for s in sys.argv[1:])) as case,
    ):
        rows = owner(case["remote_env"], {"op": "list"})["sessions"]
        assert {row["id"] for row in rows} == {
            case["manager"],
            case["worker"],
            case["unrelated"],
        }
        assert any(
            row["id"] == case["worker"]
            and row["spawned_from"] == case["manager"].split("/", 1)[1]
            for row in rows
        )
        print(
            "Two actual owners, scoped manager, delegated worker and unrelated session passed"
        )
