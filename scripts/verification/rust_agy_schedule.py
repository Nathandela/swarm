"""Qualify scheduled ACP using an unmodified vendor executable and loopback Gemini model."""

import http.server
import json
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path

from native_fixture_sandbox import check_guards, guarded

SUMMARY = "Native scheduled ACP fixture completed."
MARKER = "SWARM_NATIVE_ACP_OPENING_5F31"
COMPLETE = "mcp_swarm_schedule_complete"


class Model(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def respond(self, body, stream=False):
        raw = json.dumps(body).encode()
        if stream:
            raw = b"data: " + raw + b"\n\n"
        self.send_response(200)
        self.send_header(
            "Content-Type", "text/event-stream" if stream else "application/json"
        )
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        self.respond(
            {
                "models": [
                    {
                        "name": "models/gemini-3-pro",
                        "displayName": "Fixture",
                        "supportedGenerationMethods": ["generateContent"],
                    }
                ]
            }
        )

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        assert 0 <= length < 8 * 2**20
        body = json.loads(self.rfile.read(length))
        tools = [
            t
            for group in body.get("tools", [])
            for t in group.get("functionDeclarations", [])
        ]
        marker = MARKER in json.dumps(body.get("contents", []))
        responses = [
            part["functionResponse"]
            for entry in body.get("contents", [])
            for part in entry.get("parts", [])
            if "functionResponse" in part
        ]
        completed = any(
            result.get("name") == COMPLETE
            and "Receipt recorded" in str(result.get("response", {}))
            for result in responses
        )
        with self.server.guard:
            self.server.observed.append((marker, completed))
        parts = [{"text": SUMMARY}]
        spec = next((tool for tool in tools if tool["name"] == COMPLETE), None)
        if (
            marker
            and spec
            and not any(result.get("name") == COMPLETE for result in responses)
        ):
            schema = spec.get("parametersJsonSchema", spec.get("parameters", {}))
            args = {"summary": SUMMARY, "links": []}
            for key, val in (
                ("toolSummary", "Record the fixture completion"),
                ("toolAction", "Record completion"),
            ):
                if key in schema.get("properties", {}):
                    args[key] = val
            parts = [{"functionCall": {"name": COMPLETE, "args": args}}]
        self.respond(
            {
                "candidates": [
                    {
                        "content": {"role": "model", "parts": parts},
                        "finishReason": "STOP",
                        "index": 0,
                    }
                ],
                "usageMetadata": {
                    "promptTokenCount": 1,
                    "candidatesTokenCount": 1,
                    "totalTokenCount": 2,
                },
            },
            "streamGenerateContent" in self.path,
        )


def verify(binary, native, root):
    home, work, tools, state = (root / n for n in ("home", "work", "bin", "state"))
    for path in (home, work, tools, state):
        path.mkdir(mode=0o700)
    config = home / ".gemini/antigravity-cli"
    (config / "cache").mkdir(parents=True)
    (config / "settings.json").write_text(
        json.dumps({"modelProvider": "gemini", "telemetry": {"enabled": False}})
    )
    (config / "cache/onboarding.json").write_text(
        json.dumps(
            {
                "consumerOnboardingComplete": True,
                "enterpriseOnboardingComplete": False,
                "onboardingComplete": True,
            }
        )
    )
    (config / "jetski_state.pbtxt").write_text(
        'post_onboarding { completed_steps: POST_ONBOARDING_STEP_TYPE_MANAGER_WELCOME completed_steps: POST_ONBOARDING_STEP_TYPE_USAGE_MODE completed_steps: POST_ONBOARDING_STEP_TYPE_AGENT_CONFIGURATION completed_steps: POST_ONBOARDING_STEP_TYPE_ADD_WORKSPACE }\ninstallation_uuid: "11111111-1111-4111-8111-111111111111"\n'
    )
    model = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Model)
    model.daemon_threads = True
    model.guard = threading.Lock()
    model.observed = []
    port = model.server_port
    threading.Thread(target=model.serve_forever, daemon=True).start()
    # Both Swarm generations filter arbitrary child variables. Only this fixture redirects the vendor client.
    agy = tools / "agy"
    agy.write_text(
        "#!/bin/sh\nexport GEMINI_API_KEY=synthetic-key GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:"
        + str(port)
        + " TMPDIR="
        + shlex.quote(str(root))
        + "\nexec "
        + shlex.quote(str(native))
        + ' "$@"\n'
    )
    agy.chmod(0o700)
    env = {
        "HOME": str(home),
        "PATH": f"{tools}:/usr/bin:/bin",
        "SHELL": "/bin/sh",
        "TERM": "xterm-256color",
        "LANG": "C.UTF-8",
        "TMPDIR": str(root),
        "SWARM_DAEMON_STATE": str(state),
        "SWARM_DAEMON_SOCK": str(root / "d"),
        "SWARM_DAEMON_REMOTE_SOCK": "",
        "SWARM_DAEMON_LOCK": str(root / "lock"),
        "SWARM_DAEMON_LOG": str(root / "daemon.log"),
    }
    check_guards(root, [port])
    version = subprocess.check_output(
        guarded([str(native), "--version"], root, [port]),
        env=env,
        cwd=work,
        text=True,
        timeout=10,
    ).strip()
    assert version == "1.5.17", f"Unqualified agy version {version}"

    def cli(*args, stdin=None, check=True):
        run = subprocess.run(
            [binary, *args],
            check=False,
            cwd=work,
            env=env,
            capture_output=True,
            text=True,
            timeout=20,
            input=stdin,
        )
        if check and run.returncode:
            raise RuntimeError(str(args[:2]) + ": " + run.stderr[-600:])
        return run

    def schedule(verb, *args, **kwargs):
        return cli("schedule", verb, *args, **kwargs)

    with (root / "daemon-err").open("w") as log:
        process = subprocess.Popen(
            guarded([str(binary), "daemon"], root, [port]),
            cwd=work,
            env=env,
            stdout=subprocess.DEVNULL,
            stderr=log,
        )
    sid = None
    try:
        until = time.monotonic() + 8
        while cli("ls", "--json", check=False).returncode:
            assert process.poll() is None and time.monotonic() < until
            time.sleep(0.05)
        job = json.loads(
            schedule(
                "create",
                "--name",
                "Native ACP",
                "--dir",
                str(work),
                "--agents",
                "agy",
                "--authority",
                "agent-default",
                "--workspace",
                "directory",
                "--cron",
                "0 0 31 12 *",
                "--timezone",
                "UTC",
                "--prompt-file",
                "-",
                stdin="Use the scheduled completion tool with summary '"
                + SUMMARY
                + "' then repeat the summary. "
                + MARKER,
            ).stdout
        )
        schedule("run", job["id"])
        approved = False
        until = time.monotonic() + 80
        while time.monotonic() < until:
            schedule("tick")
            runs = json.loads(schedule("history", job["id"]).stdout) or []
            rows = json.loads(cli("ls", "--json").stdout) or []
            if rows:
                sid = rows[0]["id"]
                if (
                    not approved
                    and rows[0]["status"].get("Interaction") == "permission"
                ):
                    screen = cli("peek", sid, check=False).stdout
                    lines = [
                        line.strip() for line in screen.splitlines() if line.strip()
                    ]
                    if (
                        "Antigravity requests permission for Run mcp_swarm_schedule_complete"
                        in lines
                        and "1. Allow" in lines
                    ):
                        assert not re.search(r"option number followed by [a-f0-9]{12}", screen)
                        cli("send", sid, "--text", "1")
                        approved = True
            if runs and runs[0]["state"] in (
                "ready",
                "completed",
                "incomplete",
                "exhausted",
                "failed",
            ):
                break
            time.sleep(0.35)
        if not runs or runs[0]["state"] != "ready":
            outcomes = [
                (attempt["state"], attempt.get("detail"))
                for run in runs
                for attempt in run["attempts"]
            ]
            raise AssertionError(f"Native ACP completion missing: {outcomes}")
        (run,) = runs
        (attempt,) = run["attempts"]
        assert run["job_id"] == job["id"] and run["completion"]["summary"] == SUMMARY
        assert (
            attempt["agent"] == "agy"
            and attempt["session_id"] == sid
            and attempt["prompt_delivered"]
        )
        assert approved, "exact native completion permission was not tested"
        assert any(marker for marker, _ in model.observed)
        assert any(recorded for _, recorded in model.observed)
        print(
            f"agy {version}: a real scheduled ACP session received its scoped completion, matched the closing summary, and reached ready ({len(model.observed)} loopback model calls)"
        )
    finally:
        if sid:
            cli("kill", sid, check=False)
        process.terminate()
        process.wait(timeout=15)
        model.shutdown()
        model.server_close()


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: rust_agy_schedule.py CANDIDATE PINNED_AGY")
    root = Path(tempfile.mkdtemp(prefix="acp-", dir=Path("/tmp").resolve()))
    try:
        verify(*(Path(arg).resolve(strict=True) for arg in sys.argv[1:]), root)
    except BaseException:
        print(f"Retained private fixture at {root}", file=sys.stderr)
        raise
    shutil.rmtree(root)
