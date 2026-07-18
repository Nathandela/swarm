# CLI Duo Adapters (agy + opencode) — Evidence

**Epic**: agy + opencode adapter integration (issue agents-tracker-5gv), plan
`.claude/tmp/cli-duo-implementation-plan.md` v6.
**Status**: Phase B (characterization fixtures) complete. This file grows one
section per phase; later phases append rather than replace.

## Phase B: fixture provenance and marker memo

### Provenance

Both committed fixtures are **promotions** of the strategy-phase exploration
captures under `docs/verification/cli-trio-exploration/` (`fx_agy.json`,
`fx_opencode.json`), per R-B1's PREFER-PROMOTION rule: they are valid 100x30
`turn`-scenario captures recorded with `swarm-char -adapter reference`
(45s bound) against agy 1.1.4 and opencode 1.17.9, and they pre-date the
adapter packages (T-6 satisfied). All R-B2 verification checks below passed
on both captures, so **no re-recording was needed** and the opencode
auto-updater-disable mechanism was not exercised this phase (see the
deferred note at the end of this section for when it will be).

Sanitization (R-B3): the agy capture renders the operator's Google account
email in its TUI header. The literal 27-byte ASCII string
`nathan.delacretaz@gmail.com` was replaced at its exact 2 raw-byte offsets —
**1760** and **7929** — with the same-length (27-byte) dummy
`user.sanitized1@example.com`, leaving every other byte, and all control
sequences, untouched (a pure substring swap inside a plain-text SGR-colored
run; the replacement never crosses an escape-sequence boundary). A broader
scan of both raw captures for email-shaped strings, API-key/token shapes
(`sk-`, `ghp_`, `AKIA`, JWT-shaped triples, `Bearer <token>`) and the words
`token`/`password`/`secret` found nothing else to redact — the only other
`token` hits in agy are the literal UI string "Thought for 3s, 276 tokens"
(a token *count*, not a credential). The opencode capture contains **no**
email or credential-shaped strings at all — nothing was sanitized in it.
Home-path strings (`~/Code/swarm/...`) were left intact per repo convention.

All R-B2 checks were re-run on the sanitized bytes (not just the source
captures) and produced byte-identical results, because both replacement
sites (1760, 7929) fall outside every measured window (busy window, idle
frame, submit marker, exit screen) for agy, and opencode had zero
replacements. See "Sanitized-fixture re-verification" below.

Committed fixtures (schema_version 1, scenario `"turn"`, no `hook_payloads`
— both CLIs are heuristic-only in this phase):
- `internal/adapter/agy/testdata/agy.json` — cli `"agy"`, version `"1.1.4"`,
  10092 bytes.
- `internal/adapter/opencode/testdata/opencode.json` — cli `"opencode"`,
  version `"1.17.9"`, 76281 bytes.

Both load and pass `adapter.Fixture.Validate()` via
`fixtureio.LoadFixture` (verified directly, not just by construction).

### Frozen marker table (binding contract for Phases C/D/E/G)

| | agy | opencode |
|---|---|---|
| Busy markers (union, any-match) | `"esc to cancel"` (persistent footer hint), `"Generating..."` (spinner label) | `"esc interrupt"` |
| Idle rule | `idle-line-equals` value `">"`, border line immediately below, bottom-3/bottom-6 windows, braille-suppressed | **none** — R-B2b/c could not be jointly satisfied with a stable idle substring within this phase's scope; opencode reports `Signals: [heuristic]` with a busy rule only, `unknown` at rest (honest T-4 outcome) |
| Conversation-id marker | `"agy --conversation="` + UUID (8-4-4-4-12 lowercase hex), terminator byte `<=0x20 \| ==0x7f \| >=0x80` | `"ses_"` (left word boundary: preceding byte non-alnum) + `>=10` alnum chars, same terminator rule |
| Bottom-region scan | "content lines" = non-blank rows after right-trim, scanned upward from the last grid row, same discipline as `lastContentLine` in `internal/engine/heuristic.go` | same |

### Byte-granularity replay methodology

