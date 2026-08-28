# Phone refit W7: Inbox, Activity, Settings, Computers (verification evidence)

Bead `agents-tracker-d45a.7`. Contract: `docs/specifications/phone-refit-playbook.md` §8
(W7.1-W7.6). Worktree `refit-w7`, branch `refit/w7`, branched from `main` at `1a0e7b29`. Each item
below records the RED run (tests written first, exact failure text), the GREEN run, and one
negative control per behavioural change (the fix perturbed back, the test shown failing, the file
restored with `git checkout --`).

Environment: go1.26.5 darwin/amd64, golangci-lint 2.12.2 (matches `.github/workflows/ci.yml`).
Gradle ran from the serialised lane script (`pgrep -f gradle-wrapper.jar` empty before start; start
stamped; every result XML checked newer than the stamp; `app/libs/swarm.aar` mtime checked
unchanged across the run). The machine was shared with three other fleets and their Gradle runs.

## Scope finding, recorded before any file was touched

The contract lists `mobile/types.go` and `mobile/app.go` as the Go change ("two additive fields").
Neither field can be sourced there: `schema.JournalRecord` carried no timestamp and no
last-activity stamp, `phonecore.CachedSession` had no stamp, `journal.Record.TS` (stamped by
`Journal.Append`) was dropped by `toWireJournalRecordWith`, and roster records (never appended)
carried nothing from `persist.Meta.LastActivity`. With only the two mobile files changed both
fields would always be 0. A phone-clock substitute was rejected: it violates "the machine's stamp"
and "twins show different ages". Reported to the orchestrator before starting; the carriage below
is additive and modelled exactly on the Name/Agent carriage that already runs end to end.

Files beyond the contract's list, all for that carriage: `internal/journal/journal.go`,
`internal/daemon/journal.go`, `internal/protocol/schema/remote.go`, `internal/skeleton/api.go`,
`internal/phonecore/journal.go`, `mobile/relay.go`, `docs/specifications/protocol.md` (the
`JournalRecord` table, a procedural obligation named there), `mobile/testdata/exported_surface.golden`
(PB-BIND-7, regenerated with `-update-surface`; the diff is exactly the two fields), and their
tests. Two further deviations, each argued at the site:

- `navHeaderDrill` (`ui/kit/NavHeaderDrill.kt`) gains the same `trailing: View? = null` as
  `navHeader`: the Computers screen uses the drill header, not `navHeader`, so the contract's
  "MachinesPanelView.kt:100 keeps compiling" could only be honoured by giving that header the slot.
- `android/gate/s24_screens_test.go`'s kit-reach inventory for `MachinesPanelView.kt` names
  `overflowControl` and `conversationMenu` where it named `denyChip`: the Forget control moved
  into the row menu, which is the contract's own composition.

