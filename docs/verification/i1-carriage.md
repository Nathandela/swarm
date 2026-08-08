# I1 — the ingest carriage: the CLI's own body reaches the producer

**Slice**: ADR-010 §6, specified since 2026-08-07 and never built. `engine.Callback` gains
`Raw json.RawMessage` alongside `Payload`; `swarm hook` keeps the CLI's whole event body, under
the existing 1 MiB `hookStdinLimit`, for events whose adapter descriptor declares `capture=raw`
and for no others; and the daemon hands THAT body to the interaction producer instead of the
callback envelope it used to hand it.

**Why it is its own slice.** `docs/verification/a1b-claude-producer.md` §10 measured the gap and
did not hedge it: *"the producer of §1–§6 shapes NOTHING in production today"* — zero items on
every capture row, because the flattener keeps top-level **strings** and `tool_input` /
`tool_response` are objects. The producer was right and unreachable. This slice is the road.

**Normative**: [ADR-010](../adr/ADR-010-adapter-structured-capture.md) §5, §6;
[ADR-009](../adr/ADR-009-structured-chat-interaction.md);
[interaction-schema.md](../specifications/interaction-schema.md).
**Ground truth**: the recorded S-B corpus (`internal/adapter/claude/testdata/interaction/`),
replayed byte for byte. **Nothing in this slice ran a CLI.**

---

## 1. Decisions recorded (the point §6 left open)

1. **The hook learns its capture rows from the environment, injected at spawn.** This is the
   question a1b §10 flagged as needing a slice of its own: `swarm hook <event>` runs as a
   **child of the CLI**, so it knows its event name and nothing about the adapter that launched
   it, and it cannot decide on its own which rows declare `capture=raw`. §10 named two ways out
   — a spawn-injected variable, or carrying the body on every row and gating it daemon-side,
   *"which trades UDS bytes for simplicity and is a deviation from §6 as written"*. **The
   variable was taken, precisely because it is not a deviation**: §6's rule ("only for events
   whose descriptor declares capture=raw") is implemented as written, and no ADR amendment is
   needed — how the declaration *travels* is mechanism, not decision. `hookclient.EnvCapture`
   (`SWARM_HOOK_CAPTURE`) joins the four variables the daemon already injects, and is derived
   from the adapter's own `SignalSources` by `adapter.CaptureEvents`.

2. **The rows are resolved in the assembly, not in the daemon.** `internal/daemon` imports no
   adapter package (layering), so `daemon.LaunchSpec` gains `CaptureEvents []string` and
   `skeleton.composeLaunchSpec` fills it beside the argv it already composes there — the one
   layer holding both the registry and the launch spec. Resolved *before* the resume/fresh-launch
   branches, so a resumed session's hooks capture exactly like a fresh one's.

3. **An oversized or unparseable body is DROPPED, not clipped.** §6 keeps "the whole body, under
   the existing 1 MiB `hookStdinLimit`". A body over the limit arrives truncated, and a truncated
   JSON object is not the whole body — worse, `json.Marshal` **fails** on an invalid
   `json.RawMessage`, so carrying one would fail the whole callback and take the session's
   **status** down with it. §6 is explicit that raw bodies never influence status; letting one
   silence it would be that rule inverted. So the retention is gated on `json.Valid` and the
   status post goes out exactly as before. Fenced by
   `TestRunHook_CapsTheBodyAndStillPostsTheStatus`.

4. **`parseHookStdin` is untouched; the read is what moved.** §6 leaves "its string-flattening
   loop and its `turn`/`interaction` injection guard" alone, and this slice leaves the whole
   function alone, signature included — its three existing tests are unmodified. stdin is a
   stream and cannot be read twice, so `readHookBody` performs the one bounded read and the
   flattener runs over those bytes from memory. The retention decision then sits in `runHook`,
   the one place that knows both the event name and the environment.

5. **The daemon does not skip a bodyless callback.** An early return on `len(cb.Raw) == 0` was
   written, and removed: it is the daemon deciding what an event can shape, which is the
   adapter's decision alone (`Interactions` takes the whole `HookPayload`, so a shaper may
   legitimately answer from the event name). It was not a judgement call in the end — two
   existing tests failed on it, verbatim in §4.

6. **`decodeHookCallback` got smaller.** It existed to capture the callback ENVELOPE's bytes
   alongside the decoded value, because the envelope was what the shaper was handed. The body
   now rides inside the callback, so the decode is ordinary and the trick is deleted.

---

## 2. RED, verbatim

All four layers' tests written before any production change. Undefined-only, as intended — the
a1-carriage.md precedent for a type addition:

