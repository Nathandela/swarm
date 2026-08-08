# A1b — a supervised session never inherits ambient Remote Control (bead e8nn, code half)

**Workpackage**: the code half of `agents-tracker-e8nn` — the spawn seam only
(`internal/persist/env.go`, `internal/shim/shim.go`, `internal/adapter/claude/claude.go`, and the
two new test files). No daemon, protocol, gateway, or phone-core file is touched.
**Governing documents**: [ADR-010](../adr/ADR-010-adapter-structured-capture.md) §5 (the Claude Code
grounding paragraph that names this residual), [spike-SC.md](spike-SC.md)'s closing note
(agents-tracker-n047, the investigation that found it), [ADR-007](../adr/ADR-007-remote-access.md)
D8 (daemon-owned launch env policy — the pattern this follows),
[ADR-004](../adr/ADR-004-security-baseline.md) item 6 (the launch-env allowlist this sits beneath).
**Obligation**: implementation-goals.md GG-5 — the failing-first runs are recorded verbatim below,
GREEN appended after. Two RED→GREEN cycles.

## 1. The residual, restated

n047 closed the "can Claude Code's native Remote Control replace swarm's hook relay" question with
**no** — but recorded one live residual, verbatim from spike-SC.md:

> The one live residual is the co-occurrence risk observed above: a user who runs Remote Control
> themselves may race swarm's `PermissionRequest` hook answer, and a supervised session must not
> inherit ambient remote-control environment — tracked as its own issue.

The observation behind it: the spike's own probe sessions displayed a live "remote control -SWARM"
status bar **inherited from the ambient environment, not configured by the spike**. A supervised
session that did the same would carry its approvals to Anthropic's relay behind swarm's back, in
parallel with — and racing — the `PermissionRequest` hook that ADR-010 makes the one-tap path.

This slice closes the *machine-side* half: swarm never hands a supervised child a remote-control
environment, and never composes a remote-control flag into its argv. The co-occurrence half (an
operator running Remote Control themselves, in their own terminal) is a live account-tied probe and
is **not** closed here — see §7.

## 2. Where the env actually flows, measured before writing anything

The honest starting position, because it changes what the fence is *for*:

| seam | agent env | remote-control variables today |
|---|---|---|
| TUI → daemon (`internal/tui/launch.go:427`, `general.go:489`) | `os.Environ()` of the terminal | present if the operator has them |
| protocol server (`internal/protocol/server.go:54`) | `persist.FilterEnv(req.Env)`; on the remote tier dropped entirely (R-POL.5) | **already dropped** — the allowlist is exact-match plus `LC_*` |
| daemon spawn (`internal/daemon/launch.go:352`) | `injectHookEnv(persist.FilterEnv(...))` — the four hook vars added post-filter | still dropped |
| shim → agent (`internal/shim/shim.go:128`, `buildEnv`) | `cfg.Env` verbatim + `TERM` if absent | **passed through verbatim** |

So the daemon tier already dropped these variables *incidentally*, as a side effect of the ADR-004
allowlist existing for a different reason (S-2: not immortalizing shell secrets in `meta.json`).
Nothing stated the remote-control invariant, nothing tested it, and the last gate before `exec`
honored whatever it was handed. That is what this slice fixes: the fence is placed at the exec gate,
where it survives an allowlist widening, a post-filter injection like `injectHookEnv`, or a
hand-written `shim-launch.json` — none of which the upstream allowlist protects against.

The argv seam had a real hole rather than an incidental cover: see §4.

## 3. The variable family, taken from the shipped binary

Not guessed. `claude --help` at **2.1.224** (offline; no session started, no network):

```
Usage: claude [options] [command] [prompt]
  --remote-control [name]               Start an interactive session with Remote
  --remote-control-session-name-prefix <prefix>
      Prefix for auto-generated Remote Control session names (default: hostname)
```

`strings` over the same binary, filtered to `CLAUDE_*` names on the remote surface:

```
CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX
CLAUDE_REMOTE_WORKFLOW_ARGS
CLAUDE_REMOTE_WORKFLOW_SCRIPT
CLAUDE_CODE_REMOTE
CLAUDE_CODE_REMOTE_ENVIRONMENT_TYPE
CLAUDE_CODE_REMOTE_HERMETIC_MODE
CLAUDE_CODE_REMOTE_MEMORY_DIR
CLAUDE_CODE_REMOTE_RAW_EVENTS_FILE
CLAUDE_CODE_REMOTE_SEND_KEEPALIVES
CLAUDE_CODE_REMOTE_SESSION_ID
CLAUDE_CODE_REMOTE_SESSION_ORIGIN
CLAUDE_CODE_REMOTE_SESSION_UUID
CLAUDE_CODE_REMOTE_SETTINGS_PATH
CLAUDE_CODE_REMOTE_SETTINGS_POLL_MS
```

**The brief named one variable; the binary ships fourteen.** `CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX`
is the `--remote-control-session-name-prefix` flag's env twin — by its own help text a *naming*
knob, not the switch that turns the feature on. The plausible switch,
`CLAUDE_CODE_REMOTE` and its session/settings group, is a different prefix entirely. Scrubbing only
the name in the brief would have produced a fence that reads as complete and leaves the more likely
enable path open, so the denylist is the two prefixes that span the whole observed family:

```go
var remoteControlEnvPrefixes = []string{
	"CLAUDE_REMOTE_",     // CLAUDE_REMOTE_CONTROL_*, CLAUDE_REMOTE_WORKFLOW_*
	"CLAUDE_CODE_REMOTE", // CLAUDE_CODE_REMOTE itself and its CLAUDE_CODE_REMOTE_* group
}
```

Prefixes rather than the fixed fourteen, so a sibling shipped later is covered on the day it
appears. **Honesty limit**: which variable actually lit the status bar the spike observed is *not*
established by `strings` and is not established here. It is a live-observation question and belongs
to §7's manual half. The fence does not depend on the answer — it removes the whole family.

## 4. RED — cycle 1, the failing-first runs

Both tests new; no production line written yet.

### 4a. Env — `internal/shim/spawn_remotecontrol_test.go`

```
$ go test ./internal/shim/ -run TestSpawn_EnvScrubsRemoteControl -count=1
--- FAIL: TestSpawn_EnvScrubsRemoteControl (0.09s)
    spawn_remotecontrol_test.go:37: agent env carries "CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=SWARM" — a supervised session must not inherit ambient Remote Control (e8nn)
    spawn_remotecontrol_test.go:37: agent env carries "CLAUDE_REMOTE_CONTROL_FUTURE_KNOB=1" — a supervised session must not inherit ambient Remote Control (e8nn)
FAIL
FAIL	github.com/Nathandela/swarm/internal/shim	1.535s
FAIL
```

This is an end-to-end spawn, not a unit call: it rides the existing `modeInfo` helper that prints
its own `environ` over the PTY (the same instrument `TestSpawn_EnvIsCapturedNotInherited` uses for
the S-6 differential), so the assertion is on what the **real child process actually saw**.

### 4b. Argv — `internal/adapter/claude/remotecontrol_test.go`

