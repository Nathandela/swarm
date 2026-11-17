"""Opt-in native-account qualification of a local manager publication and replay."""
import argparse
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import time


def stop(child):
    if child.poll() is None:
        child.terminate()
        try:
            child.wait(timeout=12)
        except subprocess.TimeoutExpired:
            child.kill()
            child.wait()


def spike(binary, provider, worker, speech_key):
    root = Path(tempfile.mkdtemp(prefix="vm-", dir="/private/tmp" if Path("/private/tmp").exists() else "/tmp"))
    state, work = root / "s", root / "w"
    state.mkdir(mode=0o700)
    work.mkdir(mode=0o700)
    env = {k: os.environ[k] for k in ("HOME", "PATH", "USER", "LOGNAME", "LANG", "TMPDIR", "SHELL", "CLAUDE_CONFIG_DIR") if k in os.environ}
    env.update(SWARM_DAEMON_STATE=str(state), SWARM_DAEMON_SOCK=str(state / "daemon.sock"),
               SWARM_DAEMON_LOCK=str(state / "daemon.lock"), SWARM_DAEMON_LOG=str(state / "daemon.log"))
    print("Private spike directory", root, flush=True)

    def run(*args, data=None, check=True):
        return subprocess.run([binary, *args], input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                              cwd=work, env=env, text=True, check=check, timeout=20)

    def wait_until(operation, timeout):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            value = operation()
            if value:
                return value
            time.sleep(0.25)
        raise RuntimeError("timed out waiting for " + operation.__name__)

    def status():
        if service.poll() is not None:
            raise RuntimeError("voice service exited: " + (root / "service.log").read_text()[-500:])
        result = run("voice", "status", check=False)
        return result.returncode == 0 and json.loads(result.stdout).get("ready")

    def completion():
        receipt = json.loads(run("voice", "receipt", "--id", uid).stdout)
        if receipt.get("phase") in ("uncertain", "rejected", "failed"):
            raise RuntimeError("admission " + receipt["phase"])
        return receipt if receipt.get("phase") == "completed" else None

    uid, marker = "manager-probe", "Purple umbrella verified."
    text = ('Use only the Swarm voice MCP tools. Call swarm_list once, then call publish with '
            'output_id "probe-reply", sequence 1, final true and exact text "' + marker +
            '". Do not run shell commands, edit files, or create workers.')
    if worker:
        marker = "Worker launch receipt verified."
        text = (f'Use only the Swarm MCP tools. Call swarm_create exactly once with action_id "create-probe", '
                f'agent "codex", name "Voice probe worker", cwd "{work}", and prompt '
                '"Reply with the words Violet pebble. Do not use tools or modify any files." '
                'After a confirmed create receipt, call publish with output_id "probe-reply", sequence 1, '
                f'final true, and text "{marker}". A launch receipt is enough. Do not claim the worker finished.')
    pcm = None
    if speech_key:
        # This fixture uses local speech synthesis and accepts no microphone input.
        audio_file = root / "synthetic.aiff"
        subprocess.run(["say", "-v", "Samantha", "-o", audio_file,
                        "Use your Swarm publish tool now. Publish one final phrase. Purple umbrella verified."],
                       check=True, timeout=20)
        pcm = subprocess.run(["ffmpeg", "-nostdin", "-loglevel", "error", "-i", audio_file, "-ar", "24000",
                              "-ac", "1", "-f", "s16le", "pipe:1"], capture_output=True, check=True,
                             timeout=20).stdout
        if not pcm:
            raise RuntimeError("empty synthetic input")
    def submit():
        if pcm is None:
            return json.loads(run("voice", "text", "--id", uid, data=text).stdout)
        result = subprocess.run([binary, "voice", "ptt", "--id", uid], env=env, cwd=work,
                                input=pcm, stdout=subprocess.PIPE, check=True, timeout=45)
        return json.loads(result.stdout)

    with (root / "daemon-stdout.log").open("w") as daemon_log, (root / "service.log").open("w") as service_log:
        daemon = subprocess.Popen([binary, "daemon"], cwd=work, env=env, stdout=daemon_log, stderr=daemon_log)
        service = None
        try:
            wait_until(lambda: (state / "daemon.sock").is_socket(), 15)
            run("voice", "setup", "--manager", provider, "--dir", str(work))
            if speech_key:
                with os.fdopen(os.open(speech_key, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as f:
                    st = os.fstat(f.fileno())
                    if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or st.st_nlink != 1 or stat.S_IMODE(st.st_mode) != 0o600:
                        raise RuntimeError("expected an owner-only speech key file")
                    key = f.read(4097)
                if len(key) > 4096:
                    raise RuntimeError("oversized speech key")
                run("voice", "setup", data=key.decode())
                del key
            service = subprocess.Popen([binary, "voice", "serve"], cwd=work, env=env,
                                       stdout=service_log, stderr=service_log)
            wait_until(status, 60)
            first = submit()
            observed = wait_until(completion, 95)
            publications = json.loads(run("voice", "publications", "--id", uid).stdout)
            published = publications[0]["text"] if len(publications) == 1 else ""
            # Spoken input cannot encode capitalization. The persisted reply still has to be byte-identical on retry.
            matches = published.casefold() == marker.casefold() if speech_key else published == marker
            if not matches or not publications[0]["final"]:
                raise RuntimeError("missing or unexpected canonical publication")
            playback = 0
            if speech_key:
                reply = subprocess.run([binary, "voice", "read", "--id", uid, "--publication", publications[0]["id"]],
                                       env=env, cwd=work, stdout=subprocess.PIPE, check=True, timeout=45).stdout
                playback = len(reply)
                if not reply or not any(reply) or playback % 2:
                    raise RuntimeError("missing read-aloud PCM")
            stop(service)
            service = subprocess.Popen([binary, "voice", "serve"], cwd=work, env=env,
                                       stdout=service_log, stderr=service_log)
            wait_until(status, 40)
            duplicate = submit()
            again = json.loads(run("voice", "publications", "--id", uid).stdout)
            if first["sequence"] != duplicate["sequence"] or duplicate["native_turn_id"] != observed["native_turn_id"] or again != publications:
                raise RuntimeError("restart changed receipt or publication")
            worker_count = 0
            for row in json.loads(run("ls", "--json").stdout):
                if row.get("name") == "Voice probe worker" and row.get("cwd") == str(work):
                    worker_count += 1
            if worker_count != int(worker):
                raise RuntimeError("unexpected worker count after retry")
            print(json.dumps({"provider": provider, "workers_after_retry": worker_count, "preview_pcm_bytes": playback, "admission": observed["phase"], "canonical_reply": published,
                              "publications_after_restart": len(again), "same_turn_after_retry": True}))
        finally:
            if speech_key:
                (state / "voice" / "openai.key").unlink(missing_ok=True)
            if service is not None:
                stop(service)
            if daemon.poll() is None:
                try:
                    roster = run("ls", "--json", check=False)
                    for row in json.loads(roster.stdout or "[]"):
                        if row.get("cwd") == str(work):
                            run("kill", row["id"], check=False)
                finally:
                    stop(daemon)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary")
    parser.add_argument("provider", choices=["codex", "claude"])
    opts = parser.add_mutually_exclusive_group()
    opts.add_argument("--speech-key-file", help="enable billed fixed synthetic PTT and Read aloud checks")
    opts.add_argument("--worker", action="store_true", help="also launch one disposable Codex worker")
    args = parser.parse_args()
    spike(str(Path(args.binary).resolve()), args.provider, args.worker, args.speech_key_file)
