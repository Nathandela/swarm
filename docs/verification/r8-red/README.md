# Wave R8 — RED evidence (2026-08-20)

> ## CORRECTION — 2026-08-20, Wave R8 CLOSING round
>
> **This document overstates the wave. Read `docs/verification/r8-green/README-closing2.md`
> first; where the two disagree, the closing document is authoritative.** (`README-closing.md`
> was authoritative from 2026-08-20 until closing round 2 on 2026-08-21; it now carries a
> banner of its own naming three of its claims as false, so read the two in that order.)
>
> The specific correction, in the words the closing review used: **R8 lands as the READ HALF
> ONLY.** Every claim in this file that a user, a phone, or "the product" CAN ENTER CONTROL of
> a terminal, or that the control plane is exercised end to end, is **STRUCK**.
> `protocol.TerminalInputSink` has no production implementation — `internal/skeleton.coreAPI`,
> which is what is passed as `srv.d`, has no `TerminalInput` method — so `handleTerminalInput`
> takes the `op_not_implemented` arm for every frame the product can produce, and there is no
> take-control affordance on any screen. The control-plane measurements in this file were taken
> at `r8Backend.written`, an IN-PROCESS FAKE standing in for a PTY, over a real
> `Gateway` + `ServeRemote`; the accurate statement of what they prove is **"bytes counted at
> the `TerminalInputSink` seam over the real gateway composition"**, which is a real and useful
> result and is not "bytes at the PTY".
>
> The positive corollary is also struck-through-into-place rather than left implied: **the
> raw-input attack surface in the shipped product is currently ZERO.**
>
> See ADR-017 amendment C0/C1 for the parking decision and its preconditions.


Bead `agents-tracker-hggx.9`. Failing-first evidence for ADR-017 and its Wave R8 scoping
amendment, captured on `darwin 25.5.0 / arm64`, Go toolchain from `$HOME/go/bin`, at commit
`5195685` with a clean working tree apart from these test files.

Nothing in this directory is a claim that R8 works. Every file here is a claim that a stated
obligation is **not met today**, recorded before any production line is written (GG-5).

---

## 1. What a reader must know before reading the numbers

**Two RED classes, and they are not equally strong.**

| Class | What the evidence looks like | Where it is used |
|---|---|---|
| **Assertion RED** | Named test failures, each printing the wrong value and the right one | `internal/vt` (the corpus), `android/gate` (the Kotlin fences), `mobile` |
| **Compile-fail RED** | `undefined: X` / `unknown field Y` naming the seam that does not exist | every package where R8 adds a type, a field or a verb |

Compile-fail RED is the repository's established pattern for a new seam
(`internal/skeleton/capability_test.go` names it: "undefined symbol -> compile-fail RED"), and
it is honest about one thing and silent about another: it proves the seam is absent, and it
**masks every other test in that package** while it stands. So the corpus that this wave's
security rests on — `internal/vt` — was deliberately written using **no symbol that does not
exist today**, and its RED is a list of named failures a reviewer can read one at a time.

**One thing this directory does NOT contain: a passing test.** GREEN's first obligation is to
re-run every command below and show each named failure resolving, and to show the GREEN PINS
in §4 still passing.

---

## 2. The evidence files

| File | Slice | Class | Command |
|---|---|---|---|
| `adversarial-read-red.txt` | S5 | assertion | `go test ./internal/vt/ -run TestR8 -count=1 -race` |
| `capability-publication-red.txt` | S1, S2 | compile-fail | `go vet ./internal/protocol/schema/ ./internal/skeleton/` |
| `instance-identity-red.txt` | S1 | compile-fail | `go vet ./internal/skeleton/` |
| `router-red.txt` | S3 | compile-fail | `go vet ./internal/protocol/ ./internal/phonecore/ ./internal/remotegw/` |
| `terminalview-red.txt` | S4 | compile-fail | `go vet ./internal/daemon/ ./internal/remotegw/` |
| `control-red.txt` | S6, S7 | compile-fail + assertion | `go vet ./internal/protocol/`, `go test ./mobile/ -run TestR8Facade` |
| `severance-red.txt` | S8 | compile-fail | `go vet ./internal/phonecore/` |
| `fallback-screen-red.txt` | S9 | assertion | `go test ./android/gate/ -run TestR8Gate -count=1` |

`go build ./...` is **green** with all of these in the tree: every addition is a `_test.go`
file, so no production package is broken by the RED itself.

---

## 3. The corpus (S5) — 8 test functions failing, 24 named failures

`internal/vt/r8_corpus_test.go` is 28 named hostile fixtures against `SnapText`, each asserting
the **exact sanitized string** rather than the absence of a control byte. A corpus that asserts
only absence cannot catch the sanitizer that answers by deleting the row.