```
$ go test ./internal/adapter/claude/ -run 'TestCommand_OperatorOptions|TestOptions_DeclareNo' -count=1
--- FAIL: TestCommand_OperatorOptionsNeverForwardARemoteControlFlag (0.01s)
    --- FAIL: TestCommand_OperatorOptionsNeverForwardARemoteControlFlag/model_smuggles_the_flag (0.00s)
        remotecontrol_test.go:51: argv[4] = "--remote-control" — an operator option forwarded a remote-control flag into a supervised argv (e8nn)
            argv: ["claude" "--settings" "{...}" "--model" "--remote-control"]
    --- FAIL: TestCommand_OperatorOptionsNeverForwardARemoteControlFlag/model_smuggles_the_prefix_flag (0.00s)
        remotecontrol_test.go:51: argv[4] = "--remote-control-session-name-prefix" — an operator option forwarded a remote-control flag into a supervised argv (e8nn)
            argv: ["claude" "--settings" "{...}" "--model" "--remote-control-session-name-prefix"]
    --- FAIL: TestCommand_OperatorOptionsNeverForwardARemoteControlFlag/model_smuggles_the_flag_with_a_name (0.00s)
        remotecontrol_test.go:51: argv[4] = "--remote-control=SWARM" — an operator option forwarded a remote-control flag into a supervised argv (e8nn)
            argv: ["claude" "--settings" "{...}" "--model" "--remote-control=SWARM"]
    --- FAIL: TestCommand_OperatorOptionsNeverForwardARemoteControlFlag/skip-permissions_still_composes_its_own_literal (0.00s)
        remotecontrol_test.go:51: argv[4] = "--remote-control" — an operator option forwarded a remote-control flag into a supervised argv (e8nn)
            argv: ["claude" "--settings" "{...}" "--model" "--remote-control" "--dangerously-skip-permissions"]
FAIL
FAIL	github.com/Nathandela/swarm/internal/adapter/claude	0.512s
FAIL
```

The `"{...}"` elision is this document's only edit to the captured output: each line carried the
full inline `--settings` hook JSON verbatim. Everything else, including argv indices, is as emitted.

Two things this run establishes.

1. **The argv hole was real, not incidental.** `model` is declared `Type: "string"` with mere
   `Suggest` values, the protocol server caps option values at 4 KiB but validates nothing else
   (`maxOptionValue`, `server.go:115`), and `optionFlags` appended the value straight after
   `--model`. Any string an operator can type into the launch form became an argv token.
2. `TestOptions_DeclareNoRemoteControlSwitch` **passed on this run** and is recorded as a
   GREEN-from-the-start **drift fence**, not a RED→GREEN claim: the option schema offers no
   remote-control switch today, and this test exists to fail on the day someone adds one.

## 5. RED — cycle 2, the widened family

With cycle 1 green, the §3 `strings` finding widened the test's denied set from the two
`CLAUDE_REMOTE_CONTROL*` names to the family the binary actually ships. The five new members failed
against cycle 1's single-prefix implementation:

```
$ go test ./internal/shim/ -run TestSpawn_EnvScrubsRemoteControl -count=1
--- FAIL: TestSpawn_EnvScrubsRemoteControl (0.17s)
    spawn_remotecontrol_test.go:56: agent env carries "CLAUDE_REMOTE_WORKFLOW_SCRIPT=/tmp/w.sh" — a supervised session must not inherit ambient Remote Control (e8nn)
    spawn_remotecontrol_test.go:56: agent env carries "CLAUDE_REMOTE_WORKFLOW_ARGS=--go" — a supervised session must not inherit ambient Remote Control (e8nn)
    spawn_remotecontrol_test.go:56: agent env carries "CLAUDE_CODE_REMOTE=1" — a supervised session must not inherit ambient Remote Control (e8nn)
    spawn_remotecontrol_test.go:56: agent env carries "CLAUDE_CODE_REMOTE_SESSION_ID=abc" — a supervised session must not inherit ambient Remote Control (e8nn)
    spawn_remotecontrol_test.go:56: agent env carries "CLAUDE_CODE_REMOTE_SETTINGS_PATH=/tmp/s.json" — a supervised session must not inherit ambient Remote Control (e8nn)
FAIL
FAIL	github.com/Nathandela/swarm/internal/shim	1.684s
FAIL
```

Exactly the five new members fail and the two originals do not — the cycle-1 implementation still
holds its ground, which is what makes this a genuine second cycle rather than a rewrite. This is
the sanctioned fixture-exercises-every-field update: the fence must exercise every denied prefix, so
it grew when the denied set grew. No pre-existing test was modified in either cycle.