```
$ go test ./internal/engine/ ./cmd/swarm/ ./internal/daemon/ ./internal/skeleton/ -count=1
# github.com/Nathandela/swarm/internal/engine [github.com/Nathandela/swarm/internal/engine.test]
internal/engine/callbackraw_test.go:41:4: unknown field Raw in struct literal of type Callback
internal/engine/callbackraw_test.go:72:3: unknown field Raw in struct literal of type Callback
internal/engine/callbackraw_test.go:81:16: out.Raw undefined (type Callback has no field or method Raw)
internal/engine/callbackraw_test.go:81:34: in.Raw undefined (type Callback has no field or method Raw)
internal/engine/callbackraw_test.go:83:76: out.Raw undefined (type Callback has no field or method Raw)
internal/engine/callbackraw_test.go:83:84: in.Raw undefined (type Callback has no field or method Raw)
FAIL	github.com/Nathandela/swarm/internal/engine [build failed]
# github.com/Nathandela/swarm/internal/daemon [github.com/Nathandela/swarm/internal/daemon.test]
internal/daemon/launch_capture_test.go:24:93: too many arguments in call to injectHookEnv
	have ([]string, string, string, string, string, []string)
	want ([]string, string, string, string, string)
internal/daemon/launch_capture_test.go:26:21: undefined: hookclient.EnvCapture
internal/daemon/launch_capture_test.go:26:51: undefined: hookclient.CaptureEnv
internal/daemon/launch_capture_test.go:38:88: too many arguments in call to injectHookEnv
	have (nil, string, string, string, string, nil)
	want ([]string, string, string, string, string)
internal/daemon/launch_capture_test.go:40:24: undefined: hookclient.EnvCapture
# github.com/Nathandela/swarm/cmd/swarm [github.com/Nathandela/swarm/cmd/swarm.test]
cmd/swarm/hook_capture_test.go:94:22: undefined: hookclient.EnvCapture
cmd/swarm/hook_capture_test.go:94:45: undefined: hookclient.CaptureEnv
cmd/swarm/hook_capture_test.go:104:45: cannot use strings.NewReader(preToolUseBody) (value of type *strings.Reader) as io.Writer value in argument to runHook: *strings.Reader does not implement io.Writer (missing method Write)
cmd/swarm/hook_capture_test.go:108:15: cb.Raw undefined (type engine.Callback has no field or method Raw)
cmd/swarm/hook_capture_test.go:109:89: cb.Raw undefined (type engine.Callback has no field or method Raw)
cmd/swarm/hook_capture_test.go:129:47: cannot use strings.NewReader(body) (value of type *strings.Reader) as io.Writer value in argument to runHook: *strings.Reader does not implement io.Writer (missing method Write)
cmd/swarm/hook_capture_test.go:133:12: cb.Raw undefined (type engine.Callback has no field or method Raw)
cmd/swarm/hook_capture_test.go:135:39: cb.Raw undefined (type engine.Callback has no field or method Raw)
cmd/swarm/hook_capture_test.go:159:32: undefined: hookclient.EnvCapture
cmd/swarm/hook_capture_test.go:161:47: cannot use strings.NewReader(preToolUseBody) (value of type *strings.Reader) as io.Writer value in argument to runHook: *strings.Reader does not implement io.Writer (missing method Write)
cmd/swarm/hook_capture_test.go:161:47: too many errors
FAIL	github.com/Nathandela/swarm/cmd/swarm [build failed]
FAIL	github.com/Nathandela/swarm/internal/daemon [build failed]
# github.com/Nathandela/swarm/internal/skeleton [github.com/Nathandela/swarm/internal/skeleton.test]
internal/skeleton/interaction_carriage_test.go:58:18: undefined: adapter.CaptureEvents
internal/skeleton/interaction_carriage_test.go:62:13: got.CaptureEvents undefined (type daemon.LaunchSpec has no field or method CaptureEvents)
internal/skeleton/interaction_carriage_test.go:64:83: got.CaptureEvents undefined (type daemon.LaunchSpec has no field or method CaptureEvents)
internal/skeleton/interaction_carriage_test.go:67:10: got.CaptureEvents undefined (type daemon.LaunchSpec has no field or method CaptureEvents)
internal/skeleton/interaction_carriage_test.go:68:52: got.CaptureEvents undefined (type daemon.LaunchSpec has no field or method CaptureEvents)
internal/skeleton/interaction_carriage_test.go:83:13: got.CaptureEvents undefined (type daemon.LaunchSpec has no field or method CaptureEvents)
internal/skeleton/interaction_carriage_test.go:84:76: got.CaptureEvents undefined (type daemon.LaunchSpec has no field or method CaptureEvents)
internal/skeleton/interaction_carriage_test.go:107:4: unknown field Raw in struct literal of type engine.Callback
FAIL	github.com/Nathandela/swarm/internal/skeleton [build failed]
FAIL
```

**One existing test call site was updated mechanically and none was weakened**:
`TestInjectHookEnv_PostFilter` calls `injectHookEnv`, which gained a parameter, so its call
passes `nil`. Every assertion in it is byte-identical to HEAD, and the new coverage is in its own
file (`launch_capture_test.go`) rather than folded into it.

---

## 3. GREEN