A throwaway Go analysis program (built as a temporary `cmd/zzphaseb-analysis`
package inside this module — internal-package import rules block a
separate-module approach even with a `replace` directive — deleted before
this phase closed) fed each fixture's `pty_capture` through the REAL
`internal/vt` emulator (`vt.NewEmulator(100, 30)`), byte by byte, decoding a
snapshot after every fed byte and evaluating the bottom-6/bottom-3 content
lines exactly as R-C1 specifies. agy (10092 bytes) was replayed at full byte
granularity directly. opencode (76281 bytes) used a coarse pass (16-byte
steps) to locate transitions cheaply, each transition then refined to its
exact byte via a bounded byte-by-byte re-scan of just its containing
16-byte chunk (an early version of this refine step had a bug that windowed
against the *previous transition's* offset instead of the current chunk's
start, causing an accidental O(n) rescan per transition — fixed before any
numbers below were recorded); the full opencode busy window was additionally
replayed at **true byte granularity end to end** (see below) as direct proof
for R-B2b, not just the coarse-refined boundary.

### Phase-window byte offsets

**agy** (10092 bytes total):
| Phase | Byte offset | Note |
|---|---|---|
| Submit (prompt fully rendered) | ~3698–3723 | raw-byte span of the last `"Say OK and nothing else."` occurrence before busy starts |
| Busy window start | **3802** | first byte at which `"Generating..."` is present in bottom-6 |
| Busy window end | **7261** | last byte at which either busy marker is present in bottom-6 |
| Settled-idle frame | **7262** | first byte satisfying the full agy idle check (bare `">"` in bottom-3, border line immediately below, no braille rune in bottom-6); busy ends at 7261 because a transient overwrite breaks the marker: at 7262 the footer reads `"?sc to cancel"` (leading `e` overwritten by `?`), so `"esc to cancel"` no longer matches; the full `"? for shortcuts"` hint only appears near offset ~7300 |
| Exit screen | 10092 (capture end) | `agy --conversation=` line renders at raw offset 10035; id `fb5e3e02-e5ef-4d25-b398-aead20366441` extracted identically from raw tail (marker at 10035, token at 10054) and the rendered grid text (char offset 2905) |

**opencode** (76281 bytes total):
| Phase | Byte offset | Note |
|---|---|---|
| Submit (prompt fully rendered) | 19821 (raw span end) | raw-byte span of `"Say OK and nothing else."` before busy starts |
| Busy window start | **33547** | first byte at which `"esc interrupt"` is present in bottom-6 |
| Busy window end | **67787** | last byte at which `"esc interrupt"` is present in bottom-6 |
| Settled candidate | 68087 | first byte 300 bytes past busy-end, confirmed non-busy and sustained for the next 500 bytes (coarse-sampled; opencode has no idle rule so only negative evidence — busy-marker absence — is asserted, per R-B2c) |
| Alt-screen exit (`\x1b[?1049l`) | 74751 | TUI leaves full-screen mode; plain-text exit summary follows |
| Exit screen / id line | 76231–76281 | `opencode -s ses_08b642915ffeYL3T6ea1DnJZDd` renders here; id extracted identically from raw tail (offset 76243) and rendered grid text (char offset 798) |

### Union-coverage proof (R-B2b)

**agy** — full byte-by-byte replay of the entire capture (10092 bytes, not
just the busy window) found the busy state true for **[3802, 6227]** and
**[6300, 7261]**, with a **72-byte gap at [6228, 6299]** where *neither*
declared marker matches as a substring. Root cause (confirmed by dumping the
rendered grid across this range): the footer row is being reused for both
labels, and mid-redraw the spinner glyph (`⣷`, a braille rune) overwrites the
leading `e` of `"esc to cancel"` while `"Generating..."` is still being typed
character-by-character over the same cells — for ~72 bytes neither string is
intact. This is **not a coverage failure that produces a false result**: at
every byte in that gap, `idle-line-equals` is either not evaluated (no bare
`">"` line is present in the bottom-3 in that specific sub-window) or would
be suppressed by the braille-rune defense (R-C1) — the braille spinner glyph
is present in the bottom-6 for the entire gap. Net effect: the engine would
read `unknown/none` for these 72 bytes, never `idle` and never a wrong
`active` — a benign, documented micro-gap, not a re-record trigger.

*Reconciliation (Phase C, post-merge):* the prediction above modeled only the
*declared* rules. The merged engine also keeps its pre-existing generic
spinner fallback, which fires on the braille glyph, so the gap actually
classifies `active` (not `unknown`) — strictly safer, same zero-idle
guarantee, proven byte-exact in `internal/engine/gridrules_fixture_test.go`.

