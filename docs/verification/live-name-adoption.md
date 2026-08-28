# Live session name adoption: evidence

Decision: [ADR-022](../adr/ADR-022-live-session-name-adoption.md). Beads: agents-tracker-tila (investigation), agents-tracker-zlvc (change).

## The data the decision rests on (owner's machine, 2026-08-28)

A read-only probe joined every session in `~/.local/state/swarm/sessions` to the name its
CLI keeps for the same conversation. Ten Claude sessions were joinable; three had drifted:

```
claude live  asm2yxqsdpha54sw swarm="WIP merging PR and modifying"   -> custom="claude-swarm"
claude ended tv2umh5rvcgmcl7y swarm="Investigate handoff feature"    -> custom="WIP investigate handoff feature"
claude live  qu7w6mvitg6dova7 swarm="WIP Gg technical prep"          -> custom="Gg technical prep"
codex  live  urdwh3vczxlbrii7 swarm="Codex story"                    -> thread_name="Codex story" originator=swarm
```

The Codex row is the existing plane working: `~/.codex/session_index.jsonl` holds
`codex-NewLatexCV` (the launch default) and then `Codex story` (the swarm rename) for that
swarm-originated thread. Driving `codex app-server` over stdio confirmed `thread/name/set`
before and after the first turn, each answered by `thread/name/updated`.

Claude's registry file for a swarm-launched session, verbatim shape (pid 2246):

```
{"pid":2246,"sessionId":"fff31caf-...","cwd":"/Users/Nathan/Code/swarm","version":"2.1.250",
 "kind":"interactive","name":"WIP correction on chat","nameSource":"user","nameSince":1787894219289,"status":"busy"}
```

## Failing first (GG-5)

Tests written before any implementation: `internal/adapter/claude/livename_test.go`,
`internal/daemon/rename_at_test.go`, `internal/skeleton/live_name_test.go`. The run fails
on the missing symbols and on nothing else:

```
internal/adapter/claude/livename_test.go:19:43: undefined: adapter.LiveNameSource
internal/adapter/claude/livename_test.go:21:21: undefined: adapter.AsLiveNameSource
FAIL	github.com/Nathandela/swarm/internal/adapter/claude [build failed]
internal/daemon/rename_at_test.go:22:9: got.NameSetAt undefined (type persist.Meta has no field or method NameSetAt)
internal/daemon/rename_at_test.go:35:14: d.RenameAt undefined (type *Daemon has no field or method RenameAt)
FAIL	github.com/Nathandela/swarm/internal/daemon [build failed]
internal/skeleton/live_name_test.go:65:7: m.NameSetAt undefined (type persist.Meta has no field or method NameSetAt)
FAIL	github.com/Nathandela/swarm/internal/skeleton [build failed]
```

## Green

Same selection after the implementation, 16 passing tests across the three packages:

```
ok  	github.com/Nathandela/swarm/internal/adapter/claude	0.759s
ok  	github.com/Nathandela/swarm/internal/daemon	3.837s
ok  	github.com/Nathandela/swarm/internal/skeleton	5.797s
```

`go test -race` on `internal/adapter/...`, `internal/daemon`, `internal/persist` and
`internal/protocol`: all ok. The whole-repo run and lint are recorded in the commit that
lands this file.

What the skeleton tests pin, each through the real hook path (`serveHookInteractions`)
with a temp home and a real claude adapter: launch stamps `NameSetAt`; a newer Claude name
is adopted with Claude's own timestamp; a later swarm rename beats an older Claude name;
the newest of several registry files wins; another session's file is ignored; the adopted
name is sanitized like any rename; oversized, malformed and non-regular files are skipped;
no conversation id means no lookup.
