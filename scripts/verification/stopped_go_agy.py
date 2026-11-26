#!/usr/bin/env python3
"""Exercise stopped classic agy from genuine pinned Go state and rollback."""
import json
import os
from pathlib import Path
import shutil
import shlex
import subprocess
import sys
import tempfile

from stopped_go_state import await_value, daemon, disk, fixture_env, listed, owner, private, refused

THREAD = "11111111-2222-4333-8444-555555555555"


def exercise(go, rust, base):
    source, tools, home = [base / n for n in ("state", "bin", "home")]
    for p in (source, tools, home):
        p.mkdir(mode=0o700)
    observed = base / "agy-argv"
    agy = tools / "agy"
    agy.write_text('#!/bin/sh\nif [ "$1" = --version ]; then echo "1.1.4"; exit; fi\n'
                   f'printf "%s\\n" "$@" > {shlex.quote(str(observed))}\n'
                   f'printf "agy --conversation={THREAD}\\n"\nexec /bin/sleep 300\n')
    agy.chmod(0o700)
    ge = fixture_env(base, source, tools)
    launch = {"agent":"agy", "cwd":str(home), "name":"Original agy", "cols":80,"rows":24,
              "env":[f"PATH={ge['PATH']}",f"HOME={ge['HOME']}"]}
    session = None
    try:
        with daemon(go,ge,base/"go.log"):
            reply=owner(ge,{"op":"launch","launch":launch})
            assert reply["op"]=="launch",reply
            session=reply["session"]["id"]
            local=session.split("/")[1]
            src=source/local
            await_value(lambda:disk(src/"meta.json")["conversation_id"]==THREAD)
        live_env = fixture_env(base, source, tools, compat=True) | {"SWARM_DAEMON_LIVE_GO_COMPAT":"1"}
        with daemon(rust, live_env, base / "live-rust.log"):
            assert listed(live_env)[0]["id"] == session
        assert disk(src/"meta.json")["status"]["Process"]=="running"
    finally:
        if session:
            with daemon(go,ge,base/"go.log"):
                assert owner(ge,{"op":"kill","session_id":session})["op"]=="ok"
                await_value(lambda:listed(ge)[0]["status"]["Process"]=="exited")
    assert disk(src/"meta.json")["conversation_id"]==THREAD
    assert not (src/"native-process.json").exists()
    assert not (src/"native-writers-stopped.json").exists()
    assert disk(src/"exit.json")["exit_code"]==disk(src/"meta.json")["exit_code"]
    copy=base/"copy"
    shutil.copytree(source,copy)
    ge=fixture_env(base,copy,tools)
    re=fixture_env(base,copy,tools,compat=True)
    owned=copy/local
    metadata=owned/"meta.json"
    launchfile=owned/"shim-launch.json"
    with daemon(go,ge,base/"copy-go.log"):
        expected,=listed(ge)
    for change in (
        {"require_launch_identity":"false"},
        {"scheduled_codex_backend":0},
        {"backend_required":[]},
        {"agy_input":{}},
        {"argv":[str(agy),"acp"]},
        {"argv":[str(agy),"--settings",'{"statusLine":{"command":"/unqualified/hook"}}']},
        {"argv":[str(agy),"--settings",'{"statusLine":{},"statusLine":{}}']},
        {"backend_program":str(agy)},
        {"cli_identity":disk(launchfile)["cli_identity"]|{"path":str(tools/"foreign-actor")}},
    ):
        before=launchfile.read_bytes()
        value=disk(launchfile)|change
        private(launchfile,value)
        refused(rust,re,"Go launch binding or native writer is unqualified")
        assert disk(launchfile)==value
        launchfile.write_bytes(before)
    private(owned/"native-process.json",{"schema_version":3})
    refused(rust,re,"Go shim or backend custody remains")
    (owned/"native-process.json").unlink()
    before={p.name:p.read_bytes() for p in owned.iterdir() if p.is_file() and p!=metadata}
    with daemon(rust,re,base/"rust.log"):
        assert listed(re)==[expected]
        cli=subprocess.check_output([rust,"ls","--json"],env=re,text=True)
        assert json.loads(cli)==[expected]
        id=expected["id"]
        assert owner(re,{"op":"rename","session_id":id,"name":"Renamed under Rust"})["op"]=="ok"
        assert owner(re,{"op":"set_tag","session_id":id,"tag":"restored"})["op"]=="ok"
    assert before=={p.name:p.read_bytes() for p in owned.iterdir() if p.is_file() and p!=metadata}
    with daemon(go,ge,base/"copy-go.log"):
        assert listed(ge)==[expected|{"name":"Renamed under Rust","tag":"restored"}]
        reply=owner(ge,{"op":"launch","launch":launch|{"options":{"resume_from":id}}})
        assert reply["op"]=="launch",reply
        resumed=reply["session"]["id"]
        try:
            await_value(lambda:"--conversation\n"+THREAD in observed.read_text())
        finally:
            assert owner(ge,{"op":"kill","session_id":resumed})["op"]=="ok"
            await_value(lambda:any(r["id"]==resumed and r["status"]["Process"]=="exited" for r in listed(ge)))
    print("PASS stopped classic agy: copied Go custody, Rust labels, pinned Go native resume")


if __name__=="__main__":
    if len(sys.argv)!=3:
        sys.exit("usage: stopped_go_agy.py PINNED_GO RUST")
    go,rust=(str(Path(argument).resolve(strict=True)) for argument in sys.argv[1:])
    with tempfile.TemporaryDirectory(prefix="agy-",dir=os.path.realpath("/tmp")) as root:
        exercise(go,rust,Path(root))
