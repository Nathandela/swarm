"""Refuse stopped managed Go OpenCode without reopening its native database."""

from contextlib import closing
from pathlib import Path
import sqlite3
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from stopped_go_state import daemon, disk, listed, owner, refused


def rehearse(go, rust, root, configured_env, wait, requests):
    state = root / "state"
    env = configured_env | {"SWARM_DAEMON_SOCK": str(state / "daemon.sock"),
                            "SWARM_DAEMON_LOCK": str(state / "daemon.lock"),
                            "SWARM_DAEMON_REMOTE_SOCK": str(state / "remote.sock")}
    prompt = "STOPPED_ORIGINAL_GO_OPEN_CODE. Reply with the private fixture only."
    launch = {"agent": "opencode", "name": "Stopped Go OpenCode",
              "cwd": str(root / "project"), "rows": 28, "cols": 100,
              "options": {"model": "fixture/fixture", "agent": "build"},
              "initial_prompt": prompt, "env": [f"{k}={v}" for k, v in env.items()]}
    with daemon(go, env, root / "go.log"):
        reply = owner(env, {"op": "launch", "launch": launch}, timeout=55)
        assert reply["op"] == "launch", reply
        session_id = reply["session"]["id"]
        session = state / session_id.split("/")[-1]
        try:
            wait(lambda: disk(session / "meta.json")["opencode_binding"]["phase"] == "ready"
                 and listed(env)[0]["status"]["Turn"] == "idle"
                 and any(request["primary"] for request in requests))
        finally:
            assert owner(env, {"op": "kill", "session_id": session_id})["op"] == "ok"
            wait(lambda: listed(env)[0]["status"]["Process"] == "exited")
        stopped, = listed(env)
    metadata = disk(session / "meta.json")
    binding = metadata["opencode_binding"]
    assert metadata["schema_version"] == 3 and binding["phase"] == "ready"
    assert disk(session / "exit.json")["exit_code"] == metadata["exit_code"]
    assert binding["native_created_at_ms"] > 0 and metadata.get("account_binding") is None
    database = root / "data/opencode/opencode.db"
    assert database.is_file()
    with closing(sqlite3.connect(f"{database.as_uri()}?mode=ro", uri=True)) as reader:
        assert reader.execute("SELECT id, directory, time_created FROM session WHERE id=?",
                              (metadata["conversation_id"],)).fetchone() == (
            metadata["conversation_id"], launch["cwd"], binding["native_created_at_ms"])
        turns, = reader.execute(
            "SELECT count(*) FROM part WHERE session_id=? AND json_extract(data, '$.text')=?",
            (metadata["conversation_id"], prompt)).fetchone()
        assert turns == 1

    def preserved():
        return ({p.name: p.read_bytes() for p in session.iterdir() if p.is_file()},
                {p.name: p.read_bytes() for p in database.parent.glob("opencode.db*")},
                sorted(p.name for p in state.iterdir() if p.is_dir()), len(requests))

    before = preserved()
    compat = env | {"SWARM_DAEMON_STOPPED_GO_COMPAT": "1"}
    for test_env in (compat, compat | {"SWARM_DAEMON_LIVE_GO_COMPAT": "1"}):
        refused(rust, test_env, "Go schema Some(3) requires offline qualification")
        assert preserved() == before
    with daemon(go, env, root / "go-restored.log"):
        restored, = listed(env)
        assert restored["status"]["Process"] == "exited"
        for key in ("id", "cwd", "agent_type", "exit_code", "conversation_id"):
            assert restored.get(key) == stopped.get(key), key
    after = preserved()
    assert after[1:] == before[1:]
    assert disk(session / "meta.json")["opencode_binding"] == binding
    assert after[0]["exit.json"] == before[0]["exit.json"]

    with daemon(go, env, root / "go-restored.log"):
        resume = launch | {"options": {"resume_from": session_id}, "initial_prompt": ""}
        reply = owner(env, {"op": "launch", "launch": resume}, timeout=55)
        assert reply["op"] == "launch", reply
        resumed_id = reply["session"]["id"]
        resumed_path = state / resumed_id.split("/")[-1] / "meta.json"
        try:
            def ready_resume():
                row = disk(resumed_path)
                return row if row["opencode_binding"]["phase"] == "ready" else None

            row = wait(ready_resume)
            assert resumed_id != session_id and row["conversation_id"] == metadata["conversation_id"]
            resumed = row["opencode_binding"]
            assert resumed["mode"] == "resume" and resumed["generation"] != binding["generation"]
            for key in ("native_created_at_ms", "profile_env"):
                assert resumed[key] == binding[key], key
            assert len(requests) == before[-1]
        finally:
            assert owner(env, {"op": "kill", "session_id": resumed_id})["op"] == "ok"
            wait(lambda: any(row["id"] == resumed_id and row["status"]["Process"] == "exited"
                             for row in listed(env)))
    print("PASS stopped managed Go OpenCode: Rust preserved source; Go resumed the same native root")