```
$ go test ./internal/engine/ -run 'TestCallbackRaw_' -count=1 -v
=== RUN   TestCallbackRaw_NeverInfluencesTheDerivedStatus
--- PASS: TestCallbackRaw_NeverInfluencesTheDerivedStatus (0.00s)
=== RUN   TestCallbackRaw_SurvivesTheHookWireVerbatim
--- PASS: TestCallbackRaw_SurvivesTheHookWireVerbatim (0.00s)
=== RUN   TestCallbackRaw_IsOmittedWhenAbsent
--- PASS: TestCallbackRaw_IsOmittedWhenAbsent (0.00s)
PASS
ok  	github.com/Nathandela/swarm/internal/engine	0.581s

$ go test ./cmd/swarm/ -run 'TestRunHook_|TestParseHookStdin_' -count=1 -v
--- PASS: TestRunHook_KeepsTheCLIsOwnBodyOnACaptureRow (0.01s)
--- PASS: TestRunHook_KeepsNoBodyOnANonCaptureRow (0.00s)
--- PASS: TestRunHook_KeepsNoBodyWhenTheDaemonDeclaredNoCaptureRows (0.00s)
    --- PASS: .../an_adapter_with_no_capture_extension_declares_an_empty_list (0.00s)
    --- PASS: .../the_variable_absent_entirely_(an_older_daemon,_or_an_unsupervised_hook) (0.00s)
--- PASS: TestRunHook_CapsTheBodyAndStillPostsTheStatus (0.02s)
--- PASS: TestParseHookStdin_ExtractsClaudePayloadFields (0.00s)
--- PASS: TestParseHookStdin_Totality (0.00s)
--- PASS: TestParseHookStdin_SkipsReservedDimensionKeys (0.00s)
PASS
ok  	github.com/Nathandela/swarm/cmd/swarm	1.186s

$ go test ./internal/daemon/ -run 'TestInjectHookEnv_' -count=1 -v
--- PASS: TestInjectHookEnv_CarriesTheAdaptersCaptureRows (0.00s)
--- PASS: TestInjectHookEnv_ASessionWithNoCaptureRowsDeclaresNone (0.00s)
--- PASS: TestInjectHookEnv_PostFilter (0.00s)
PASS
ok  	github.com/Nathandela/swarm/internal/daemon	2.354s

$ go test ./internal/skeleton/ -run 'TestCarriage_|TestComposeLaunchSpec_Carries|TestComposeLaunchSpec_AnAdapter|TestInteractionCapture_|TestClaudeChainE2E_' -count=1 -v
--- PASS: TestInteractionCapture_AnAdapterWithoutTheExtensionEmitsNothing (1.84s)
--- PASS: TestInteractionCapture_AnUnshapeableInteractionEmitsNothing (0.04s)
--- PASS: TestInteractionCapture_ShapesTheEnvelopeAndTheKindFieldsOntoTheJournal (0.03s)
--- PASS: TestInteractionCapture_SuccessiveRecordsOfOneRefShareOneItemID (0.28s)
--- PASS: TestInteractionCapture_TheTurnOpensOnAUserMessageAndClosesOnATerminalAgentMessage (0.28s)
--- PASS: TestInteractionCapture_AnAuthenticatedHookReachesTheProducer (1.71s)
--- PASS: TestComposeLaunchSpec_CarriesTheAdaptersCaptureRows (0.00s)
--- PASS: TestComposeLaunchSpec_AnAdapterWithNoCaptureExtensionDeclaresNoRows (0.00s)
--- PASS: TestCarriage_AnAuthenticatedHookPostShapesTheCLIsOwnBody (0.41s)
--- PASS: TestCarriage_AHookPostWithNoCapturedBodyShapesNothing (0.69s)
--- PASS: TestClaudeChainE2E_TheRecordedCorpusRendersAndBothVerdictsResolveOnThePhone (6.67s)
PASS
ok  	github.com/Nathandela/swarm/internal/skeleton	12.853s
```

`TestCarriage_AnAuthenticatedHookPostShapesTheCLIsOwnBody` is the slice's acceptance test and
the answer to a1b §10's zero: a hook callback posted over `hookclient.Post` to a real daemon
socket, carrying a **real recorded body**, now produces the golden items. The two events are
chosen for what the old path destroyed — `UserPromptSubmit`'s prompt survived flattening but only
nested under `payload`, where the shaper's top-level read found nothing; `PreToolUse`'s
`tool_input` is an **object** the flattener dropped outright, so the tool card had no path to
name. Both are asserted on their shaped content, not on a count.

---

## 4. The guard that cost two tests, and why it is gone

An early return in `serveHookInteractions` skipped any callback with no captured body. It reads
as an obvious optimization — most hook posts are non-capture rows, and resolving the session's
adapter to hand it an empty body is work for no item. It is a behaviour change, and two existing
tests said so under `-race`:

```
$ go test -race ./internal/skeleton/ -count=1
--- FAIL: TestInteractionCapture_AnAuthenticatedHookReachesTheProducer (10.68s)
    interaction_capture_test.go:355: the journal holds 0 interaction record(s) for f633bfnvbunyi7pi after 10s; want 1
--- FAIL: TestInteractionE2E_ApprovalAndMessageReachThePhoneAndSurviveAReseed (48.33s)
    interaction_e2e_test.go:112: timed out after 45s: both interaction items reached the phone's transcript
        machine sessions:
          ep-f3bee056/db5r3h5fkanbgf56 group=working
FAIL	github.com/Nathandela/swarm/internal/skeleton	299.824s
```

