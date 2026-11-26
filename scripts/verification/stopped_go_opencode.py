#!/usr/bin/env python3
"""Verify historical stopped classic OpenCode and later Go readability."""
import json
import os
from pathlib import Path
import shutil
import shlex
import subprocess
import sys
import tempfile

from stopped_go_state import await_value, daemon, disk, fixture_env, listed, owner, private, refused

THREAD = "ses_20261125zzzzzzzzzzzzzzzzzz"


def exercise(go, current_go, rust, base):
    source, tools, home = [base / n for n in ("state", "bin", "home")]
    for p in (source, tools, home):
        p.mkdir(mode=0o700)
    observed = base / "opencode-argv"
    opencode = tools / "opencode"
    opencode.write_text('#!/bin/sh\nif [ "$1" = --version ]; then echo "1.17.9"; exit; fi\n'
                   f'printf "%s\\n" "$@" > {shlex.quote(str(observed))}\n'
                   f'printf "opencode -s {THREAD}\\n"\nexec /bin/sleep 300\n')
    opencode.chmod(0o700)
    ge = fixture_env(base, source, tools)
    launch = {"agent":"opencode", "cwd":str(home), "name":"Original OpenCode", "cols":80,"rows":24,
              "options":{"model":"fixture/model","agent":"fixture-agent"}, "initial_prompt":"fixture prompt",
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
        refused(rust, fixture_env(base,source,tools,compat=True)|{"SWARM_DAEMON_LIVE_GO_COMPAT":"1"},
                "unsupported live Go shim state or wire version")
        assert disk(src/"meta.json")["status"]["Process"]=="running"
    finally:
        if session:
            with daemon(go,ge,base/"go.log"):
                assert owner(ge,{"op":"kill","session_id":session})["op"]=="ok"
                await_value(lambda:listed(ge)[0]["status"]["Process"]=="exited")
    assert disk(src/"meta.json")["conversation_id"]==THREAD
    assert disk(src/"meta.json")["shim_wire_version"]==1
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
    for path, change, reason in [
        (metadata, {"schema_version":3}, "requires offline qualification"),
        (metadata, {"account_binding":{}}, "unqualified Go role"),
        (launchfile, {"opencode":{}}, "Go launch binding or native writer is unqualified"),
        (launchfile, {"account_binding":{}}, "Go launch binding or native writer is unqualified"),
        (launchfile, {"argv":[str(opencode),"--port","9090"]}, "Go launch binding or native writer is unqualified"),
        (launchfile, {"argv":[str(opencode),"--session",THREAD,"--session",THREAD]}, "Go launch binding or native writer is unqualified"),
        (launchfile, {"argv":[str(opencode),"--session","ses_1234567890"]}, "Go launch binding or native writer is unqualified"),
        (launchfile, {"argv":[str(opencode),"--agent","foreign"]}, "Go launch binding or native writer is unqualified"),
        (launchfile, {"argv":[str(opencode),"--prompt","one","--prompt","two"]}, "Go launch binding or native writer is unqualified"),
    ]:
        before=path.read_bytes()
        value=disk(path)|change
        private(path,value)
        refused(rust,re,reason)
        assert disk(path)==value
        path.write_bytes(before)
    raw=launchfile.read_bytes()
    for prefix in (b'{"account_binding":{},"account_binding":null,',b'{"Account_Binding":{},'):
        altered=prefix+raw.lstrip()[1:]
        launchfile.write_bytes(altered)
        refused(rust,re,"Go launch evidence unavailable")
        assert launchfile.read_bytes()==altered
        launchfile.write_bytes(raw)
    proof=owned/"opencode-binding.json"
    private(proof,{"managed":"test"})
    refused(rust,re,"Go shim or backend custody remains")
    proof.unlink()
    before={p.name:p.read_bytes() for p in owned.iterdir() if p.is_file() and p!=metadata}
    with daemon(rust,re,base/"rust.log"):
        assert listed(re)==[expected]
        cli=subprocess.check_output([rust,"ls","--json"],env=re,text=True)
        assert json.loads(cli)==[expected]
        id=expected["id"]
        assert owner(re,{"op":"rename","session_id":id,"name":"Renamed under Rust"})["op"]=="ok"
        assert owner(re,{"op":"set_tag","session_id":id,"tag":"restored"})["op"]=="ok"
    assert before=={p.name:p.read_bytes() for p in owned.iterdir() if p.is_file() and p!=metadata}
    with daemon(current_go,ge,base/"current-go.log"):
        assert listed(ge)==[expected|{"name":"Renamed under Rust","tag":"restored"}]
    with daemon(go,ge,base/"copy-go.log"):
        assert listed(ge)==[expected|{"name":"Renamed under Rust","tag":"restored"}]
        reply=owner(ge,{"op":"launch","launch":launch|{"options":{"resume_from":id}}})
        assert reply["op"]=="launch",reply
        resumed=reply["session"]["id"]
        try:
            await_value(lambda:"--session\n"+THREAD in observed.read_text())
        finally:
            assert owner(ge,{"op":"kill","session_id":resumed})["op"]=="ok"
            await_value(lambda:any(r["id"]==resumed and r["status"]["Process"]=="exited" for r in listed(ge)))
    print("PASS historical OpenCode: v0.18.2 stopped copy, Rust edits, current Go readability, v0.18.2 resume")


if __name__=="__main__":
    if len(sys.argv)!=4:
        sys.exit("usage: stopped_go_opencode.py GO_V0182 PINNED_CURRENT_GO RUST")
    binaries=(str(Path(argument).resolve(strict=True)) for argument in sys.argv[1:])
    with tempfile.TemporaryDirectory(prefix="oc-",dir=os.path.realpath("/tmp")) as root:
        exercise(*binaries,Path(root))
