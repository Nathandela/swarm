# Wave R5 — phone remote launch: GREEN evidence

- epic: `agents-tracker-hggx` / bead: `agents-tracker-hggx.6` (Wave R5)
- role: GREEN (round 1), over the verified-failing RED inventory in
  `docs/verification/r5-red/` (dated 2026-08-16, HEAD f8338e1); ROUND 2 (review
  fix-pack, same date, 16:20–16:50 UTC), ROUND 3 (review fix-pack, same date,
  17:15–17:30 UTC) and ROUND 4 (review fix-pack, same date, 17:45–18:15 UTC) are
  recorded in their own sections at the end — where an earlier sentence below was made
  false by a later fix, the sentence is amended in place and marked
  "(round 2)"/"(round 3)"/"(round 4)". One round-4 correction is a retraction rather
  than an amendment: the pre-round-4 end-to-end claim was FALSE for every real
  provider, and it is marked as such where it stands.
- date: 2026-08-16 (15:00–15:35 UTC round 1; 16:20–16:50 UTC round 2), working tree
  on HEAD f8338e1
- scope authority: playbook "Wave R5" (lines ~768–786); ADR-007 B144(b) — the D8
  Phase-2 launch-execution deferral is LIFTED, every D8 restriction retained
  (allowed-root/symlink/options/environment policy, two-phase reservation, no
  phone-supplied env, explicit phone confirm, hard-coded option denylist).

## Disclosure, stated first

This wave's bar is **code-complete + gates green + fault-injection evidence**. The
physical exit (a physical phone launching each supported provider over cellular,
terminal attaching to the same session) is the OWNER's demonstration and has **not**
been run here; nothing below claims it. What a user can and cannot complete end to
end today is itemized in the last two sections.

## Deliverable 1 — machine-authored presets and setup UX

- `internal/daemon/launchpreset.go`: `LaunchPreset` custody (`SaveLaunchPresets` /
  `LoadLaunchPresets`, 0600 enforced even over a pre-existing file, missing file ==
  zero presets), `PresetRevision` (deterministic, content-bound, option-order
  independent), `ResolveLaunchPreset` with `ErrUnknownPreset` / `ErrStalePreset`
  sentinels, `LaunchSpecForPreset` (symlink-resolved root as Cwd, nil ClientEnv
  always, copied options, operation id + prompt carried, unresolvable root refused
  before any spec exists). Fenced by `internal/daemon/r5_launchpreset_test.go`.
- `cmd/swarm/presets.go`: `swarm remote presets add/list/edit/remove` (edit/remove
  are round 2) — minted stable opaque `preset-` id, printed content revision, root
  stored CANONICAL (symlink-resolved), nonexistent root refused at authoring time
  naming the path, explicit "no presets" empty state, usage naming every verb.
  Round 2 closed the lifecycle gap the review named: `remove` is the operator path
  that makes `unknown_preset` reachable (the id stops being authored), `edit`
  re-authors IN PLACE (same id, new revision) and is the operator path behind
  `stale_preset`, and `--agent` is validated against the adapter registry at
  authoring time (a typo'd provider refuses naming the registered choices — the
  file's own setup-time principle, now applied to its own field). Fenced by
  `cmd/swarm/r5_presets_test.go` + `r5_round2_test.go`. Round 3 (review MEDIUM):
  the allowlisted OPTIONS are now actually authorable — repeatable
  `--option key=value` on `add` and `edit` (edit REPLACES the authored set; a
  malformed entry or the reserved `worktree` key refuses at authoring time naming
  it), so `LaunchPreset.Options` is no longer a documented fiction and the
  preset-path R-POL.4 denylist is reachable in production. Fenced by
  `cmd/swarm/r5_round3_test.go`.
  (The playbook sentence "`swarm remote init` publishes machine-authored launch
  presets" is realized as the dedicated `presets` verb under `swarm remote` — same
  setup surface, separately invokable and idempotent per preset.)

## Deliverable 2 — signed op, stable refusal codes, activity, operation-status

- Wire: `schema.SessionLaunchReq` / `LaunchPresetView` / `OperationOutcomeView` /
  `ActivityRecord`; `SessionLaunchContentHash` binds preset id + revision + prompt;
  stable codes `unknown_preset` / `stale_preset`; outcomes `applied` /
  `outcome_unknown` / `unknown_operation`; `Control.session_launch/presets/
  preset_policy_revision/subject_operation_id/operation_outcome` (documented in
  `docs/specifications/protocol.md`; GG-7 bidirectional drift check green).
- `internal/protocol/remote_launch.go`: the REAL `session_launch` /
  `launch_presets` / `operation_status` handlers behind the SAME
  `requireRemoteAuthz` choke point (kill switch first; forged key, revoked pairing
  and read-only/read+approve tier all `not_authorized` at the one authenticator;
  body-version gate; every semantic refusal BEFORE argv composition — observable
  as zero backend `Launch` calls). D10 activity records for both the applied
  launch and every semantic refusal. Fenced by
  `internal/protocol/r5_sessionlaunch_test.go` (12 tests).
- The Wave R1 `op_not_implemented` rows for `session_launch`/`operation_status`
  are superseded by implementation exactly as pre-recorded in
  `docs/verification/r5-red/go-red.txt` §3: `r1_refusalops_test.go` (protocol and
  remotegw) had ONLY its expected codes retargeted (`wantCode`, a body for the
  now-bodied op, and the crash-redelivery driver moved to the still-refusal-only
  `composer_send`); every choke-point ordering assertion stands and runs green.
  `composer_send` / `turn_interrupt` / `terminal_control_begin` /
  `terminal_control_end` remain refusal-only.

## Deliverable 3 — exact path, two-phase reservation, fault injection

The preset path composes a `daemon.LaunchSpec` and calls the SAME
`DaemonAPI.Launch` free-form launch rides — same hard-coded `remoteForbiddenOptions`
denylist (R-POL.4), same `LaunchPolicy` allowed-root seam (R-POL.3, fail-closed
absent), same two-phase idempotent reservation keyed on the signed operation id.
`(*Daemon).OperationOutcome` (`internal/daemon/operationoutcome.go`) is the
read-only reconciliation surface; it applies the same liveness test the replay
path applies — INCLUDING, since round 3, replay's own phantom rule (`ShimPID != 0`;
the round-1/2 surface answered a false authoritative "applied" for the phase-1
reservation shape, review MAJOR 1) — so status and replay agree. Round 3 also made
a completed launch's idempotency record TERMINAL (`PhaseCompleted` on Launch
success and on a proven-spawned adoption), which is what closes the review's
proven second-process window: a replay of a completed operation whose session was
since deliberately Deleted refuses with `daemon.ErrLaunchOpConsumed` instead of
re-driving, and `operation_status` answers that shape authoritatively (`applied`).