The same run pins the fence's **narrowness** with three controls that must survive:
`CLAUDE_CODE_ENTRYPOINT=cli` (a `CLAUDE_` variable outside the family),
`SWARM_CLAUDE_REMOTE_CONTROL_NOTE=1` (contains the text, is not a `CLAUDE_` name), and
`EDITOR=CLAUDE_REMOTE_CONTROL_IS_A_VALUE` (the text in a VALUE, never a name). They pin that the
match is anchored to the variable name rather than a substring sweep.

## 6. GREEN

```
$ go test ./internal/shim/ -run TestSpawn_EnvScrubsRemoteControl -race -count=1 -v
=== RUN   TestSpawn_EnvScrubsRemoteControl
--- PASS: TestSpawn_EnvScrubsRemoteControl (1.25s)
PASS
ok  	github.com/Nathandela/swarm/internal/shim	3.932s
```

```
$ go test ./internal/adapter/claude/ -run 'TestCommand_OperatorOptionsNeverForwardARemoteControlFlag|TestOptions_DeclareNoRemoteControlSwitch' -count=1 -v
=== RUN   TestCommand_OperatorOptionsNeverForwardARemoteControlFlag
--- PASS: TestCommand_OperatorOptionsNeverForwardARemoteControlFlag (0.01s)
    --- PASS: TestCommand_OperatorOptionsNeverForwardARemoteControlFlag/no_options (0.00s)
    --- PASS: TestCommand_OperatorOptionsNeverForwardARemoteControlFlag/honest_model (0.00s)
    --- PASS: TestCommand_OperatorOptionsNeverForwardARemoteControlFlag/model_smuggles_the_flag (0.00s)
    --- PASS: TestCommand_OperatorOptionsNeverForwardARemoteControlFlag/model_smuggles_the_prefix_flag (0.00s)
    --- PASS: TestCommand_OperatorOptionsNeverForwardARemoteControlFlag/model_smuggles_the_flag_with_a_name (0.00s)
    --- PASS: TestCommand_OperatorOptionsNeverForwardARemoteControlFlag/unknown_option_key_is_not_a_flag_source (0.00s)
    --- PASS: TestCommand_OperatorOptionsNeverForwardARemoteControlFlag/skip-permissions_still_composes_its_own_literal (0.00s)
=== RUN   TestOptions_DeclareNoRemoteControlSwitch
--- PASS: TestOptions_DeclareNoRemoteControlSwitch (0.00s)
PASS
ok  	github.com/Nathandela/swarm/internal/adapter/claude	0.675s
```

### The whole diff

| file | change |
|---|---|
| `internal/persist/env.go` | `remoteControlEnvPrefixes` (the denylist, beside the existing allowlist) + `ScrubRemoteControl` + `hasAnyPrefix`. ~15 lines of code under ~20 lines of grounding comment. |
| `internal/shim/shim.go` | `buildEnv` opens with `env = persist.ScrubRemoteControl(env)`; one import. |
| `internal/adapter/claude/claude.go` | `optionFlags`: `if m := opts["model"]; m != ""` gains `&& !strings.HasPrefix(m, "-")`. |
| `internal/shim/spawn_remotecontrol_test.go` | new — the env fence. |
| `internal/adapter/claude/remotecontrol_test.go` | new — the argv fence + the option-schema drift fence. |

Two design notes worth their line.

**Why the policy lives in `persist` and the application in `shim`.** ADR-007 D8 makes the launch
environment daemon policy, and `internal/persist/env.go` is where that policy already lives (its own
header calls itself the normative launch-environment allowlist). Splitting the list into `shim`
would have given the codebase two env policies in two packages. Applying it at `buildEnv` rather
than at `daemon.spawnShim` is the deliberate half: `spawnShim` composes the env, `buildEnv` is the
last thing that touches it before `exec`, and only the latter cannot be bypassed.