Both drive a scripted adapter double that shapes from the **event** and never reads the body —
a legitimate shape, because `Interactions` takes the whole `HookPayload`. The guard was the
daemon pre-judging what an event can produce, which ADR-010 §5 puts squarely with the adapter.
Removed; the tests were not touched. A non-capture row still shapes nothing, because the SHAPER
finds nothing in an empty body — which is where that judgement belongs.

---

## 5. Teeth — five mutations, each applied for real, each caught, each reverted

Every one was applied to the production source, run, and reverted (`grep -c MUTATION` = 0 after
each; the final tree is verified in §6).

| # | Mutation | Caught by |
|---|---|---|
| 1 | `runHook` carries the body on **every** event (capture gate removed) | `TestRunHook_KeepsNoBodyOnANonCaptureRow`, both `..._KeepsNoBodyWhenTheDaemonDeclaredNoCaptureRows` subtests, and `..._CapsTheBodyAndStillPostsTheStatus` |
| 2 | the `json.Valid` guard removed (a truncated body is carried) | `TestRunHook_CapsTheBodyAndStillPostsTheStatus` |
| 3 | `serveHookInteractions` hands the shaper an envelope again | `TestCarriage_AnAuthenticatedHookPostShapesTheCLIsOwnBody` |
| 4 | `composeLaunchSpec` never sets `CaptureEvents` | `TestComposeLaunchSpec_CarriesTheAdaptersCaptureRows` |
| 5 | `injectHookEnv` never injects `SWARM_HOOK_CAPTURE` | both `TestInjectHookEnv_*` capture tests |

Verbatim, the two that name the failure mode most exactly:

```
# MUTATION 2 — the json.Valid guard removed
--- FAIL: TestRunHook_CapsTheBodyAndStillPostsTheStatus (0.07s)
    hook_capture_test.go:184: runHook exit code 1 on an oversized body; want 0 -- the status post must survive a body the cap refuses

# MUTATION 3 — the shaper is handed an envelope again
--- FAIL: TestCarriage_AnAuthenticatedHookPostShapesTheCLIsOwnBody (13.43s)
    interaction_carriage_test.go:113: the journal holds 0 interaction record(s) for jjnmq5auee3s4z2g after 10s; want 2
```

Mutation 3 reproduces a1b §10's measured zero exactly, from the other direction: with the
envelope back in place the shipped producer shapes nothing at all.

---

## 6. Final verification

```
$ go build ./...                      (clean)
$ go vet ./...                        (clean)
$ go vet -tags realcli ./internal/smoke/   (clean — the live harness compiles; see §7)
$ gofmt -l <every file this slice touched>  (clean; internal/skeleton/api.go's pre-existing
                                             misplaced `errors` import is untouched and predates it)

$ go test ./... -count=1
(no FAIL lines; the whole module green)

$ go test -race ./internal/skeleton/ -count=1
ok  	github.com/Nathandela/swarm/internal/skeleton	202.263s
$ go test -race ./internal/daemon/ ./cmd/swarm/ -count=1
ok  	github.com/Nathandela/swarm/internal/daemon	40.278s
ok  	github.com/Nathandela/swarm/cmd/swarm	38.130s
$ go test -race ./internal/engine/ ./internal/hookclient/ ./internal/adapter/ -count=1
ok  	github.com/Nathandela/swarm/internal/engine	6.543s
ok  	github.com/Nathandela/swarm/internal/hookclient	2.040s
ok  	github.com/Nathandela/swarm/internal/adapter	2.449s

$ go test ./internal/verify/ -count=1     (B94 reachability + the Phase-B fences)
ok  	github.com/Nathandela/swarm/internal/verify	7.174s
$ go test ./internal/protocol/ -count=1   (the protocol.md drift fences)
ok  	github.com/Nathandela/swarm/internal/protocol	11.292s
```

`golangci-lint run` over the six changed packages reports **59 issues (errcheck 50, staticcheck
9)** — byte-identical to the same command run against a stashed (HEAD) tree, so this slice adds
none. `internal/verify`'s B94 ledger needs no new entry: `adapter.CaptureEvents`,
`hookclient.CaptureEnv` and `hookclient.CapturesRaw` are all called from production paths a
`cmd/` main reaches.

---

## 7. What this slice did NOT do, stated plainly

- **The live-CLI recording harness is compile-verified only.** `internal/smoke/realcli.go`
  (build tag `realcli`, billable, runs the real CLIs) mirrors `daemon.injectHookEnv`, so it now
  injects the fifth variable too, and `callbacksToHookPayloads` prefers `cb.Raw` over the
  flattened payload when a callback carries one — without that, a re-recorded fixture would
  still hold only top-level strings. **Neither change was executed**: this program does not
  start live sessions. `go vet -tags realcli ./internal/smoke/` is the whole of its evidence.
- **No `Decision` write-back.** The carriage is ingest-only. Answering a `PermissionRequest`
  from the phone still goes through the approval path built in A1; ADR-010 §4's core-executed
  `DecisionAction` reply has no caller yet.
- **No push-wake change.** ADR-010 §4's two wake rules (an `approval_request` append is
  wake-eligible on its own; a suppressed wake is deferred, never dropped) remain unbuilt.
- **Codex is unaffected.** Its producer is still deferred (D1); it declares no capture rows, so
  its sessions inject an empty list and behave exactly as before.
