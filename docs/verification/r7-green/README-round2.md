# GREEN evidence: Wave R7 round 2 -- the five BLOCKING review findings, fixed and mutation-proved

- **Date:** 2026-08-20 (UTC; per-command timestamps in `r7-green-round2.txt`)
- **Role:** GREEN (implementer), round 2. Round 1 is `README.md` beside this file; RED is
  `docs/verification/r7-red/`.
- **Bead:** `agents-tracker-hggx.8`
- **Design:** ADR-013 Amendment 2026-08-20 **revision 3**, ADR-010 Amendment 2026-08-20
  **revision 3** (both written this round; the revision notes say what changed and why)

## What a reader CAN and CANNOT conclude

**CAN.** All five BLOCKING findings are fixed, and each fix is proved by MUTATION -- the
production line is changed and a permanent test fails, with the failure message printed in
`r7-green-round2.txt`. **Eleven of eleven mutations fire**, including the two that round 1
disclosed as vacuous. Three protocol facts were measured **against the real installed codex
0.147.0** this round, at zero cost to the owner's account (no model turn was ever started):
`turn/steer` with the daemon-minted ULID round 1 shipped is REFUSED by the real server;
`turn/interrupt` with such an id answers exactly the string this daemon swallows as benign; and
`thread/loaded/list` answers the `{"data": ["<id>"]}` shape the restart-rejoin path decodes.

**CANNOT.** *The wave's exit criterion is still not demonstrated.* No test has driven a real
Codex TUI and the phone against one live thread. The probe that would (`internal/appserver/
r7_realcli_test.go`, `TestR7RealCLI_TheDaemonAdoptsTheTUIsOwnThreadAndSteersTheCLIsOwnTurnId`)
is written and was RUN, and it **failed for a credential reason, not a protocol one** -- see
"The live probe that could not be run" below. The one-thread topology therefore rests on the R1
gate's RECORDED `thread/started` broadcast to a second client, which is real evidence from a
slightly different ordering, and not on a run of this build. No Android work is in this slice.

## The five BLOCKING findings

| # | Finding | Fix | Production file:line |
|---|---|---|---|
| B1 | `turn/steer`/`turn/interrupt` carried the DAEMON's minted ULID, so every mid-turn phone send was rejected and every Stop reported false success | `adapter.Interaction.TurnRef` carries the CLI's own `turnId`; the daemon keeps it per open turn and names it. A turn with no native id is REFUSED, never bridged | `internal/adapter/interaction.go:192` (`TurnRef`), `internal/adapter/codex/interaction.go` (6 shapers, 3 params structs), `internal/skeleton/serve.go:158` (`nativeTurns`), `internal/skeleton/interaction.go:326` (`turnIDLocked`), `internal/skeleton/chat.go:282` (steer), `chat.go:518` (interrupt) |
| B2 | a PHONE-answered approval never cleared its card | the pending-request table is split: answering consumes ANSWERABILITY, only the server's `serverRequest/resolved` consumes the id->ref mapping | `internal/skeleton/backend.go:265` (`takeServerRequest`) |
| B3 | the daemon and the TUI ran on two different Codex threads | the daemon starts NO thread; it adopts the agent's from `thread/started` and joins it with `thread/resume`, retrying only the recorded rollout race and gapping a late join | `internal/skeleton/backend.go:173` (`adoptBackendThread`), `internal/skeleton/backendconnect.go:173` (`joinSessionBackend`), `:287` (`subscribeThread`, replaced in round 4 by `resumeThreadOnce` + `subscribeSessionThread`), `:325` (`discoverLoadedThread`) |
| B4 | a daemon restart tore a live session's structured plane silently | `Serve` gained `connectBackendsForRunning()` beside `startHookDrainsForRunning()`; a rejoin that cannot dial gaps and degrades, a rejoin that succeeds does neither | `internal/skeleton/serve.go:448`, `internal/skeleton/backendconnect.go:143` (`connectBackendsForRunning`) / `:231` (`rejoinSessionBackend`) |
| B5 | an agent-exec failure orphaned the app-server | the one exit from `Run` that skipped finalization now fires the group kill and joins the backend | `internal/shim/shim.go:306` |