Interpretations recorded so they are on the record: (1) W7.4's "untitled" trailing section keeps
the existing `Journal` heading, so "no day heading" and "an all-zero page renders as today" both
hold and `ActivityPanelViewTest` stays untouched (W5 removes that word per its own table);
(2) `brokenNotice` stays under its row (a fault is about the row, not the form) and only
`ADD_LIMITS` folds into the form block; (3) the header action's word `Add` is typed in the view,
because the model (`MachinesPanelScreen.kt`) is frozen this wave and the action is chrome that
reveals the form whose own CTA still spends `ADD_LABEL`; (4) an empty need token renders the
Group's own word, so `KitTag.NEED` never renders an empty string; (5) the two new Kotlin model
fields (`SessionRow.lastActivityUnixMs`, `JournalRow.tsUnixMs`) have NO default, by
`SessionRow.agent`'s documented rule, so seven and four test fixtures gained one `= 0L` line each
(`TriageInboxTest`, `HumanNamesTest`, `PhoneScaffoldViewTest`, `ScreenAirSweepTest`,
`NeedVocabularyTest`, `MachineAndLaunchTest`, and the wave's own files).

Tests that pinned exactly the behaviour W7 replaces, rewritten to the new contract (none is in the
contract's untouched list): `TriageInboxViewTest` `every group renders a heading, in the model's
order, empty or not` and `an empty section renders its own copy under its own heading` (replaced by
the two W7.2 tests) and the empty-block count in `a section with rows renders one row per session
and no empty block` (4 - 1 became 1); `ActivityPanelTest` `each row is its record's SessionID, Type
and Group, verbatim` (became `each row is its session and the W5 word`), `the emphasis is the
session, then the Group, then nothing` (became `..., or nothing`: the Group is folded into the word,
so a Group emphasis would name a span the body no longer holds, which `activityRow` refuses), and
`the panel renders one section, because nothing supports the mock's two` (retitled `one section per
day`, as the contract lists); `SettingsPanelConnectionViewTest` `the section carries its own heading,
above the row` (became the new order); `TriageInboxScreenTest` `a row carries the wire's own words
and invents nothing` narrowed to `scope = "mbp"` so its verbatim-token assertion is not joined by the
All scope's machine suffix. One test of this wave was corrected after its RED for a fence the
codebase carries (PB-DS-9, no `visibility =` in a screen): `the add form is hidden until the header
action is pressed` now defines "on screen" as composed under the root, and the round-2/3 position
assertions press Add first, as the contract says they change.

Untouched negative controls, byte-identical against `1a0e7b29` (`git diff --stat 1a0e7b29 HEAD --`
on each prints nothing): `SettingsPanelScreenTest.kt` (the proof W7.5 is view-only),
`SettingsSurfaceReplaceTest.kt`, `MachinesPanelScreenTest.kt`, `NavHeaderDrillTest.kt`, the models
`SettingsPanel.kt` and `MachinesPanelScreen.kt`, and `ActivityPanelTest`'s `no row renders its
cursor, because a cursor is not a time` (the function body hashes identically in both revisions:
`a7f16655f32e2825`). All pass in the final run.

## Commits

```
dc1bf6d8 Carry the record stamp and the session's last activity to the phone   (Go, W7.1/W7.4)
cea399bc Computers: Add behind the header action, Forget behind the row menu    (W7.6)
092ea64d Settings: computer first, destructive last                             (W7.5)
35444177 Inbox: empty sections collapse, except Needs you                       (W7.2)
8c82216c Inbox: every row's second line says state and age                      (W7.1 Kotlin)
eaf1c9bd Activity: by day, with a time, tappable                                (W7.4 Kotlin)
3ef888a1 Pin the bytes an unstamped record wrote before the stamps existed      (ruling, constraint 2)
86fda2f3 The Inbox age is time in the current state, not time since launch     (review SHOULD-FIX 2)
086242fa Computers: the open Add form survives a redraw                         (review SHOULD-FIX 1)
```

W7.3 lands nothing, as the contract says: the launch panel keeps its place under the list.

## AAR rebuild (the one legitimate AAR move; the orchestrator must rebuild again at merge)

`. android/toolchain.env && android/build-aar.sh` once, after the Go carriage was green, at
`dc1bf6d8`:

```
aar before: mtime=1787894316 Aug 28 07:18:36 2026 size=12650903 sha256=087e68ce852f2c4e... at 2026-08-28T05:49:30Z head=dc1bf6d8
building .../refit-w7/android/app/libs/swarm.aar for arm64-v8a x86_64 (androidapi 21)
build-aar exit=0
aar after:  mtime=1787896202 Aug 28 07:50:02 2026 size=12657610 sha256=39b06bdc2bb820f6... at 2026-08-28T05:50:02Z
```

Kotlin run 1 finished at 05:47:03Z against the old artifact (mtime 1787894316); runs 2-5 ran against
the new one (mtime 1787896202); every lane log records `aar unchanged` for its own span. Before the
rebuild `android/gate`'s PB-BIND-7 gate reported the two members `Session.getLastActivityUnixMs`
and `JournalEntry.getTSUnixMs` IN THE GOLDEN, NOT IN THE AAR; after it the package is green (below).

## Go carriage (serves W7.1 and W7.4)

Tests written first: `internal/protocol/schema/stamps_test.go` (`TestJournalRecordOmitsZeroStamps`,
`TestJournalRecordRoundTripsStamps`), `internal/skeleton/stampwire_test.go`
(`TestWireJournalRecordCarriesTS`, `TestWireJournalRecordCarriesLastActivity`,
`TestWireJournalRecordInventsNoStamps`), `internal/daemon/stamprecord_test.go`
(`TestJournalRecordForCarriesLastActivity` x4 transitions, `TestRosterSnapshotCarriesLastActivity`),
`internal/phonecore/stampfold_test.go` (`TestCacheAppliesLastActivityVerbatim`),
`mobile/stampfacade_test.go` (`TestSessionCarriesLastActivityUnixMs` -- "a zero LastActivity crosses
as 0", `TestJournalEntryCarriesTSUnixMs`).

### RED

```
## ./internal/protocol/schema
internal/protocol/schema/stamps_test.go:32:85: unknown field TS in struct literal of type JournalRecord
internal/protocol/schema/stamps_test.go:32:93: unknown field LastActivity in struct literal of type JournalRecord
FAIL	github.com/Nathandela/swarm/internal/protocol/schema [build failed]
## ./internal/skeleton
internal/skeleton/stampwire_test.go:24:10: got.TS undefined (type protocol.JournalRecord has no field or method TS)
internal/skeleton/stampwire_test.go:35:3: unknown field LastActivity in struct literal of type journal.Record
FAIL	github.com/Nathandela/swarm/internal/skeleton [build failed]
## ./internal/daemon
internal/daemon/stamprecord_test.go:44:12: rec.LastActivity undefined (type journal.Record has no field or method LastActivity)
FAIL	github.com/Nathandela/swarm/internal/daemon [build failed]
## ./internal/phonecore
internal/phonecore/stampfold_test.go:21:33: cs.LastActivity undefined (type CachedSession has no field or method LastActivity)
FAIL	github.com/Nathandela/swarm/internal/phonecore [build failed]
## ./mobile
mobile/stampfacade_test.go:20:7: s.LastActivityUnixMs undefined (type Session has no field or method LastActivityUnixMs)
mobile/stampfacade_test.go:45:13: stamped.TSUnixMs undefined (type *JournalEntry has no field or method TSUnixMs)
FAIL	github.com/Nathandela/swarm/mobile [build failed]
```

All five packages fail to compile on the missing fields (the right reason: no implementation).
One fixture-discipline gate caught the first GREEN attempt -- `TestS10_NoTestFixtureStampsANonzeroRosterCursor`:
`internal/protocol/schema/stamps_test.go:32 (cursor 3)` on a roster record -- and the fixture was
corrected (cursor removed), not the gate.

### GREEN

```
--- PASS: TestJournalRecordOmitsZeroStamps (0.00s)
--- PASS: TestJournalRecordRoundTripsStamps (0.01s)
ok  	github.com/Nathandela/swarm/internal/protocol/schema	1.769s
--- PASS: TestWireJournalRecordCarriesTS (0.00s)
--- PASS: TestWireJournalRecordCarriesLastActivity (0.00s)
--- PASS: TestWireJournalRecordInventsNoStamps (0.00s)
ok  	github.com/Nathandela/swarm/internal/skeleton	8.295s
--- PASS: TestJournalRecordForCarriesLastActivity (0.00s)   (launched, exited, lost, group_transition)
--- PASS: TestRosterSnapshotCarriesLastActivity (0.03s)
ok  	github.com/Nathandela/swarm/internal/daemon	4.581s
--- PASS: TestCacheAppliesLastActivityVerbatim (0.00s)
ok  	github.com/Nathandela/swarm/internal/phonecore	5.836s
--- PASS: TestSessionCarriesLastActivityUnixMs (0.00s)
--- PASS: TestJournalEntryCarriesTSUnixMs (0.02s)
ok  	github.com/Nathandela/swarm/mobile	2.714s
```

Golden: `go test ./mobile -run TestPBBIND7_ExportedSurfaceMatchesTheGolden` FAIL before,
regenerated with `-update-surface`, ok after; diff is `+field JournalEntry.TSUnixMs int64` and
`+field Session.LastActivityUnixMs int64`.

### Negative controls (clean tree at eaf1c9bd; each file restored with `git checkout --`, 0 dirty after)

```
## Negative control Go-1: the roster record's LastActivity line deleted (internal/daemon/journal.go)
=== RUN   TestRosterSnapshotCarriesLastActivity
    stamprecord_test.go:67: roster record for stamped carries LastActivity 0001-01-01 00:00:00 +0000 UTC; want 2026-08-28 09:34:00 +0000 UTC -- the roster is the only path by which a reconnected session reaches the phone, so it must carry the stamp (W7.1)
--- FAIL: TestRosterSnapshotCarriesLastActivity (0.04s)
## Negative control Go-2: the facade stamps the journal entry 0 (mobile/relay.go, TSUnixMs: 0)
=== RUN   TestJournalEntryCarriesTSUnixMs
    stampfacade_test.go:46: JournalEntry.TSUnixMs = 0; want 1787909880000, the daemon's own record stamp (W7.4)
--- FAIL: TestJournalEntryCarriesTSUnixMs (0.02s)
```

(A first attempt at Go-1 substituted `time.Time{}` and failed to BUILD -- that file does not import
`time` -- so it was discarded as invalid and redone by deleting the line.)

### Orchestrator ruling (received after the branch was first pushed): byte identity as a control

The ruling accepted the carriage and asked for three things. (1) One Go commit of its own with
its own RED and the golden's diff shown: `dc1bf6d8`, above. (2) Byte identity as a NEGATIVE
CONTROL: one test that a record with zero stamps serialises to exactly the bytes it did before,
and one that a persisted phonecore cache without the field still loads -- the ruling asked for
these in the same commit, and they are in `3ef888a1` instead, because `dc1bf6d8` had already been
pushed under six later commits when the ruling arrived and this branch is not rewritten.
(3) Nothing non-additive: no field renamed or reordered, no existing JSON key changed, no
behaviour change when the stamps are absent (the pins below are that proof); `mobile/relay.go` was
touched for exactly one line, `TSUnixMs: unixMs(rec.TS)` in `onJournal`, because that is the only
site where a wire record becomes a facade `JournalEntry`.

The pinned literals were MEASURED, not typed: a detached worktree of `1a0e7b29` ran a throwaway
`go run` that marshalled the three shapes (`w7-base-bytes.txt` in the scratchpad):

```
wire:    {"cursor":3,"session_id":"m/s1","type":"launched","group":"working","agent":"claude","name":"api"}
journal: {"schema_version":1,"cursor":3,"ts":"0001-01-01T00:00:00Z","session_id":"m/s1","type":"launched","group":"working","agent":"claude","name":"api"}
cache:   {"SessionID":"m/s1","Group":"working","Agent":"claude","Name":"api","Present":true,"Capabilities":null}
```

(The journal line's zero `ts` is the pre-existing `TS` field's own behaviour, untouched; what the
pin holds is that `last_activity` is absent.) Tests, all green on the final tree:
`TestJournalRecordUnstampedBytesAreUnchanged` (schema), `TestRecordUnstampedLineIsUnchanged`
(journal), `TestCachedSessionUnstampedBytesAreUnchanged` and
`TestPersistedCacheWithoutTheStampStillLoads` (phonecore; the second decodes the base literal inside
the `sessions` container and finds every field and a zero stamp).

Negative control Go-3 (`omitzero` stripped from the three new tags, then `git checkout --`, 0 dirty):

```
--- FAIL: TestJournalRecordUnstampedBytesAreUnchanged (0.01s)
    stamps_test.go:62: an unstamped record serialises as
          {"cursor":3,...,"name":"api","ts":"0001-01-01T00:00:00Z","last_activity":"0001-01-01T00:00:00Z"}
        want the bytes main@1a0e7b29 wrote
          {"cursor":3,"session_id":"m/s1","type":"launched","group":"working","agent":"claude","name":"api"}
--- FAIL: TestRecordUnstampedLineIsUnchanged (0.01s)
    stampbytes_test.go:20: ... ,"name":"api","last_activity":"0001-01-01T00:00:00Z"}  want ... ,"name":"api"}