This same replay independently reproduces the plan's cited **"offset ~6132"
hard-frame class**: in the range roughly [6100, 6227] (just before the gap
above), the bottom-3 shows a bare `">"` with a border line directly below it
— the exact idle-corroboration shape — while `"Generating..."` is briefly
absent (the "Thought Process" collapsible section has hidden the spinner
line). `"esc to cancel"` remains present in the footer throughout this
span, so `busy-contains` fires and (by R-C1's stated precedence,
`busy-contains > idle-line-equals`) `idle-line-equals` is never reached.
Without the `"esc to cancel"` busy rule this exact frame would have been
misclassified `idle` — this is the empirical justification for R-D4
declaring it.

**opencode** — the coarse pass located the busy window at [33547, 67787]
with **zero** coarse-granularity (16-byte step) gaps. This was then
independently confirmed by a **dedicated true byte-by-byte replay of the
full 34240-byte busy window** (a fresh emulator primed to byte 33547, then
fed one byte at a time to 67787, checking bottom-6 at every step): **zero
gaps** — `"esc interrupt"` is present in the bottom-6 content lines at
literally every single byte offset inside the busy window. opencode's busy
signal has no spinner-glyph co-occurrence risk (single static marker text,
never redrawn mid-generation the way agy's footer is), so no micro-gap class
exists for it.

### Negative evidence (R-B2c)

**agy**: at the settled-idle frame (offset 7262) and sampled across the
following settled window, neither `"esc to cancel"` nor `"Generating..."`
appears anywhere in the bottom-6 — at exactly 7262 the footer is the
transient `"?sc to cancel"` (marker broken by overwrite), settling to
`"? for shortcuts"` near ~7300.

**opencode**: at the settled candidate (offset 68087) and for the following
500 bytes (16-byte-step sampled), `"esc interrupt"` is absent from the
bottom-6 in every sample. Per R-B2/R-C1, opencode declares no idle rule, so
this is the full extent of the required negative evidence — no idle-frame
corroboration check applies.

### Idle corroboration (R-B2d, agy only)

At the settled-idle frame (offset 7262), the bottom-6 content lines (rows
counted upward from the last non-blank row) contain: a border line, the
bare-`">"` prompt line, another border line, and above that the footer
(the transient `"?sc to cancel"` at 7262, settling to `"? for shortcuts"`
by ~7300 — neither a busy marker) and prior transcript
lines. The bottom-3 line at content-index 2 is `">"` (`strings.TrimSpace`
equal), and the content line immediately below it (content-index 1, i.e.
physically the next row down the screen) is a box-border line
(`────...────`, 100% border-set runes, well over the 80% threshold). No
braille rune (U+2801–U+28FF) appears anywhere in the bottom-6. All four
R-B2d conditions hold.

### Conversation-id extraction (R-B2a)

Both ids were extracted two ways — from the raw byte tail (searching the
last marker occurrence, matching R-D6/R-E6's actual search order) and from
the final rendered grid's plain text — and both methods agree:

- agy: `fb5e3e02-e5ef-4d25-b398-aead20366441` (valid 8-4-4-4-12 lowercase-hex
  UUID shape), marker `agy --conversation=` at raw offset 10035, UUID token starting at 10054 / grid char offset 2905.
- opencode: `ses_08b642915ffeYL3T6ea1DnJZDd` (26 alnum chars after the
  prefix, well over the >=10 minimum, left word boundary satisfied — the
  preceding byte is a space), found at raw offset 76243 / grid char offset
  798.

Both tokens terminate cleanly on a control byte (`\r`/`\n`) well before EOF,
so the "unterminated at EOF" rejection rule is not exercised by either
fixture (noted as an adversarial-test gap for the Phase D/E unit suites,
which must cover it synthetically).

### opencode "Update Available" modal (R-B1 documentation requirement)

The opencode capture contains a real update-available modal dialog. Its
text (`"Update Available"`) is written to the raw byte stream exactly once,
at raw offset 21660 (before the prompt is even submitted — submit is at
19821, so the modal appears in the ~1.8s between submit and generation
start). The rendered dialog box is fully drawn and visible in the grid from
approximately **offset 22000** onward, and stays visible continuously
through **at least offset 70000** — i.e. it overlaps the ENTIRE busy window
[33547, 67787] and the beginning of the settled window (it is still visible
at the chosen settled candidate offset 68087). It clears from the screen
somewhere between offset 70000 and 72000, shortly before the TUI leaves
alt-screen mode (74751) to print the exit summary — most likely dismissed as
part of the Ctrl+C quit sequence's redraw, not by any explicit dismissal in
the drive script.

Despite fully overlapping the busy window, the modal **never breaks R-B2b**:
the true byte-by-byte replay of the whole busy window (above) found zero
gaps in `"esc interrupt"` coverage. The modal box does not render into the
bottom-6 rows the busy/idle rules scan — it overlays the upper/middle
portion of the 30-row grid, not the status-bar region.

**Auto-update-disable mechanism for a future re-record**: not exercised
this phase (promotion succeeded, so no re-record was needed). `opencode
--help` exposes no relevant flag, so the mechanism was confirmed by
inspecting the installed 1.17.9 binary directly (`strings` on
`/usr/local/lib/node_modules/opencode-ai/bin/opencode.exe`, a Bun-compiled
executable that still carries its source strings): the environment variable
`OPENCODE_DISABLE_AUTOUPDATE` is read at startup and treated as true when
its value lowercases to `"true"` or `"1"` (`env.OPENCODE_DISABLE_AUTOUPDATE:
j("OPENCODE_DISABLE_AUTOUPDATE")`, where `j` does exactly that comparison).
Setting `OPENCODE_DISABLE_AUTOUPDATE=1` as a process env var for the
swarm-char child (never exported globally, never written to any config
file) is therefore the correct non-mutating mechanism — it touches no disk
state at all, which trivially satisfies R-B1's "without touching user
global config." A second, file-based mechanism also exists (lower priority
since it requires writing a file): the embedded config-schema help text
documents a top-level `"autoupdate": true | false | "notify"` key in
`opencode.json`/`opencode.jsonc`, resolved from (among other locations) a
project config file the CLI walks up to from its cwd — so a temporary
project-scoped config in the swarm-char recording cwd would also work
without touching `~/.config/opencode/opencode.json` (global). Both should
be re-verified against whatever opencode version is installed at the time
of the next actual re-record, since these are undocumented-by-`--help`
internals and could drift between releases.

### Sanitized-fixture re-verification (R-B3)

All R-B2 checks above were re-run a second time, unmodified, directly against
the two COMMITTED fixture files (`internal/adapter/agy/testdata/agy.json`,
`internal/adapter/opencode/testdata/opencode.json`) — not the source
exploration captures. Every offset is byte-identical to the pre-sanitization
run:

- agy: busy transitions at 3802/6228/6300/7262 (unchanged), submit at 3698,
  settled-idle at 7262 with the same border/`">"` detail string, exit-screen
  id `fb5e3e02-e5ef-4d25-b398-aead20366441` at raw offset 10035 / grid offset
  2905 (unchanged) — and the final rendered grid's account-email line now
  reads `user.sanitized1@example.com (Antigravity Starter Quota)`, confirming
  the redaction actually took effect in the byte stream the emulator
  renders, not just in the JSON's surface text.
- opencode: busy window [33547, 67787] with zero gaps at both coarse and
  full byte-granularity (re-confirmed), submit at 19821, settled candidate
  at 68087, exit id `ses_08b642915ffeYL3T6ea1DnJZDd` at raw offset 76243 /
  grid offset 798, modal timeline unchanged — expected, since opencode had
  zero email/secret occurrences to redact.

This confirms sanitization did not disturb any marker, phase-window, or
id-extraction result on either fixture.

### Deferred / carried forward

- opencode's `OPENCODE_DISABLE_AUTOUPDATE` mechanism above is documented but
  untested — the next actual re-record (if fixture drift ever forces one)
  must verify it against the then-current opencode release before relying on
  it.
- agy's 72-byte micro-gap class [6228, 6299] and the offset-~6132 hard frame
  are now permanent regression-test fodder for R-C5 (full-timeline replay) —
  R-C5 must assert zero `idle` emissions across the WHOLE busy window
  [3802, 7261], not just at the two frames called out here.
- Neither fixture's id token is EOF-truncated, so the "unterminated at EOF"
  rejection path is untested by real captures; Phase D/E's adversarial unit
  tests must synthesize it.

## Phase G: verification

Phases A-F are merged: agy and opencode are registered production adapters
(`internal/adapter/registry`) with the engine's descriptor-driven grid rules
(`internal/engine/gridrules.go`) live, and already proven byte-exact offline
against both committed fixtures by `internal/engine/gridrules_fixture_test.go`
(R-C5). Phase G closes the remaining gap: does the REAL assembled stack —
registry resolution, launch-argv composition, daemon/shim spawn, the shim's PTY,
the daemon's periodic grid sampler, `engine.OnOutput`, persisted status — 
reproduce those same verdicts, and do the real CLIs themselves round-trip a
resumed conversation with the exact production argv.

### R-G1: production-path e2e with replay binaries

**Test**: `TestE2E_ReplayProductionPath_AgyOpencode`,
`internal/e2e/replay_e2e_test.go`. Runtime ~13-33s across five repeated runs
(build + daemon startup + ~7.3s of agy holds + ~4.2s of opencode holds run
concurrently, plus polling overhead); comfortably under the 60s bound, `-race`
clean.

**Mechanics**: two REPLAY BINARIES, named exactly `agy` and `opencode`, are
built at test time via `go build` from generated Go source (stdlib-only, no
module imports, so it compiles standalone) written to `t.TempDir()`. Each
binary reads its own committed fixture (`internal/adapter/{agy,opencode}/
testdata/*.json`) and writes the raw `pty_capture` bytes to stdout in SEGMENTS
cut at the Phase B marker-memo byte offsets, holding output at each target
state before advancing:

| agy segment | bytes | hold |
|---|---|---|
| startup | [0,3802) | 300ms |
| busy (pre-hard-frame) | [3802,6150) | 1200ms |
| hard-frame region (offset~6132 false-idle repro class) | [6150,6228) | 1200ms |
| marker-gap transient (neither declared marker intact) | [6228,6300) | 1200ms |
| busy tail | [6300,7262) | 1200ms |
| settled idle | [7262,10035) | 2200ms |
| exit screen (`agy --conversation=<uuid>`) | [10035,10092) | 300ms |

| opencode segment | bytes | hold |
|---|---|---|
| startup (incl. the "Update Available" modal) | [0,33547) | 300ms |
| busy (`esc interrupt`, zero-gap) | [33547,67787) | 1700ms |
| settled (no idle rule declared) | [67787,76243) | 2200ms |
| exit id line (`opencode -s ses_<id>`) | [76243,76281) | 300ms |

Every hold exceeds `internal/skeleton/serve.go`'s `gridPoll`/`eventPoll` 200ms
sampling cadence by 6-11x, so the daemon's real sampler deterministically
observes each state on more than one tick (R-G1's stated bound). Both sessions
are launched through the CLIENT protocol exactly as a real launch would be
(`c.Launch(protocol.LaunchReq{Agent: "agy"/"opencode", ...})`, geometry 100x30
matching the fixtures' recorded geometry, both running CONCURRENTLY — a small
incidental proof of `skeleton.go`'s per-session sampling goroutines / no-head-
of-line-blocking design, FIX 7), then status is polled via `c.List()` every
50ms and every sample recorded with its elapsed time, for both sessions off the
SAME poll, until both sessions' process has exited or a 40s bound elapses.

**DEVIATION from the plan's suggested "pass the fixture path via env var"**: a
custom env var does not survive to the exec'd agent process.
`internal/persist/env.go`'s `FilterEnv` is a normative ALLOWLIST (ADR-004 item
6, invariant S-2) applied to the launch `ClientEnv` before it becomes the
agent's env (PATH/HOME/SHELL/TERM/locale/venv/provider-key names only); an
arbitrary `SWARM_REPLAY_FIXTURE` would be silently dropped. The fixture's
absolute path is instead baked into the compiled replay binary as a build-time
string constant (one build per CLI name, from a small Go template) — this is a
property of the TEST HARNESS, not a production launch input, so it correctly
leaves the allowlist boundary (which the launch this test drives DOES go
through, unmodified) completely undisturbed. Noted here as a deliberate,
disclosed deviation, not a shortfall.

**Assertions and results** (all passed):
- agy: `turn=active` first observed, then `turn=idle` first observed
  strictly after it, with the elapsed gap between them `>= 3s` (a regression
  guard well below the ~4.8s the four busy-hold segments are scheduled to
  occupy — a premature/false idle firing during the hard-frame or marker-gap
  holds would collapse this gap far below 3s) and `>= 5` distinct `active`
  samples recorded in between (sustained observation spanning both the
  pre-hard-frame hold and the hard-frame hold itself, not a single lucky
  tick). Exit-screen conversation id `fb5e3e02-e5ef-4d25-b398-aead20366441`
  (the real committed fixture's id — the replay streams the committed bytes
  verbatim) extracted and persisted (`waitForConversationID` against the
  daemon's meta store).
- opencode: `turn=active` observed during its busy hold; `turn=idle` NEVER
  observed at any point across the whole session life (opencode declares no
  idle rule, R-B4 — asserted as a whole-stream invariant, the strongest form
  of "never idle ever"); `turn=unknown` observed after the last `active`
  sample (the honest "settled -> unknown" T-4 outcome); the final recorded
  sample is not `active`. Exit id `ses_08b642915ffeYL3T6ea1DnJZDd` (the real
  committed fixture's id) extracted and persisted.

**TDD red-first evidence**: `.claude/tmp/phase-g-red-evidence.txt` captures a
run with both `agySegments` and `opencodeSegments` temporarily stubbed to only
their startup slice (never emitting the busy/settled bytes). The real assembled
stack correctly failed the test — `agy: turn=idle never observed after active`
— proving the assertions are load-bearing (they fail when the signal is
genuinely absent) rather than vacuous; the sample log in that run also shows
`turn=active` was reached from the startup slice alone (agy's pre-prompt
screen already carries transient spinner content), which is a fine, harmless
detail — the meaningful failure is the missing settle. The stub was then
reverted to the full segment schedule above and the test passed (`--- PASS:
TestE2E_ReplayProductionPath_AgyOpencode`).

### R-G2: real-CLI user snippets (bounded, env-gated, not in CI)

Run interactively in this session against the REAL installed CLIs (agy 1.1.4,
opencode 1.17.9 — both already authenticated in this environment from Phase
B's original characterization). Harness: a throwaway `cmd/zzg2harness` command
package (built, exercised, then `rm -rf`'d — never committed, confirmed absent
from `git status`) driving the REAL registry adapters' `Command()`/`Resume()`/
`ExtractConversationID` plus the real `internal/vt` emulator for the busy-
marker proof; `cmd/swarm-char` (built fresh) as the PTY-driving exec harness,
following the exact quote pattern of the existing
`docs/verification/cli-trio-exploration/drive_{agy,oc}.txt` (a literal `0x03`
Ctrl+C byte, sent twice, 400ms apart, ~25s after submit). All runs used
`-cwd` the worktree root (already an agy-trusted workspace per
`~/.gemini/antigravity-cli/settings.json`'s `trustedWorkspaces` — a scratch
subdirectory was tried first and hit agy's per-EXACT-path trust prompt, so it
was abandoned in favor of the already-trusted root; `git status` confirmed
clean, no stray files, after every run). `OPENCODE_DISABLE_AUTOUPDATE=1` was
exported for both opencode runs.

**1. Detection** (`registry.Names() x adapter.Detect(name, detect.Host{})`,
the exact `detectAgents` path):

| adapter | production | Found | Version | InRange |
|---|---|---|---|---|
| agy | true | true | 1.1.4 | true |
| claude | true | true | 2.1.214 | true |
| codex | true | true | (unversioned) | false |
| opencode | true | true | 1.17.9 | true |
| reference | false | false | — | — |

agy and opencode both detect cleanly, in range, alongside claude. codex is
found but its version banner did not parse in this environment (pre-existing,
unrelated to this phase's adapters — not investigated further, out of scope).

**2. agy round-trip** — real argv composed by the REAL adapter (`agy.New().
Command(...)`, options `model="Gemini 3.5 Flash (Low)"`, `InitialPrompt="Say
OK and nothing else."`):

```
agy --model "Gemini 3.5 Flash (Low)" --prompt-interactive "Say OK and nothing else."
```

Transcript excerpt (initial turn, ANSI-stripped, email redacted per the R-B3
convention):

```
▄▀▀▄        Antigravity CLI 1.1.4
▀▀▀▀▀▀       user.sanitized1@example.com
▀▀▀▀▀▀▀▀      Gemini 3.5 Flash (Low)
   ▄▀▀    ▀▀▄     ~/Code/swarm/.claude/worktrees/cli-trio-integration
> Say OK and nothing else.
⣾  Generating...
esc to cancel
  OK
press ctrl+c again to exit
Resume with -c (or command below):
agy --conversation=0ce8720a-256f-47f7-b145-2e2ba103cb44
```

Busy-marker proof via the REAL `internal/vt` emulator (not a raw-byte scan —
a raw substring search for `"esc to cancel"`/`"Generating..."` FAILED even
though both are visibly present once rendered, because agy types its footer
character-by-character with cursor-show/hide escape codes interleaved between
individual characters; only the DECODED GRID — exactly what the production
`busy-contains` rule reads — shows them as contiguous text): `"esc to cancel"`
first observed at rendered byte 3488, `"Generating..."` at byte 3424, both
absent (as expected) from the settled/exit frame.

`ad.ExtractConversationID(nil, tail)` (the REAL R-D6 extraction, run against
the real capture) recovered `ok=true id=0ce8720a-256f-47f7-b145-2e2ba103cb44`
— matching the exit-screen marker exactly.

Resume argv, composed by the REAL adapter (`agy.New().Resume(...)`, unmodified
— production invokes it verbatim via `composeLaunchSpec`):

```
agy --conversation 0ce8720a-256f-47f7-b145-2e2ba103cb44
```

Driven with the follow-up prompt "Repeat my previous message back to me,
verbatim." (typed via the drive script, 6s after spawn + Enter, then the same
~25s-then-double-Ctrl+C quit). Transcript excerpt:

```
> Say OK and nothing else.
  OK
> Repeat my previous message back to me, verbatim.
▸ Thought for 3s, 281 tokens
  Prioritizing Tool Usage
  Say OK and nothing else.
press ctrl+c again to exit
Resume with -c (or command below):
agy --conversation=0ce8720a-256f-47f7-b145-2e2ba103cb44
```

**Retention assertions (automated, string match on the capture)**: PASS — the
resumed capture contains `"Say OK and nothing else."` verbatim (the model's
answer to the follow-up literally echoes the original prompt back), the prior
turn `"> Say OK and nothing else. / OK"` is already present in the resumed
session's scrollback (proving the SAME conversation was loaded, not a fresh
one), and the resumed session's exit-screen id is byte-identical to the
original (`0ce8720a-256f-47f7-b145-2e2ba103cb44`) — a real resume, not a
relabeled fresh launch. (Incidental, expected: agy's own default model
`"Gemini 3.1 Pro (High)"` took over on resume, since `Resume()`'s argv per R-D5
carries only `--conversation <id>`, no model flag — exactly what production
composes.)

**3. opencode round-trip** — real argv (`opencode.New().Command(...)`, options
`model="opencode/deepseek-v4-flash-free"` — the specified free-tier model
worked on the first attempt, so the ollama/qwen3:4b fallback was never
needed):

```
opencode --model opencode/deepseek-v4-flash-free --prompt "Say OK and nothing else."
```

Transcript excerpt (initial turn):

```
Say OK and nothing else.
Build · DeepSeek V4 Flash Free
■⬝⬝⬝⬝⬝⬝⬝esc interrupt
⠋ Thinking
+ Thought: 227ms
OK
Session   Request to say OK
Continue  opencode -s ses_089cb3878ffeUL81NnKWbOp1La
```

Busy-marker proof via the real `internal/vt` emulator: `"esc interrupt"` first
observed at rendered byte 24704, absent from the settled/exit frame (same
raw-vs-rendered caveat as agy above — opencode's footer is likewise typed
through interleaved escape codes, so only the decoded grid shows it as
contiguous text, which is exactly why the production rule reads the grid, not
raw bytes).

`ad.ExtractConversationID(nil, tail)` recovered
`ok=true id=ses_089cb3878ffeUL81NnKWbOp1La`, matching the exit line.

Resume argv, composed by the REAL adapter (`opencode.New().Resume(...)`,
unmodified):

```
opencode --session ses_089cb3878ffeUL81NnKWbOp1La
```

Driven with the same follow-up prompt and quit pattern. **Retention
assertions**: PASS — the resumed capture contains `"Say OK and nothing else."`
verbatim, and the resumed session's exit line carries the byte-identical
session id (`opencode -s ses_089cb3878ffeUL81NnKWbOp1La`) — a real resume.

**Summary**: both CLIs round-tripped successfully on the first attempt, within
the plan's bound of max 2 turns per CLI (one initial + one resume, each); no
fallback model or temporary opencode config was needed.

### R-G3: attached-vs-detached sampling note

From `internal/skeleton/serve.go` (`sampleGrid`, `tapGrids`,
`captureConversationID`) and `internal/shim/server.go` (`acceptLoop`,
`server.curConn`):

- The shim is a v1 single-slot server: `acceptLoop` calls `listener.Accept()`
  then blocks inside `serveConn(conn)` until that ONE connection ends before
  looping back to `Accept()` for the next ("Exactly one client connection is
  served at a time," `internal/shim/server.go:37-38`). A second dial while a
  client is attached is accepted at the kernel/socket level but sits queued —
  it is not served until the current connection's `serveConn` returns.
- The daemon's grid tap (`tapGrids`, `gridPoll` = 200ms) drives
  `sampleGridAsync` per running session in its OWN goroutine each tick, deduped
  so at most one sample is ever in flight per session (no pile-up). Each
  sample calls `sampleGrid`, which itself does a full `Attach` (dial + hello +
  attach control frame + wait for the snapshot, bounded by
  `shimAttachTimeout` = 10s) and closes the stream IMMEDIATELY after reading
  the one snapshot — holding the shim's single slot for only the few
  milliseconds of that round-trip when the shim is otherwise idle.
- **Detached** (no client holding the session): each 200ms tick's sample dial
  is served almost instantly (the shim's `Accept` is idle), so
  `engine.OnOutput` gets a fresh grid roughly every 200ms — the cadence R-G1's
  holds are sized against.
- **Attached** (a client — e.g. the TUI, or a held test attach) is
  continuously connected: the periodic sample dial queues behind that live
  connection and, per `sampleGrid`'s own doc comment, "the sample's dial times
  out and is ignored" if the client does not detach within the 10s
  `shimAttachTimeout` — a silent skip, not a hang (the per-session goroutine +
  dedup means this never blocks OTHER sessions' cadence, L1's no-head-of-line-
  blocking property, FIX 7). Net effect: while a client stays attached to a
  session, the grid-driven half of status derivation (the ENTIRE signal source
  for agy/opencode, which are heuristic-only, R-D4/R-E4) is starved and freezes
  at whatever it last observed, resuming only once the client detaches (or a
  fresh 200ms tick's dedup-cleared attempt succeeds once the slot frees).
  Hook/typed-signal-driven status (claude/codex) is unaffected, since it flows
  over the separate hook socket, not the shim's attach slot. The client's OWN
  live view is also unaffected — its attach stream keeps receiving real-time
  output frames regardless; only the DAEMON's own background sampling side-
  channel is blocked.
- This is precisely why conversation-id capture (Epic 11 C1) was moved OFF the
  grid tap onto an independent transcript-file poll
  (`captureConversationID`'s doc comment states this exact rationale
  verbatim), and is independently proven by the existing
  `TestE2E_ConversationCapture_DuringHeldAttach_C1`
  (`internal/e2e/capture_c1_e2e_test.go`) — that test holds an attach for a
  session's whole life and confirms id capture still completes, precisely
  because it does NOT depend on the grid tap's own attach succeeding.

### Deferred / carried forward (Phase G)

- R-G3 is a documentation note synthesized from source inspection plus the
  EXISTING `TestE2E_ConversationCapture_DuringHeldAttach_C1` as corroborating
  proof; no new test was written specifically for the attached-vs-detached
  status-freeze behavior (as opposed to conversation-id capture, which already
  has one). If this ever needs to be a regression-tested guarantee rather than
  a documented architectural property, that is follow-up work.
- The busy-marker "user-visible proof" in R-G2 uses the real `internal/vt`
  emulator (not a raw-byte substring scan, which was tried first and shown to
  be unreliable — see the transcript notes above); the authoritative
  classification proof (through the actual `busy-contains`/`idle-line-equals`
  engine rules, not just marker presence) remains R-G1 (byte-identical replay
  of the same committed fixtures) and R-C5 (offline full-timeline replay).
- codex's version-banner parsing failure in the R-G2 detection sweep (found,
  unversioned, out of range) is pre-existing and unrelated to the agy/opencode
  work; not investigated as out of this phase's scope.