## Mutation verdicts -- ELEVEN of eleven FIRE

Each row: the production line was changed, the named test(s) run, the failure observed, the
change reverted. Full output with the failure messages is in `r7-green-round2.txt`.

| # | Mutation applied to production code | Test that must fail | Verdict |
|---|---|---|---|
| M1 | `turn/steer` sends `expectedTurn` (the daemon ULID) again | `TestR7Fix_MidTurnSteerNamesTheCLIsOwnTurnIdAndNeverTheDaemonsMintedULID`, `TestR7ComposerSink_AMidTurnSend...` | **FIRES** (both; the ULID is printed beside the native UUIDv7) |
| M2 | `turn/interrupt` sends `req.ExpectedTurn` again | `TestR7Fix_InterruptNamesTheCLIsOwnTurnId...`, `TestR7Interrupt_ABackendSession...` | **FIRES** (both) |
| M3 | the no-native-id refusal is removed | `TestR7Fix_ASteerWithNoCLITurnIdIsREFUSED...` | **FIRES** |
| M4 | answering consumes `byID` again | `TestR7Fix_ThePHONEAnsweredApprovalIsRetired...`, `..._IsAttributedToThePhone` | **FIRES** (both) |
| M5 | the pump stops adopting `thread/started` | `TestR7Fix_ThePumpAdoptsTheAgentsOwnThread...` | **FIRES** |
| M6 | `Serve` drops `connectBackendsForRunning()` | `TestR7Fix_ServeCatchesUpTheBackendsOfSESSIONS...` | **FIRES** |
| M7 | a failed rejoin no longer gaps or degrades | `TestR7Fix_ARestartWhoseBackendIsGoneGAPSAndDEGRADES...` | **FIRES** (both assertions) |
| M8 | `registerSession` mints a hook token for a backend session | `TestR7Fix_ASessionWithABackendIsRegisteredWithNOHookToken` | **FIRES** -- round 1's vacuous fence #7, closed |
| M9 | the codex adapter drops `TurnRef` on a delta | `TestR7FixTurnRef_EveryShapedFrameCarriesTheCLIsOwnTurnId` | **FIRES** |
| M10 | `DefaultItemWindow` back to 125 ms | `TestR7ItemWindow_AtThreeStreamingSessionsTheTerminalPlaneSTILLGetsSlots` | **FIRES** -- round 1's vacuous fence #6, closed |
| M11 | the agent-exec error path no longer contains the backend | `TestR7ShimBackend_AnAgentThatCannotEXECStillTakesItsBackendDownWithIt` | **FIRES** |

### The two vacuous fences round 1 disclosed are now closed, and here is what was wrong

**#6 (`DefaultItemWindow`).** The N=3 rig drove `Offer` alone. `ItemAdmission` releases on an
Offer, so with no release ticker the release rate was bounded by the OFFER INSTANTS (5/s) --
under `CoalescingSink`'s 8 slots/s at BOTH window widths, so 125 ms and 250 ms were
indistinguishable to it. The rig now models `releaseInteractions`' `Flush` ticker, which is what
production runs, and at a 125 ms floor the terminal plane drops to **2 slots/s** and the test
fails. The arithmetic was always right; the test now measures it.

**#7 (single-writer).** The engine-side test passed a `Callback` with an EMPTY `Token`, so its
refusal came from the engine's own empty-token check regardless of what the SESSION was
registered with. The property §R7.3 asks for lives in `registerSession`, so the fence moved
there: a new skeleton test declares a backend in the session's REAL persisted launch config,
calls the REAL `registerSession` with a token, and presents the engine with that same token.
A sibling asserts a session WITHOUT a backend still accepts its token, so the fence cannot be
satisfied by a daemon that simply stopped minting them.

### MEDIUM 7's two named fences, corrected rather than supplemented