- **`ADR-010` is unamended, deliberately.** §6 is implemented as written; the spawn-injected
  variable is the mechanism by which "the descriptor declares it" reaches a process that cannot
  read descriptors, not a change to what §6 decides. The alternative §10 floated — carrying the
  body on every row and gating daemon-side — *would* have needed an amendment, and was not taken.

---

# I1b — the LIVE-PATH chain test: `swarm hook` to the phone, prose included

**Added 2026-08-08.** Everything above proves the carriage *daemon-side*: §3's acceptance test
posts a hand-composed `engine.Callback` over `hookclient.Post`, and the chain test
(`interaction_chain_e2e_test.go`) enters lower still, at `captureInteractions`. Neither of them
runs the hop a **live** CLI actually takes — the hook **process**. This section is that hop, end
to end, with the agent's own prose asserted on the phone.

**File**: `internal/skeleton/interaction_chain_live_test.go`
(`TestClaudeLiveChainE2E_TheRecordedBodiesEnterThroughSwarmHookAndThePhoneRendersTheProse`).
**No production code changed** — this slice is a test and its evidence. **No CLI was run**;
`swarm hook` is ours, the bodies are the recorded S-B corpus byte for byte.

---

## 8. What it drives, and the one thing that made it necessary

`cmd/swarm` is **package main**, so `runHook` / `parseHookStdin` / `readHookBody` are unreachable
from any in-process test in the module — which is exactly why the hop between "hook fires" and
"callback arrives" had no end-to-end cover. So the test **spawns the real binary**: `swarmBin`,
the same `swarm` the rig already builds and the daemon launches its shims with, invoked as
`swarm hook <event>` with the recorded body on **stdin** — the literal command line `claude.go`'s
`hookSettingsJSON` writes into the CLI's settings.

| Hop | What runs |
|---|---|
| `swarm hook <event>` | **REAL, a separate process.** `runHook` → `readHookBody` → `parseHookStdin` → `hookclient.FromEnv` (real sequence file) → `CapturesRaw`'s env gate → `Post` |
| daemon | **REAL.** `serveHook` → `HandleCallback` (S6 token auth) → `serveHookInteractions` |
| shaper | **REAL.** `internal/adapter/claude`, reached through the daemon's own `adapterFor` seam |
| producer | **REAL.** §2 envelope, turn, §5 caps, ADR-010 §7's append floor, journal |
| gateway / relay / phone | **REAL**, `cmd/swarm-remote` as a separate process, real WebSocket, real `swarmmobile.App` |

**The environment is the daemon's own**, not a hand-written one: it is read back out of the 0600
`shim-launch.json` the daemon wrote at spawn, so session id, token, socket path and the monotonic
**sequence file** are the values a live hook would get.

**The timestamp problem the chain test has to work around does not exist here.** That test must
overwrite each fixture's `received_at_ms` before replay (a 2026-07-18 instant replayed as *now*
opens and closes an approval's window three weeks in the past — a1b §9). Here the recorded field
never leaves the fixture: the hook process carries no timestamp and `serveHookInteractions` stamps
`time.Now()` itself. ADR-010 §3 is structural on this path, not asserted.

### 8.1 The two dressings, and their measured cost

The rig launches the scripted **fake** agent, because launching agent `claude` would start a real
CLI against Anthropic and this program does not do that. A fake session's registry lookup yields
no adapter, and its injected `SWARM_HOOK_CAPTURE` is therefore empty. So exactly two values are
substituted, **both read off the shipped adapter rather than written by hand**:

1. `adapter.CaptureEvents(claude.New())` → the hook process's `SWARM_HOOK_CAPTURE`;
2. `claude.New()` → the daemon's `adapterFor` seam, assigned **under `itemMu`** (the lock
   `resolveAdapter` reads that field beneath) rather than with the bare unsynchronized write the
   other interaction tests make. That was the objection the chain test's header raised against
   this approach on a `-race` rig; one line answers it.

**What the dressing costs, measured rather than guessed.** A fake session registers no
`SignalSources` either, so `deriveDims` maps these events to no status dimension and
`HandleCallback` returns **early** — an unmapped event is a benign no-op — *before* `applyTyped`,
where G5's replay guard lives. The engine's **token check runs before that early return** and is
fully exercised (§9, mutation 4). The sequence **value** is not (§9.1, mutation 3 — uncaught, and
recorded as such). The same early return means no turn/interaction dimension moves on this
session, so IS-LIFE-2's `answered_locally` — which a real claude session's `Stop` would fire on a
pending card — is not reached here either.

### 8.2 What it asserts

Eight recorded records, **six items**, because the two tool calls fold open+close into one row
each (IS-DELTA-3) and the recording's `Notification` is not a capture row — the **hook process**
drops that body itself, on the env gate.

- `user_message` = the prompt the owner typed, verbatim across five hops;
- two `tool_run` rows, both `completed`, with the Read's `action{type,path}` and its
  `output_excerpt` — **fields that exist only inside the nested `tool_input`/`tool_response`
  objects**, so both are proof the CLI's own body made the whole trip (a1b §10's measured zero);