(Round 4, review MAJOR 2 + LOW 4: the phantom rule now also gates the PRIMARY LAUNCH
REPLY, not just the status read — a concurrent driver arriving inside the winner's
phase-1 window is answered `daemon.ErrLaunchOutcomeUnknown` / wire `outcome_unknown`
rather than being handed a Running-with-no-process reservation as an applied session;
and the terminal-record rule now covers the LOST branch as well as the row-MISSING
one, so a completed operation whose session later went LOST reads `applied` and
refuses the re-drive instead of spawning a second agent. See the round-4 section.)

Fault-injection evidence (`internal/daemon/r5_launchfault_test.go`, run of
2026-08-16T15:33Z, also green under `-race -count=1`):

```
--- PASS: TestR5Fault_HappyLaunchIsAuthoritativeApplied (1.22s)
--- PASS: TestR5Fault_UnknownOperationIsNotInvented (0.04s)
--- PASS: TestR5Fault_CrashBetweenReserveAndSpawn_OutcomeUnknownNeverSilent (0.29s)
--- PASS: TestR5Fault_CrashAfterSpawnBeforeConfirm_ReplayAdoptsTheOneLiveProcess (0.27s)
--- PASS: TestR5Fault_DoubleDriver_ConcurrentSameOperationID_ExactlyOneProcess (0.19s)
```

- crash between reserve and spawn → `outcome_unknown`, never silent; replay
  re-drives to exactly one process, and the re-drive retires the phantom
  reservation (LOST, ShimPID==0) its own operation left, so the converged lists
  hold exactly one session per operation (`launch.go` `resolveReplay` — the one
  production change to the existing launch path, restricted to phantoms; a lost
  session that once recorded a real shim keeps its row). Round 2: the retire runs
  THROUGH `rollbackReserved`, compensating the phantom's PreLaunch side effect
  (the worktree) via `PreDelete` over its persisted meta — the round-1 bare
  `dropReserved` leaked it permanently (review BLOCKER 1; see the round-2
  section).
- crash after spawn before confirm → replay ADOPTS the one live shim (same id,
  same PID, one session).
- disclosed CEILINGS on "at most one process", stated here beside the claim
  (round 3; previously only in the round-2 BLOCKER-2 paragraph): (a) W4 — a
  re-drive past a session marked LOST because reconcile could not identity-match
  a LIVE orphan shim spawns a second agent beside that orphan; `launch.go`'s own
  SAFETY CEILING comment documents it, closing it needs orphan-PID tracking
  (follow-up 4c, `TestLaunchCrashReplay_W4_LiveOrphanAgent_TODO` skipped). (b) The
  round-3 delete-window closure covers every launch whose `Launch` RETURNED
  (record completed) — a launch that crashed before its terminal record and was
  never re-claimed keeps the W1-shaped re-drive semantics by design (that IS the
  crash-recovery re-driver).
- concurrent double-driver of one operation id → exactly one process, both
  callers converge; `OperationOutcome` answers `applied` with the winner.
- the outcome read is side-effect-free; an unknown id is never invented.

One test-side timing fix, disclosed: the double-driver's agent-process count now
POLLS for the winner's pid file (the package's own `readPIDFile` convention)
before asserting **exactly one** — the un-polled stat was a latent race the
`-race` gate exposed (the instrumented agent binary starts after `Launch`
returns). The asserted bound is unchanged.

One other test-side edit, demanded by its own fence: `internal/phonecore`'s
PB-GW-6 IssuedAt sweep fired on the two new seal producers
(`SealSessionLaunchEnvelope`, `SealLaunchPresetsEnvelope`) the moment they were
declared — exactly what its completeness half exists to do — and both were added
to its covered set (`issuedat_test.go`), where the stamp assertions now run over
them. No assertion was weakened anywhere; the only RED-inventory edit besides the
poll above is the pre-recorded R1 supersession retarget.

## Deliverable 4 — convergence, tiers, phone UX