**Why a flag-shaped `model` is dropped rather than rejected.** No model alias starts with `-`, so
the value is meaningless as a model and the drop lands on the CLI's own default — identical to what
an empty value already does. It also matches the function's existing habit: a
`dangerously-skip-permissions` value other than `"true"` is likewise ignored rather than raised.

### Gates

```
go build ./...     OK
go vet ./...       OK
golangci-lint run  no finding in any file this slice created or edited
                   (5 pre-existing errcheck findings in internal/persist, unrelated lines)
go test ./...      green
```

Two caveats on those gate runs, recorded rather than hidden.

This slice was executed in a worktree **shared with the concurrent a1b Claude-Code-producer slice**,
so a whole-tree `go test ./...` transiently observes that slice's in-flight RED (one such run failed
in `internal/adapter/claude/interaction_test.go:270`, the producer's own prompt-card cycle, and was
green on the next run once their GREEN landed). Every result attributed to *this* slice above is
from a run of its own tests, and `internal/adapter/claude` + `internal/shim` were both confirmed
green together after the fact.

One observed flake: on the first full-suite run
`cmd/swarm TestRunShim_LaunchesAgentPersistsAndLeadsSession` failed with "shim pid never became its
own session leader" after 16.5 s. It passed at `-count=3` in isolation and the whole `cmd/swarm`
package passed on re-run. It is a load-related timing flake, and it cannot be this slice's: that
test asserts the **shim process** is a session leader via `ensureSession`'s re-exec, which composes
its env from `os.Environ()` in `cmd/swarm/main.go` and never passes through `buildEnv` at all.

## 7. NOT closed here — the manual half, and the ceilings

**The live co-occurrence probe is account-tied and was not run.** Running `claude --remote-control`
for real against Anthropic's relay, alongside a supervised session, to observe whether the operator's
own Remote Control races swarm's `PermissionRequest` answer — that is the observation n047's residual
names, and it needs a claude.ai subscription and a live session. This slice was executed under a
no-live-session constraint (no `--remote-control`, no pairing, no login). It is the manual half and
remains open. What §3 leaves for it specifically: **which** variable lights the status bar, which
`strings` cannot answer.

Three ceilings, marked `ponytail:` at the seam where they bite:

- **The positional `InitialPrompt` is still unseparated.** `Command` appends it as the last argv
  element with no `--` separator, so a prompt beginning with `-` is parsed as a flag rather than
  text. Closing it means emitting `--` before the positional, which changes every claude launch argv
  and whose acceptance should be confirmed against the live CLI first. Left open deliberately: under
  the frozen threat model it is not an attack path (B133 — the phone is trusted; the operator types
  their own prompt), so it is a robustness fix, not a security one.
- **`Resume` passes the captured conversation id unguarded.** `claude --resume <id>` takes the id
  from `ExtractConversationID`, i.e. from the CLI's own output, so a repository that printed
  `Session --remote-control ` could shape a flag-shaped id. Machine-side and out of this
  workpackage's scope (VT is machine-side, frozen), but it is the same argv class and should be
  closed with the same one-line prefix guard when someone touches that seam.
- **`internal/adapter/detect` inherits the daemon's full environment.** `Host.Run`
  (`detect.go:44`) execs `claude --version` with no `cmd.Env`, so the probe sees everything the
  daemon has, remote-control variables included. It starts no session and prints a version banner,
  which is why it is a ceiling and not a finding — but it is the one remaining `claude` exec in the
  tree that does not go through the scrubbed path.

## 8. What a reader should check first if this fence ever trips

The two prefixes in `remoteControlEnvPrefixes` are a snapshot of Claude Code **2.1.224**. If a
future CLI moves the feature to a name outside `CLAUDE_REMOTE_*` / `CLAUDE_CODE_REMOTE*` — say a
plain `CLAUDE_RC` — the fence goes quietly stale: the tests keep passing because they test the
family the code declares. Re-running the §3 `strings` filter against the installed binary is the
cheap re-grounding, and it is the first thing to do on any CLI-drift re-record.