--- FAIL: TestCachedSessionUnstampedBytesAreUnchanged (0.01s)
    stampfold_test.go:54: ... ,"Capabilities":null,"last_activity":"0001-01-01T00:00:00Z"}  want ... ,"Capabilities":null}
```

Gates after `3ef888a1`: `go build` exit=0, `go vet` exit=0, `golangci-lint` 0 issues; `go test -race`
on schema, journal and phonecore ok. The commit changes no exported surface, so the AAR built at
`dc1bf6d8` is still the artifact PB-BIND-7 measures against, unchanged.

## Kotlin runs

Five lane runs. Filters as listed; `--rerun-tasks --no-build-cache` on each.

| run | purpose | classes | result |
|---|---|---|---|
| 1 | RED, behavioural (W7.2, W7.5) | TriageInboxViewTest, SettingsPanelViewTest, SettingsPanelMachinesEntryTest, SettingsPanelConnectionViewTest | `gradle exit=1`, `tests=45 failures=7`, aar unchanged (1787894316), xml fresh 4/4 |
| 2 | RED, compile (W7.6, W7.1, W7.4: new API) | NavHeaderTest, MachinesPanelViewTest, TriageInboxScreenTest, NeedVocabularyTest, ActivityPanelTest, ActivityPanelViewTest | `gradle exit=1`, `compileDebugUnitTestKotlin` fails on the 22 unresolved members below; main compiled (W7.2/W7.5 already in) |
| 3 | GREEN, full suite | all | `gradle exit=0`, `tests=1614 failures=0 errors=0 skipped=0`, aar unchanged (1787896202), xml fresh 203/203 |
| 4 | GREEN, full suite after the PB-DS-9/PB-DS-6 fence fix | all | `gradle exit=0`, `tests=1614 failures=0 errors=0 skipped=0`, aar unchanged, xml fresh 203/203 |
| 5 | negative controls (six perturbations, one run) | the six classes | `tests=102 failures=17`, every failure attributable to its perturbation (below) |

Run 2's compile RED, one line per missing member (paths relative to `app/src/test/kotlin/dev/swarm/phone/`):

```
e: ui/kit/NavHeaderTest.kt:34:72 No parameter with name 'trailing' found.
e: ui/kit/NavHeaderTest.kt:49:62 No parameter with name 'trailing' found.
e: ui/screens/ActivityPanelTest.kt:52:51 No parameter with name 'nowUnixMs' found.
e: ui/screens/ActivityPanelTest.kt:55:86 No parameter with name 'tsUnixMs' found.
e: ui/screens/ActivityPanelTest.kt:203:78 Unresolved reference 'time'.
e: ui/screens/ActivityPanelViewTest.kt:60:9 No parameter with name 'onSelectSession' found.
e: ui/screens/ActivityPanelViewTest.kt:91:110 No parameter with name 'tsUnixMs' found.
e: ui/screens/MachinesPanelViewTest.kt:106:38 Unresolved reference 'ADD_TOGGLE'.
e: ui/screens/MachinesPanelViewTest.kt:150:38 Unresolved reference 'ROW_MENU'.
e: ui/screens/NeedVocabularyTest.kt:90:35 Cannot access 'fun needCopy(need: String, group: String): String': it is private in 'dev.swarm.phone.ui.screens.TriageInboxScreen'.
e: ui/screens/ScreenAirSweepTest.kt:643:61 No parameter with name 'sessionId' found.
e: ui/screens/TriageInboxScreenTest.kt:49:9 No parameter with name 'lastActivityUnixMs' found.
e: ui/screens/TriageInboxScreenTest.kt:66:9 No parameter with name 'nowUnixMs' found.
```

## W7.1 Every Inbox row's second line says state and age

Tests first: `TriageInboxScreenTest` `the need line is the state word and the age`, `an absent stamp
draws no age rather than the epoch`, `the All scope appends the machine`, `a row whose records
carried no need still says its state`, `twins show different ages`; `NeedVocabularyTest` keeps its
table, now against `TriageInboxScreen.needCopy`. RED: run 2 (`lastActivityUnixMs`, `nowUnixMs`,
`needCopy` private). GREEN: runs 3 and 4.

Negative control (run 5; `TriageInboxScreen.kt` with the age dropped from the line, `ageOf(...)` -> `""`):

```
FAILURE TriageInboxScreenTest > the need line is the state word and the age
    org.junit.ComparisonFailure: expected:<Working[ · 4m]> but was:<Working[]>
