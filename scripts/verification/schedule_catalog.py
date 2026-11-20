"""Smoke-test schedule management through a built binary in a private directory."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def verify(binary, root):
    state = root / "state"
    repo = root / "gaston"
    repo.mkdir()
    subprocess.run(["git", "init", "-q", repo], check=True)
    subprocess.run(["git", "-C", repo, "-c", "user.name=Schedule QA", "-c",
                    "user.email=qa@localhost", "commit", "--allow-empty", "-qm", "base"], check=True)
    env = dict(os.environ, SWARM_DAEMON_STATE=str(state), SWARM_DAEMON_SOCK=str(root / "swarm.sock"))

    def command(*args, stdin=None, check=True):
        return subprocess.run([binary, "schedule", *args], env=env, text=True,
                              input=stdin, capture_output=True, check=check)

    def read(*args, stdin=None):
        return json.loads(command(*args, stdin=stdin).stdout)

    prompt = "Review setup friction and leave findings in this chat."
    assert not read("templates", "list")
    template = read("templates", "create", "Weekly DX review", "-", stdin=prompt)
    job = read("create", "--name", "Weekly DX", "--dir", str(repo), "--workspace",
               "worktree", "--authority", "agent-default", "--agents", "codex",
               "--weekly", "Monday", "--time", "09:00", "--timezone",
               "Europe/Zurich", "--prompt-file", "-", stdin=template["prompt"])
    job_id = job["id"]
    assert read("list", "--json")[0]["id"] == job_id
    assert read("show", job_id)["spec"]["directory"] == str(repo.resolve())
    spec = dict(job["spec"], name="Gaston DX")
    edit = ("edit", job_id, "--spec-file", "-", "--revision", job["revision_id"])
    edited = read(*edit, stdin=json.dumps(spec))
    assert edited["spec"]["name"] == "Gaston DX" and edited["revision_id"] != job["revision_id"]
    assert command(*edit, stdin=json.dumps(spec), check=False).returncode != 0
    assert read("pause", job_id)["state"] == "paused"
    assert read("resume", job_id)["state"] == "active"
    read("templates", "edit", template["id"], "New title", "-", stdin="A revised template")
    assert read("show", job_id)["spec"]["prompt"] == prompt
    assert read("archive", job_id)["state"] == "archived"
    host_job = read("create", "--name", "Host clock", "--dir", str(repo), "--workspace",
                    "directory", "--authority", "agent-default", "--agents", "agy,claude",
                    "--weekdays", "--time", "08:30", "--prompt-file", "-", stdin=prompt)
    assert host_job["spec"]["calendar"]["time_zone"] == "host"
    assert host_job["spec"]["agents"] == ["agy", "claude"]
    assert not read("history", host_job["id"])
    command("templates", "delete", template["id"])
    assert not read("templates", "list")
    assert command("create", "--cron", "invalid", check=False).returncode != 0
    print("PASS catalog, scheduling vocabulary, immutable prompt copy, lifecycle and templates")


if __name__ == "__main__":
    with tempfile.TemporaryDirectory(prefix="sqa-") as temp:
        verify(str(Path(sys.argv[1]).resolve()), Path(temp))
