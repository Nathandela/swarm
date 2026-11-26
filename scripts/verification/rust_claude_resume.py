#!/usr/bin/env python3
"""Resume a real disposable Claude session in the original private profile."""

import argparse
import http.server
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import struct
import sys
import tempfile
import threading
import time

sys.dont_write_bytecode = True
from native_fixture_sandbox import guarded, check_guards
from parity_runtime import control, fixtures, read_control
from rust_claude_worker_input import Provider, save

TASK = "Reply with a short synthetic test answer."


def verify(swarm, installed, root, server, worktree=False, through_board=False):
    home, work, state, tools = [root / name for name in ("home", "work", "s", "bin")]
    for directory in (home, work, state, tools, home / ".claude"):
        directory.mkdir(mode=0o700)
    binary = tools / "claude"
    shutil.copyfile(installed, binary)
    binary.chmod(0o700)
    staged = tools / "swarm"
    shutil.copyfile(swarm, staged)
    staged.chmod(0o700)
    owner_path = str(root / "d")
    env = dict(PATH=f"{tools}:/usr/bin:/bin", HOME=str(home), TMPDIR=str(root), TERM="xterm-256color",
               LANG="en_US.UTF-8", ANTHROPIC_API_KEY="synthetic-resume",
               SWARM_DAEMON_STATE=str(state), SWARM_DAEMON_SOCK=owner_path)
    guard = lambda argv: guarded(argv, root, [server.server_port])
    version = subprocess.check_output(guard([binary, "--version"]), env=env, text=True).split()[0]
    save(home / ".claude.json", dict(hasCompletedOnboarding=True, lastOnboardingVersion=version,
         theme="dark", projects={str(work):dict(hasTrustDialogAccepted=True)},
         customApiKeyResponses=dict(approved=[env["ANTHROPIC_API_KEY"]], rejected=[])))
    save(home / ".claude/settings.json", dict(env=dict(
        ANTHROPIC_BASE_URL=f"http://127.0.0.1:{server.server_port}",
        CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC="1", DISABLE_AUTOUPDATER="1",
        CLAUDE_CODE_TMPDIR=str(root), CLAUDE_CODE_MAX_RETRIES="0")))
    check_guards(root,[server.server_port])
    def cli(*args):
        run = subprocess.run(guard([staged,*args]), env=env, cwd=work,
                             capture_output=True, timeout=15)
        assert run.returncode==0,(args,run.stderr.decode(errors="replace"))
        return run.stdout

    def wire(req, caps=()):
        with socket.socket(socket.AF_UNIX) as peer:
            peer.settimeout(12)
            peer.connect(owner_path)
            control(peer,"hello",endpoint_id="",protocol_version=1,capabilities=caps)
            hello=read_control(peer)
            control(peer,"launch",endpoint_id=hello["endpoint_id"],launch=req)
            return read_control(peer)

    def eventually(test,label,timeout=15):
        end = time.monotonic()+timeout
        while time.monotonic()<end:
            if value:=test():
                return value
            time.sleep(.08)
        raise AssertionError(label)

    if worktree:
        for argv in (["init","-q"],["-c","user.name=Synthetic","-c","user.email=fixture@example.invalid",
                                    "-c","commit.gpgsign=false","commit","--allow-empty","-qm","fixture"]):
            subprocess.run(guard(["/usr/bin/git",*argv]),env=env,cwd=work,check=True,capture_output=True)
    previous={}
    daemon=None
    try:
        with (root / "daemon.log").open("wb") as log:
            daemon=subprocess.Popen(guard([staged,"daemon"]),env=env,cwd=work,stderr=log,stdout=log)
            eventually(lambda:Path(owner_path).exists(),"daemon")
            if worktree:
                created=wire(dict(agent="claude",name="resume fixture",cwd=str(work),worktree=True,
                                  initial_prompt=TASK,cols=100,rows=30))
                assert created["op"]=="launch",created
                original=created["session"]["id"]
            else:
                original=cli("spawn","--cli","claude","--name","resume fixture","--dir",str(work),"--prompt",TASK).decode().strip()
            source=state / "rust-v1" / original.split("/")[1]
            initial=json.loads((source/"session.json").read_text())
            effective=Path(initial["checkout"]["path"]) if worktree else work

            def staged_reply():
                screen=cli("peek",original)
                if b"I am using this for local development" in screen:
                    cli("send",original,"--key","down")
                    cli("send",original,"--key","enter")
                if b"trust the files" in screen and b"trust this folder" in screen:
                    cli("send",original,"--key","enter")
                return b"Synthetic native input processed" in screen

            eventually(staged_reply,"private loopback reply")
            proof=json.loads((source/"observation.json").read_text())
            native=proof["conversation_id"]
            profile=json.loads((source/"session.json").read_text())["claude_profile"]
            assert profile["home"]==str(home) and "synthetic-resume" not in json.dumps(profile)
            fixtures.owned(root,None,previous)
            launch=dict(agent="claude",name="resume fixture",cwd=str(work),
                        options={"resume_from":original},cols=100,rows=30)
            assert wire(launch)["op"]=="error"
            cli("kill",original)
            eventually(lambda:(source/"exit.json").exists(),"durable stopped worker")
            exited=json.loads((source/"owner.json").read_text())
            def reaped():
                actual=fixtures.generation(exited["pid"])
                if isinstance(actual,tuple): actual=actual[0]*1_000_000+actual[1]
                return actual is None or int(actual)!=exited["birth"]
            eventually(reaped,"shim reaped")
            changed=launch | {"env":[f"{key}={value}" for key,value in (env | {"ANTHROPIC_API_KEY":"unauthorized-other"}).items()]}
            assert wire(changed)["op"]=="error"
            for base,name in [(effective,"settings.local.json"),(root,"settings.json")]:
                directory=base/".claude"
                existed=directory.exists()
                directory.mkdir(mode=0o700,exist_ok=True)
                config=directory/name
                assert not config.exists()
                save(config,{"env":{"ANTHROPIC_BASE_URL":"https://unqualified.invalid"}})
                try:
                    original_ids={path.name for path in source.parent.iterdir() if len(path.name)==24}
                    refused=wire(launch)
                    assert refused["op"]=="error" and refused.get("error_code")=="invalid_field",refused
                    assert {path.name for path in source.parent.iterdir() if len(path.name)==24}==original_ids
                finally:
                    config.unlink()
                    if not existed: directory.rmdir()
            if through_board:
                shutil.copy2(fixtures.__file__, tools / "driver")
                board = fixtures.Board(guard([staged]), root, env)
                try:
                    board.wait_bytes(b"resume fixture")
                    board.send(b"r")

                    def successor():
                        board.drain()
                        return next((r for r in json.loads(cli("ls", "--json")) if r.get("resumed_from") == original), None)

                    row = eventually(successor, "successor from ordinary board resume")
                    resumed = {"op": "launch", "session": row}
                    board.wait_bytes(b"Claude Code")
                    mark = len(board.capture)
                    board.send(b"\x11")
                    board.wait_bytes(b"resume fixture", mark)
                    board.send(b"\x1b[27u")
                    fixtures.wait_for(lambda: (board.drain(), board.child.poll())[1] is not None, "board exit")
                    assert board.child.returncode == 0
                    print("The board resumed the selected Claude session and returned from its PTY")
                finally:
                    board.close()
            else:
                resumed=wire(launch)
            assert resumed["op"]=="launch",resumed
            assert resumed["session"]["resumed_from"]==original
            assert resumed["session"]["cwd"]==str(effective)
            new_id=resumed["session"]["id"]
            directory=state / "rust-v1" / new_id.split("/")[1]
            assert json.loads((directory/"observation.json").read_text())["conversation_id"]==native
            assert wire(launch)["op"]=="error"
            # Replay the committed launch plan through the real shim after an
            # ancestor changes. It must fail before opening a native worker.
            denied=directory.parent/"4f0000000000000000000000"
            denied.mkdir(mode=0o700)
            reservation=json.loads((directory/"session.json").read_text())
            reservation.update(id=denied.name, token="9f0000000000000000000000")
            save(denied/"session.json",reservation)
            ancestor=root/".claude"
            ancestor.mkdir(mode=0o700)
            save(ancestor/"settings.local.json",{"apiKeyHelper":"fixture must not execute"})
            payload=json.dumps({"argv":["--resume",native]}).encode()
            try:
                result=subprocess.run(guard([staged,"shim","--rust-session",denied]),
                                      input=struct.pack(">IB",len(payload)+1,1)+payload,
                                      env=env,cwd=work,capture_output=True,timeout=8)
                assert result.returncode!=0 and not (denied/"owner.json").exists(),result
                refused=json.loads((denied/"exit.json").read_text())
                assert "change native authority" in refused["error"],refused
            finally:
                shutil.rmtree(ancestor)
                shutil.rmtree(denied)
            print("Claude project and ancestor authority changes refused before reservation and native exec")
            caps=("hands-off-handoff",)
            forged=dict(agent="claude",name="successor",cwd=str(root / "nonexistent"),
                        initial_prompt="CLIENT_FORGED_PROMPT",options={"handoff_from":new_id})
            assert wire(forged)["op"]=="error"
            assert wire(forged | {"options":{"handoff_from":new_id,"resume_from":original}},caps)["op"]=="error"
            if worktree:
                assert wire(forged,caps)["op"]=="error"  # Temporary Git destinations remain refused.
            else:
                project="".join(ch if ch.isascii() and ch.isalnum() else "-" for ch in str(work))
                source_file=home / ".claude/projects" / project / f"{native}.jsonl"
                backup=source_file.with_suffix(".fixture")
                source_file.rename(backup)
                try:
                    source_file.symlink_to(backup)
                    assert wire(forged,caps)["op"]=="error"
                finally:
                    source_file.unlink()
                    backup.rename(source_file)
                offered=wire(forged,caps)
                assert offered["op"]=="launch",offered
                successor=offered["session"]["id"]
                assert offered["session"]["spawned_from"]==new_id.split("/")[1] and offered["session"]["cwd"]==str(work)
                child=state / "rust-v1" / successor.split("/")[1]
                def pointer_prompt():
                    proof=child / "observation.json"
                    if not proof.exists(): return None
                    conversation=json.loads(proof.read_text()).get("conversation_id")
                    if not conversation: return None
                    path=source_file.with_name(f"{conversation}.jsonl")
                    if not path.exists(): return None
                    text=path.read_text()
                    return text if f"conversation uid: {native}" in text else None
                prompt=eventually(pointer_prompt,"verified original transcript pointers in successor")
                assert str(source_file) in prompt and "CLIENT_FORGED_PROMPT" not in prompt
                assert any(row["id"]==new_id and row["status"]["Process"]=="running" for row in json.loads(cli("ls","--json")))
                cli("kill",successor)
                cli("watch",successor,"--until","completed","--timeout","5s")
                print("Claude hands-off ignored caller prompt and cwd, followed exact transcript, and left source alive")
            cli("kill",new_id)
            cli("watch",new_id,"--until","completed","--timeout","5s")
            print("Claude",version,"resumed the exact native conversation and refused live/changed-account duplicates")
    finally:
        if daemon and daemon.poll() is None:
            daemon.terminate()
            daemon.wait(timeout=3)
        cleanup=fixtures.reap(root,None,previous)
        print("cleanup",cleanup)
        if cleanup["remaining"]:
            raise RuntimeError(f"fixture processes survived at {root}")


if __name__=="__main__":
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate",required=True,type=fixtures.binary_path)
    parser.add_argument("--provider",required=True,type=fixtures.binary_path)
    parser.add_argument("--worktree",action="store_true")
    parser.add_argument("--board", action="store_true", help="resume using the ordinary dashboard action")
    args=parser.parse_args()
    root=Path(tempfile.mkdtemp(prefix="cr-",dir=Path("/tmp").resolve()))
    server=http.server.ThreadingHTTPServer(("127.0.0.1",0),Provider)
    server.daemon_threads=True
    server.observed=[]
    server.sentinel=root/"must-not-exist"
    thread=threading.Thread(target=server.serve_forever,daemon=True)
    thread.start()
    success=False
    try:
        verify(args.candidate,args.provider,root,server,args.worktree,args.board)
        assert not server.sentinel.exists()
        success=True
    finally:
        server.shutdown()
        server.server_close()
        if success:
            shutil.rmtree(root)
        else:
            print(f"retained private fixture at {root}",file=sys.stderr)
