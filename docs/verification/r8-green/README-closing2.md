# Wave R8 — CLOSING round 2 (2026-08-21): the READ half, made correct and described truthfully

Bead `agents-tracker-hggx.9`. **This document is authoritative for Wave R8.** Where it
disagrees with `README-closing.md`, `README-round3.md`, `README-round2.md`, `README.md` or
`../r8-red/README.md`, it wins; `README-closing.md` now opens with a CORRECTION banner naming
the three claims of its own that are struck, and the earlier files keep theirs.

Captured on `darwin 25.5.0 / arm64`, Go from `$HOME/go/bin`, `golangci-lint v2.12.2`, JDK 21
(`/usr/local/opt/openjdk@21/...`), Gradle 9.5.1 wrapper, Android SDK at
`/usr/local/share/android-commandlinetools`.

---

## 0. THE ONE SENTENCE

**R8 still ships the READ half only; the control half is still PARKED** (ADR-017 C0/C1, and
this round built none of it). What changed is that two correctness defects **in the half that
ships** are closed, one false evidence claim is corrected, and each fix is fenced by a test
that fails when the production line moves.

---

## 1. THE THREE FINDINGS, AND WHAT WAS DONE

| # | Finding (closing review, round 2) | Verdict | Fix | Fence | Mutation |
|---|---|---|---|---|---|
| **F6-2** | The machine's reap BLANK carries a ZERO `rendered_at`; `ageOf(0)=0`; `watchLapsed(0)=false` — so **round 3's blank actively defeated round 4's lapse detector** and a foregrounded screen sat blank forever | **CONFIRMED, fixed** | `TerminalGrid.machineStoppedRendering` (geometry, not age) + `TerminalFallbackBinding.watchLapsed(grid)`; `PhoneSurface.heldWatchLapsed` asks about the GRID | `TerminalFallbackWatchTest` (6 tests, JVM), `internal/remotegw/r8r5_reapblank_test.go` (real ServeRemote + real Gateway + real TerminalWatcher), `internal/phonecore/r8r5_viewreset_test.go`, `android/gate/r8r5_blanklapse_test.go` (wiring only) | **MUT-B, MUT-D, MUT-E fire** |
| **D0** | `reset` is on the wire and **nothing read it**, and the view epoch is **process-local** — so a daemon restart reproduces the T4-a frozen screen F5 was written to prevent | **CONFIRMED, fixed** | `SnapshotCache.Apply` adopts a frame the machine marked `reset` unconditionally; `viewEpochSeq`'s comment corrected | `internal/phonecore/r8r5_viewreset_test.go` (4 tests) | **MUT-A fires** |
| **F8-2** | Kill/delete severance was fenced at the **helper**, never at the **call site**: deleting all four production call sites left `./internal/protocol/` green | **CONFIRMED (evidence only; the product was correct), fixed** | no production change — a fence | `internal/protocol/r8r5_severcallsite_test.go` (both handler branches, driven to a reply over a real `ServeRemote`) | **MUT-C fires on both tests; the round-4 tests stay green under it** |

Red transcripts: `../r8-red/closing2-red.txt`. Mutation transcripts:
`../r8-red/closing2-mutations.txt`.

---

## 2. WHAT A USER CAN AND CANNOT DO — the rows this round changes

### Corrected CAN rows

| A user can… | Where it is true in the code | What proves it |
|---|---|---|
| Have a lapsed watch **re-established**, including when the lapse arrives as the machine's own BLANK | `TerminalFallbackScreen.kt` `watchLapsed(grid)` / `machineStoppedRendering`; `PhoneSurface.kt` `heldWatchLapsed` → `reconcileTerminalWatch`'s same-session branch | `TerminalFallbackWatchTest`; `android/gate/r8r5_blanklapse_test.go`; machine side `internal/remotegw/r8r5_reapblank_test.go` |
| Keep reading a session **through a daemon restart** rather than a frozen pre-restart screen | `internal/phonecore/snapshot.go` `SnapshotCache.Apply` (reset = hard reset) | `internal/phonecore/r8r5_viewreset_test.go` |

The previous round's version of the first row (`README-closing.md:46`) was **false on the blank
path** and is struck in that file's banner.

### CANNOT (unchanged, and re-checked this round)

- **Type into a terminal.** No production implementor of `protocol.TerminalInputSink` exists;
  `internal/verify/r8r4_parkedcontrol_test.go` fails the day one appears. This round added no
  control verb, no sink, no affordance.