FAILURE TriageInboxScreenTest > twins show different ages
    java.lang.AssertionError: expected:<[Working · 4m, Working · 3h]> but was:<[Working, Working]>
FAILURE TriageInboxScreenTest > the All scope appends the machine
    org.junit.ComparisonFailure: expected:<Working · [4m · ]Nathan's MBP> but was:<Working · []Nathan's MBP>
```

Done when: `KitTag.NEED` renders the state word always (an empty token falls back to the Group's word,
an unknown token stays verbatim by the existing rule), an age only from a non-zero stamp, and twins
show different ages (`Working · 4m` / `Working · 3h`).

## W7.2 Empty sections collapse, except Needs you

Tests first: `TriageInboxViewTest` `an empty non-blocked section draws no heading at all`, `an empty
needs-you still says Nothing waiting`; `TriageInboxScreenTest:135` retitled `an empty needs-you
section is still a section and says Nothing waiting`; `:161` (four sections in the model) untouched.

RED (run 1):

```
FAILURE TriageInboxViewTest > an empty non-blocked section draws no heading at all
    java.lang.AssertionError: an empty Working, Ready for review or Done section still draws its heading (and a caption under it), so a one-session inbox is three headings over nothing (phone-refit-playbook W7.2) expected:<[Needs you]> but was:<[Needs you, Working, Ready for review, Done]>