- one `file_change` with the hunk rendered from the recorded `structuredPatch` (IS-FC-1);
- one `approval_request`: summary, `mode=card`, `{allow:Yes, deny:No}`, and both leak fences
  (`keystrokes` off the item, no `verdict` beside a decision);
- **one `agent_message` whose text is the recorded `last_assistant_message`**, read out of the
  fixture rather than pasted, `status=completed`;
- **one turn holding all six**, the closing `agent_message` included (IS-ENV-1).

A **forged** post is sent *first*: the same binary, the same recorded `Stop` body, one wrong
`SWARM_HOOK_TOKEN`. `serveHook` runs the engine's token check **before** the interaction plane
precisely so a local process that found the socket cannot write to the owner's transcript
(conn.go), and the exact count is what refutes it — a second `agent_message` would be an
unauthenticated one.

---

## 9. RED — verbatim, and the one mutation that was NOT caught

**There is no undefined-symbol RED to show, and pretending otherwise would be dishonest**: this
slice adds no production symbol. Every hop it fences already shipped (I1 above, A1b). So the
failing-first runs were produced the only way that means anything here — by **removing, one at a
time, each production behaviour the test exists to fence**, running it, and reverting. Every
mutation was applied to the real source and reverted against a byte-for-byte backup (`cmp` rc=0
each time; `grep MUTATION` over non-test sources = 0 at the end).

**MUTATION 1 — `runHook` never carries the body** (the capture gate in `cmd/swarm/main.go`
disabled). The hop nothing else covered:

```
--- FAIL: TestClaudeLiveChainE2E_TheRecordedBodiesEnterThroughSwarmHookAndThePhoneRendersTheProse (51.47s)
    interaction_chain_live_test.go:120: timed out after 45s: the five items the recorded turn produces before its reply reached the phone
        machine sessions:
          ep-8ec3002c/sgnppwdei5p7coal group=working
```

**MUTATION 2 — `Stop` dropped from the claude adapter's capture rows** (`claude.go`'s row
`capture: true` → `false`). The prose never leaves the CLI, and the wait that names it says so:

```
--- FAIL: TestClaudeLiveChainE2E_TheRecordedBodiesEnterThroughSwarmHookAndThePhoneRendersTheProse (52.21s)
    interaction_chain_live_test.go:124: timed out after 45s: the agent's own reply -- Stop's last_assistant_message, shaped into an agent_message -- reached the phone
        machine sessions:
          ep-36d8bde8/jcnnlwtkbsfzfhsp group=working
```

The two-stage wait is *why* that message names the reply. A single wait on a count would have
reported "six items never arrived" for a carriage that dropped the prompt just as readily as for
one that dropped the prose; the first stage waits for the five pre-reply items, the second for the
reply itself.