---

## 3. THE SEAM THIS ROUND DOES NOT PRETEND TO CLOSE

The F6-2 property spans the gomobile boundary: the machine authors the blank in Go, the rule
that reads it is Kotlin, and `PhoneSurface.heldWatchLapsed` needs a live `App` (libgojni) so no
JVM test can reach it. **No single test drives it end to end, and this document does not claim
one does.** It is held by three tests meeting at one set of values plus one source-shape gate:

1. `internal/remotegw/r8r5_reapblank_test.go` — the machine really sends `{no lines, cols 0,
   rows 0, rendered_at zero}` when the real `TerminalWatcher` reaps a watch on the real
   `Gateway` peeking a real `protocol.ServeRemote`. It also asserts a LIVE view carries
   geometry, so the discriminator is not vacuous.
2. `internal/phonecore/r8r5_viewreset_test.go` — those values survive the phone's real cache.
3. `TerminalFallbackWatchTest` — the rule answers LAPSED for exactly those values, and NOT
   LAPSED for a live screen inside the horizon and for a machine that sends no render time.
4. `android/gate/r8r5_blanklapse_test.go` — the wiring, and **only** the wiring: the surface
   must hand the rule the grid rather than an age it extracted from it. It is a source scan and
   its comment says so.

---

## 4. GATES

| Gate | Result |
|---|---|
| `go build ./...` | **exit 0** |
| `go vet ./...` | **exit 0** |
| **`go test ./...` (WHOLE REPO, not a package list)** | **exit 0** — see §4.1 |
| `golangci-lint run` (v2.12.2) | **exit 0** |
| Android `test` (both variants, `--no-daemon --rerun-tasks --no-build-cache`, from `scripts/o2-gradle-run.sh`) | **exit 0**; 175 XML files per variant, all fresh; **1401 tests, 0 failures, 0 errors** in each |
| `internal/verify` (B94) + GG-7 `protocolmd` bidi | inside the whole-repo run; `TestProtocolMD*` re-run green after the `reset` row edit |

`swarm.aar` mtime `2026-08-21 06:59` — **unchanged by this round**: the fix needed no new
gomobile field, so the Android suite ran against the same native library the wave built.

### 4.1 Gate discipline

The whole-repo run was started **after** every mutation was restored and each restored file was
verified byte-identical by sha256 against its pre-mutation hash (hashes in
`../r8-red/closing2-mutations.txt`). No `go test <package list>` result is reported anywhere in
this document as if it were the gate.

The gate was run **twice**, and the second run is the one reported: the first was started before
this round's documentation and a `gofmt` of one new test file landed, so it was not a run of the
final tree. Both were `go build ./...` → `go vet ./...` → `go test ./...` → `golangci-lint run`,
all four exit 0. In the second run some packages report `(cached)` — `internal/remotegw`,
`internal/skeleton`, `mobile/conformance` — because their inputs are byte-identical to the first
run's, which executed them; the packages this round changed (`internal/phonecore` 29.4s,
`internal/protocol` 26.7s, `android/gate` 20.6s, `mobile` 29.5s, `internal/verify` 18.0s) all
re-ran.

---

## 5. TREE AND PROCESS HYGIENE

- **No `git add`, `commit`, `stash`, `checkout` or `clean`** was run at any point. Every
  mutation was backed up with `cp` and restored with `cp`, each restore sha256-verified.
- **Every rig this round adds reaps its children in `t.Cleanup`**: the remotegw fence closes the
  watcher (which joins every peek goroutine) and reuses the wave's own `serveR8Remote`, whose
  server is closed in cleanup; the protocol fence uses `serveRemoteAPISrv`/`rawDial`, both of
  which close in cleanup.
- **Process / PTY counts, measured rather than asserted.** The closing review reported 108
  matching processes / 75 PTYs at the end of its run. After this round's two whole-repo gate
  runs, five mutations and the two Gradle runs: **124 matching processes / 83 PTYs** (first
  measurement this round, taken mid-run: 122 / 82). Net ≈ **+16 processes / +8 PTYs**, in the
  same cohorts the review named — `swarm-fake-agent` from one `swsk-bin` dir, and six each from
  several `swarm-daemon-bin` and `go-build .../daemon.test` temp dirs. **No baseline was taken
  at the very start of this round, and that is stated rather than back-filled.** The leak is in
  the pre-existing daemon/shim e2e rigs, not in anything this round added; it is bead
  `agents-tracker-ev0w`.

