#!/usr/bin/env python3
"""Drive automatic CLI refresh and restart recovery through a private daemon."""
from pathlib import Path
import json
from datetime import datetime, timezone
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time

from parity_agy import wait_for

THREAD = "11111111-1111-4111-8111-111111111111"
FIXTURE = r'''
import json, os, signal, subprocess, sys, time
from pathlib import Path

args = sys.argv[1:]
if args == ["--version"]:
    print("VERSION")
    raise SystemExit(0)
if args == ["--help"]:
    print("  --settings Inline settings")
    raise SystemExit(0)
assert args[0] == "--settings"
if "--conversation" in args:
    assert args[-2:] == ["--conversation", "THREAD"]
    with Path("resumes").open("a") as output:
        print("VERSION", file=output)
    deadline = time.monotonic() + 12
    while not Path("release-successor").exists():
        assert time.monotonic() < deadline
        time.sleep(.025)
else:
    def stop(sig, frame):
        Path("source-stop").touch()
        time.sleep(.7)
        raise SystemExit(0)
    signal.signal(signal.SIGTERM, stop)
subprocess.run(json.loads(args[1])["statusLine"]["command"], shell=True, check=True,
               input=json.dumps({"agent_state":"idle","conversation_id":"THREAD"}).encode())
print("Provider ready", flush=True)
while True:
    time.sleep(.1)
'''


