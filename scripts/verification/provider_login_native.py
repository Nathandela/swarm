"""Qualify the real provider-wide login coordinator in an isolated Linux VM.

Credential collection/restoration stays on that VM. Only aggregate continuity
evidence is printed; native transcripts and daemon logs stay in the private run.
"""
import argparse
import json
import os
import pathlib
import secrets
import signal
import socket
import struct
import subprocess
import time


def parse_args():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ["swarm", "seed", "native", "run-dir", "source-a", "source-b"]:
        p.add_argument("--" + name, required=True, type=pathlib.Path)
    p.add_argument("--provider", required=True, choices=["claude", "codex"])
    p.add_argument("--check-arguments", action="store_true")
    a = p.parse_args()
    for path in [a.swarm, a.seed, a.native]:
        if not path.is_file() or not os.access(path, os.X_OK):
            p.error("explicit executable binaries required")
    for path in [a.source_a, a.source_b]:
        if not path.is_dir() or path.resolve() != path:
            p.error("canonical existing source homes required")
    if a.source_a == a.source_b or a.run_dir.exists():
        p.error("distinct source homes and a new run directory required")
    if len(str(a.run_dir / "s" / ("x" * 16) / "backend.sock")) > 103:
        p.error("run path exceeds native Unix-socket budget")
    if not __debug__:
        p.error("assertions must remain enabled")
    return a


def readn(sock, length):
    out = b""
    while len(out) < length:
        part = sock.recv(length - len(out))
        if not part:
            raise RuntimeError("private protocol EOF")
        out += part
    return out


def read_frame(sock):
    length = struct.unpack(">I", readn(sock, 4))[0]
    assert 1 <= length <= 16 << 20
    raw = readn(sock, length)
    assert raw[0] == 1
    return json.loads(raw[1:])


def write_frame(sock, value):
    raw = json.dumps(value).encode()
    sock.sendall(struct.pack(">I", len(raw) + 1) + b"\x01" + raw)


