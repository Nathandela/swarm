# S5 evidence — shared design tokens (PB-TOK-1, PB-TOK-2, PB-TOK-3)

**Commit**: `638b61b` — two files, +250: `internal/design/tokens.json` and
`internal/design/tokens_test.go`. **Requirements**: 3. **Decision**: ADR-007 **B3** (one skin:
Substrate).
**See also**: `remote-phaseB-s13-evidence.md` — PB-TOK-4 was split out of PB-TOK-2 by this slice's
reviewer and is owned by S13.

> **RECONSTRUCTED**, 2026-07-25, from the commit, the diff and the tests, re-run at HEAD.
> **Read the "What is NOT established" section — one acceptance criterion is not met today.**

## What shipped

`internal/design/tokens.json`: all **31** `--p-*` tokens of the **Substrate (d1)** skin, extracted
verbatim from `docs/research/remote-control-design-directions.html`, plus a `terminal_peek` block
holding token **references** (`"fg": "--p-hero"`, `"font": "--p-mono"`) rather than duplicated
values. Schema version, source path, skin and mode are all fields in the file, so the pin is data
rather than convention.

Four tests, each dense:

| Test | What it establishes |
|---|---|
| `TestTokenSourceExistsAndMatchesSchema` | The file is checked in, parses, and has the declared shape. **Trailing JSON garbage is rejected** — see below |
| `TestTokenSourceMatchesChosenSkinInDesignHTML` | **Exact equality in BOTH directions** between the JSON and the chosen skin's block in the design HTML: every token the HTML defines is in the JSON (completeness) and every token in the JSON is defined by the HTML (drift) |
| `TestChosenSkinIsSubstrateAndPinnedDark` | PB-TOK-2's decision is asserted, not merely recorded: `skin == "substrate"`, `mode == "dark"` |
| `TestTerminalPeekIsPhosphorGreenMonospace` | PB-TOK-3: `terminal_peek.fg` is **exactly** `--p-hero`, it names a token that exists, and the font token's value is a monospace stack |

## Per requirement

| Requirement | What proves it | Status |
|---|---|---|
| PB-TOK-1 (one machine-readable token source; **the single origin for the Android theme**; theme generated from or asserted against the JSON) | `TestTokenSourceExistsAndMatchesSchema` + the drift half of `TestTokenSourceMatchesChosenSkinInDesignHTML` | **Half met.** The source exists and is drift-guarded. **Nothing joins it to the Android theme** — see below |
| PB-TOK-2 (exactly one skin chosen, recorded in the ADR, pinned by the token source) | `TestChosenSkinIsSubstrateAndPinnedDark`; ADR-007 **B3** records the decision and supersedes the 2026-07-23 amendment that retained the *pair* and never chose | Met |
| PB-TOK-3 (phosphor-green monospace terminal peek; purple stays retired) | `TestTerminalPeekIsPhosphorGreenMonospace` | Met for the token source. The emulator-evidence half is not delivered |

## What the independent review changed — it approved the DATA and rejected the TESTS

This is the useful part of S5's record. The reviewer accepted the extracted values — **12
mutated-JSON and 8 mutated-HTML probes, plus an independent brace-depth re-extraction confirming
all 31 values byte-faithful** — and then found four ways the *tests* were under-discriminating.

1. **`terminal_peek.fg` accepted near-black "greens".** The hue classifier had a saturation floor
   but **no lightness floor**, so `--p-hero-ink` `#06150c` (v = 0.082) passed while being
   **invisible text on a `#08090a` background**. Under the Void skin it was worse: `--p-well`
   `#050807` classified as phosphor green. Now pinned to `--p-hero` **exactly**, with a negative
   control proving the previously-blessed value fails.
2. **The chosen skin was asserted nowhere.** A well-formed **Void** source passed every test. PB-TOK-2
   is a decision requirement, and nothing held the decision. Now pinned, and recorded in the spec
   and ADR.
3. **The purple half of the classifier was structurally unreachable** — exact HTML<->JSON equality
   plus a `d1`/`d2`-only skin gate already exclude the retired `d3` direction. About **65 lines of
   HSV were deleted** in favour of a one-line equality check that is *strictly stronger*, with a
   comment recording why so nobody restores it. (The comment is still in
   `tokens_test.go:186-189`: "do not reintroduce an HSV classifier for it.")