def run(binary, codex_fixture=None, changed=False):
    with tempfile.TemporaryDirectory(prefix="mtn-", dir=Path("/tmp").resolve()) as tmp:
        root = Path(tmp)
        home, work, tools, state = (root / name for name in ("home", "work", "bin", "s"))
        for folder in (home, work, tools, state):
            folder.mkdir(mode=0o700)
        sock = state / "d"
        env = {"PATH":f"{tools}:/usr/bin:/bin", "HOME":str(home),"SHELL":"/bin/sh",
               "TERM":"xterm-256color", "TMPDIR":str(root), "SWARM_DAEMON_STATE":str(state),
               "SWARM_DAEMON_SOCK":str(sock),"SWARM_DAEMON_LOG":str(root / "daemon.log")}
        provider = tools / "agy"

        def install(version):
            candidate = tools / ".provider"
            candidate.write_text(f"#!{sys.executable}\n" + FIXTURE.replace("VERSION",version).replace("THREAD",THREAD))
            candidate.chmod(0o700)
            candidate.replace(provider)

        def cli(*args, expect=0):
            result = subprocess.run([binary,*args], cwd=work, env=env, capture_output=True,
                                    text=True, timeout=15)
            assert result.returncode == expect, (args, result.stdout, result.stderr)
            return result.stdout

        def start():
            process = subprocess.Popen([binary,"daemon"],cwd=work,env=env,
                                       stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            wait_for(sock.exists,"daemon socket")
            return process

        def plain_launch(agent="agy"):
            def request(peer, body):
                data = b"\x01" + json.dumps(body).encode()
                peer.write(struct.pack(">I",len(data))+data)
                peer.flush()
                response = peer.read(struct.unpack(">I",peer.read(4))[0])
                return json.loads(response[1:])
            with socket.socket(socket.AF_UNIX) as conn:
                conn.settimeout(10)
                conn.connect(str(sock))
                with conn.makefile("rwb") as peer:
                    endpoint = request(peer,{"op":"hello","protocol_version":1})["endpoint_id"]
                    launch = request(peer,{"op":"launch","endpoint_id":endpoint,
                        "launch":{"agent":agent,"name":"source","cwd":str(work),"options":{}}})
                    assert launch["op"]=="launch",launch
                    return launch["session"]["id"]

        def rows():
            return json.loads(cli("ls","--json")) or []

        def refresh():
            return json.loads(cli("refresh","--json"))["records"] or []

        if codex_fixture:
            shutil.copyfile(codex_fixture, tools / "codex")
            (tools / "codex").chmod(0o700)
            credentials = home / ".codex"
            credentials.mkdir(mode=0o700)
            auth = credentials / "auth.json"
            auth.write_text(json.dumps({"auth_mode":"chatgpt","tokens":{"account_id":"fixture-a"}}))
            auth.chmod(0o600)
            (work / ".fixture-seeded-rollout").touch()
            daemon = start()
            try:
                source = plain_launch("codex")
                wait_for(lambda: next((r for r in rows() if r["id"] == source
                                       and r["status"]["Turn"] == "idle"), None), "idle Codex")
                observed = state / "rust-v1" / source.split("/")[-1] / "observation.json"
                thread = json.loads(observed.read_text())["conversation_id"]
                instant = datetime.fromtimestamp(int(thread[:13].replace("-", ""), 16) / 1000, timezone.utc)
                day = credentials / "sessions" / instant.strftime("%Y/%m/%d")
                day.mkdir(mode=0o700, parents=True)
                rollout = day / f"rollout-{instant.strftime('%Y-%m-%dT%H-%M-%S')}-{thread}.jsonl"
                rollout.write_text(json.dumps({"type":"session_meta","payload":{"cwd":str(work),"id":thread}}) + "\n")
                rollout.chmod(0o600)
                if changed:
                    cli("relogin", "--auto", "off")
                    auth.write_text(json.dumps({"auth_mode":"chatgpt","tokens":{"account_id":"fixture-b"}}))
                    missing = rollout.with_suffix(".held")
                    rollout.rename(missing)
                    refused = cli("relogin")
                    assert "hold" in refused and len(rows()) == 1
                    assert rows()[0]["status"]["Process"] == "running"
                    missing.rename(rollout)
                    report = cli("relogin","--dry-run")
                    assert "would" in report, report
                    assert len(rows()) == 1
                    cli("relogin", "--auto", "on")
                    def recovered():
                        journal = state / "rust-refresh.json"
                        return journal.exists() and json.loads(journal.read_text())["records"].get(
                            source.split("/")[-1], {}
                        ).get("state") == "complete"
                    wait_for(recovered, "changed account watcher rejoin", timeout=70)
                    wait_for(lambda: not any(r["id"] == source and r["status"]["Process"] == "running"
                                            for r in rows()), "retained source exit")
                else:
                    report = cli("relogin","--dry-run","--force")
                    assert "would" in report, report
                    assert len(rows()) == 1
                    result = cli("relogin","--force")
                    assert "started" in result, (result, observed.read_text(), (root / "daemon.log").read_text(), rows())
                successors = [r for r in rows() if r.get("resumed_from") == source]
                assert len(successors) == 1
            except Exception:
                print("rows", rows(), file=sys.stderr)
                if (state / "rust-refresh.json").exists():
                    print("journal", (state / "rust-refresh.json").read_text(), file=sys.stderr)
                if (root / "daemon.log").exists():
                    print((root / "daemon.log").read_text(), file=sys.stderr)
                raise
            finally:
                for row in rows():
                    if row["status"]["Process"] == "running":
                        cli("kill",row["id"])
                daemon.terminate()
                daemon.wait(timeout=5)
            print("Automatic changed-account Codex rejoin passed." if changed
                  else "Forced native Codex same-account rejoin passed, including dry-run.")
            return

        install("1.4.0")
        daemon = start()
        try:
            source = plain_launch()
            wait_for(lambda: next((r for r in rows() if r["id"] == source
                                   and r["status"]["Turn"] == "idle"), None), "idle source")
            install("1.4.1")
            wait_for(lambda: refresh() and refresh()[0]["state"] == "pending", "observed newer candidate", timeout=40)
            assert not (work / "resumes").exists()
            wait_for(lambda: (work / "source-stop").exists(), "durably claimed provider stop", timeout=40)
            daemon.kill()
            daemon.wait(timeout=5)
            # The socket belongs to the vanished daemon until the replacement acquires its lock.
            sock.unlink(missing_ok=True)
            daemon = start()
            wait_for(lambda: (work / "resumes").exists() and refresh()[0]["state"] == "waiting",
                     "one reserved successor awaiting native evidence", timeout=45)
            daemon.kill()
            daemon.wait(timeout=5)
            sock.unlink(missing_ok=True)
            (work / "release-successor").touch()
            daemon = start()
            complete = wait_for(lambda: refresh()[0] if refresh()[0]["state"] == "complete" else None,
                                "exact successor rejoin", timeout=70)
            assert complete["target_version"] == "1.4.1"
            successors = [r for r in rows() if r.get("resumed_from") == source]
            assert len(successors) == 1 and successors[0]["id"].endswith(complete["replacement_id"])
            assert (work / "resumes").read_text() == "1.4.1\n"
        except Exception:
            print("report", refresh(), "rows", rows(), file=sys.stderr)
            log = root / "daemon.log"
            if log.exists():
                print(log.read_text(), file=sys.stderr)
            raise
        finally:
            for row in rows():
                if row["status"]["Process"] == "running":
                    cli("kill",row["id"])
            daemon.terminate()
            daemon.wait(timeout=5)

if __name__ == "__main__":
    run(Path(sys.argv[1]).resolve(strict=True), Path(sys.argv[2]).resolve(strict=True) if len(sys.argv) > 2 else None, "--changed-account" in sys.argv[3:])
    if len(sys.argv) == 2:
        print("Automatic CLI refresh resumed the exact conversation once after a daemon crash.")
