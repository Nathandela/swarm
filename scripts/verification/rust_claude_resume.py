#!/usr/bin/env python3
"""Resume a real disposable Claude session in the original private profile."""

import argparse
import http.server
import json
import io
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


RELOCATED_PROMPT = "Synthetic followup from the explicitly relocated checkout."

class RelocationModel(Provider):
    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        messages = json.dumps(json.loads(raw).get("messages", []))
        if TASK in messages and RELOCATED_PROMPT in messages:
            self.server.joined_relocation = True
        stream, self.rfile = self.rfile, io.BytesIO(raw)
        try:
            super().do_POST()
        finally:
            self.rfile = stream

def verify(swarm, installed, root, server, *, worktree=False, through_board=False, external=False, go_owner=None, capacity=False, relocate=False):
    home, work, state, tools = [root / name for name in ("home", "work", "s", "bin")]
    for directory in (home, work, state, tools, home / ".claude"):
        directory.mkdir(mode=0o700)
    binary = tools / "claude"
    shutil.copyfile(installed, binary)
    binary.chmod(0o700)
    staged = tools / "swarm"
    shutil.copyfile(swarm, staged)
    staged.chmod(0o700)
    owner=staged
    if go_owner:
        owner=tools/"go-owner"
        shutil.copyfile(go_owner,owner)
        owner.chmod(0o700)
    owner_path = str(root / "d")
    env = dict(PATH=f"{tools}:/usr/bin:/bin", HOME=str(home), TMPDIR=str(root), TERM="xterm-256color",
               LANG="en_US.UTF-8", ANTHROPIC_API_KEY="synthetic-resume",
               SWARM_DAEMON_STATE=str(state), SWARM_DAEMON_SOCK=owner_path, SWARM_DAEMON_LOCK=str(root/"owner.lock"), SWARM_DAEMON_LOG=str(root/"owner.log"))
    guard = lambda argv: guarded(argv, root, [server.server_port])
    version = subprocess.check_output(guard([binary, "--version"]), env=env, text=True).split()[0]
    save(home / ".claude.json", dict(hasCompletedOnboarding=True, lastOnboardingVersion=version,
         theme="dark", projects={str(path):dict(hasTrustDialogAccepted=True) for path in (work,root/"relocated")},
         customApiKeyResponses=dict(approved=[env["ANTHROPIC_API_KEY"]], rejected=[])))
    save(home / ".claude/settings.json", dict(env=dict(
        ANTHROPIC_BASE_URL=f"http://127.0.0.1:{server.server_port}",
        CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC="1", DISABLE_AUTOUPDATER="1",
        CLAUDE_CODE_TMPDIR=str(root), CLAUDE_CODE_MAX_RETRIES="0")))
    check_guards(root,[server.server_port])
    def cli(*args,expected=0,overrides=None,error_contains=None):
        run = subprocess.run(guard([owner if relocate and go_owner else staged,*args]), env={**env,**(overrides or {})}, cwd=root if relocate else work,
                             capture_output=True, timeout=50 if args[0]=="reattach" else 15)
        assert run.returncode==expected,(args,run.stderr.decode(errors="replace"))
        if error_contains is not None:
            assert error_contains in run.stderr,(args,run.stderr)
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

    def joined_turn(sid, native, retained, selected):
        def prior_history():
            display = cli("peek",sid)
            if b"trust the files" in display and b"trust this folder" in display:
                cli("send", sid, "--key", "enter")
            return b"Synthetic native input processed" in display
        eventually(prior_history,"original assistant transcript at new working directory",25)
        cli("send",sid,"--text",RELOCATED_PROMPT)
        eventually(lambda: server.joined_relocation, "old and new input in the same native model request",25)
        def provider_working_directory():
            for line in retained.read_text().splitlines():
                row = json.loads(line)
                if row.get("type") == "user" and RELOCATED_PROMPT in json.dumps(row.get("message")):
                    return row.get("sessionId") == native and row.get("cwd") == str(selected)
            return False
        eventually(provider_working_directory, "new native user turn saved in selected checkout",25)

    if worktree:
        for argv in (["init","-q"],["-c","user.name=Synthetic","-c","user.email=fixture@example.invalid",
                                    "-c","commit.gpgsign=false","commit","--allow-empty","-qm","fixture"]):
            subprocess.run(guard(["/usr/bin/git",*argv]),env=env,cwd=work,check=True,capture_output=True)
    previous={}
    daemon=None
    try:
        with (root / "daemon.log").open("wb") as log:
            daemon=subprocess.Popen(guard([owner,"daemon"]),env=env,cwd=work,stderr=log,stdout=log)
            def listening():
                if daemon.poll() is not None:
                    raise RuntimeError((root/"daemon.log").read_text()[-2400:])
                return Path(owner_path).exists()
            eventually(listening,"daemon")
            if relocate and go_owner:
                original = cli("spawn","--cli","claude","--name","go relocation","--dir",str(work),"--prompt",TASK).decode().strip()
                def original_turn():
                    display = cli("peek", original)
                    if b"I am using this for local development" in display:
                        cli("send",original,"--key","down")
                        cli("send",original,"--key","enter")
                    if b"trust the files" in display and b"trust this folder" in display:
                        cli("send",original,"--key","enter")
                    return b"Synthetic native input processed" in display
                eventually(original_turn,"Go-launched original loopback turn",30)
                def native_history():
                    candidates = [p for p in (home/".claude/projects").rglob("*.jsonl") if TASK in p.read_text()]
                    return candidates[0] if len(candidates) == 1 else None
                retained = eventually(native_history,"original Go worker wrote the native transcript")
                native = retained.stem
                cli("kill",original)
                cli("watch",original,"--until","completed","--timeout","8s")
                selected=root/"relocated"
                selected.mkdir(mode=0o700)
                work.rename(root/"old-removed")
                request=dict(agent="claude",name="go relocation",cwd=str(selected),
                             options={"resume_from":original,"resume_relocate":"true"},cols=100,rows=30)
                offered=wire(request,["resume-relocate"])
                assert offered["op"]=="launch" and offered["session"]["cwd"]==str(selected),offered
                sid=offered["session"]["id"]
                joined_turn(sid,native,retained,selected)
                cli("kill",sid)
                cli("watch",sid,"--until","completed","--timeout","8s")
                print("pinned Go relocated Claude's original conversation and completed its new turn in the selected checkout")
                return
            if external:
                def native_cli(*args):
                    result=subprocess.run(guard([binary,*args]),env=env,cwd=work,capture_output=True,timeout=35,umask=0o077)
                    assert result.returncode==0,(args,result.stdout,result.stderr)
                    return result.stdout
                native_cli("--bg","--name","External fixture",TASK)
                if capacity:
                    native_cli("--bg","--name","Second external fixture",TASK)
                    def live_native():
                        rows=json.loads(native_cli("agents","--json","--all"))
                        return [row["sessionId"] for row in rows if row.get("kind")=="background"
                                and row.get("state") not in ("done","stopped","complete","completed","failed","cancelled","canceled")]
                    natives=eventually(lambda: live_native() if len(live_native())==2 else None,
                                       "two real live Claude Agent View sources",timeout=30)
                    for native in natives:
                        eventually(lambda: any(b"Synthetic native input processed" in p.read_bytes()
                            for p in (home/".claude/projects").rglob(f"{native}.jsonl")),
                            "native source has a real reply",timeout=20)
                    agy=tools/"agy"
                    agy.write_text('#!/bin/sh\nif [ "$1" = --version ]; then echo 1.1.0; exit 0; fi\nexec /bin/sleep 1000\n')
                    agy.chmod(0o700)
                    occupied=[]
                    for i in range(63):
                        result=wire(dict(agent="agy",name=f"Capacity {i}",cwd=str(work)))
                        assert result["op"]=="launch",result
                        occupied.append(result["session"]["id"])
                    roster=json.loads(cli("ls","--json"))
                    assert len([row for row in roster if row["status"]["Process"]=="running"])==63
                    cli("reattach","--cli","claude","--all","--take-over",expected=1,
                        error_contains=b"batch exceeds available session capacity")
                    assert set(live_native())==set(natives)
                    cli("kill",occupied.pop())
                    eventually(lambda: sum(row["status"]["Process"]=="running" for row in json.loads(cli("ls","--json")))==62,
                               "exactly two available owner slots")
                    race=root/"race"
                    race.mkdir(mode=0o700)
                    wrapper=race/"claude"
                    stopping=race/"stopping"
                    release=race/"release"
                    wrapper.write_text(f"""#!/bin/sh
if [ "$1" = daemon ] && [ "$2" = stop ]; then
  touch '{stopping}'
  while [ ! -f '{release}' ]; do /bin/sleep 0.05; done
fi
exec '{binary}' "$@"
""")
                    wrapper.chmod(0o700)
                    request=subprocess.Popen(guard([staged,"reattach","--cli","claude","--all","--take-over"]),
                        env={**env,"PATH":f"{race}:{env['PATH']}"},cwd=work,
                        stdout=subprocess.PIPE,stderr=subprocess.PIPE)
                    outcome=[]
                    contender=threading.Thread(target=lambda:outcome.append(
                        wire(dict(agent="agy",name="Competing launch",cwd=str(work)))),daemon=True)
                    try:
                        eventually(stopping.exists,"owner holds admission during native stop")
                        contender.start()
                        time.sleep(0.3)
                        assert not outcome
                    finally:
                        release.touch()
                    acquired,err=request.communicate(timeout=45)
                    assert request.returncode==0,err
                    contender.join(timeout=12)
                    assert len(outcome)==1 and outcome[0]["op"]=="error" and "session limit" in outcome[0]["error"],outcome
                    assert acquired.count(b"reattached")==2 and not live_native(),acquired
                    roster=json.loads(cli("ls","--json"))
                    assert sum(row["status"]["Process"]=="running" for row in roster)==64
                    for row in roster:
                        if row["status"]["Process"]=="running":
                            cli("kill",row["id"])
                    print("63/64 preserved both native sources; at 62/64 takeover reserved both slots ahead of a competing launch")
                    return

                def original():
                    rows=json.loads(native_cli("agents","--json","--all"))
                    return next((row for row in rows if row.get("kind")=="background" and row.get("sessionId")),None)
                row=eventually(original,"native Claude Agent View registered original",timeout=25)
                native=row["sessionId"]
                proof=lambda:next((path for path in (home/".claude/projects").rglob(f"{native}.jsonl")
                                   if b"Synthetic native input processed" in path.read_bytes()),None)
                transcript=eventually(proof,"native Claude wrote the external conversation",timeout=25)
                assert native.encode() in cli("reattach","--cli","claude","--all","--dry-run")
                if original()["state"] not in ("done","stopped","complete","completed","failed","cancelled","canceled"):
                    cli("reattach","--cli","claude","--all",expected=1)
                    assert json.loads(cli("ls","--json"))==[]
                live = original()["state"] not in ("done","stopped","complete","completed","failed","cancelled","canceled")
                if live and not go_owner:
                    refused = wire(dict(agent="claude",name="unconfirmed",cwd=str(work),
                        options={"resume_conversation_id":native}),("external-resume",))
                    assert refused["op"]=="error" and "exact stopped source" in refused["error"]
                if live:
                    legacy_path=str(root/"old-owner")
                    legacy=socket.socket(socket.AF_UNIX)
                    legacy.bind(legacy_path)
                    legacy.listen()
                    def old_owner():
                        with legacy:
                            peer,_=legacy.accept()
                            with peer:
                                hello=read_control(peer)
                                assert hello["op"]=="hello"
                                control(peer,"hello",endpoint_id="legacy-owner",protocol_version=1,
                                        capabilities=["external-resume"])
                    fallback=threading.Thread(target=old_owner,daemon=True)
                    fallback.start()
                    cli("reattach","--cli","claude","--all","--take-over",expected=1,
                        overrides={"SWARM_DAEMON_SOCK":legacy_path})
                    fallback.join(timeout=4)
                    assert original()["state"] not in ("done","stopped","complete","completed","failed","cancelled","canceled")

                    fake=root/"duplicate-bin"
                    fake.mkdir(mode=0o700)
                    marker=root/"native-stop-must-not-run"
                    fixture=fake/"claude"
                    data=json.dumps([original(),original()])
                    fixture.write_text("#!/bin/sh\ncase \"$1\" in\n--version) echo '2.1.329 (Claude Code)' ;;\nagents) printf '%s' '"+data+"' ;;\n*) touch '"+str(marker)+"'; exit 3 ;;\nesac\n")
                    fixture.chmod(0o700)
                    cli("reattach","--cli","claude","--all","--take-over",expected=1,
                        overrides={"PATH":f"{fake}:/usr/bin:/bin"})
                    assert not marker.exists()
                    assert original()["state"] not in ("done","stopped","complete","completed","failed","cancelled","canceled")
                    if not go_owner:
                        entry=original()
                        inventory=json.dumps([entry])
                        changed=json.dumps([entry,{**entry,"sessionId":"01997f00-face-7000-8000-00000000cde0"}])
                        count=fake/"calls"
                        fixture.write_text(f"""#!/bin/sh
case "$1" in
--version) echo '2.1.329 (Claude Code)' ;;
agents)
  echo x >> '{count}'
  if [ "$(wc -l < '{count}')" -ge 3 ]; then printf '%s' '{changed}'; else printf '%s' '{inventory}'; fi ;;
*) touch '{marker}'; exit 3 ;;
esac
""")
                        cli("reattach","--cli","claude","--all","--take-over",expected=1,
                            overrides={"PATH":f"{fake}:/usr/bin:/bin"})
                        assert len(count.read_text().splitlines())==3 and not marker.exists()
                        assert original()["state"] not in ("done","stopped","complete","completed","failed","cancelled","canceled")
                    print("old owner, duplicate UUID, and changed inventory protections preserved the live source")
                before=transcript.read_bytes()
                def turn_count(data):
                    rows = [json.loads(line) for line in data.splitlines()]
                    return sum(row.get("type")=="user" and TASK in json.dumps(row.get("message"))
                               for row in rows)
                initial_turns=turn_count(before)
                assert initial_turns>=1
                if go_owner and live:
                    cli("reattach","--cli","claude","--all","--take-over",expected=1,
                        error_contains=b"cannot reserve the whole Claude takeover batch")
                    assert original()["state"] not in ("done","stopped","complete","completed","failed","cancelled","canceled")
                    native_cli("daemon","stop","--any")
                imported=cli("reattach","--cli","claude","--all","--take-over")
                row,=json.loads(cli("ls","--json"))
                identity=row["id"]
                assert identity.encode() in imported
                directory=state/"rust-v1"/identity.split("/")[1]
                if not go_owner:
                    assert json.loads((directory/"observation.json").read_text())["conversation_id"]==native
                eventually(lambda:b"Synthetic native input processed" in cli("peek",identity),
                           "new owner viewer rendered the prior background answer")
                current=transcript.read_bytes()
                assert current.startswith(before) and turn_count(current)==initial_turns
                assert original()["state"] in ("done","stopped","complete","completed","failed","cancelled","canceled")
                if not go_owner:
                    cli("reattach","--cli","claude","--all","--take-over",expected=1)
                cli("kill",identity)
                cli("watch",identity,"--until","completed","--timeout","5s")
                print("Claude Agent View external acquisition preserved prior answer and did not replay the user turn; source was live before takeover:",live)
                return
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
            if relocate:
                selected = root / "relocated"
                selected.mkdir(mode=0o700)
                work.rename(root / "old-removed")
                assert wire(launch)["op"] == "error"
                retained, = (home / ".claude/projects").rglob(native + ".jsonl")
                other = retained.parent.with_name("other-private-project")
                other.mkdir(mode=0o700)
                alias = other / retained.name
                shutil.copyfile(retained, alias)
                relocated = launch | {"cwd":str(selected), "options":{"resume_from":original,"resume_relocate":"true"}}
                caps = ["resume-relocate"]
                assert wire(relocated)["op"] == "error"
                refused = wire(relocated, caps)
                assert refused["op"] == "error" and "multiple transcripts" in refused["error"], refused
                alias.unlink()
                trap = selected / ".claude"
                trap.mkdir(mode=0o700)
                save(trap/"settings.local.json", {"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"true"}]}]}})
                assert wire(relocated, caps)["op"] == "error"
                (trap/"settings.local.json").unlink()
                trap.rmdir()
                successor = wire(relocated, caps)
                assert successor["op"] == "launch" and successor["session"]["cwd"] == str(selected), successor
                sid = successor["session"]["id"]
                joined_turn(sid,native,retained,selected)
                resumed = state/"rust-v1"/sid.split("/")[1]
                assert json.loads((resumed/"observation.json").read_text())["conversation_id"] == native
                assert json.loads((resumed/"session.json").read_text())["claude_resume"]["source_cwd"] == str(work)
                cli("kill", sid)
                eventually(lambda:(resumed/"exit.json").exists(),"relocated worker exit")
                print("native Claude resumed the original transcript at the selected checkout; duplicate transcript and unsafe destination refused")
                return
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
    parser.add_argument("--relocate", action="store_true", help="resume a stopped native conversation after its original directory disappears")
    parser.add_argument("--board", action="store_true", help="resume using the ordinary dashboard action")
    parser.add_argument("--external",action="store_true")
    parser.add_argument("--go-owner",type=fixtures.binary_path)
    parser.add_argument("--capacity",action="store_true")
    args=parser.parse_args()
    if args.capacity and not args.external or args.go_owner and not (args.external or args.relocate) or args.capacity and args.go_owner:
        parser.error("--capacity is an external case; --go-owner qualifies external or relocated acquisition")
    if args.relocate and (args.external or args.worktree or args.board):
        parser.error("--relocate runs a separate missing-directory case")
    if args.external and (args.worktree or args.board):
        parser.error("--external uses its own native background session")
    root=Path(tempfile.mkdtemp(prefix="cr-",dir=Path("/tmp").resolve()))
    server=http.server.ThreadingHTTPServer(("127.0.0.1",0),RelocationModel if args.relocate else Provider)
    server.joined_relocation = False
    server.daemon_threads=True
    server.observed=[]
    server.sentinel=root/"must-not-exist"
    thread=threading.Thread(target=server.serve_forever,daemon=True)
    thread.start()
    success=False
    try:
        verify(args.candidate, args.provider, root, server, worktree=args.worktree,
               through_board=args.board, external=args.external, go_owner=args.go_owner, capacity=args.capacity, relocate=args.relocate)
        assert not server.sentinel.exists()
        success=True
    finally:
        server.shutdown()
        server.server_close()
        if success:
            shutil.rmtree(root)
        else:
            print(f"retained private fixture at {root}",file=sys.stderr)