**8 fixtures are RED**, each a spoof with a named payoff on a phone and **no control byte in
the payload** for a control-byte filter to catch:

| Fixture | What the attacker buys |
|---|---|
| `line_separator_u2028` / `paragraph_separator_u2029` | Android lays these out as line breaks: one grid row becomes two and every row below it shifts |
| `soft_hyphen_u00ad` | `pay<SHY>pal.com` displays as `paypal.com` and compares unequal to it |
| `mongolian_vowel_separator_u180e` | the zero-width separator outside the U+200B–U+200F range the F7 fix covered |
| `word_joiner_u2060`, `invisible_operators_u2061_u2064` | five more zero-width runes that splice or hide content |
| `interlinear_annotation_ufff9_ufffb` | one visible string carrying a second, hidden one |
| `tag_block_ue0000_ue007f` | a complete invisible ASCII alphabet — an entire second sentence inside a line that reads as innocuous |

Plus four whole-property failures: `NoRowEverBecomesTwo` (every Unicode Zl/Zp rune),
`ZeroWidthFormatRunesAreDroppedNotSpaced` (15 runes), `CombiningMarkDepthIsClampedPerCell`
(500 marks on one cell survive unclamped), `ARowIsBoundedByItsDeclaredGeometry` (a Snap
declaring 80 columns rendered a **4 MB single line**), `AGridIsBoundedByItsDeclaredRowCount`
(a Snap declaring 24 rows flattened to **50 000 lines**), and
`ColumnParityIsPreservedUnderSanitization` (8 fixtures).

### 3.1 Why this is a LIVE path and not only defence in depth

The corpus feeds hand-built `Snap`s, so the obvious objection is that the daemon builds its
`Snap` from its own emulator and a hand-built one is hypothetical. It is not.

`internal/daemon/terminalrender.go:140-147` (`renderInitial`) decodes **`stream.Snapshot()`** —
bytes produced by another process, the shim, carried over a pipe — and pushes
`vt.SnapText(snap)` straight to the phone before its own emulator is even seeded.
`vt.DecodeSnapshot` validates the version number and performs **no sanitization of run text at
all** (`emulator.go:595-607`). `TestR8RealPath_TheWireSuppliedInitialSnapshotIsTheLiveHostilePath`
drives exactly that: a well-formed wire snapshot whose run text carries U+2028 reaches the
phone-facing projection unchanged. `SnapText`'s own doc comment already claims this duty —
"hostile bytes that reach a Snap **by any path**" (`render.go:140-143`) — and this is the path
it is about.

### 3.2 A ruling this RED pass had to make, with the measurement behind it

U+2028/U+2029 are in the **DROP** class, not the replace class. The reason is measured, not
assumed: on the real pipeline the emulator gives them **no cell at all** —
`above<U+2028>below` renders as a ten-column row, not an eleven-column one — so replacing them
with a space would add a column the emulator never counted, which is the exact parity break
the drop treatment exists to prevent. `r8ZeroWidthClass` in the corpus is a second,
independent spelling of the drop set; if production's `stripControls` and that predicate ever
disagree, the parity test says so.

### 3.3 The combining-mark clamp is fenced, not numbered

`TestR8Corpus_CombiningMarkDepthIsClampedPerCell` asserts a **shape**, so GREEN picks the
number and cannot pick "none": the base survives, the surviving marks are a **prefix** of the
supplied marks (nothing reordered, nothing invented), and the count is strictly less than what
was supplied **and at most 16**. Sixteen is a ceiling, not a recommendation — UAX #15's
stream-safe format bounds a defective combining sequence at 30 and a real terminal cell
carries a handful.

---

## 4. GREEN PINS — passing today, and they must stay passing

Recorded here because a corpus with no green in it cannot tell "we fixed it" from "we broke it
differently".

