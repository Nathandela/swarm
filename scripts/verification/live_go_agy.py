#!/usr/bin/env python3
"""Keep an actual classic agy PTY alive through Go → Rust → Go."""

import http.server
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import threading
import time

from live_go_shim import LIVE, attach, connect, snapshot
from native_fixture_sandbox import check_guards, guarded
from rust_agy_handoff import HandoffModel, eventually
from stopped_go_state import daemon, disk, fixture_env, listed, owner, private, refused


def rehearse(go, rust, native, root, cold=False):
    home, work, tools, state = (root / name for name in ("home", "work", "bin", "state"))
    for path in (home, work, tools, state):
        path.mkdir(mode=0o700)

    config = home / ".gemini/antigravity-cli"
    (config / "cache").mkdir(parents=True)
    private(config / "settings.json", {"modelProvider":"gemini", "telemetry":{"enabled":False}})
    private(config / "cache/onboarding.json", {"consumerOnboardingComplete":True, "onboardingComplete":True})
    (config / "jetski_state.pbtxt").write_text(
        "post_onboarding { completed_steps: POST_ONBOARDING_STEP_TYPE_MANAGER_WELCOME "
        "completed_steps: POST_ONBOARDING_STEP_TYPE_USAGE_MODE completed_steps: POST_ONBOARDING_STEP_TYPE_AGENT_CONFIGURATION "
        "completed_steps: POST_ONBOARDING_STEP_TYPE_ADD_WORKSPACE }\n"
        'installation_uuid: "11111111-1111-4111-8111-111111111111"\n')

    model = http.server.ThreadingHTTPServer(("127.0.0.1", 0), HandoffModel)
    model.daemon_threads = True
    model.guard, model.calls = threading.Lock(), []
    port = model.server_port
    threading.Thread(target=model.serve_forever, daemon=True).start()
    check_guards(root, [port])
    go_env = fixture_env(root, state, tools) | {
        "TERM":"xterm-256color", "LANG":"C.UTF-8", "TMPDIR":str(root), "SHELL":"/bin/sh",
    }
    rust_env = go_env | {"SWARM_DAEMON_STOPPED_GO_COMPAT":"1", LIVE:"1"}
    assert subprocess.check_output(guarded([str(native), "--version"], root, [port]),
                                   env=go_env, cwd=work, timeout=10).strip() == b"1.5.17"

    worker_file = root / "worker-pid"
    wrapper = tools / "agy"
    wrapper.write_text(
        '#!/bin/sh\nif [ "$1" != --version ]; then printf "%s\\n" "$$" > ' + shlex.quote(str(worker_file)) + '; fi\n'
        'export GEMINI_API_KEY=synthetic-key GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:' + str(port)
        + ' TMPDIR=' + shlex.quote(str(root)) + '\nexec ' + shlex.quote(str(native)) + ' "$@"\n')
    wrapper.chmod(0o700)

    def bounded(binary, label):
        path = tools / label
        command = guarded([str(binary)], root, [port])
        path.write_text("#!/bin/sh\nexec " + shlex.join(command) + ' "$@"\n')
        path.chmod(0o700)
        return path

    go, rust = bounded(go, "go"), bounded(rust, "rust")
    active = None
    rust_held = None

    def visible(env):
        return "\n".join(snapshot(env, active)["lines"])

    def input_bytes(env, payload, enter=False):
        with connect(env) as connection:
            attach(connection, active)
            send = connection[0]
            send(3, payload)
            if enter:
                time.sleep(.25)
                send(3, b"\r")
            time.sleep(.15)

    def same_process():
        pid = int(worker_file.read_text())
        shim = disk(session / "meta.json")["shim_pid"]
        if sys.platform == "linux":
            lineage = []
            for current in (shim,pid):
                raw = Path(f"/proc/{current}/stat").read_text().rsplit(")",1)[1].split()
                lineage.append((current, int(raw[19])))
            assert int(raw[1]) == shim and int(raw[2]) == pid and int(raw[3]) == pid
            return lineage
        result = subprocess.check_output(["/bin/ps", "-p", f"{shim},{pid}",
                                        "-o", "pid=,ppid=,pgid=,lstart="], text=True)
        worker = next(line.split() for line in result.splitlines() if line.split()[0] == str(pid))
        assert worker[1:3] == [str(shim),str(pid)]
        return sorted((fields[0], fields[2:]) for fields in map(str.split, result.splitlines()))

    def transcript_for(prompt):
        def find():
            for path in config.glob("brain/*/.system_generated/logs/transcript.jsonl"):
                complete = [json.loads(line) for line in path.read_bytes().splitlines(keepends=True)
                            if line.endswith(b"\n")]
                starts = [i for i,row in enumerate(complete)
                          if row.get("type") == "USER_INPUT" and prompt in row.get("content", "")]
                if starts and any(row.get("type") == "PLANNER_RESPONSE" for row in complete[starts[-1]+1:]):
                    return path
        return eventually(find, f"native agy response to {prompt}", timeout=25)

    warmup = "AGY_GO_OWNER_ORIGINAL_24F1"
    rust_prompt = "AGY_RUST_OWNER_CONTINUED_24F2"
    rollback_prompt = "AGY_GO_OWNER_RESTORED_24F3"
    try:
        with daemon(go, go_env, root / "go.log"):
            request = {"agent":"agy", "cwd":str(work), "name":"Go native agy", "rows":30, "cols":100,
                       "initial_prompt":"Please reply with the fixture summary. " + warmup,
                       "env":[f"{name}={go_env[name]}" for name in ("HOME","PATH","TERM","LANG","SHELL")]}
            launched = owner(go_env, {"op":"launch", "launch":request}, timeout=40)
            assert launched["op"] == "launch", launched
            active = launched["session"]["id"]
            session = state / active.split("/")[-1]
            eventually(lambda: "trust the contents" in visible(go_env), "native scratch workspace trust")
            input_bytes(go_env, b"\r")
            transcript = transcript_for(warmup)
            thread = transcript.parents[2].name
            identities = same_process()
            print("pinned Go established original agy conversation", flush=True)

        forged = session / "native-process.json"
        assert not forged.exists()
        private(forged, {"schema_version":3})
        try:
            refused(rust, rust_env, "Go shim or backend custody remains")
            assert same_process() == identities, (same_process(), identities)
        finally:
            forged.unlink()

        with daemon(rust, rust_env, root / "rust.log"):
            assert listed(rust_env)[0]["id"] == active and same_process() == identities
            input_bytes(rust_env, ("Please reply with the fixture summary. " + rust_prompt).encode(), enter=True)
            assert transcript_for(rust_prompt) == transcript
            print("Rust attached and continued the original agy terminal", flush=True)

        with daemon(go, go_env, root / "go-rollback.log"):
            assert listed(go_env)[0]["id"] == active and same_process() == identities
            input_bytes(go_env, ("Please reply with the fixture summary. " + rollback_prompt).encode(), enter=True)
            assert transcript_for(rollback_prompt) == transcript
            input_bytes(go_env, b"/exit", enter=True)
            eventually(lambda: disk(session / "meta.json")["status"]["Process"] == "exited", "native agy exit")
            assert disk(session / "meta.json")["conversation_id"] == thread
        print("same session, native conversation and PTY across Go → Rust → Go", flush=True)
        if cold:
            cold_prompt = "AGY_RUST_CREATED_FOR_GO_24F4"
            with daemon(rust, rust_env, root / "rust-create.log"):
                created = owner(rust_env, {"op":"launch", "launch":request | {
                    "name":"Rust-created agy", "initial_prompt":"Please reply with the fixture summary. " + cold_prompt
                }}, timeout=30)
                assert created["op"] == "launch", created
                rust_held = active = created["session"]["id"]
                local = active.split("/")[-1]
                candidate_state = state / "rust-v1" / local
                original = transcript_for(cold_prompt)
                native_id = original.parents[2].name
                assert native_id != thread
            export = [rust, "daemon", "export-stopped-go"]
            live_refusal = subprocess.run(export, env=rust_env, capture_output=True, timeout=12)
            assert live_refusal.returncode and b"live or uncertain Rust shim" in live_refusal.stderr, live_refusal
            with daemon(rust, rust_env, root / "rust-drain.log"):
                input_bytes(rust_env, b"/exit", enter=True)
                eventually(lambda: (candidate_state / "go-stop-proof.json").exists()
                           and any(row["id"] == active and row["status"]["Process"] == "exited"
                                   for row in listed(rust_env)), "proved Rust agy stop")
            rust_held = None
            original_hold = original.with_name("unpublished-fixture.jsonl")
            original.rename(original_hold)
            try:
                result = subprocess.run(export, env=rust_env, capture_output=True, timeout=12)
                assert result.returncode and not (state / local).exists(), result
            finally:
                original_hold.rename(original)
            subprocess.run(export, env=rust_env, check=True, timeout=12)
            assert not (state / local / "shim-launch.json").exists()
            with daemon(go, go_env, root / "go-native-resume.log"):
                offered = owner(go_env, {"op":"launch","launch":request | {
                    "name":"Restored Rust agy", "initial_prompt":"",
                    "options":{"resume_from": active}
                }}, timeout=40)
                assert offered["op"] == "launch", offered
                active = offered["session"]["id"]
                session = state / active.split("/")[-1]
                resumed_prompt = "AGY_PINNED_GO_RESUMED_RUST_24F5"
                eventually(lambda: "trust the contents" in visible(go_env) or "Antigravity" in visible(go_env),
                           "restored native terminal")
                if "trust the contents" in visible(go_env):
                    input_bytes(go_env, b"\r")
                input_bytes(go_env, resumed_prompt.encode(), enter=True)
                assert transcript_for(resumed_prompt) == original
                input_bytes(go_env, b"/exit", enter=True)
                eventually(lambda: (session / "exit.json").exists(), "restored agy stopped")
            print("pinned Go resumed the stopped Rust-created agy conversation", flush=True)
    finally:
        if rust_held:
            with daemon(rust, rust_env, root / "rust-cleanup.log"):
                assert owner(rust_env, {"op":"kill", "session_id":rust_held})["op"] == "ok"
        if active and not rust_held and not (session / "exit.json").exists():
            with daemon(go, go_env, root / "cleanup.log"):
                assert owner(go_env, {"op":"kill", "session_id":active})["op"] == "ok"
                eventually(lambda: (session / "exit.json").exists(), "fixture cleanup")
        model.shutdown()
        model.server_close()


if __name__ == "__main__":
    os.umask(0o077)
    rehearse(*(Path(arg).resolve(strict=True) for arg in sys.argv[1:5]), cold="--cold" in sys.argv[5:])
