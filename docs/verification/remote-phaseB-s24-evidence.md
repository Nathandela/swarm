# S24 evidence — the four screens, at HEAD `a7510fc`

**Requirements owned: PB-DS-9, PB-DS-11, PB-DS-12.** PB-DS-11 is shipped and already carries
its own evidence (`remote-phaseB-s24-red/`); PB-DS-9 and PB-DS-12 are **NOT MET** and this file
is written to say precisely what they are and are not. It also carries PB-APP-3's navigation
half, which is S16's row but landed here.

**Commits**: `5461ad1` (tab bar and three destinations), `7575ab2` (agent name reaches the row)
with `5f45f34` behind it on the Go side, `f2b5b4b` (session detail model), `4b4cde0` (session
detail view), `277be51` (the detail becomes reachable, Kill stops being a loose button).

**Written 2026-08-02.** Every claim below was checked by opening the file or running the
command, not inferred from a name. Where a claim could not be checked, it says so instead.

## What ran, and what did not

| | |
|---|---|
| `go build ./...` | green |
| `go vet ./internal/verify/ ./android/gate/` | green |
| `go test ./android/gate/` (whole package) | **green, 2026-08-02 at `a7510fc`** |
| `go test ./android/gate/ -run 'TestPBDS6\|TestPBDS9\|TestPBDS11\|TestPBAPP3'` | green — 20 tests |
| `./gradlew test` (the Kotlin lane) | **NOT RUN. See below — this is the largest gap in this file.** |

### The Kotlin assertions in this file were NOT executed for it

605 `@Test` methods exist across 78 files under `android/app/src/test`, and 75 of them are in
the nine files this slice's screens are tested by. **This file did not run any of them.** The
Android build lane was held by another agent while it was written, and `android/build-aar.sh`
overwrites the artifact every Kotlin compile links against, so running it would have
interfered with in-flight work.

**And no other evidence file covers them either.** The most recent Robolectric certificate is
`remote-phaseB-s23-head-evidence.md`, which names HEAD `5493de1`. **Twenty-one commits have
touched production Kotlin since that commit** — including every commit in this file's header.

That is the same defect `s23-head-evidence.md` was itself written to close: its opening
paragraph records that every prior certificate named `446f1cb` while nine commits had landed
since, one of which changed rendering. The hole has reopened at more than twice the size. So
the honest statement of the Kotlin lane at `a7510fc` is: **the tests exist, they are named
below, and nothing in the repository establishes that they pass at this commit.** Whoever runs
the lane next should write that certificate; it is not written here because it could not be.

## What landed

**The tab scaffold and navigation** (`5461ad1`). `ui/screens/PhoneScaffoldView.kt`, four
destinations behind one bar, with `TabBar.kt` gaining a tap handler. Tested by
`PhoneScaffoldViewTest` (8) and `PhoneSurfaceNavigationTest` (4). The failing-first run is
recorded in `remote-phaseB-s25-red/navigation-red.txt`, in three separate red runs because the
change had three failing halves and each had to be red for its own reason.

**The machines screen** (`5461ad1`, `7575ab2`). `ui/screens/MachinesPanel.kt` and
`MachinesPanelView.kt`, tested by `MachinesPanelTest` (11) and `MachinesPanelViewTest` (11).
Red run: `remote-phaseB-s25-red/machines-red.txt`. **It renders no machine data — see §Gaps.**

**The activity feed.** `ui/screens/ActivityPanel.kt` and `ActivityPanelView.kt`, tested by
`ActivityPanelTest` (9) and `ActivityPanelViewTest` (9), red run
`remote-phaseB-s25-red/activity-red.txt`. Built earlier and never evidenced until now, which
is why it appears here rather than in its own slice's file.

**The session detail** (`f2b5b4b`, `4b4cde0`, `277be51`). Model, then view, then reachability,
in that order and in three commits. `SessionDetailPanelTest` (8), `SessionDetailViewTest` (11).
`277be51` also moved Kill off the launch surface into the detail screen, removing a loose
control, and `TestPBAPP3_TheSessionDetailIsReachedFromTheApp` now passes — it was failing
earlier the same day, when `sessionDetailView` existed with no caller outside its own package.

**The agent span** (`7575ab2`, `5f45f34`). The Go side added `Session.Agent` to the facade and
carried it from the journal through `internal/phonecore` to the bound surface; the Kotlin side
threaded it into `FacadeBridge`, `TriageInbox`, `ui/kit/SessionRow.kt` and `MachinesPanel`.
`mobile/testdata/exported_surface.golden:56` carries `field Session.Agent string`, and
`android/gate/aarsurface_test.go` (added the same day) now fails if the built AAR disagrees
with that golden — which is what made the day's stale-artifact incident visible.

## Gaps — read this section as carefully as the one above