FAILURE TriageInboxViewTest > an empty needs-you still says Nothing waiting
    java.lang.AssertionError: the empty Needs you section collapsed with the others. ... expected:<[Needs you, Working]> but was:<[Needs you, Working, Ready for review, Done]>
FAILURE TriageInboxViewTest > a section with rows renders one row per session and no empty block
    java.lang.AssertionError: ... Only the empty Needs you section says so expected:<1> but was:<3>
```

GREEN: runs 3 and 4. Negative control (run 5; the guard line removed from `TriageInboxView.kt`): the
same three failures with the same text (`expected:<[Needs you]> but was:<[Needs you, Working, Ready
for review, Done]>`, `expected:<1> but was:<3>`).

## W7.3 New session stays where it is

Nothing lands. The launch panel keeps its place under the list; `approvalSlot()` and its
`indexOfChild(launchHost)` anchor are untouched (`PhoneSurface.kt` changed by 4 lines, all W7.4's
Activity tap wiring).

## W7.4 Activity: by day, with a time, tappable

Tests first: `ActivityPanelTest` `rows are grouped by day newest day first`, `an absent stamp
renders no time and no day heading`, `a stamped row carries its time`, `zero-stamp rows trail the
stamped days`, `one section per day` (`:55` retitled), `each row is its session and the W5 word`;
`:82` `no row renders its cursor` untouched; `ActivityPanelViewTest` `a row opens its session`,
`a stamped row draws its time under its day heading`. RED: run 2 (`tsUnixMs`, `nowUnixMs`, `time`,
`onSelectSession`). GREEN: runs 3 and 4.

Negative control (run 5; `ActivityPanel.kt` with no row counted as stamped, `partition { false }`):

```
FAILURE ActivityPanelTest > rows are grouped by day newest day first
    java.lang.AssertionError: expected:<[Today, Yesterday]> but was:<[Journal]>
FAILURE ActivityPanelTest > one section per day
    java.lang.AssertionError: three records stamped on one day were split into [Journal] expected:<[Today]> but was:<[Journal]>
FAILURE ActivityPanelTest > zero-stamp rows trail the stamped days
    java.lang.AssertionError: expected:<2> but was:<1>