`TestR7ComposerSink_AMidTurnSendDispatchesTurnSteerCarryingTheNATIVEExpectedTurnId` asserted
`expectedTurnId != ""`, which the wrong id satisfies -- a test whose name states a property its
body does not check. It now opens its turn by driving the RECORDED `item/started` frame through
the REAL pump and the REAL codex shaper, and asserts EQUALITY with the recorded turn id. Its
interrupt sibling, which asserted only that two keys were present, does the same. No assertion
was weakened; both were the reason the defect survived round 1.

## What was measured against the REAL codex 0.147.0 this round

`TestR7RealCLI_TheProtocolPRECONDITIONSBehaveAsTheFixASSUMES`, run 2026-08-20 under an isolated
`CODEX_HOME` and a scratch workspace. **It starts no turn, so no model is called and the owner
is charged nothing.**

```
MEASURED: thread/loaded/list -> {"data": [01a01e24-23ba-7a80-a0ae-67168ac2c512]}
MEASURED, review BLOCKING 1 as a fact: turn/steer with the daemon-minted ULID
  "01M0EZR7QB3ANMVBNVNNF8CJC8" -> appserver: rpc error -32600: no active turn to steer
MEASURED: turn/interrupt with a daemon-minted turn id
  -> appserver: rpc error -32600: no active turn to interrupt
...and that is EXACTLY the string benignInterruptError swallows, so round 1's Stop reported
  SUCCESS to the phone for a turn it never touched
```

Separately, `internal/appserver`'s WebSocket client completed the HTTP/1.1 upgrade and an
`initialize` round trip against the real server, so the transport layer is verified live and not
only against fixtures.

**An environment fact that cost this round real time and is recorded so it costs nobody else
any.** The Go toolchain on this host is **amd64 under Rosetta**. A test-spawned universal binary
inherits the translated parent's x86_64 preference, so `node` runs as x86_64 and codex's npm
launcher dies with `Missing optional dependency @openai/codex-darwin-x64` before it binds a
socket -- surfacing only as an unexplained "never became servable" timeout. Every `codex` spawn
in the realcli file now goes through `/usr/bin/arch -arm64`. The R1 gate never hit this because
it drove codex from an arm64 Python.

## The live probe that could not be run, and why that is a decision and not an omission

`TestR7RealCLI_TheDaemonAdoptsTheTUIsOwnThreadAndSteersTheCLIsOwnTurnId` -- the probe that would
settle BLOCKING 3 by driving a real TUI -- **was run three times and fixed twice**, and it still
fails at its first assertion: the daemon connection never received `thread/started` within 90 s.

Run 1 failed on the Rosetta/x86_64 defect described above. Run 2 got a booting TUI that sat on an
interactive prompt -- `Update available! 0.147.0 -> 0.148.0 / 1. Update now (runs npm install -g
@openai/codex) 2. Skip 3. Skip until next version` -- and created no thread; the probe now writes
the single digit `2` and **never Enter**, because the default selection is "Update now" and this
test must not npm-install over the owner's codex as a side effect. Run 3 cleared the prompt, and
the TUI proceeded to the **sign-in screen**. Its captured screen ends in codex's sign-in splash
and the words "sign in again."; the app-server's own log says why:

```
ERROR codex_login::auth::manager: Failed to refresh token: 401 Unauthorized:
  "message": "Your refresh token has already been used to generate a new access token.
              Please try signing in again.",
  "code": "refresh_token_reused"
```

The isolated `CODEX_HOME` this round used was seeded from the R1 gate's own isolated home, whose
copied credential can no longer refresh -- **because the owner's real `~/.codex` rotated the
refresh token after the gate copied it.** That is the mechanism, observed. The TUI is not signed
in, so it creates no thread, so nothing is broadcast: **the failure is a credential state, not a
protocol result.**