- **The whole real-path suite** (`TestR8RealPath_*` except §3.1's) passes. The emulator already
  consumes OSC 52 / OSC 0 / OSC 8 / DCS / APC, survives chunk-split sequences, and drops
  zero-width runes before they reach a cell. **The layered story is: the emulator's grid model
  is the first defence and `SnapText` is the second, independent one — and it is the second one
  that is broken.**
- `TestR8Corpus_InvalidUnicodeBecomesAnExplicitReplacementGlyph` **passes today**, emergently:
  `strings.Map` decodes an invalid byte as `utf8.RuneError` and writes U+FFFD. It is pinned as
  behaviour rather than left as a side effect, per playbook:457-458.
- `TestR8RealPath_TheWindowTitleNeverReachesThePhone` passes. The omission is load-bearing and
  was untested: OSC 0/2 is fully session-controlled text, and the moment a fallback header
  wants a string, `Snap.Title` is the nearest one to hand.
- `TestR8RealPath_DeviceQueriesGenerateNoReplyOnTheRenderPath` passes. The render path never
  calls `SetReplyWriter`, which is what makes "the fallback is a read" true at the byte level.
- `TestR8Gate_TheRetiredPeekSymbolsStayBannedByName` and
  `TestR8Gate_NoStructuredScreenNamesTheFallbackRenderPath` pass, and are the two fences most
  likely to be quietly relaxed while the fallback screen is being built.

---

## 5. Two DISCLOSED DIVERGENCES — measured, pinned, and not closed

Both are recorded as fixtures pinned to the **measured** behaviour, so a change of mind has to
come past a test and say so.

1. **CSI 5i / 4i (media copy) is not implemented.** The phone shows `divertedvisible` where an
   xterm shows `visible`. This is a **fidelity divergence between the owner's surface and the
   phone's**, not an injection: the phone receives literal text and runs no parser.
2. **8-bit C1 introducers are not honoured.** An OSC written with U+009D/U+009C renders as
   literal text rather than being consumed. A terminal running with S8C1T would consume it, so
   the two surfaces again differ — but the phone sees strictly *more*, inert, and the C1 bytes
   themselves are stripped. Teaching the emulator 8-bit C1 would **add a second parser path for
   hostile bytes**, which is the opposite of what this wave is for.

**What a user can and cannot conclude from a fallback screen, stated plainly:** the row they
read is what the machine's own emulator resolved, sanitized; it is **not** guaranteed to be
byte-for-byte what the owner's terminal draws, and the two known reasons are above.

---

## 6. Obligations this RED pass hands to GREEN in writing

1. **`TestSessionCapabilities_WireShape`** (`internal/protocol/schema/capability_test.go:60`)
   pins the record's JSON byte-for-byte at six keys. `session_instance` and `terminal_control`
   break it. The repair is a **strengthening and only that**: the pinned literal gains two keys
   and loses none. If that edit removes a key, the rule was weakened.
2. **`s11_lease_test.go:218-224`** pins backgrounding's by-consequence answer. Amendment T8-b
   makes backgrounding a direct trigger, so the row is amended **as a strengthening** —
   backgrounding severs directly **and** still severs through the disconnect. The assertion set
   grows; nothing is deleted. This is OPEN-C6's sanctioned edit and the only shape it may take.
3. **`lease.go:48-57`** still cites §6.0's withdrawn 60 s biometric freshness. T7 obliges the
   comment to cite this ADR in the same change that implements the ruling.
4. **`profile.go:22-23`** says ADR-016's three fields "are not yet declared here", nineteen
   lines above the three fields. `TestR8Profile_StaleCommentAboutADR016IsRepaired` fails until
   it is fixed. A comment that contradicts the struct it documents is how a field gets added
   twice.
5. **GG-7**: every new wire body and field needs its `docs/specifications/protocol.md` row in
   the same commit, and the Meaning cell must never contain the literal phrase "JSON key" —
   write "wire name".
6. **`capability_test.go`'s two fences are untouched by this pass and must stay untouched.**
   `TestR8Publication_AStructuredAdapterGetsNoFallbackAndNoControl` adds the layer *above* them
   rather than replacing them: a derivation that is right and an authoring path that overrides
   it would leave both green and the phone wrong.

---

## 7. What is NOT covered by this RED pass, stated rather than implied

- **Kotlin unit tests (Robolectric).** The Android obligations here are Go source fences in
  `android/gate`. A rendered-tree assertion cannot state a deletion obligation, which is what
  most of S9 is; but the fences also cannot prove the screen *renders* correctly. GREEN owes
  Robolectric coverage for the fallback body, built from a script file with `swarm.aar` and
  result-XML mtimes captured before and after.
- **The signed-op end-to-end drive for `terminal_control_begin`.** The R8b control tests here
  pin the wire shape, the code values, the lease-plane separation and the refusal-stub removal.
  Driving a *signed* begin over the assembled remote-tier server (wrong sender, wrong profile,
  wrong instance, replayed op id) is owed once the handler exists — R8b's slice S6, on the
  `r6_composersend_test.go` harness pattern.
- **The daemon idle-expiry timer on a driven clock** (T6-c's "a generation with zero inbound
  frames is severed within 30 s"). The seam it hangs off does not exist yet; the phone-side
  half (`TestR8Control_BackgroundingSeversDirectly`) is here.
- **Relay replay / wrong-signer frames.** Covered by the existing `crypto.MailboxReceiver`
  seq-gate suites; R8's addition is that the *generation* is bound to the sender, which is
  S6's signed-drive above.
- **The T3 version-skew gate** stays deferred to `agents-tracker-hggx.2.1`, with the CANNOT-YET
  line stated: until it lands, an unrecognised Claude or Codex build opens as structured chat
  rather than as a labelled read-only terminal.