```

Done when: `cursor` appears nowhere on screen (`:82` green, the time is a cell and never in the
body); an all-zero page renders as it does today (one `Journal` section, no times, no day heading).

## W7.5 Settings: computer first, destructive last

Tests first: `SettingsPanelViewTest` `the section order is computer, remote access, notifications,
replace` (tag sequence) and `a working switch draws no remote access block between the card and the
switches`; `SettingsPanelMachinesEntryTest` assertions moved onto the card's chevron
(`SettingsTag.MACHINES_ENTRY`, announcing `MachinesPanelScreen.ENTRY_LABEL`, after the card, fires
`onOpenMachines`; an unwired panel composes none); `SettingsPanelConnectionViewTest` counts one
status line (`KitTag.MACHINE_META` x1, a guard) and pins the new order. `SettingsPanelScreenTest:55`
untouched and green (view-only proof); `SettingsSurfaceReplaceTest` untouched and green.

RED (run 1):

```
FAILURE SettingsPanelViewTest > the section order is computer, remote access, notifications, replace
    java.lang.AssertionError: ... expected:<[settings.nav, settings.section.label, settings.connection.row, settings.machines.entry, settings.connection.remote, settings.section.label, settings.row, settings.row, settings.section.label, settings.machine.row, settings.machine.replace]> but was:<[settings.nav, settings.section.label, settings.machine.row, settings.machine...
FAILURE SettingsPanelMachinesEntryTest > theEntryComposesByItsRecordedNameAndFires
    java.lang.AssertionError: the chevron does not announce 'Computers'; a control that exists must be findable by its recorded name expected:<Computers> but was:<null>
FAILURE SettingsPanelConnectionViewTest > the computer card leads, under its own heading, and the pairing row trails
    java.lang.AssertionError: expected:<[settings.nav, settings.section.label, settings.connection.row, settings.section.label, settings.row, settings.row, settings.section.label, settings.machine.row]> but was:<[settings.nav, settings.section.label, settings.machine.row, settings.section.label, settings.connection.row, settings.section.label, settings.row, settings.row]>
```

GREEN: runs 3 and 4. Negative control (run 5; `SettingsPanelView.kt` with the pairing block moved
back above the computer card): `the section order is computer, remote access, notifications, replace`
and `a working switch draws no remote access block ...` fail with `but was:<[settings.nav,
settings.section.label, settings.machine.row, ...]>`.

## W7.6 Computers: Add behind the header, Forget behind the row

Tests first: `NavHeaderTest` (new) `a trailing action is drawn after the status slot`, `a null
trailing draws nothing`; `MachinesPanelViewTest` `the add form is hidden until the header action is
pressed`, `forget is not on the row, it is in the row menu`, `a healthy two-computer panel draws two
rows and nothing below them`; the two existing Forget tests open the row menu first; round-2/3
position assertions press Add first; destructiveness assertions unchanged (`filterTouchesWhenObscured`
asserted on the menu's Forget row). `MachinesPanelScreenTest` untouched and green. RED: run 2
(`trailing`, `ADD_TOGGLE`, `ROW_MENU`). GREEN: run 4 (run 3 was green on a first shape that hid the
form block with `visibility = GONE`; `android/gate` then failed PB-DS-9 "3 visibility writes in the
screen package" and PB-DS-6 "does not reach denyChip", so the block is now composed and removed by
the action, the gate's inventory names the menu, and run 4 is the GREEN of the shipped shape).

Negative controls (run 5; `NavHeader.kt` ignoring `trailing`; `MachinesPanelView.kt` with the Add
action composing nothing and the overflow opening nothing):

```
FAILURE NavHeaderTest > a trailing action is drawn after the status slot
    java.lang.AssertionError: the trailing action is not the LAST thing on the header row; it reads outward from the title -- counter, status, then the action (W7.6) expected same:<android.widget.TextView...> was not:<android.view.View...>
FAILURE MachinesPanelViewTest > the add form is hidden until the header action is pressed
    java.lang.AssertionError: pressing Add did not compose the form
FAILURE MachinesPanelViewTest > forget is not on the row, it is in the row menu
    java.lang.AssertionError: no view on the machines screen reads "Forget this computer" (found 0, wanted at least 1); a control that exists must be findable by its recorded copy
```

(`everyRowOffersForgetAndItNamesItsMachine`, `aBrokenRowStillOffersForget` and
`addComputerIsFindableByItsRecordedCopyAndFires` fail the same way under the same perturbation.)

Done when: a healthy two-computer panel draws two rows and nothing below them (asserted);
`MachinesPanelScreenTest` passes untouched.

## Gates on the final tree (eaf1c9bd)

```
go build ./...        build exit=0
go vet ./...          vet exit=0
golangci-lint run     0 issues.   lint exit=0
```

`go test -race -count=1 -timeout 40m ./...` (env -u SWARM_* prefix), started at `dc1bf6d8` plus the
uncommitted Kotlin tree, 61 packages ok, 7 with no test files, 3 FAIL:

- `android/gate`: `TestPBDS6_EveryRecomposedScreenIsBuiltOutOfTheKit`,
  `TestPBDS9_AScreenComposesWhatIsOnItRatherThanHidingWhatIsNot` -- real findings against W7.6's
  first shape, fixed as described above; `go test -race ./android/gate` afterwards:
  `ok  github.com/Nathandela/swarm/android/gate  93.957s` (PB-BIND-7 against the rebuilt AAR included).
- `internal/daemon`: `TestLaunch_InjectsHookEnvToAgent` (`SWARM_SESSION_ID="", want ...` beside
  `daemon: serve: daemon: another instance is already running`) and `internal/e2e`:
  `TestE2E_ReplayProductionPath_AgyOpencode` (`idle observed only 2.716s after the first active
  sample (want >= 3s)`) -- both load-timing failures under a concurrent full Gradle run, rerun once
  in isolation per the protocol: `--- PASS: TestLaunch_InjectsHookEnvToAgent (1.69s)`,
  `--- PASS: TestE2E_ReplayProductionPath_AgyOpencode (12.28s)`.

Kotlin, final: run 4, `tests=1614 failures=0 errors=0 skipped=0`, `gradle exit=0`, aar unchanged
(mtime 1787896202), 203/203 result files newer than the start stamp.

Lane logs and gate outputs are in the session scratchpad (`w7-run{1..5}-*.log`, `w7-go-*.txt`,
`w7-aar-rebuild.log`).

## Review round (two SHOULD-FIX; the third, the byte pins, was closed by 3ef888a1)

The sections above are the record as it stood at the first push and are left as written; the
field they call `LastActivity` is `StateSince` since `86fda2f3`, for the reason below.

### SHOULD-FIX 1 (W7.6 defect): the open Add form collapsed on the next redraw

`MachinesPanelView.kt` composed the form block only inside the header action's click, and
`drawMachines` (`PhoneSurface.kt`) rebuilds the view whenever the panel or the minute changes, so
the first event after a minute boundary while the user was typing removed the block (the fields
survived in `addComputerSlot()`, the block did not). Fixed as proposed and nothing more: `addFormOpen`
is held on the surface beside `addComputerSlot()`, passed into `machinesPanelView` (composed at draw
when true; the action flips it through `onToggleAddForm` and still composes or removes the block on
the current draw, so the press is seen at once), and `machinesAddFormDrawn` joins the redraw guard.

Test first: `MachinesPanelViewTest` `the open add form survives a redraw` -- opens the form, redraws
with the surface's state, asserts the block is composed; closes it, redraws, asserts it is not.

RED (run 6, lane script, `--tests MachinesPanelViewTest --tests TriageInboxScreenTest`;
`compileDebugUnitTestKotlin` fails, main compiled):

```
e: ui/screens/MachinesPanelViewTest.kt:384:13 No parameter with name 'addFormOpen' found.
e: ui/screens/MachinesPanelViewTest.kt:385:13 No parameter with name 'onToggleAddForm' found.
```

GREEN: run 7 (below). Negative control (run 8; the compose-at-draw line replaced by a no-op, the
file restored by re-applying the edit -- not by `git checkout`, since the fix was not yet committed):

```
FAILURE MachinesPanelViewTest > the open add form survives a redraw
    java.lang.AssertionError: the open add form collapsed on the next redraw; the block must be composed at draw from the surface's state, not only by the toggle's click
SUMMARY tests=13 failures=1 errors=0 skipped=0
```

### SHOULD-FIX 2 (ruling): the age measures time in the current state

`persist.Meta.LastActivity` is written only at launch (`launch.go:288`) and exit
(`reconcile.go:229-231`), so an age counted from it read "launched 3h ago" and twins launched
together aged identically. The stamp is now `next.EffectiveGroupEnteredAt()` (that method's own
fallbacks for pre-`GroupEnteredAt` records included) at the roster snapshot and the four
transitions, and named for what it is end to end, nothing having shipped under the old name:
`journal.Record.StateSince` (`state_since,omitzero`), `schema.JournalRecord.StateSince`
(`state_since`), phonecore `CachedSession.StateSince` (tag kept for the byte pins),
`Session.StateSinceUnixMs`, Kotlin `stateSinceUnixMs`; the protocol.md row, the golden and the
fixtures follow. `JournalEntry.TSUnixMs` is unaffected.

Tests first (renamed, plus the source-distinguishing case: `GroupEnteredAt` set and `LastActivity`
an hour earlier, so the two sources cannot be confused): `TestJournalRecordForCarriesStateSince`
(x4), `TestJournalRecordForStateSinceFallsBackAsTheMetaDoes`, `TestRosterSnapshotCarriesStateSince`,
`TestWireJournalRecordCarriesStateSince`, `TestCacheAppliesStateSinceVerbatim`,
`TestSessionCarriesStateSinceUnixMs`, the schema round trip; Kotlin fixtures renamed.

RED (Go, `w7-go-red-2.txt`; every package fails to compile on the missing field):

```
internal/protocol/schema/stamps_test.go:32:82: unknown field StateSince in struct literal of type JournalRecord
internal/skeleton/stampwire_test.go:35:3: unknown field StateSince in struct literal of type journal.Record
internal/daemon/stamprecord_test.go:50:12: rec.StateSince undefined (type journal.Record has no field or method StateSince)
internal/phonecore/stampfold_test.go:22:33: cs.StateSince undefined (type CachedSession has no field or method StateSince)
mobile/stampfacade_test.go:20:7: s.StateSinceUnixMs undefined (type Session has no field or method StateSinceUnixMs)
```

RED (Kotlin, run 6): `No parameter with name 'stateSinceUnixMs' found` at every `SessionRow`
fixture (`TriageInboxTest`, `HumanNamesTest` x2, `NeedVocabularyTest`, `PhoneScaffoldViewTest`,
`ScreenAirSweepTest`, `TriageInboxScreenTest`, `TriageInboxViewTest`).

GREEN (Go, `w7-go-green-2.txt`): all of the above PASS; the four byte pins still PASS unchanged
(their literals never carried the old key); golden: FAIL before, regenerated, ok after, diff
`-field Session.LastActivityUnixMs int64` / `+field Session.StateSinceUnixMs int64`.

Negative control Go-4 (the stamp sourced from `LastActivity` again at both daemon sites; the file
then RE-EDITED back, see the note below):

```
    stamprecord_test.go:51: launched record carries StateSince 2026-08-28 08:34:00 +0000 UTC; want 2026-08-28 09:34:00 +0000 UTC, next.EffectiveGroupEnteredAt() -- the age is time in the current state, not time since launch (W7.1 ruling)
    (the same for exited, lost, group_transition)
--- FAIL: TestJournalRecordForCarriesStateSince (0.01s)
    stamprecord_test.go:91: roster record for stamped carries StateSince 2026-08-28 08:34:00 +0000 UTC; want 2026-08-28 09:34:00 +0000 UTC
--- FAIL: TestRosterSnapshotCarriesStateSince (0.04s)
```

### AAR rebuild 2 (the facade field is renamed)

`android/build-aar.sh` once more, after the Go change was green and before the Kotlin run:

```
aar before: mtime=1787896202 Aug 28 07:50:02 2026 size=12657610 sha256=39b06bdc2bb820f6... at 2026-08-28T07:37:03Z
build-aar exit=0
aar after:  mtime=1787902643 Aug 28 09:37:23 2026 size=12657410 sha256=b1034cc0fb5b90c2... at 2026-08-28T07:37:24Z
```

Runs 6 (RED) ran against the previous artifact; runs 7 and 8 against the new one; every lane log
records `aar unchanged` for its own span. The orchestrator rebuilds again at merge.

### Gates on the final tree (086242fa)

```
go build ./...        build exit=0
go vet ./...          vet exit=0
golangci-lint run     0 issues.   lint exit=0
```

`go test -race -count=1 -timeout 40m ./...` (env -u SWARM_* prefix, concurrent with Kotlin run 7):
62 packages ok, 7 with no test files, 2 FAIL, both load-timing and both PASS rerun once in
isolation per the protocol: `internal/e2e` `TestE2E_ReplayProductionPath_AgyOpencode` (the same
replay-idle window as before; `--- PASS (15.93s)` alone) and `internal/skeleton`
`TestS18_ARevokeCarriesTheSIGNEDTargetAndNotTheSigner` (the package took 487s under the shared
load; `--- PASS (4.42s)` alone). Recorded honestly: the FIRST isolated rerun of S18 failed to build,
because negative control Go-4's `git checkout --` had restored `internal/daemon/journal.go` to
HEAD -- the pre-rename version, the StateSince edit being uncommitted at that moment. The edit was
re-applied (the file is byte-identical to the one the full run used, as the diff in `86fda2f3`
shows) and the rerun passed. Every later restore in this round was done by re-editing.

Kotlin, final: run 7, full suite, `tests=1615 failures=0 errors=0 skipped=0`, `gradle exit=0`, aar
unchanged (mtime 1787902643), 203/203 result files newer than the start stamp.

Lane note: before run 6 the lane script waited on a false positive -- another fleet's shell whose
own typed text contained the literal `gradle-wrapper.jar` (the self-match §1.7 warns about). The
script's check matched an actual Gradle JVM only for runs 6-8 (`pgrep -f 'java.*-jar .*gradle-wrapper\.jar'`)
and has since been aligned to the orchestrator's corrected rule, `pgrep -f '^/[^ ]*java .*gradle-wrapper[.]jar'`
in a script file and never in a typed command; nothing else about the lane discipline changed.