This is not a sales document. Each of the following is a real limit on what the four screens
do today, and each is worse to discover from a running build than from this file.

**1. The Machines tab renders NO machine data.** It shows an empty state that says so. Two of
`MachinePane`'s fields have no honest source on this handset: `presence` is `App.Presence`, a
**blocking relay round-trip**, and this surface's `render()` is driven by the event stream — so
calling it per redraw would issue one relay RPC per journal record. `android/unbound-verbs.tsv`
line 52 records that as a decision rather than an omission. `pairedDeviceName` has **no bound
accessor at all**: the exported surface has no such method or field. Neither may be invented
(ADR-007 B135). The copy at `MachinesPanel.kt:199` deliberately says what is true of the phone
— that it cannot read the details — rather than anything about the machine, because "no
machines" and "your machine is unreachable" are both claims this handset is in no position to
make. Tracked by **agents-tracker-xtj** (P1, open).

**2. Opening a session with no snapshot yet CRASHES THE APP.** `mobile/app.go:815` answers
`App.Peek` with `classed(ErrClassNotFound, ...)` when the router holds no snapshot;
`FacadeBridge.terminalPeek` did not catch it, and `PhoneEvents` dispatches redraws through
`main.post { }`, so the refusal arrives as an uncaught exception on the main looper. **"No
frame has arrived yet" is the normal state, not an edge case** — every session is in it for the
whole round trip after `terminalWatch`. Tracked by **agents-tracker-9ds** (P1, open), being
fixed as this was written. **Nothing in this file should be read as saying the detail screen
works.**

**3. The session detail has no composer and no undelivered-input ledger.** Deliberately, and
nothing composer-shaped is drawn in the meantime — `SessionDetailView.kt:94` records that an
empty bar at the bottom would promise an affordance that does not exist. The two ship together
or not at all, on **agents-tracker-hxv** (P1, open). The loose typed/send controls stay where
they are until then.

**4. No test asserts that `FacadeBridge` maps anything.** `swarmmobile.Session` is a gomobile
class backed by cross-compiled `.so` files and **cannot be constructed on the unit-test JVM**,
so the bridge's field mappings — `agent`, `group`, `need`, `present`, `title` — are checked by
**compilation alone**. The 605-test suite proves the screens, the models and the kit; about the
bridge it proves only that it compiles against whatever AAR is on disk. See §Findings for the
tracking problem this has.

**5. Quick-reply chips and tool cards are unbuilt**, because no verb backs them. The IME commit
path is **agents-tracker-76j** (P2, open): only the clipboard half of `App.Paste` ships.

**6. Derivation row 9's blur is impossible at `minSdk 33`** — `RenderEffect` blurs a view's own
content, not what is behind it — which is the second site of the limitation on
**agents-tracker-dw8** (P2, open), the first being the tab bar.

**7. None of these three requirements is DERIVED.** No one has broken these screens' fences on
purpose and watched them fail. The red captures under `remote-phaseB-s25-red/` are
failing-first records, which is a different and weaker claim: they show the tests failed before
the code existed, not that they still fail when the code is broken.

## Findings this file adds

**The FacadeBridge gap has no open tracker.** Gap 4 above is recorded only in the notes and
close reason of **agents-tracker-1wb, which is CLOSED** — closed correctly, because the
artifact-vs-source half it was filed for was fixed in `fba1964`. Its close reason ends by
noting that the other half "remains unverified". A gap recorded on a closed issue appears in no
"what is left" view, so it will not reach the committee through the tracker. It needs its own
issue.

**A `FacadeBridgeTest.kt` now exists and does not close gap 4.** It was uncommitted and
in-flight while this was written. It is a failing-first fence for agents-tracker-9ds, and its
own header states that it cannot construct `swarmmobile.App` and that no assertion in it calls
`terminalPeek` — it tests the error-class interpretation seam, which is pure Kotlin. That is
the right scope for it. It is named here so that its existence is not later mistaken for
coverage of the mappings.

**The red captures are filed under `s25-red/` and no S25 slice exists.** The three files name
themselves "S25" in their headings, but `remote-phaseB-manifest.tsv` assigns no requirement to
an S25 — the work satisfies S24's rows. The captures are cited above by their real paths so a
reader can find them; the naming is recorded here rather than corrected, because renaming
checked-in evidence would break the citations already pointing at it.

## What this evidence does NOT establish

Every Go assertion above is over checked-in source or the build gate; **no assertion here is
over rendered pixels.** There is no screenshot corpus, no emulator run and no device run — as
the S22 and S23 files also state, `PB-KEY-8` refuses a software-backed keystore and fails
closed before any screen renders. A screen can satisfy every test named here and be unusable,
and gap 2 is a live instance of exactly that: the detail screen's suite is green and opening a
session crashes the app.