- List convergence is structural: the preset path reaches the same
  registry/`List()` both tiers read (spec through the one `Launch`), and
  `RemoteOperationOutcome` namespaces the applied session id to the wire form the
  phone's roster carries. (The physical terminal-attach demonstration is the
  owner's, per the disclosure above.)
- Refusals before argv composition, each with its stable code: kill switch
  (`kill_switch`), read-only/read+approve tier (`not_authorized`), unknown preset
  (`unknown_preset`), stale profile (`stale_preset`), offline target (phone-side
  `ErrClassOffline` before anything is SIGNED or SEALED — round-2 wording fix: the
  inert `SessionLaunchReq` struct and its content hash are composed before the
  offline check inside `signedCommand`; nothing is signed, sealed, queued, and no
  send-seq is burnt — `PendingOpCount` stays 0. Round 2 also added a flag-shaped
  prompt refusal machine-side, below).
- `internal/skeleton/launchpresets.go` wires the assembled daemon:
  `LaunchPresetSource` (custody → wire views, sentinels mapped, root re-resolved
  through `LaunchSpecForPreset` so D8's no-gap rule holds), `OperationStatusSource`,
  `ActivityRecorder` (owner-readable `remote-activity.log`, 0600, one JSON line per
  mutation/refusal), and the `launch_presets` READ class in `actionClass`.
  `internal/remotegw` forwards `launch_presets` (Op == Action) and refuses a
  stripped `session_launch` body at the gateway (launch/approve precedent), body
  riding unchanged.
- `mobile/launchpresets.go`: `App.LaunchPresets` (pure read of what the machine
  published — honest empty state), `App.RefreshLaunchPresets` (the signed list
  read; reply adopted on `Outcome` claim), `App.SessionLaunch` (signed confirm at
  the displayed revision; live-only, offline refused before signing/sealing
  (round-2 wording fix — the inert request struct is composed first); empty
  id/revision refused before signing). PB-BIND-3 rows `launch.presets` /
  `launch.confirm`; surface golden regenerated via `-update-surface`; AAR rebuilt
  via `android/build-aar.sh` and the aar-surface gate green.
- `android/.../LaunchPresetScreen.kt`: the pure screen model — affordances
  NEW_SESSION / SELECT_PRESET / CONFIRM_LAUNCH / CANCEL_LAUNCH; availability
  resolver `launchAvailabilityFor` (offline > kill switch > tier > no-presets,
  unknown tier fails closed); the five confirmation facts + echoed revision;
  distinct copy per stable refusal; OUTCOME_UNKNOWN copy claims neither success
  nor failure; NO_PRESETS names `swarm remote presets`. JVM suite
  `LaunchPresetScreenTest.kt`: 9 tests, 0 failures.

## Gates (all commands of 2026-08-16, PATH=$HOME/go/bin:$PATH)

- `go build ./...` — green.
- `go vet ./...` — exit 0.
- `golangci-lint run` (v2.12.2) — 0 issues.
- `go test -race -count=1` on owned packages — green:
  `internal/daemon`, `internal/protocol`, `internal/protocol/schema`,
  `internal/remotegw`, `cmd/swarm`, `mobile`, `internal/phonecore`,
  `internal/skeleton`.
- `go test ./mobile/... ./internal/verify/ ./android/gate/` — green (conformance
  KATs included).
- `go test -count=1` over EVERY remaining package in the module (the 52 packages
  outside the owned/affected set, run 15:41–15:52 UTC) — green: zero failures
  across the whole tree.
- `TestB94` (reachability ledger) — green with ZERO new allowlist rows: every new
  exported symbol is production-reachable (CLI → custody; assembled skeleton →
  handlers; mobile facade roots). No rows deleted (none existed for R5 symbols).
- Android gradle unit lane (script `/tmp/r5_green_androidunit.sh`,
  `JAVA_HOME=/usr/local/opt/openjdk@21/...`, `ANDROID_HOME=/usr/local/share/
  android-commandlinetools`, `./gradlew --no-daemon --rerun-tasks
  --no-build-cache :app:testDebugUnitTest`): start 15:22:33Z, BUILD SUCCESSFUL in
  4m10s, end 15:26:47Z; `TEST-dev.swarm.phone.ui.screens.LaunchPresetScreenTest.xml`
  fresh from this run, tests=9 failures=0 errors=0. `android/app/libs/swarm.aar`
  mtime 15:22 UTC (rebuilt this session via `android/build-aar.sh`, BEFORE the
  gradle run that consumed it).

## What a user CAN complete end to end today

- Terminal: author and list presets (`swarm remote presets add/list`), with
  canonical roots, minted ids and visible revisions; refusals name the offending
  path.
- Machine side, over the real assembled daemon: a signed `launch_presets` answers
  the authored list; a signed `session_launch` at the confirmed revision launches
  through the exact policy + two-phase path; every refusal lands its stable code
  before argv composition and is recorded (with the applied launches) in
  `remote-activity.log`; `operation_status` reconciles applied /
  outcome_unknown / unknown_operation.

  **CORRECTED IN ROUND 4 (review BLOCKER 1). As written before round 4 this claim
  was FALSE for a real provider**, and the correction is recorded here rather than
  quietly rewritten. `internal/protocol/remote_launch.go` composed the spec with
  `ClientEnv: nil` (right, per ADR-007 D8) and nothing then supplied the daemon's
  side of D8: `skeleton.lookPathIn` reads PATH from that env alone, so a bare
  adapter `argv[0]` could never resolve, and `daemon.spawnShim` built the agent
  env as `injectHookEnv(persist.FilterEnv(nil), ...)` — no PATH, no HOME. Every
  `session_launch` naming a `claude`/`codex` preset refused at
  `cc.srv.d.Launch(spec)` with `launch: resolve claude binary: agent binary
  "claude" not found on the agent PATH`. The owner's physical exit would have
  failed 100% of the time. What made this invisible: the skeleton suite bypassed
  `coreAPI.Launch` with a pre-supplied `Argv` AND a `ClientEnv` carrying PATH, and
  the protocol suite's fake `DaemonAPI` composes no argv at all.

  What is true NOW, and pinned by
  `internal/skeleton/r5_round4_test.go:TestR5Round4_RemoteLaunchOfAProductionPresetResolvesOnTheDaemonsOwnPath`
  — the REAL assembled `coreAPI.Launch`, `fakeAgentBin ""` (the production gate
  armed), a production-shaped preset spec (`AgentType "claude"`, options, initial
  prompt), `ClientEnv` nil, and a real executable named `claude` reachable ONLY
  from the daemon's own PATH: the launch succeeds, `argv[0]` resolves to that
  binary, and the session's env carries PATH and HOME. `daemon.PolicyEnv`
  (`internal/daemon/policyenv.go`) is D8's other half: a supplied client env is
  used as before, and a launch with NO client env gets the daemon's own process
  environment through the SAME normative allowlist (`persist.FilterEnv`, S-2 —
  PATH, HOME, SHELL, TERM, locale, venv/conda, the two provider keys; everything
  else still dropped). The allowlist was NOT widened. The phone contributes
  nothing to it, and ADR-006's billing inheritance holds one level up: the daemon
  process is the user's machine environment. A provider that is genuinely absent
  from that PATH still refuses with the binary-not-found reason (pinned by the
  sibling test), so the fix creates no silent success.
- Phone core (gomobile facade): select/confirm verbs are wired to that wire path,
  with the offline and invalid-selection refusals phone-side.

## What a user CANNOT complete yet (parked, with owners)

- ~~The Android launch SCREEN composition~~ — **RESOLVED IN ROUND 2** (review
  BLOCKER 3): the flow is composed and reachable; see the round-2 section. The
  three unbound-verbs rows are DELETED per their own deletion condition.
- **LaunchPanel.kt retirement**: the Phase B free-form agent+cwd form is untouched
  and remains composed BELOW the preset flow in the same launch host; its
  retirement from the remote surface is recorded as this wave's decision but the
  file is not deleted here (deleting it would orphan `App.Launch` and the
  `launch` coverage row — a separate, deliberate slice).
- **Phone-side `operation_status` verb**: the wire carries
  `subject_operation_id` end to end (gateway copies it; round 2 BINDS it into the
  signed tuple via `schema.OperationStatusContentHash`), but no facade verb
  issues it yet; the phone's reconciliation today is the reply-plane `Outcome`.
  Consequently the screen's OUTCOME_UNKNOWN state has no live producer yet.
- **A definitively FAILED launch reports `outcome_unknown` on a later
  operation_status read** (review LOW, disclosed rather than changed): when
  `d.Launch` errors after the reservation, the phone received an authoritative
  refusal AT THE TIME, but the surviving idempotency record points at a
  rolled-back session, so a later status read answers `outcome_unknown` — the
  playbook wording ("authoritative or outcome_unknown") permits it, and an
  authoritative-negative answer needs a fourth wire state, deferred with this
  note rather than half-added. (Round 3: the SIBLING case the next review named —
  a launch that definitively APPLIED and was then deliberately deleted — no
  longer reads `outcome_unknown`: the completed record survives Delete and
  answers `applied` with its session id. Only the failed-launch case remains
  parked on the missing fourth state.)
- **The physical exit**: each provider over cellular + terminal attach — the
  owner's, per the bead.

## Round 2 — review fix-pack (2026-08-16, 16:20–16:50 UTC)

Over the round-1 tree, against the recorded review findings. TDD: every fix has a
dated failing-first record in `docs/verification/r5-red/go-red-round2.txt` and
`android-red-round2.txt` (compile-RED and behavioral-RED classified per file; the
one deliberate fence that passes at RED — the gateway redelivery coverage — is
labelled as such there). No existing assertion was weakened; every RED file is
additive, and the one gate-table edit (`android/gate/s24_screens_test.go`) ADDS a
composed screen's claim.

### BLOCKER 1 — the phantom's worktree no longer leaks

`internal/daemon/launch.go`: the replay path retires the phantom reservation
through `rollbackReserved` semantics instead of a bare `dropReserved` —
`resolveReplay` now returns the phantom's PERSISTED meta and the retire runs
`PreDelete` over it (the file's own stated rule), so the prior run's PreLaunch
side effect (the Epic 12 git worktree) is compensated before the row is erased
forever. Fenced by `r5_round2_test.go`
`TestR5Round2_PhantomRetireCompensatesPreLaunchSideEffect` (RED: "PreDelete
compensated 0 side effects, want exactly 1") plus a no-hook control test; the
whole `TestR5Fault` suite re-run green under `-race`.

### BLOCKER 2 — the OUTCOME_UNKNOWN copy stops promising what the code does not do

`LaunchPresetScreen.kt`: the copy claimed a re-confirm "re-sends the same
operation and can never create a second session" — false (a fresh operation id is
minted per `App.SessionLaunch`; and even one id can re-drive past a live orphan,
the documented W4 ceiling). The honest copy: "Check the session list first:
confirming again sends a new launch and may create a second session."
`LaunchPresetRound2Test.kt` pins it (must NOT contain "never"/"same operation",
MUST warn "new launch" + "second session") — the round-1 fence that let the false
claim through is strengthened, not weakened: its overclaim sweep still runs.

### BLOCKER 3 — the launch flow is composed and reachable, and TIER has a data source

- **Tier source (new wire fact)**: the `launch_presets` reply now stamps
  `device_capability` — the SIGNING device's registry-pinned tier, read
  machine-side through the new `protocol.DeviceCapabilitySource` seam
  (`skeleton.coreAPI.DeviceCapability` over the same pinned registry
  `authorizeCommand` uses; never from the wire, empty when no seam — never
  invented). Documented in `protocol.md` (GG-7 bidi check green). The facade
  adopts it (`App.LaunchCapability`, "" until a reply arrives), the golden and the
  AAR are regenerated, and `FacadeBridge.launchPresetFlow` feeds it to the
  model's resolver. Fenced end to end: `internal/protocol/r5_round2_test.go`
  (stamped for the authenticated signer; absent seam answers empty),
  `internal/skeleton/r5_launchpresets_test.go` (registry read), and
  `mobile/r5_round2_test.go` (wire fact, replace-on-adopt, refusals adopt
  nothing).
- **First-run state**: an empty tier resolves the new `FETCHING` availability
  state (its own copy naming the fetch remedy) — not a TIER_FORBIDS slander and
  not a button; a NON-empty unrecognised tier still fails closed as TIER_FORBIDS
  (both pinned in `LaunchPresetRound2Test.kt`).
- **Composition**: `LaunchPresetView.kt` (kit components, claimed in the s24
  composition table) is composed by `PhoneSurface.drawLaunch` ABOVE the free-form
  form in the existing launch host on the Inbox destination: heading, the
  resolver's named denial, the machine-authored rows (a row IS the select
  control), the surface-owned "Fetch presets" control (one signed
  `launch_presets`, its reply claimed by operation id — the adoption moment), the
  D8 confirmation sheet (`AlertDialog`: machine label, provider, canonical
  workspace path, worktree behavior sentence, echoed revision, the one free-text
  prompt box; Start session / Cancel are named controls), the signed
  `session_launch` at the DISPLAYED revision, and the delivery line resolved by
  the new model mapping `noticeStateFor(code)` (APPLIED on the reply op,
  PENDING while unclaimed, each stable refusal state, catch-all REFUSED carrying
  the machine's words). `App.LaunchPresets` / `App.RefreshLaunchPresets` /
  `App.SessionLaunch` / `App.LaunchCapability` are all CALLED from production
  Kotlin; the three unbound-verbs rows are deleted and the bound-verb ledger gate
  is green.

### MAJOR 1 — a flag-shaped prompt can no longer reach argv as a flag

`internal/protocol/remote_launch.go`: on the remote tier an initial prompt whose
first non-space byte is `-` is refused `policy` BEFORE any spec exists (the
adapters append the prompt as the last argv token with no `--`, so
`--dangerously-skip-permissions` typed as a prompt WAS the flag the denylist
refuses). Interior dashes untouched; negative control pinned. The pre-existing
owner-tier ceiling (B133) is unchanged and remains recorded in
`docs/verification/a1b-rc-scrub.md`; what round 2 closes is the phone-authored
path this wave opened.

### MAJOR 2 — operation_status's subject is bound into the signed tuple

`schema.OperationStatusContentHash(subject)` now rides the authz content slot
(session slot stays the sentinel), recomputed daemon-side from the forwarded
subject — a gateway (or any read-tier device replaying a captured frame) can no
longer re-point a valid signature at another operation id. The stale
"not part of the signed tuple" comment on `RemoteCommand.SubjectOperationID` is
corrected, `protocol.md` updated. No production signer existed yet (the phone
verb is parked), so no signer migration was needed; the future facade verb must
call the same function (the no-re-derivation rule, stated on it).

### MAJOR 3 / MEDIUM 1 / MEDIUM 2 — see deliverable-1 amendment and:

- `internal/skeleton/r5_launchpresets_test.go` (NEW, the previously untested
  assembled-daemon half): custody -> wire views + content-bound policy revision,
  sentinel mapping + D8 root re-resolution, the namespaced operation-status
  answer over a real launched daemon, the pinned-registry capability seam, and
  the activity log's 0600 custody INCLUDING over a pre-existing 0644 file — the
  chmod gap is fixed in `RecordRemoteActivity` (behavioral RED first).
- `internal/remotegw/r5_round2_test.go`: crash-shaped redelivery of a WELL-FORMED
  `session_launch` (the one op in the vocabulary whose redelivery can spawn):
  exactly one re-forward, same operation_id (what the daemon's idempotency keys
  on), preset body intact, window closes on run 3. A coverage fence — passes at
  RED by design, labelled in the RED record.

### Round-2 gates (all of 2026-08-16, PATH=$HOME/go/bin:$PATH)

- `go build ./...` green; `go vet ./...` exit 0; `golangci-lint run` v2.12.2 — 0
  issues.
- `go test -race -count=1`: `internal/daemon` `internal/protocol`
  `internal/protocol/schema` `internal/remotegw` `cmd/swarm` `mobile`
  `internal/phonecore` `internal/skeleton` — all ok.
- `go test -count=1 ./android/gate/` — ok (bound-verb ledger with the three rows
  deleted; aar-surface gate against the REBUILT AAR; the s24 sweep with the new
  screen claimed; both R5 UI gates).
- `TestB94` — green; delta: ZERO allowlist rows added, ZERO deleted (the
  bidirectional check enforces both directions; every new exported symbol is
  production-reachable).
- Whole-tree `go test -count=1 ./...` — one failure on the parallel sweep and it
  is stated rather than hidden: `cmd/swarm TestRunShim_LaunchesAgentPersistsAndLeadsSession`
  ("shim never became its own session leader"), a PRE-EXISTING setsid-timing test
  untouched by this wave, failing only under whole-tree parallel load; it passes
  in isolation, and the full `cmd/swarm` package passes standalone AND under
  `-race -count=1` (both re-run after the sweep). Every other package in the tree
  is green on the sweep.
- Android gradle unit lane (script `/tmp/r5r2_androidunit_green.sh`, JAVA_HOME
  openjdk@21, ANDROID_HOME command-line tools, `--no-daemon --rerun-tasks
  --no-build-cache :app:testDebugUnitTest`): AAR rebuilt 16:34 UTC via
  `android/build-aar.sh` (with `App.LaunchCapability` + PresetInfo surface);
  gradle 16:34:29Z -> BUILD SUCCESSFUL in 4m21s -> 16:38:51Z;
  `TEST-...LaunchPresetScreenTest.xml` tests=9 failures=0 and
  `TEST-...LaunchPresetRound2Test.xml` tests=7 failures=0, both stamped
  16:38:26Z (after the AAR they consumed).

### What a user CAN now complete end to end (round-2 amendment)

From the shipped app's real entry (Inbox destination, launch host): see the
availability state with its named reason (first-run FETCHING, offline, kill
switch, tier — now fed by the machine's own `device_capability` stamp — and
zero-presets each with distinct copy and remedy); fetch the machine's presets
with a visible refusal if the fetch is refused; select a row; read the five
confirmation facts plus the echoed revision; type the one optional prompt;
confirm or cancel; and see the verb's visible outcome — APPLIED, PENDING, each
stable refusal sentence, or the catch-all refusal carrying the machine's words.
Still honest: OUTCOME_UNKNOWN has no live phone-side producer until the
operation_status facade verb lands (parked above), and the physical-exit
demonstration remains the owner's.

## Round 3 — review fix-pack (2026-08-16, 17:15–17:30 UTC)

Over the round-2 tree, against the recorded round-3 review findings. TDD: every
fix has a dated failing-first record in `docs/verification/r5-red/go-red-round3.txt`
and `android-red-round3.txt` (compile-RED vs behavioral-RED classified per file).
No existing assertion was weakened; every round-3 RED file is additive and no
prior test was edited.

### MAJOR 1 — no false authoritative mid-flight (proven by the review's probe)

`internal/daemon/operationoutcome.go`: `usable` now also requires
`ShimPID != 0` — resolveReplay's own phantom rule applied to the status read. A
status read landing between reserve and spawn (phase-1 meta persisted, Running,
no process) answers `outcome_unknown`, never "applied" naming a session whose
launch can still fail and roll back. The op is live for READ-tier devices today,
so this was a wire-reachable false positive. Fenced by
`TestR5Round3_MidFlightReservationIsNeverAuthoritativeApplied` (probe at
`phaseReserved`, the review's exact injection point).

### MAJOR 2 — replay after Delete can no longer spawn a second process

`internal/daemon/launch.go`: a launch that returns success now COMPLETES its
idempotency record (`d.idem.Complete`, fsync'd terminal phase), and a replay
that adopts a proven-spawned session (`ShimPID != 0`, never a mid-flight
reservation a double-driver's loser might observe) completes it too.
`resolveReplay` refuses a completed operation whose session row is GONE with the
new stable sentinel `daemon.ErrLaunchOpConsumed` instead of re-driving — the
review's probe (Launch → Delete → Launch same signed op ⇒ NEW session, new shim,
second agent's pidfile written, inside the 1-hour signature validity and the
gateway's pinned crash-shaped redelivery) now refuses with zero sessions and
zero spawns. Fenced by
`TestR5Round3_ReplayAfterDeleteRefusesInsteadOfSpawningASecondProcess` and
`TestR5Round3_AdoptedReplayThenDeleteAlsoRefusesRedrive`; the whole
`TestR5Fault`/`TestR5Round2`/`TestLaunchCrashReplay` families re-run green under
`-race -count=1`. The residual ceilings (W4 live orphan; a launch that crashed
before its terminal record) are now disclosed BESIDE the deliverable-3 claim
above, not only in the round-2 paragraph.

### LOW — applied-then-deleted reads back `applied`, not `outcome_unknown`

Same mechanism: the completed record survives Delete, so `OperationOutcome`
answers `applied` with the session id for an operation the machine completed —
the shipped OUTCOME_UNKNOWN copy ("confirming again may create a second
session") no longer describes a definitively completed launch. Fenced by
`TestR5Round3_AppliedThenDeletedReadsBackAuthoritativeApplied`. (The
definitively-FAILED sibling stays parked on the missing fourth wire state,
amended in place above.)

### MAJOR (D3) — the FETCH PRESETS refusal is no longer silently swallowed

- `android/.../PhoneSurface.kt`: the launch outcome claim is ONE-SHOT —
  `presetOp` clears once a landed (non-PENDING) outcome has rendered. Before,
  the latched op re-claimed the resolved launch every render pass and its stale
  sentence unconditionally overwrote the fetch refusal written in the same pass
  (`App.Outcome` is a persistent map), so a machine refusal of the fetch verb
  had NO surface — a silent primary control, the bead's named defect class.
- `android/.../LaunchPresetScreen.kt`: the fetch verb's refusal renders through
  its OWN copy, `fetchNoticeFor` — a refused READ says the preset list could not
  be fetched, never "not authorized to launch sessions" (copy about a verb the
  user did not press).
- Fenced: `android/gate/r5_round3_ui_test.go` (source-level, the gate idiom for
  Surface behavior) + `LaunchPresetRound3Test.kt` (JVM, 3 tests: distinct copy
  per shared state, the kill-switch fact retained, the machine's words carried).

### MEDIUM — `--option` authoring (see the deliverable-1 amendment above)

`cmd/swarm/presets.go` gains repeatable `--option key=value` on `add`/`edit`
(edit replaces; malformed and reserved-`worktree` entries refuse at authoring
time), fenced by `cmd/swarm/r5_round3_test.go` (3 tests, including
options-move-the-revision).

### Round-3 gates (all of 2026-08-16, PATH=$HOME/go/bin:$PATH)

- `go build ./...` green; `go vet ./...` exit 0; `golangci-lint run` v2.12.2 —
  0 issues.
- `go test -race -count=1`: `internal/daemon` `internal/protocol`
  `internal/protocol/schema` `internal/remotegw` `cmd/swarm` `mobile`
  `internal/phonecore` `internal/skeleton` — all ok (full packages, not just the
  R5 filters).
- `go test -count=1 ./android/gate/` — ok (both round-3 gates green among them).
- `TestB94` — green; delta: ZERO allowlist rows added, ZERO deleted (the one new
  exported symbol, `daemon.ErrLaunchOpConsumed`, is production-reachable through
  `resolveReplay`).
- Android gradle unit lane (script `/tmp/r5r3_androidunit_green.sh`, JAVA_HOME
  openjdk@21, ANDROID_HOME command-line tools, `--no-daemon --rerun-tasks
  --no-build-cache :app:testDebugUnitTest`): 17:19:07Z → BUILD SUCCESSFUL in
  4m12s → 17:23:20Z; `LaunchPresetRound3Test` 3/0/0, `LaunchPresetRound2Test`
  7/0/0, `LaunchPresetScreenTest` 9/0/0, all XMLs stamped 17:22:56Z. The AAR is
  UNTOUCHED this round (mtime unchanged from round 2) — correct, because round 3
  changes no `mobile/` Go surface.

---

## Round-4 review fix-pack (2026-08-16)

RED evidence: `docs/verification/r5-red/go-red-round4.txt` and
`docs/verification/r5-red/android-red-round4.txt`. Four findings, all closed.

### BLOCKER 1 — the remote launch path could not launch any real provider

Recorded in full, including the false claim it invalidated, under "What a user CAN
complete end to end today" above. Code: `internal/daemon/policyenv.go` (new,
`PolicyEnv`), `internal/daemon/launch.go` (the launch environment is resolved ONCE at
the top of `launch()`, so the persisted meta and the env the shim execs with cannot
disagree), `internal/skeleton/api.go` (`coreAPI.Launch` resolves the env BEFORE argv,
because argv0 resolution depends on the PATH inside it). Fenced by
`internal/skeleton/r5_round4_test.go` (2 tests) and
`internal/daemon/r5_round4_test.go` (2 of its 4).

### MAJOR 2 — the concurrent loser was handed a phantom as an authoritative success

`internal/daemon/launch.go:resolveReplay` now gates its idempotent-success return on
`s.meta.ShimPID != 0` — the SAME phantom rule round 3 gave the status read. A present,
Running session with no recorded process is the winner's phase-1 reservation; the loser
is answered `ErrLaunchOutcomeUnknown` and the launch neither claims a session nor drives
a second process for it.

On the wire that answer keeps its meaning: `schema.CodeOutcomeUnknown`
(`internal/protocol/schema/launchpreset.go`, value `outcome_unknown` — deliberately the
same string `operation_status` reports, ADR-017 T9's vocabulary reaching the phone one
hop earlier) is replied by `internal/protocol/remote_launch.go` for that sentinel alone;
every other daemon refusal still reads `policy`. The D10 activity record for it says
`outcome_unknown`, not `refused` — a launch that may be running must not be logged as
turned away. On the phone, `LaunchPresetScreen.noticeStateFor("outcome_unknown")` now
maps to `LaunchDeliveryNotice.OUTCOME_UNKNOWN`, whose round-1 copy claims neither success
nor failure and sends the user to the session list first.

Fenced by `internal/daemon/r5_round4_test.go` (the reviewer's own probe shape: crash at
`phaseReserved`, then `resolveReplay(op, fresh-id)`, plus the same assertion through the
real `Launch`), `internal/protocol/r5_round4_test.go` (2 tests) and
`LaunchPresetRound4Test.kt`.

ONE PRE-EXISTING TEST WAS AMENDED, disclosed in go-red-round4.txt §5:
`TestR5Fault_DoubleDriver_ConcurrentSameOperationID_ExactlyOneProcess` required BOTH
drivers to return a session, which is the round-1 encoding of this defect. Its
at-most-one-process assertions are unchanged; it now additionally requires that any
returned session have `ShimPID != 0` and that a non-returning driver fail ONLY with
`ErrLaunchOutcomeUnknown`. Verified non-flaky at `-count=6`, with and without `-race`.

### MEDIUM 3 — the fetch refusal was swallowed for the whole in-flight launch window

Root cause fixed, not narrowed again: the fetch verb has its OWN slot end to end —
`PhoneSurface.presetFetchDelivery` (written only by the fetch block, cleared by the press
that retries and by a fetch that succeeds), `LaunchPresetPanel.fetchNotice`, and
`LaunchPresetTag.FETCH_DELIVERY` drawn as its own line directly under the FETCH control.
Neither verb's sentence can now be a function of the other verb's state. Fenced by
`android/gate/r5_round4_ui_test.go` (3 source-level pins) and `LaunchPresetRound4Test.kt`
(the in-flight window specifically: a PENDING launch notice and a fetch refusal are both
carried, independently).

### LOW 4 — a COMPLETED operation whose session went LOST

Resolution: APPLY THE RULE, do not disclose an inconsistency. `resolveReplay`'s LOST
branch now refuses a completed record with `ErrLaunchOpConsumed` exactly as the
row-MISSING branch does, and `OperationOutcome` answers `applied` for any completed
record whatever became of its session. Why this and not the disclosure option: a terminal
record is written only when `launch()` RETURNED SUCCESS, so it is the machine's own proof
the launch happened; LOST is a later fact about the PROCESS (reconcile could not
re-identify the shim), never evidence the launch did not apply. Re-driving it spawns a
second agent for an operation that already applied, under a signature valid for the whole
command-validity window and on a redelivery the gateway is PINNED to perform. The W3
crash re-driver is untouched: a launch that died mid-flight never reaches
`PhaseCompleted` (`resolveStaleLaunches` fails those records on Open), so
prepared/executing records pointing at LOST sessions still re-drive. Status and replay
now agree on this branch, which is the property `operationoutcome.go` already claimed.
`ErrLaunchOpConsumed`'s message was corrected from "its session was since deleted" to
"its session is no longer usable" to cover both branches honestly.

### Round-4 gates (all of 2026-08-16, PATH=$HOME/go/bin:$PATH)

- `go build ./...` green (17:58:42Z); `go vet ./...` exit 0; `golangci-lint run`
  v2.12.2 — 0 issues (18:02Z, after fixing 2 QF1008 findings in the new test file).
- `go test -race -count=1 ./internal/daemon/ ./internal/skeleton/
  ./internal/protocol/... ./internal/persist/` — green (17:59:12Z → 18:05:18Z;
  `internal/daemon` re-run green after the amended double-driver test), and
  `go test -race -count=1 ./internal/remotegw/ ./cmd/swarm/ ./mobile/
  ./internal/phonecore/` — green (18:20:44Z → 18:21:29Z). Together this is the same
  owned-package race set rounds 1–3 used.
- `go test -count=1 ./...` over the WHOLE module — green (18:05:47Z → 18:11:02Z), zero
  failures anywhere in the tree. STATED HONESTLY: a second full-tree run at
  18:14:09Z→18:19:27Z hit ONE failure,
  `cmd/swarm TestRunShim_LaunchesAgentPersistsAndLeadsSession` ("shim pid never became
  its own session leader"), which is a known-shape timing flake under a loaded parallel
  run and is NOT in this round's blast radius (round 4 touches no `cmd/swarm` or
  `internal/shim` code — `git diff HEAD -- cmd/swarm/ internal/shim/` shows only the
  pre-existing R5 `remote.go` hunk). Re-run `-count=3` on that test: green; the whole
  `cmd/swarm` package re-run: green; and again under `-race`: green.
- `TestB94` — green, 691 exported symbols examined, 57 unreachable and all accounted
  for; delta ZERO allowlist rows added and ZERO deleted (the three new exported symbols
  — `daemon.PolicyEnv`, `daemon.ErrLaunchOutcomeUnknown`, `schema.CodeOutcomeUnknown` —
  are all production-reachable).
- Android gradle unit lane (script `/tmp/r5r4_androidunit_green.sh`, JAVA_HOME
  openjdk@21, ANDROID_HOME command-line tools, `--no-daemon --rerun-tasks
  --no-build-cache :app:testDebugUnitTest`): 17:54:40Z → BUILD SUCCESSFUL in 3m50s →
  17:58:31Z; `TEST-...LaunchPresetRound4Test.xml` tests=3 failures=0 errors=0, stamped
  by this run. The AAR is UNTOUCHED this round (correct: round 4 changes no `mobile/`
  Go surface).

### Not changed this round, and why

- The `swarm` binary is not rebuilt into any AAR and no `mobile/` exported surface
  moved, so the surface golden and `screen_coverage.tsv` are untouched.
- The W4 safety ceiling (a re-drive racing a LIVE orphan shim) is unchanged and still
  tracked by the skipped `TestLaunchCrashReplay_W4_LiveOrphanAgent_TODO`; round 4
  narrows WHEN a re-drive happens but does not add orphan-process tracking.