def run(a):
    a.run_dir.mkdir(mode=0o700)
    state = a.run_dir / "s"
    state.mkdir(mode=0o700)
    host = a.run_dir / "home"
    host.mkdir(mode=0o700)
    binary = a.run_dir / "bin"
    binary.mkdir(mode=0o700)
    (binary / "swarm").symlink_to(a.swarm)
    (binary / a.provider).symlink_to(a.native)
    projects = [a.run_dir / "project-1", a.run_dir / "project-2"]
    for project in projects:
        project.mkdir(mode=0o700)
    env = {"HOME": str(host), "USER": os.environ.get("USER", "ubuntu"),
           "PATH": str(binary) + ":/usr/bin:/bin", "TERM": "xterm-256color",
           "SWARM_DAEMON_STATE": str(state), "SWARM_DAEMON_SOCK": str(state / "d.sock"),
           "SWARM_DAEMON_LOCK": str(state / "d.lock"), "SWARM_DAEMON_LOG": str(state / "d.log"),
           "DISABLE_AUTOUPDATER": "1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}
    result = {"provider": a.provider, "run": str(a.run_dir), "production_owner_protocol": True,
              "discussions": [], "steps": [], "a_b_a_passed": False,
              "existing_owner_processes_signaled": False, "credential_exported": False}
    daemon = None
    seeded = False
    log = None

    def custody(action):
        command = [str(a.seed), "-action", action, "-state", str(state), "-provider", a.provider,
                   "-source-a", str(a.source_a), "-source-b", str(a.source_b)]
        completed = subprocess.run(command, capture_output=True, text=True, timeout=30)
        if completed.returncode:
            raise RuntimeError("credential custody " + action + " refused: " + completed.stderr.strip()[:300])
        return json.loads(completed.stdout)

    def rpc(op, **body):
        with socket.socket(socket.AF_UNIX) as sock:
            sock.settimeout(40)
            sock.connect(str(state / "d.sock"))
            write_frame(sock, {"op": "hello", "protocol_version": 1,
                               "capabilities": ["semantic-send", "logins.manage.v1", "journal"]})
            hello = read_frame(sock)
            assert hello.get("op") == "hello"
            write_frame(sock, {"op": op, "endpoint_id": hello["endpoint_id"], **body})
            reply = read_frame(sock)
            if reply.get("op") == "error":
                raise RuntimeError("owner protocol " + op + " refused: " + str(reply.get("error_code", "")))
            return reply

    def registry():
        return json.loads((state / "logins/registry.json").read_text())

    def activate(login):
        view = rpc("login_manage", logins_request={"action": "list"})["logins_result"]
        rpc("login_manage", logins_request={"action": "activate", "provider": a.provider,
                                             "login_id": login, "expected_revision": view["revision"]})
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            current = registry()
            op = current.get("switches", {}).get(a.provider)
            active = current["providers"].get(a.provider, {})
            if not op and active.get("active_id") == login:
                return active["epoch"]
            if op and op.get("last_error") and time.monotonic() + 84 > deadline:
                raise RuntimeError("provider switch held: " + op["last_error"][:300])
            time.sleep(0.2)
        raise RuntimeError("provider switch deadline")

    def metas():
        return [json.loads(path.read_text()) for path in state.glob("*/meta.json")]

    def visible_rows():
        return [row for row in rpc("list").get("sessions", []) if row["agent"] == a.provider
                and not row.get("roster_hidden") and process(row) == "running"]

    def process(row):
        status = row.get("status", {})
        return status.get("process", status.get("Process"))

    def transcript(conversation):
        home = state / "logins/provider" / a.provider / "native"
        paths = list(home.rglob("*" + conversation + "*.jsonl"))
        assert len(paths) == 1, "exact native transcript missing or ambiguous"
        rows = [json.loads(line) for line in paths[0].read_text().splitlines() if line.strip()]
        users, answers, models, tools = [], [], [], []
        for row in rows:
            if a.provider == "claude":
                if row.get("isSidechain"):
                    continue
                message = row.get("message") or {}
                content = message.get("content", "")
                if isinstance(content, list):
                    for item in content:
                        if item.get("type") in ["tool_use", "tool_result"]:
                            tools.append(item["type"])
                    content = "\n".join(item.get("text", "") for item in content if item.get("type") == "text")
                if row.get("type") == "user" and isinstance(content, str):
                    users.append(content)
                if row.get("type") == "assistant" and isinstance(content, str) and content:
                    answers.append(content)
                    if message.get("model"):
                        models.append(message["model"])
            else:
                payload = row.get("payload") or {}
                if row.get("type") == "response_item":
                    kind = payload.get("type")
                    if kind == "message":
                        content = "\n".join(x.get("text", "") for x in payload.get("content", [])
                                            if x.get("type") in ["input_text", "output_text"])
                        if payload.get("role") == "user":
                            users.append(content)
                        elif payload.get("role") == "assistant":
                            answers.append(content)
                    elif kind in ["function_call", "custom_tool_call", "web_search_call"]:
                        tools.append(kind)
                if row.get("type") == "turn_context" and payload.get("model"):
                    models.append(payload["model"])
        return users, answers, models, tools

    def await_answer(marker, conversation, expected_prompts):
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            try:
                users, answers, models, tools = transcript(conversation)
                canary_users = [x for x in users if "SWARM_LOGIN_" in x or "first reply" in x]
                if answers and answers[-1].strip() == marker and len(canary_users) == expected_prompts:
                    assert not tools, "unexpected native tool execution"
                    return {"conversation_id": conversation, "marker_recalled": True,
                            "canary_prompt_count": len(canary_users), "tool_count": len(tools),
                            "native_model": models[-1] if models else ""}
            except (AssertionError, FileNotFoundError, json.JSONDecodeError):
                pass
            time.sleep(0.25)
        raise RuntimeError("native marker reply deadline")

    try:
        result["source_custody"] = custody("seed")
        seeded = True
        log = (a.run_dir / "daemon-private.log").open("w")
        daemon = subprocess.Popen([str(a.swarm), "daemon"], env=env, cwd=a.run_dir,
                                  stdout=log, stderr=log, start_new_session=True)
        until = time.monotonic() + 15
        while not (state / "d.sock").exists():
            if daemon.poll() is not None or time.monotonic() > until:
                raise RuntimeError("private daemon startup failed")
            time.sleep(0.1)
        saved = registry()["logins"]
        assert len(saved) == 2
        login_a, login_b = [x["id"] for x in saved]
        first_epoch = activate(login_a)
        native_home = state / "logins/provider" / a.provider / "native"
        # Disposable projects get explicit native trust preferences before any
        # writer starts; context initialization itself always uses production code.
        if a.provider == "claude":
            path = native_home / ".claude.json"
            settings = json.loads(path.read_text())
            settings.update({"hasCompletedOnboarding": True, "theme": "dark"})
            settings["projects"] = {str(p): {"hasTrustDialogAccepted": True,
                                                "hasCompletedProjectOnboarding": True} for p in projects}
            path.write_text(json.dumps(settings))
            path.chmod(0o600)
        else:
            with (native_home / "config.toml").open("a") as stream:
                for project in projects:
                    stream.write("\n[projects." + json.dumps(str(project)) + "]\ntrust_level = \"trusted\"\n")
        model = "haiku" if a.provider == "claude" else "gpt-6-luna"
        canaries = []
        for index, project in enumerate(projects):
            marker = "SWARM_LOGIN_" + secrets.token_hex(12)
            prompt = "Remember this marker for this conversation and reply exactly " + marker + ". Use no tools."
            options = {"model": model}
            if a.provider == "codex":
                options["sandbox"] = "read-only"
            reply = rpc("launch", launch={"agent": a.provider, "name": "qualification-" + str(index + 1),
                        "cwd": str(project), "options": options, "env": [k + "=" + v for k, v in env.items()],
                        "cols": 110, "rows": 36, "initial_prompt": prompt})
            sid = reply["session"]["id"]
            local = sid.split("/", 1)[-1]
            deadline = time.monotonic() + 45
            conversation = ""
            while time.monotonic() < deadline:
                meta = json.loads((state / local / "meta.json").read_text())
                conversation = meta.get("conversation_id", "")
                if conversation:
                    break
                time.sleep(0.2)
            assert conversation, "native conversation identity was not captured"
            canaries.append((marker, conversation, str(project)))
            result["discussions"].append(await_answer(marker, conversation, 1))
        result["steps"].append({"account": "A", "epoch": first_epoch, "discussions": 2})
        for account, login in [("B", login_b), ("A", login_a)]:
            epoch = activate(login)
            live = visible_rows()
            assert len(live) == 2, "whole provider fleet did not resume"
            step = {"account": account, "epoch": epoch, "discussions": []}
            for marker, conversation, cwd in canaries:
                candidates = [m for m in metas() if m.get("conversation_id") == conversation
                              and m.get("login_runtime", {}).get("epoch") == epoch
                              and process(m) == "running"]
                assert len(candidates) == 1 and candidates[0]["cwd"] == cwd
                meta = candidates[0]
                launch = json.loads((state / meta["id"] / "shim-launch.json").read_text())
                assert not any(marker in arg for arg in launch["argv"]), "continuation replayed an initial prompt"
                if a.provider == "codex":
                    assert "--model" not in launch["argv"], "continuation overrode the native saved model"
                sid = next(row["id"] for row in live if row["id"].endswith("/" + meta["id"]))
                before = transcript(conversation)
                expected = 2 if account == "B" else 3
                assert len([x for x in before[0] if "SWARM_LOGIN_" in x or "first reply" in x]) == expected - 1
                rpc("send_input", session_id=sid, send_input={"text": "What exact marker did you return in your first reply in this conversation? Reply only that marker. Use no tools.", "submit": True})
                proof = await_answer(marker, conversation, expected)
                proof["same_cwd"] = True
                original = result["discussions"][canaries.index((marker, conversation, cwd))]
                proof["same_model"] = proof["native_model"] == original["native_model"]
                assert proof["same_model"], "native model changed across login switch"
                step["discussions"].append(proof)
            result["steps"].append(step)
        result["a_b_a_passed"] = True
    except Exception as exc:
        result["failure"] = {"type": type(exc).__name__, "reason": str(exc)[:500]}
    finally:
        if daemon and daemon.poll() is None:
            try:
                for row in rpc("list").get("sessions", []):
                    if process(row) == "running":
                        rpc("kill", session_id=row["id"])
                deadline = time.monotonic() + 12
                while any(process(m) == "running" for m in metas()) and time.monotonic() < deadline:
                    time.sleep(0.2)
                proofs = []
                while time.monotonic() < deadline:
                    proofs = [json.loads(p.read_text()) for p in state.glob("*/native-writers-stopped.json")]
                    if len(proofs) == len(metas()) and all(p.get("writers_stopped") and p.get("descendants_contained") for p in proofs):
                        break
                    time.sleep(0.1)
                result["stopped_writer_proofs"] = {"count": len(proofs),
                    "all_stopped": bool(proofs) and all(p.get("writers_stopped") and p.get("descendants_contained") for p in proofs)}
                if result["a_b_a_passed"]:
                    assert len(proofs) == 6 and result["stopped_writer_proofs"]["all_stopped"], "whole-fleet stopped-writer proof missing"
            except Exception as exc:
                result["cleanup_failure"] = type(exc).__name__
                result["a_b_a_passed"] = False
            daemon.send_signal(signal.SIGTERM)
            try:
                daemon.wait(timeout=8)
            except subprocess.TimeoutExpired:
                daemon.kill()
                daemon.wait()
        if log:
            log.close()
        if seeded or (state / ("qualification-sources-" + a.provider + ".json")).exists():
            try:
                result["source_restoration"] = custody("restore")
            except Exception as exc:
                result["restoration_failure"] = str(exc)[:350]
                result["a_b_a_passed"] = False
        (a.run_dir / "result.json").write_text(json.dumps(result, indent=2) + "\n")
        print(json.dumps(result, indent=2))
    return 0 if result["a_b_a_passed"] else 1


if __name__ == "__main__":
    arguments = parse_args()
    raise SystemExit(0 if arguments.check_arguments else run(arguments))