**Seeding from the owner's live `~/.codex/auth.json` would very likely log their real Codex CLI
out.** `~/.codex/auth.json` holds a ChatGPT OAuth refresh token and no API key; an isolated
server that refreshes it rotates it, the new token lands in the isolated home, and the owner's
real home is left holding a used one -- which is precisely the state the 401 above describes.
That is a side effect on the owner's account, and it is an owner's call, not an implementer's.
**No copy of the live credential was made.** The probe is written and is now runnable end to end -- both
environment defects are fixed and it captures the app-server's log AND the TUI's own screen on
failure. It needs exactly one thing: `CODEX_HOME=<scratch>/codex-home codex login`.

Consequence, stated plainly: **the one-thread topology is not verified against a live TUI.** It
rests on the R1 gate's recorded `thread/started` delivered to a second client for a thread the
TUI created, plus the gate's recorded `thread/resume`-as-rejoin behaviour. Both are real
recordings; neither is a run of this build. The two remaining `//go:build realcli` measurements
(Q3 rollout-to-resume, Q4 `thread/read` losslessness) are likewise written and NOT taken, for the
same reason.

## Wired vs parked, corrected

**WIRED (production-reachable):**

- everything round 1's README lists as wired, MINUS the one line corrected below
- `Interaction.TurnRef` -> `nativeTurns` -> `turn/steer` / `turn/interrupt`
- thread adoption from `thread/started`, `thread/resume` with the rollout retry,
  `thread/loaded/list` discovery on the restart path
- `connectBackendsForRunning` from `Serve`, and with it `noteBackendRejoined`, which round 1
  disclosed as having no production caller
- the shim's containment of its backend on the agent-exec error path

**PARKED, and named as such:**

- **CORRECTION to round 1's README.** It listed "the capability derivation" in the WIRED column.
  That is wrong: `deriveSessionCapabilities` has **no production caller** -- `capability.go:91`,
  `:228-237` and `chat.go:289` all say so in as many words, and B94 cannot catch it because the
  reachability ledger covers exported symbols only. Its mutation fence (#10 of round 1) does
  fire, so the derivation is *correct*; it is simply not *reached*. Wiring it is the
  capability-publication slice's work (Mirror M5), not R7's, and `requireStructuredComposer`'s
  own comment already carries that as a named residual.
- **The live TUI probe and the two realcli measurements** (above).
- **A session adopted at RECONCILE keeps its hook token.** `registerSession` runs inside
  `daemon.Open`, before `d.core` exists, so the single-writer suppression cannot see that the
  session has a backend. It costs Codex nothing today (it posts no hooks and its typed rows have
  never fired through that path), but it is a second potential producer on one high-water
  namespace for a restarted daemon, and `connectBackendsForRunning` is the natural place to
  clear it once the engine grows a re-registration seam that does not reset the high-water.
- **`thread/read` backfill is never attempted.** A rejoin after a restart subscribes forward; it
  does not recover what happened while the daemon was away. Whether a lossless backfill exists at
  all is Q4.
- **No Android work.**

## Gate

`go build ./...`, `go vet ./...`, `golangci-lint run` (v2.12.2), `go test -race -count=1` on the
twelve owned packages plus `internal/verify` (the B94 reachability ledger), plus the GG-7
`protocol.md` bidi drift check. Timestamps and exit codes: `r7-green-round2.txt`.

**B94 delta: none.** No exported symbol was added this round -- `TurnRef` is a field on an
existing exported struct, and every new function is unexported. No allowlist row was added and
none was deleted.

**GG-7 delta: none.** No wire field and no op changed. `TurnRef` is machine-side and never
reaches the wire (fenced by `TestR7Fix_TheNativeTurnIdIsMachineSideAndNeverReachesTheWire`), and
`backend_joined_late` is a value inside an existing `structured_gap` record's free-text reason,
not a new key.

> **Superseded 2026-08-20 (round 4).** `backend_joined_late` no longer exists. Round 4's review
> ruled the rule behind it factually false -- `no rollout found` is returned *because* no turn
> has begun, so a join that had to retry missed nothing -- and it is replaced in the same
> position by `backend_prior_history`, which fires only when the thread had ALREADY run turns
> this daemon could not read. The GG-7 statement above (a value in a free-text reason, not a new
> key) holds unchanged for the replacement. See `README-round4.md` and ADR-013 revision 4.
