"""Witness the ordinary vendor agy transcript and the hands-off launch boundary."""

import http.server
import json
import shlex
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path

from native_fixture_sandbox import check_guards, guarded
from parity.test_board import owner_request, p
from rust_agy_schedule import Model, MARKER, SUMMARY


class HandoffModel(Model):
    def do_POST(self):
        length = int(self.headers["Content-Length"])
        assert 0 <= length < 8 * 2**20
        body = json.loads(self.rfile.read(length))
        with self.server.guard:
            self.server.calls.append(json.dumps(body.get("contents", []), ensure_ascii=False))
        self.respond(
            {"candidates": [{"content": {"role": "model", "parts": [{"text": SUMMARY}]},
                             "finishReason": "STOP", "index": 0}]},
            "streamGenerateContent" in self.path,
        )


def eventually(test, label, timeout=18):
    until = time.monotonic() + timeout
    while time.monotonic() < until:
        if result := test():
            return result
        time.sleep(.12)
    raise AssertionError(f"timed out waiting for {label}")


def verify(candidate, native, root):
    home, work, tools, state = (root / name for name in ("home", "work", "bin", "state"))
    for path in (home, work, tools, state):
        path.mkdir(mode=0o700)
    config = home / ".gemini/antigravity-cli"
    (config / "cache").mkdir(parents=True)
    (config / "settings.json").write_text(json.dumps({"modelProvider":"gemini", "telemetry":{"enabled":False}}))
    (config / "cache/onboarding.json").write_text(json.dumps({"consumerOnboardingComplete":True,
                                                              "onboardingComplete":True}))
    (config / "jetski_state.pbtxt").write_text(
        'post_onboarding { completed_steps: POST_ONBOARDING_STEP_TYPE_MANAGER_WELCOME '
        'completed_steps: POST_ONBOARDING_STEP_TYPE_USAGE_MODE '
        'completed_steps: POST_ONBOARDING_STEP_TYPE_AGENT_CONFIGURATION '
        'completed_steps: POST_ONBOARDING_STEP_TYPE_ADD_WORKSPACE }\n'
        'installation_uuid: "11111111-1111-4111-8111-111111111111"\n')
    model = http.server.ThreadingHTTPServer(("127.0.0.1", 0), HandoffModel)
    model.daemon_threads = True
    model.guard, model.calls = threading.Lock(), []
    port = model.server_port
    threading.Thread(target=model.serve_forever, daemon=True).start()
    wrapper = tools / "agy"
    wrapper.write_text(
        "#!/bin/sh\nexport GEMINI_API_KEY=synthetic-key GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:"
        + str(port) + " TMPDIR=" + shlex.quote(str(root)) + "\nexec "
        + shlex.quote(str(native)) + ' "$@"\n')
    wrapper.chmod(0o700)
    sock = str(root / "d")
    env = {
        "HOME":str(home), "PATH":f"{tools}:/usr/bin:/bin", "SHELL":"/bin/sh",
        "TERM":"xterm-256color", "LANG":"C.UTF-8", "TMPDIR":str(root),
        "SWARM_DAEMON_STATE":str(state), "SWARM_DAEMON_SOCK":sock, "SWARM_DAEMON_REMOTE_SOCK":"",
        "SWARM_DAEMON_LOCK":str(root / "lock"), "SWARM_DAEMON_LOG":str(root / "daemon.log"),
    }
    check_guards(root, [port])
    version = subprocess.check_output(guarded([str(native), "--version"], root, [port]),
                                      env=env, cwd=work, text=True, timeout=10).strip()
    assert version in ("1.4.7", "1.5.17"), version

    def cli(*args, check=True):
        return subprocess.run([candidate, *args], cwd=work, env=env, capture_output=True,
                              text=True, check=check, timeout=12)

    def rows():
        return json.loads(cli("ls", "--json").stdout) or []

    def offer(sid):
        return owner_request(sock, "hands-off-handoff", {"op":"launch", "launch":{
            "agent":"agy", "cwd":str(root / "wrong-project"), "initial_prompt":"FORGED_INITIAL",
            "options":{"handoff_from":sid}, "env":[f"{k}={v}" for k, v in env.items()],
        }})

    with (root / "daemon.log").open("w") as log:
        daemon = subprocess.Popen(guarded([str(candidate), "daemon"], root, [port]),
                                  cwd=work, env=env, stdout=log, stderr=log)
    board = None
    try:
        eventually(lambda: not cli("ls", "--json", check=False).returncode, "owner socket")
        sid = cli("spawn", "--cli", "agy", "--name", "VendorSource", "--dir", work,
                  "--prompt", "Reply with the private fixture summary. " + MARKER).stdout.strip()
        session = state / "rust-v1" / sid.split("/")[-1]
        observation = session / "observation.json"

        def trust_prompt():
            return "trust the contents" in cli("peek", sid, check=False).stdout

        eventually(trust_prompt, "native scratch-workspace trust prompt")
        cli("send", sid, "--key", "enter")

        def native_log():
            for path in config.glob("brain/*/.system_generated/logs/transcript.jsonl"):
                complete = [json.loads(line) for line in path.read_bytes().splitlines(keepends=True)
                            if line.endswith(b"\n")]
                if any(row.get("type") == "USER_INPUT" and MARKER in row.get("content", "") for row in complete) \
                    and any(row.get("type") == "PLANNER_RESPONSE" for row in complete):
                    return path

        source = eventually(native_log, "native USER_INPUT and PLANNER_RESPONSE")
        thread = source.parents[2].name

        def bound():
            return observation.exists() and json.loads(observation.read_text())["conversation_id"] == thread

        if version == "1.4.7":
            assert not bound(), "1.4.7 unexpectedly supplied a live conversation binding"
            assert offer(sid)["op"] == "error", "unbound live source was offered"
            cli("send", sid, "--text", "/exit")
            eventually(lambda: rows()[0]["status"]["Process"] == "exited", "graceful native exit")
            eventually(bound, "native exit footer conversation ID")
        else:
            eventually(bound, "live vendor conversation ID")
            assert rows()[0]["status"]["Process"] == "running"
        # A matching sibling and a symlink at the direct native path cannot stand
        # in for the bound conversation's regular file.
        old = source.with_name("original.jsonl")
        source.rename(old)
        source.symlink_to(old)
        try:
            assert offer(sid)["op"] == "error", "symlink transcript was offered"
            assert len(rows()) == 1
        finally:
            source.unlink()
            old.rename(source)

        board = p.Board(guarded([str(candidate)], root, [port]), str(work), env)
        board.wait_bytes(b"VendorSource")
        board.send(b"h")
        board.wait_bytes("● agy".encode())
        if version != "1.4.7":
            board.send(b"\t\t\t\x1b[C")
        board.wait_bytes(b"hands-off")
        board.send(b"\r")

        def successor():
            board.drain()
            return next((row for row in rows() if row.get("spawned_from") == sid.split("/")[-1]), None)

        child = eventually(successor, "public board hands-off successor")
        assert child["cwd"] == str(work), child
        prompt = eventually(
            lambda: next((call for call in model.calls if str(source) in call and thread in call), None),
            "actual successor request with original native transcript pointer",
        )
        assert "FORGED_INITIAL" not in prompt
        assert next(row for row in rows() if row["id"] == sid)["status"]["Process"] == (
            "exited" if version == "1.4.7" else "running"
        )
        print(f"agy {version}: public hands-off used the authenticated "
              f"{'exited' if version == '1.4.7' else 'live'} native transcript and exact project.")
    finally:
        if board:
            board.close()
        for row in rows():
            if row["status"]["Process"] == "running":
                cli("kill", row["id"], check=False)
        daemon.terminate()
        daemon.wait(timeout=10)
        model.shutdown()
        model.server_close()


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: rust_agy_handoff.py CANDIDATE VENDOR_AGY")
    with tempfile.TemporaryDirectory(prefix="agy-ho-", dir=Path("/tmp").resolve()) as scratch:
        verify(*(Path(arg).resolve(strict=True) for arg in sys.argv[1:]), Path(scratch))