---

## 6. FOLLOW-UPS THIS ROUND DELIBERATELY DID NOT DO

1. **A sentence for a blanked screen.** After the fix the blank is transient — the screen
   re-watches — but if the re-watch is refused the user sees an empty grid with no explanation.
   New UI copy is product surface, not a fix, so it is filed rather than smuggled in.
2. **A durable view epoch.** `viewEpochSeq` still restarts in every daemon process. Reading
   `reset` closes the defect; persisting the counter would be a second way to say the same
   thing.
3. **The parked control half.** Unchanged, and still gated on ADR-017 C1 (per-frame device
   binding) and C2 (a replacement-notification seam).
4. **A stated bound on the blank discriminator, recorded rather than hidden.** The phone reads
   "the machine stopped rendering" off a view with **no geometry**. A real render never has
   none — `RenderTerminalView` falls back to 80x24 and otherwise carries the stream's own
   geometry (`internal/daemon/terminalrender.go:34-35`), which is what
   `internal/remotegw/r8r5_reapblank_test.go` asserts on the live frame. The one input that
   could collide is a stream whose INITIAL snapshot declares `0x0`: its views would read as
   blanks and the screen would re-watch on each redraw instead of drawing an (empty) grid. That
   costs one relay append per redraw while such a session is on the glass, it is bounded by the
   screen being open, and such a session renders nothing either way — so it is recorded here as
   a known bound rather than defended against with a wire field the facade does not carry.

---

## 7. POST-WORKFLOW ORCHESTRATOR FIX (closing round 2's own blocking finding)

The round-2 closing review rejected on one blocker: the C6 degrade-on-read guard covered ONE
of Validate's two boolean clauses (`terminal_control && !terminal_fallback`), while its KDoc
and ADR-017 C6 claimed both. The mutual-exclusion shapes still laundered:
`{structured_chat:true, terminal_fallback:true, terminal_control:true}` read back as the
valid `{false, true, true}` granting `AllowsTerminalControl()` — the reviewer proved it with
an executable probe against a real Daemon and a real capabilities.json.

Fixed by the orchestrator, TDD order, 2026-08-21:

1. **RED** — `TestR8R4Capability_ADegradeOnReadNeverLaundersAnInvalidRecordIntoAGrant`
   extended from one invalid shape to all three (`control-without-fallback`,
   `mutual-exclusion-with-control`, `mutual-exclusion-watch-only`), each self-checked
   invalid via `Validate()` before use. Run on the unfixed guard: FAIL on
   `mutual-exclusion-with-control` with `AllowsTerminalControl()` granted — the reviewer's
   exact probe result.
2. **GREEN** — `internal/skeleton/capability.go`: the guard is now
   `(c.StructuredChat && c.TerminalFallback) || (c.TerminalControl && !c.TerminalFallback)`,
   both boolean clauses written as `Validate` writes them. Fence green.
3. **MUTATION** — the mutual-exclusion arm removed, fence FAILS; file restored and
   byte-verified with `cmp`; fence green again.
4. ADR-017 C6 amended in place: it now records that the first fix covered one clause and
   which round closed the rest. The KDoc's "checks Validate's TWO BOOLEAN CLAUSES" sentence
   is now true as written.

The round-2 review's two non-blocking findings are filed, not fixed: agents-tracker-65bj
(relay seam wire-key pins, P1) and agents-tracker-folg (read-verb wrap evasion of the
routing fence). Round 1's coverage beads: agents-tracker-k99y (lapse-predicate JVM table),
agents-tracker-46y7 (streamStale facade fence).

Gate table for the final tree (orchestrator run, 2026-08-21):

| Gate | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0 |
| `golangci-lint run` (v2.12.2, = CI) | 0 issues |
| `go test -race -count=1` (owned packages) | exit 0 |
| `go test -count=1 ./...` — THE gate, whole repo | exit 0 (06:58-07:13Z) |

Machine hygiene during the run: 168 leaked rig processes / 105 PTYs found before the run
(the closing workflow's own test leakage, bead agents-tracker-ev0w's class); 90 ppid==1
orphans reaped by explicit pid mid-run, PTYs 105 -> 27; the gate run itself left 42 more,
reaped after commit. No Kotlin changed after the round-2 fix agent's Gradle run, so its
Android lane result (0 failures, 0 errors, XML-counted) stands for this tree.