**MUTATION 4 — `serveHook` captures BEFORE the engine authenticates** (the `return` on
`HandleCallback`'s error removed). The forged post reaches the transcript:

```
--- FAIL: TestClaudeLiveChainE2E_TheRecordedBodiesEnterThroughSwarmHookAndThePhoneRendersTheProse (6.78s)
    interaction_chain_live_test.go:137: the phone holds 0 file_change item(s); want 1. items: agent_message,approval_request,user_message,tool_run,tool_run
    interaction_chain_live_test.go:142: the phone holds 5 item(s) agent_message,approval_request,user_message,tool_run,tool_run; the recording's EIGHT records make exactly six. A SEVENTH is content this turn does not contain -- and a second agent_message is the FORGED Stop, which means the interaction plane wrote an item the engine refused
```

Note the shape of that failure: the forged `agent_message` arrives **first**, satisfies the
reply-wait before the real records land, and the run then reads a half-arrived transcript. The
mutation is caught, and the diagnostic names the forged Stop.

### 9.1 MUTATION 3 was NOT caught, and that is a recorded limit, not a footnote

**Mutation**: `hookclient.sequenceFromEnv` returns a constant `1`, so every `swarm hook`
invocation posts sequence 1 — an exact replay, seven times over.

```
$ go test ./internal/skeleton/ -run 'TestClaudeLiveChainE2E_' -count=1
ok  	github.com/Nathandela/swarm/internal/skeleton	8.934s
```

**Green. Why**: this session's engine registration carries no `SignalSources` (agent `fake`), so
`deriveDims` returns nothing and `HandleCallback` takes its "unmapped event → benign no-op" early
return *before* `applyTyped`, which is where the `seq > s.turnSeq` / `seq > s.interSeq` guard
lives. The sequence file is genuinely read and incremented by each spawned hook — G5's mechanism
runs — but on this session **the daemon never inspects the number**, so the test cannot fence it.
On a real claude session it would: the same replay would fail `applyTyped`, `HandleCallback` would
error, and `serveHookInteractions` would never be called. **That guard is covered by
`internal/engine`'s own tests, and this chain does not extend to it.** Stated here rather than
left for an auditor to find.

---

## 10. GREEN and final verification

```
(Run on the fully reverted tree — every mutation restored from a byte-for-byte backup.)

$ go test ./internal/skeleton/ -run 'TestClaudeLiveChainE2E_' -count=1 -v
=== RUN   TestClaudeLiveChainE2E_TheRecordedBodiesEnterThroughSwarmHookAndThePhoneRendersTheProse
--- PASS: TestClaudeLiveChainE2E_TheRecordedBodiesEnterThroughSwarmHookAndThePhoneRendersTheProse (7.40s)
PASS
ok  	github.com/Nathandela/swarm/internal/skeleton	8.385s

$ go test -race ./internal/skeleton/ -run 'TestClaudeLiveChainE2E_|TestClaudeChainE2E_|TestCarriage_' -count=1 -v
--- PASS: TestCarriage_AnAuthenticatedHookPostShapesTheCLIsOwnBody (3.83s)
--- PASS: TestCarriage_AHookPostWithNoCapturedBodyShapesNothing (0.66s)
--- PASS: TestClaudeChainE2E_TheRecordedCorpusRendersAndBothVerdictsResolveOnThePhone (8.21s)
--- PASS: TestClaudeLiveChainE2E_TheRecordedBodiesEnterThroughSwarmHookAndThePhoneRendersTheProse (4.29s)
PASS
ok  	github.com/Nathandela/swarm/internal/skeleton	19.385s

  (and, with the two older interaction suites folded in, to prove the new file's
   locked adapterFor assignment did not disturb the ones that write it bare:
   go test -race ./internal/skeleton/ -run '...|TestInteractionCapture_|TestInteractionE2E_'
   ok  	github.com/Nathandela/swarm/internal/skeleton	27.964s)

$ go build ./...                                              (clean)
$ go vet ./...                                                (clean)
$ gofmt -l internal/skeleton/interaction_chain_live_test.go   (clean; the three files gofmt still
                                                               reports in the tree — api.go's
                                                               pre-existing misplaced `errors`
                                                               import and two test files identical
                                                               to HEAD — are untouched by this slice)

$ go test -race ./internal/skeleton/ -count=1
ok  	github.com/Nathandela/swarm/internal/skeleton	205.104s

$ go test ./internal/protocol/ ./internal/verify/ ./internal/adapter/... ./cmd/... -count=1
ok  	github.com/Nathandela/swarm/internal/protocol	13.363s   (the protocol.md drift fences)
ok  	github.com/Nathandela/swarm/internal/verify	12.539s   (B94 reachability + the Phase-B fences)
ok  	(every adapter package, every cmd)

$ go test ./... -count=1
(no FAIL lines; the whole module green, mobile/conformance included)
```

`golangci-lint run ./internal/skeleton/...` reports **27 issues (errcheck 24, staticcheck 3)** —
byte-identical to the same command with the new file moved out of the tree, so this slice adds
none. `internal/verify`'s B94 ledger is untouched: the file adds no exported symbol.

**Nothing was committed. `bd` was not used. No CLI was run, live or otherwise.**

## 11. What this test still does NOT do

- **It does not resolve an approval.** The verdict round trips (allow and deny, echoed off the
  phone's own card) stay in `interaction_chain_e2e_test.go`, which owns them; they are downstream
  of ingest and unaffected by which door the body came through. This test asserts the card, not
  the answer.
- **It does not exercise `adapterFor`'s registry lookup**, for the same reason the chain test does
  not: the adapter is handed in, because the alternative is launching a real `claude`.
- **It does not exercise the G5 sequence guard** (§9.1), nor any status-plane transition, because
  a fake session maps no dimensions.
- **It replays one fixture.** The Bash/prompt-card fixture's second recorded `Stop` and the
  no-escalation fixture's two are covered by the producer's golden corpus test and by the chain
  test's second leg; a second replay here would re-measure the same carriage.

---

# I1c — adversarial review finding: "verbatim" is SEMANTIC, not byte-exact

**Added 2026-08-08**, from the spec-conformance review of everything since 7af7060. Not a
production defect: no shipped behaviour changes, and no rule of ADR-010 §6 is broken. It is a
**claim** that was wrong about its own transport, in the one place a future slice would go
looking before building on it.

## 12. The finding

`TestCallbackRaw_SurvivesTheHookWireVerbatim` asserted `Callback.Raw` crosses `hookclient.Post`
byte for byte, and its comment offered that property to a later "hash taken over what was
captured". `encoding/json` does not provide it. `json.Marshal` runs `compact` over a
`json.RawMessage` with `escapeHTML` **on** (the default for `json.Marshal`), so it rewrites `<`,
`>` and `&` to `\u003c` / `\u003e` / `\u0026` and strips inter-token whitespace. The existing test
passed only because its body contains none of the three — and `&&`, `>` and `<` are ordinary
content in agent prose and in a Bash `tool_input`.

Measured, on `internal/engine`'s own type:

```
in  : {"hook_event_name":"Stop","last_assistant_message":"run `foo && bar` if a<b"}
out : {"hook_event_name":"Stop","last_assistant_message":"run `foo \u0026\u0026 bar` if a\u003cb"}
byte-identical: false
decoded last_assistant_message: "run `foo && bar` if a<b"
```

The last line is the point: every shaper reads its fields with `json.Unmarshal`, which decodes the
escapes back, so **the shaped content is the CLI's own** and §6 holds. What does not hold is
byte-exactness, and nothing in the tree depends on it today — `Callback.Raw` has exactly three
consumers (`cmd/swarm`'s assignment, `skeleton.serveHookInteractions`' pass-through to
`HookPayload.Raw`, and `internal/smoke`'s fixture recorder), and none reads it bytewise.

## 12.1 RED, verbatim

The new test written against the OLD claim — a body carrying the three bytes, asserted byte-exact:

```
$ go test ./internal/engine/ -run TestCallbackRaw_TheEncoderEscapesHTMLBytesAndTheContentSurvives -count=1 -v
=== RUN   TestCallbackRaw_TheEncoderEscapesHTMLBytesAndTheContentSurvives
    callbackraw_test.go:111: Raw crossed the wire as {"tool_name":"Bash","tool_input":{"command":"grep -c a b \u0026\u0026 echo \u003cdone\u003e"}}; want {"tool_name":"Bash","tool_input":{"command":"grep -c a b && echo <done>"}} -- encoding/json escapes <, > and & inside a RawMessage, so a byte-exact expectation here is wrong about the transport
--- FAIL: TestCallbackRaw_TheEncoderEscapesHTMLBytesAndTheContentSurvives (0.01s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/engine	0.689s
FAIL
```

## 12.2 What changed

Test-only; no production line moved.

1. **A new test**, `TestCallbackRaw_TheEncoderEscapesHTMLBytesAndTheContentSurvives`, pinning both
   halves: the escaping is the transport's ONE rewrite (any other change is a carriage defect),
   and the decoded field is identical to the CLI's own.
2. **The existing test's comment corrected.** No assertion in it was weakened, removed or
   reordered — `TestCallbackRaw_SurvivesTheHookWireVerbatim` still runs its original body and its
   original byte-equality check. Only the sentence offering byte-exactness to a future hash was
   rewritten to say what the transport actually guarantees.

## 12.3 GREEN

```
$ go test ./internal/engine/ -run TestCallbackRaw_ -count=1 -v
=== RUN   TestCallbackRaw_NeverInfluencesTheDerivedStatus
--- PASS: TestCallbackRaw_NeverInfluencesTheDerivedStatus (0.00s)
=== RUN   TestCallbackRaw_SurvivesTheHookWireVerbatim
--- PASS: TestCallbackRaw_SurvivesTheHookWireVerbatim (0.01s)
=== RUN   TestCallbackRaw_TheEncoderEscapesHTMLBytesAndTheContentSurvives
--- PASS: TestCallbackRaw_TheEncoderEscapesHTMLBytesAndTheContentSurvives (0.00s)
=== RUN   TestCallbackRaw_IsOmittedWhenAbsent
--- PASS: TestCallbackRaw_IsOmittedWhenAbsent (0.00s)
ok  	github.com/Nathandela/swarm/internal/engine	0.681s
```

## 12.4 Two gaps found by the same review and NOT fixed here

Recorded so neither is discovered later as an unexplained silence. Both predate this slice; what
I1 changed is that both are now **reachable in production**, because the carriage is what first
puts a live CLI's body into the journal.

- **IS-ST-1 has no producer-side guard against a re-delivered hook.** The engine's G5 sequence
  guard catches an exact replay (same sequence, refused before `serveHookInteractions`), but a
  genuine re-invocation of `swarm hook <event>` reads a FRESH sequence off the counter file, so it
  is accepted and shaped again. Measured, driving one `PreToolUse` and the same `PostToolUse`
  twice over `hookclient.Post` at 400 ms spacing:

  ```
  after PreToolUse:  1 record   [0] tool_run in_progress  item_id=01KZG9M9BP4ZMN3M0JYR1RR7YX
  after PostToolUse: 2 records  [1] tool_run completed    item_id=01KZG9M9BP4ZMN3M0JYR1RR7YX
  after PostToolUse: 3 records  [2] tool_run completed    item_id=01KZG9M9BP4ZMN3M0JYR1RR7YX   <-- IS-ST-1
  ```

  interaction-schema.md §4: "An `item_id` SHALL reach at most one terminal status, and SHALL emit
  no further record after it." `shapeItem`/`noteItemLocked` do not enforce it. A `Stop` re-delivery
  is the milder shape of the same thing — no `Ref`, so it mints a second `item_id` and the phone
  shows the reply twice rather than an illegal second terminal. The fix is a producer-side rule
  (refuse a record for an `item_id` already terminal, or key on the CLI's own id), and choosing
  between "drop the duplicate" and "supersede" is a schema question, not a carriage one. Not
  fixed here.
- **ADR-010 §6's third clause — "redacted ... daemon-side before anything is journaled" — has no
  implementation.** The excerpting half is built (§5's caps, `capFields`/`clipStrings`); a search
  for redaction over `internal/` finds only the crypto packages' `String()` hygiene. No
  verification file records it as deferred. §7 above lists what I1 did not do and this belongs on
  that list.

## 12.5 Gates, after the change

```
$ go build ./...   (clean)
$ go vet ./...     (clean)
$ gofmt -l internal/engine/callbackraw_test.go   (clean)
$ go test ./internal/engine/ -count=1
ok  	github.com/Nathandela/swarm/internal/engine
```
