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
