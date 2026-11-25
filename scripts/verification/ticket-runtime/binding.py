"""Read trusted launch facts written by a dedicated Swarm supervisor before the shim starts."""

import json
import os
from pathlib import Path
import re
import stat
import time


def require(condition, message):
    if not condition:
        raise ValueError(message)


def owned(path, kind, secret=True):
    info = path.lstat()
    require(
        path.is_absolute() and path.resolve() == path and kind(info.st_mode),
        "unexpected path or symlink",
    )
    require(
        info.st_uid in (os.getuid(), 0) and (not secret or not info.st_mode & 0o022),
        "unsafe owner or write permissions",
    )
    return path


def private_json(path):
    owned(path.parent, stat.S_ISDIR)
    owned(path, stat.S_ISREG)
    require(
        path.stat().st_mode & 0o077 == 0 and path.stat().st_size <= 1048576,
        "unsafe record permissions or size",
    )
    return json.loads(path.read_text())


def session_binding(config, profile, sessions, sid, workspace):
    active = private_json(profile / "assignment.json")
    key = active["assignment"]
    require(
        re.fullmatch(r"[a-zA-Z0-9_-]{1,100}", key)
        and active["expires_at"] > time.time(),
        "no active assignment",
    )
    require(
        active["generation"] == config["generation"]
        and active["supervisor"] == str(sessions),
        "assignment belongs to a different supervisor",
    )
    expected_socket = active["bridge_socket"]
    expected_conversation = active["conversation"]
    expected_auth = active["auth_file"]
    require(
        re.fullmatch("discord-teammate-[0-9a-f]{64}", expected_conversation),
        "invalid discussion",
    )
    workspaces = owned(Path(config["workspaces"]), stat.S_ISDIR)
    require(
        workspace != workspaces
        and workspace.is_relative_to(workspaces)
        and str(workspace) == active["workspace"],
        "assignment does not authorize this workspace",
    )
    for private in (
        profile,
        sessions,
        Path(expected_socket).parent,
        Path(expected_auth).parent,
    ):
        require(
            not workspace.is_relative_to(private)
            and not private.is_relative_to(workspace),
            "workspace overlaps private state",
        )
    require(
        all(
            not Path(p).is_relative_to(workspace)
            for p in active["provider_sockets"].values()
        ),
        "workspace contains provider capabilities",
    )
    meta = private_json(sessions / sid / "meta.json")
    shim = private_json(sessions / sid / "shim-launch.json")
    require(
        meta["id"] == sid
        and shim["session_id"] == sid
        and shim["session_dir"] == str(sessions / sid),
        "different session",
    )
    require(
        meta["agent_type"] == "codex"
        and shim["native_provider"] == "codex"
        and shim["cwd"] == str(workspace),
        "different provider or workspace",
    )
    require(
        shim["backend_program"] == str(profile / "codex")
        and shim["argv"][0] == str(profile / "codex"),
        "unconfined native provider",
    )
    require(
        shim["backend_socket_path"] == str(sessions / sid / "codex.sock"),
        "different native endpoint",
    )

    def manager(row):
        return (
            row.get("role") == "manager"
            and not row.get("manager_native_cli")
            and row.get("manager_conversation_id") == expected_conversation
            and row.get("manager_bridge_socket") == expected_socket
            and row.get("manager_auth_file") == expected_auth
        )

    values = [
        value.partition("=")[2]
        for value in shim.get("env", ())
        if value.startswith("SWARM_TEAMMATE_WORKER_BRIDGE=")
    ]
    token = None
    if manager(meta):
        require(
            not values and shim.get("backend_required") is True,
            "manager bridge mismatch",
        )
        role = "dispatcher"
    else:
        parent = meta.get("spawned_from", "")
        require(
            re.fullmatch("[a-z2-7]{16}", parent)
            and meta.get("spawn_intent") == "delegate"
            and len(values) == 1,
            "worker lacks a typed parent or bridge",
        )
        parent_meta = private_json(sessions / parent / "meta.json")
        require(
            parent_meta["id"] == parent and manager(parent_meta),
            "worker belongs to a different manager",
        )
        socket, _, token = values[0].rpartition(":")
        require(
            socket == expected_socket and re.fullmatch("[0-9a-f]{64}", token),
            "wrong worker bridge",
        )
        role = "worker"
    require(
        active["provider_sockets"][role] != expected_socket,
        "provider cannot use controller transport",
    )
    return active, role, token
