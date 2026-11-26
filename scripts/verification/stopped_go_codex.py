#!/usr/bin/env python3
"""Verify a stopped ordinary Codex PTY and its joined Go app-server evidence."""
import json
import os
from pathlib import Path
import shlex
import shutil
import stat
import subprocess
import sys
import tempfile

from stopped_go_state import await_value, daemon, disk, fixture_env, listed, owner, private, refused

THREAD = "01997f00-face-7000-8000-00000000cde0"


def exercise(go, rust, synthetic, base):
    source, tools, home = (base / n for n in ("state", "bin", "home"))
    for path in (source, tools, home):
        path.mkdir(mode=0o700)
    args = base / "codex-argv"
    codex = tools / "codex"
    codex.write_text(
        '#!/bin/sh\nif [ "$1" = --version ]; then echo "codex-cli 0.167.0"; exit; fi\n'
        f'if [ "$1" != app-server ]; then printf "%s\\n" "$@" > {shlex.quote(str(args))}; fi\n'
        f'exec {shlex.quote(synthetic)} "$@"\n'
    )
    codex.chmod(0o700)
    ge = fixture_env(base, source, tools)
    launch = {"agent": "codex", "cwd": str(home), "name": "Original Codex",
              "cols": 80, "rows": 24, "env": [f"PATH={ge['PATH']}", f"HOME={ge['HOME']}"]}
    with daemon(go, ge, base / "go.log"):
        opened = owner(ge, {"op":"launch", "launch":launch})
        assert opened["op"]=="launch",opened
        session=opened["session"]["id"]
        local=session.split("/")[1]
        path=source/local
        try:
            await_value(lambda: disk(path/"meta.json")["conversation_id"]==THREAD)
            native=disk(path/"native-process.json")
            assert native["backend_pid"] and native["backend_start_time"]
        finally:
            assert owner(ge,{"op":"kill","session_id":session})["op"]=="ok"
            await_value(lambda:listed(ge)[0]["status"]["Process"]=="exited")
    assert disk(path/"native-writers-stopped.json")["native"]==native
    assert disk(path/"exit.json")["backend_exit"]==143
    assert (path/"codex.sock").is_socket()
    orig_re=fixture_env(base,source,tools,compat=True)
    refused(rust,orig_re,"Go shim or backend custody remains")
    assert (path/"codex.sock").is_socket()
    copy=base/"copy"
    def socket_entries(directory,names):
        return [name for name in names if stat.S_ISSOCK((Path(directory)/name).lstat().st_mode)]
    shutil.copytree(source,copy,ignore=socket_entries)
    ge=fixture_env(base,copy,tools)
    re=fixture_env(base,copy,tools,compat=True)
    path=copy/local
    meta=path/"meta.json"
    with daemon(go,ge,base/"copy.log"):
        expected,=listed(ge)
    launch_witness=disk(path/"shim-launch.json")
    for filename,changes,reason in [
        ("shim-launch.json",{"backend_args":launch_witness["backend_args"]+["--listen","unix:///foreign.sock"]},"Go launch binding or native writer is unqualified"),
        ("shim-launch.json",{"argv":launch_witness["argv"]+["exec"]},"Go launch binding or native writer is unqualified"),
        ("shim-launch.json",{"backend_cwd":str(base)},"Go launch binding or native writer is unqualified"),
        ("shim-launch.json",{"backend_socket_path":str(base/"wrong.sock")},"Go launch binding or native writer is unqualified"),
        ("shim-launch.json",{"backend_env":False},"Go launch binding or native writer is unqualified"),
        ("exit.json",{"backend_exit":None},"missing Go Codex backend exit"),
        ("native-writers-stopped.json",{"native":native|{"backend_start_time":0}},"stop proof disagrees"),
    ]:
        file=path/filename
        saved=file.read_bytes()
        altered=disk(file)|changes
        private(file,altered)
        refused(rust,re,reason)
        assert disk(file)==altered
        file.write_bytes(saved)
    identities={p.name:p.read_bytes() for p in path.iterdir() if p.is_file() and p!=meta}
    with daemon(rust,re,base/"rust.log"):
        assert json.loads(subprocess.check_output([rust,"ls","--json"],env=re,text=True))==[expected]
        id=expected["id"]
        assert owner(re,{"op":"rename","session_id":id,"name":"Codex under Rust"})["op"]=="ok"
        assert owner(re,{"op":"set_tag","session_id":id,"tag":"retained"})["op"]=="ok"
    assert identities=={p.name:p.read_bytes() for p in path.iterdir() if p.is_file() and p!=meta}
    with daemon(go,ge,base/"copy.log"):
        assert listed(ge)==[expected|{"name":"Codex under Rust","tag":"retained"}]
        assert disk(meta)["conversation_id"]==THREAD
        reply=owner(ge,{"op":"launch","launch":launch|{"options":{"resume_from":id}}})
        assert reply["op"]=="launch",reply
        resumed=reply["session"]["id"]
        try:
            await_value(lambda:THREAD in args.read_text().splitlines())
            assert disk(copy/resumed.split("/")[1]/"meta.json")["conversation_id"]==THREAD
        finally:
            assert owner(ge,{"op":"kill","session_id":resumed})["op"]=="ok"
            await_value(lambda:any(r["id"]==resumed and r["status"]["Process"]=="exited" for r in listed(ge)))
    print("PASS stopped Codex: joined Go backend, preserved Rust labels, pinned Go resume")


if __name__=="__main__":
    if len(sys.argv)!=4:
        sys.exit("usage: stopped_go_codex.py PINNED_GO RUST PINNED_GO_FAKE_CODEX")
    bins=(str(Path(arg).resolve(strict=True)) for arg in sys.argv[1:])
    with tempfile.TemporaryDirectory(prefix="cd-",dir=os.path.realpath("/tmp")) as root:
        exercise(*bins,Path(root))