4. **Trailing JSON garbage was accepted.** `Decode` never called `More()`, so `{...}{"junk":true}`
   passed a gate **Android's parser would reject** — a token source that is valid to Go and invalid
   to the consumer.

Findings 1 and 3 together are the slice's real lesson: a *classifier* that answers "is this
green-ish?" is weaker than an *equality* to the token that is supposed to be there, and the
classifier's slack is exactly where an invisible-text bug lives.

## Failing-first evidence (GG-5)

No preserved RED transcript. The commit records the four-agent cycle explicitly — "a test author
wrote the failing tests, a separate implementer extracted the tokens, an independent reviewer
attacked both, and a fourth agent applied the findings" — and the tests landed with the data in one
commit. What is durable and checkable:

- **The negative control inside finding 1**: the previously-blessed `--p-hero-ink` value is proven
  to fail the tightened assertion. That is a real mutation result rather than a claim.
- **The 20 mutation probes (12 JSON, 8 HTML) are NOT preserved anywhere in the repository.** They
  are recorded in the commit message only. The durable substitute is the bidirectional equality
  test, which fails on any single-token drift in either artifact.
- **PB-TOK-4's existence is itself failing-first evidence for PB-TOK-2**: the reviewer showed that
  PB-TOK-2's second criterion was an assertion about the *app*, which S5 does not own, so under
  PB-DOC-7's exactly-once rule it had nowhere to live — "a `DayNight` parent could ship with no test
  failing". Splitting it out is what made it testable. See the S13 evidence.

## What is NOT established — and it is PB-TOK-1's own acceptance criterion

**PB-TOK-1 requires the JSON to be "the single origin for the Android theme", with the theme
"generated from or asserted against the JSON". No test does this, and the values do not match.**

Measured at HEAD:

| | token source | shipped Android theme |
|---|---|---|
| background | `--p-bg` `#08090a` | `swarm_background` `#FF101114` |
| primary text | `--p-ink` `#f7f8f8` | `swarm_text_primary` `#FFE6E8EB` |
| secondary text | `--p-ink2` `#8a8f98` | `swarm_text_secondary` `#FF9BA1A8` |

`grep -rn 'tokens.json\|internal/design'` over every `.go`, `.kt`, `.xml` and `.kts` in the tree
returns **hits only inside `internal/design/` itself**. Nothing in `android/` reads the token
source, and no Go or Kotlin test compares them.

This is **disclosed in the code rather than hidden** — `android/app/src/main/res/values/colors.xml`
says so in a comment: *"These are placeholders for the skeleton. PB-TOK-1/PB-TOK-2 own the real
token source and S16 owns the screens that paint with them"* — and `SwarmTheme.EXPECTED_DARK_COLORS`
duplicates the same three placeholder literals so `ThemeNightModeTest` compares the resolved theme
"against a recorded number rather than against itself".

So the honest statement is: **S5 delivered the token source and its drift guard; the join to the
Android theme is not delivered by any slice yet.** It is not S13's — S13 shipped a skeleton and said
so — and S16 owns the screens. Until a test asserts the Android resources against `tokens.json`,
PB-TOK-1's second clause is unmet and the two artefacts are free to diverge, which they currently
do. **Recommended owner: S16**, with the assertion living wherever the screens are painted.

The same gap applies to PB-TOK-3's second half: its criterion is "asserted against the token source
**+ emulator evidence**", and there is no emulator evidence. The token-source half is done.

## Gates, re-run at HEAD 2026-07-25

`go test ./internal/design/ -count=1 -v` — **4 tests, all PASS**, `ok 0.71 s`.

## Accepted residuals

- **The design HTML is the upstream authority and it is a research artifact.**
  `docs/research/remote-control-design-directions.html` is unversioned in any machine-readable
  sense; the JSON records its path and a `schema` integer, but nothing pins its *content hash*. An
  edit to the HTML that changes a Substrate token makes the drift test fail (good), but an edit that
  changes something else entirely is invisible (fine), and a *replacement* of the file would be
  caught only through the token block.
- **Light mode is deferred to Phase C** (§5), which is why the token source pins `mode: "dark"` and
  why PB-TOK-4 exists at all.
- **`--p-hero-ink` `#06150c` is a legitimate token** and stays in the set; the review's finding was
  about the *classifier accepting it as the peek foreground*, not about the value itself. It is the
  ink colour that sits **on** the hero fill.
