# Substrate component derivations (PB-DS-7)

Substrate specifies 14 components. The product needs 38. The other 24 exist only in
`docs/research/remote-control-mock.html`, which predates the skin decision and is painted in an
iOS-derived palette the product retired (`#ff9f0a`, `#bf5af2`, `#30d158`, `#1c1c1e`, `#ff453a`,
`#8e8e93`, `#636366`, `#39393d`, `#48484a`, and the documentation page's own chrome accent
`#e2a33b`). This document derives a Substrate specification for each of them so an implementer never
has to invent one.

**Authority.** `docs/research/remote-control-design-directions.html` (the `.d1` block plus the shared
structural CSS) is authoritative for anything it defines, and it is the source of idiom for
everything it does not. `remote-control-mock.html` is authoritative for structure, copy and
behaviour; its colours and radii are exactly what this document replaces.
`internal/design/tokens.json` is the only palette that may be spent — 31 tokens, nothing else.
ADR-007 B134 is already decided and is consumed here, not re-argued: `ReadyForReview` takes
`--p-ok` and `Completed` takes `--p-ink3`; the fonts are the platform families; no decorative
animation; `minSdk 33` retires the blur and blend-mode fallbacks.

**What this document is not.** It does not restate the 14 components Substrate already specifies
(§B1-B14 of the design inventory). It does not decide PB-DS-1 (the 2 dp spacing grid), PB-DS-2 (the
18 named text styles) or PB-DS-5 (the five effects) — it consumes them, and where it needs something
they do not supply it says so and names the addition.

**Units.** CSS px in a 386x812 mock equals iOS pt equals Android dp at 1:1. Every number below is dp
unless it is a type size, which is sp.

## How to read a cell

Every cell is one of four things, and nothing else:

| Form | Example | Meaning |
|---|---|---|
| A token | `--p-elev` | Spend it as-is. The resolved ARGB is shown once, in parentheses. |
| A derivation over tokens | `--p-ink3` at 40% (`#6662666D`, over `--p-card` -> `#FF303236`) | The formula is the specification; the ARGB after it is the *output*, shown so a reviewer can check the arithmetic and an implementer can assert it in a test. It is not an authored value. |
| A spacing step or frame constant | `space_12`, `tabbar_height` | From PB-DS-1's 2 dp grid (2/4/6/8/10/12/14/16/18/24) or its three frame constants (`screen_top` 54, `screen_bottom` 76, `tabbar_height` 74), plus the four derived constants named in §5. |
| A named exception | **exception: scannability** | A literal that is not a token, always with its reason. There are five in this document and they are enumerated in §7. |

One thing that is *not* a specification and must not be read as one: a **citation** of the value being
replaced. These are always prefixed `mock` and always sit immediately beside their replacement (`--p-att`
(`#FFF1A10D`) — mock `#ff453a` retired). They are there so a reviewer can see what was dropped. No
citation is ever the value an implementer writes.

A **type / ink** cell names two independent things: the *metric* style from PB-DS-2 (size, weight,
tracking, family, line-height) and the *ink* token. Colour is not welded to a metric style —
Substrate itself binds one `Label.Button` to three inks across the three CTA variants, so the same
convention holds throughout.

**Alpha fills composite against whatever is behind them.** Where a derivation is an alpha over a
token rather than an opaque mix, the resolved value is given for the surface the component actually
sits on, and it changes if the component is moved.

---

## 1. The four judgement calls

These are the decisions the artifacts could not make. Each is argued here and then appears as a row
in §3.

### 1.1 The focus ring: `--p-hero`, 2 dp, 2 dp offset

> **AMENDED 2026-08-07, ADR-009 D3.** The ring resolves to **2 dp `--p-hero` champagne**, and the
> Substrate argument below is kept as the record of how it got there rather than deleted.
>
> **What changed is the premise and not the reasoning.** The paragraph below removes `--p-hero`
> because "hero is the fill of `.chip.on` and the ink of `.ptabs .on`, so a hero ring around an
> unselected chip would say the opposite of what is true" — hero meant SELECTED, and only that.
> In Obsidian the accent means **you**: needs-you, CTA, focus, live counter, brand, unified
> deliberately (ADR-009 D3, Consequences). A ring that says "you are here" is the fifth thing the
> one accent says, not a sixth meaning bolted onto a fill colour. The rejection had a reason and
> the reason is gone.
>
> **The neutrals swap sides.** `--p-ink` and `--p-ink2` are now the rejected pair, for the reason
> §1.1 already gives against `--p-ink2` and which Obsidian extends to `--p-ink`: a ring in the
> body ink over a warm ladder whose hairline is `--p-hair` reads as a heavier border, not as
> focus. The four status tokens stay rejected — they mean state — with the one caveat ADR-009 D6
> records: `--p-att` value-aliases `--p-hero`, so the ring and the NeedsInput indicator resolve to
> the same bytes today. That is the ALIAS being visible, not the ring meaning a status; the two
> keep separate rows in `android/design-tokens.tsv` precisely so a future skin can break it.
>
> **The measured contrast**, computed over Obsidian's ladder: `--p-hero` `#c9a876` is **8.74:1**
> on `--p-bg`, **8.22:1** on `--p-card` and **7.69:1** on `--p-elev` — every one of them well
> above the 3:1 WCAG floor ADR-009 D8.1 holds non-text indicators to, and the ring is one.
>
> **The residual inverts and shrinks.** Substrate's white ring sat against the hero-filled CTA at
> 1.88:1; a champagne ring against a champagne fill is 1:1 and would vanish outright. The
> `space_2` offset is what saves it, exactly as the paragraph below says it saved the white one:
> the ring is read against the surface the button sits on, never against the button.

The artifact's `:focus-visible` uses `#e2a33b`. That is the *documentation page's* chrome accent —
the colour of its own tab buttons and rail headings — and it is not a product token at all. It leaked
into both artifacts because both are documentation pages that happen to contain a phone. The product
has no focus ring.

The constraints are hard. The ring must be visible against `--p-bg`, `--p-card` **and** `--p-elev`,
because focus lands on chips (on the ground), rows (on cards) and CTAs (on the elevated sheet). It
must not read as a status colour, which removes `--p-att`, `--p-work`, `--p-ok` and `--p-err` — all
four now carry a `status.Group` after B134. And it must not read as *selected*, which removes
`--p-hero`: hero is the fill of `.chip.on` and the ink of `.ptabs .on`, so a hero ring around an
unselected chip would say the opposite of what is true.

That leaves the neutrals.

| Candidate | vs `--p-bg` | vs `--p-card` | vs `--p-elev` | Verdict |
|---|---|---|---|---|
| `--p-ink` `#f7f8f8` | 18.73:1 | 17.91:1 | 17.19:1 | **Chosen.** |
| `--p-ink2` `#8a8f98` | 6.14:1 | 5.87:1 | 5.63:1 | Rejected: it is the resting colour of chip labels, row summaries and the toast suffix. A ring in it reads as a border, not as focus. |
| `--p-hero` `#53ce7c` | 9.95:1 | 9.52:1 | 9.13:1 | Rejected: means selected/live. |

**Decision: `--p-ink`, 2 dp stroke, `space_2` offset, corner radius equal to the focused component's
radius plus 2 dp** so the ring stays concentric. One ring, not Material's two-tone pair — the two-tone
ring exists to survive both light and dark backgrounds, and this product is dark-only (§7 of the
requirements makes light mode a Phase C non-goal). White is the loudest neutral the skin owns, it
carries no state and no brand, and at 17:1 on the worst of the three surfaces it cannot be missed.

The residual: on the hero-filled primary CTA, a white ring sits directly against the hero fill at
1.88:1, which is visible but soft. The `space_2` offset is what saves it — the ring is separated from
the fill by 2 dp of `--p-elev`, so it is read against the sheet, not against the button.

### 1.2 The scrim and the grab handle

Neither has a near token, and they fail in opposite directions.

**Scrim: `--p-bg` at 70% (`#B308090A`).** The mock uses `rgba(0,0,0,0.5)`. Pure black is not in the
palette; `--p-bg` `#08090a` is the palette's floor and is within a hair of it, so deriving the scrim
over `--p-bg` keeps the cell inside the token set at no visual cost. The alpha moves from 50% to 70%
for a reason: at 50%, `--p-ink` body text behind the scrim resolves to `#7F8081`, which is roughly the
brightness of `--p-ink2` — still readable, still competing with the sheet. At 70% it resolves to
`#505151`, legible as shape and not as content, which is what a scrim is for. Cards behind it resolve
to `#FF0A0B0C`, three units above the ground.

No blur. Substrate's only declared blur is the one paired with `--p-tabbg`, and adding a second
blurred surface invents a light effect the skin does not have. `minSdk 33` makes it free, which is not
a reason to spend it.

**Grab handle: `--p-ink3` `#62666d`.** The mock's `#48484a` sits at 1.86:1 against its `#1c1c1e`
sheet. Reproducing that *ratio* on `--p-elev` needs roughly `#444547`, and the palette has nothing
there. The two honest candidates:

| Candidate | vs `--p-elev` | Note |
|---|---|---|
| `--p-hair` `#23252a` | 1.19:1 | Rejected. It is the structural line colour, and at 1.19:1 a 5 dp bar is effectively invisible. |
| `--p-ink3` `#62666d` | 3.17:1 | **Chosen.** The token for recessive marks (section labels, agent names, metadata). No alpha arithmetic, and it clears the 3:1 non-text floor, which matters if drag-to-dismiss ships. |
| `--p-ink3` at 55% (`#8C62666D`, over `--p-elev` -> `#FF3F4246`) | 1.81:1 | Recorded fallback. Matches the mock's weight almost exactly. Take this if review finds the flat handle too loud. |

Radius `--p-dot-r` 4 dp on a 5 dp-tall box degenerates to a full round cap — the second instance of
the degeneracy PB-DS-4 already records for the status dot, and worth noting there so it does not read
as a coincidence.

### 1.3 The disabled / stale CTA pair: `--p-hair` fill, `--p-ink3` ink, no bloom

The approval sheet's primary CTA goes dead when the daemon-side request expires. The mock uses
`#39393d` on `#8e8e93` (3.53:1). Substrate has no disabled values at all.

**Decision: fill `--p-hair` `#23252a`, ink `--p-ink3` `#62666d`, and `--p-cta-fx` removed.** The
removed bloom is the load-bearing part. Substrate's stated rule is *nothing glows unless it is alive*,
and an expired request is definitionally not alive; a disabled button that kept its 18 dp phosphor
halo would contradict the skin's one explicit rule about light.

The pair resolves to 2.66:1, below the 3:1 UI floor, **by intent**. WCAG 1.4.3 exempts inactive
controls, and PB-DS-12's floors are written for interactive elements — this button is not clickable
and not focusable. The check that matters is the one against its neighbours: the tertiary
`View session first` button directly below it carries `--p-ink` on `--p-card` at 17.91:1. Dead and
alive are 6.7x apart in the same stack, which is the discrimination that has to hold.

The state's *meaning* is carried by the stale note, not by the greyed button: `Body.Secondary` in
`--p-att`, centred, `space_10` above it. `--p-att` because Substrate already binds the expiry
countdown (`.bind .x`) to it, so the colour that was counting down is the colour that says it ran out.
The rejected alternative was ink `--p-ink2` (4.72:1) — legible, but close enough to the tertiary
button's register to read as merely another enabled control.

### 1.4 Badge versus live counter: ship both, because they count different things

The mock puts a red count badge on the Inbox tab. Substrate's artifact instead puts a phosphor
`3 LIVE` counter in the nav header and has no badge. Shipping both as drawn *is* a contradiction: two
counters, two colours, two definitions, both about the same list, both on screen at once.

They are not, however, the same instrument.

- The **tab badge** is the cross-screen attention carrier. It counts `NeedsInput` only, and it is the
  one thing that survives when the user is on Activity or Settings. Deleting it costs the
  product its premise — *see the moment an agent needs you* — everywhere except one tab.
- The **header counter** is the in-context liveness readout. It counts what is running. It answers
  "how much is in flight", never "what needs me".

**Decision: ship both, with the badge recoloured to `--p-att` and the two counts made disjoint in
meaning.** Red is retired here on semantics, not on taste: `--p-err` means denial, failure and
destruction in this product, and a session waiting for a human is none of those. `NeedsInput` is
`--p-att` in the row dot, the row rail and the row border already; the badge is the fourth site of the
same state and takes the same token. Ink is `--p-hero-ink` (8.79:1 on att) — Substrate defines exactly
one ink-for-saturated-fills token, and inventing a second rule ("hero fills take hero-ink, attention
fills take the ground colour") is a rule nobody remembers. `--p-bg` at 9.34:1 was the alternative; the
two are indistinguishable at 10 sp and consistency wins.

The counter keeps Substrate's spec verbatim (`Label.Live`, `--p-hero`, mono, 0.07em tracking,
right-aligned). What its arithmetic *is* remains open — see §8.1.

---

## 2. The reuse rule that shrinks the rest of the work

Before the table: three findings collapse most of the remaining 24 into components that already
exist.

**Substrate gives affordances chrome, not colour.** The mock paints every text action in
`var(--accent)` — the back button, the "Terminal" action, the composer's voice button, the read-only
note's `[Take control]`, the pairing CTA. That is the documentation page's amber, and there is no
product token to swap in, because Substrate never uses coloured text as an affordance. Its one
tertiary control, `.a2-more`, is a *bordered surface*: `--p-card` fill, 1 dp `--p-hair`, `--p-ink`
label. So every accent-text affordance in the mock becomes either a bordered control or a plain
`--p-ink` glyph. This deletes the accent leak without inventing a colour, and it happens to fix
PB-DS-12 as well: an inline text span cannot carry a 48 dp touch target, and three of these were
inline spans.

**Substrate's destructive idiom is a tinted fill, not an outline.** `.a2-no` is `--p-err` at 13% with
`--p-err` ink and no border. The mock also has an outline-destructive (`.rev`, 1 dp err at 50%) and an
err-bordered container (`.kills`). Shipping a tinted-fill button *and* an outline button for the same
meaning is two idioms for one thing. The revoke button becomes `.a2-no` at chip metrics; only the
container keeps a border, and it takes Substrate's own charged-container recipe (see §4).

**All floating chrome is opaque except the two bars that carry `--p-tabbg`.** The mock makes the
banner, the toast and the quick-reply chips translucent (0.95 / 0.95 / 0.9) with 18-20 px blurs. Over
Substrate's surfaces that alpha is nearly a no-op — a card behind a 95% `--p-elev` banner shifts the
composite by under one unit per channel — but over *bright* content it is not: `--p-ink` text at 5%
adds 12/255, so what the banner actually does is let ghosted text bleed through it, blurred. That is
the iOS glass aesthetic, and it is off-idiom for a skin whose stated recipe is flat surfaces with 1 px
hairlines doing all the depth work. Banner, toast and quick chips go opaque, and the app loses two
blur layers. The tab bar and the composer keep `--p-tabbg` and one shared 16 dp blur, because there
the translucency is doing real work: a hero chip or an ink text line scrolling under the bar shows
through at 12% as a visible tint, which is the effect the token was pinned for.

---

## 3. The derivation table

25 rows: the 24 components enumerated in PB-DS-7, plus the scrim, which the same requirement names in
its prose but omits from the list.

| # | Component | Mock class | Surface | Border | Radius | Spacing and metrics | Type / ink | States, motion, notes |
|---|---|---|---|---|---|---|---|---|
| 1 | Toast | `.toast` | `--p-elev` (`#FF141516`), opaque — mock's 95% dropped (§2) | 1 dp `--p-hair` (mock 0.5 px; §6) | `--p-card-r` 9 (mock 12) | padding `space_10` x `space_16`; bottom `toast_bottom` = `tabbar_height` + `space_18` = 92; centred | body `Body.Message` / `--p-ink`; mono suffix `Mono.CodeSmall` / `--p-ink2` | `--p-card-fx` key-light as on every card; no drop shadow (banned). 3200 ms then hidden, no transition. Announce via an accessibility live region independent of the visual lifetime (§8.7) |
| 2 | Push banner | `.banner` | `--p-elev`, opaque — mock's 95% + blur 20 dropped (§2) | 1 dp `--p-hair` (mock 0.5 px) | `--p-sheet-r` 14 (mock 18) | padding `space_12` x `space_14`; side inset `space_10`; top `banner_top` = `screen_top` + `space_6` = 60 | source line `Body.Secondary` / `--p-ink2` with app name `Mono.InlineStrong` / `--p-ink`; message `Title.Row` / `--p-ink`, inline project `Mono.InlineStrong` / `--p-ink` | translateY(-(own height + `banner_top`)) -> 0, 350 ms `cubic-bezier(0.32,0.72,0,1)`; auto-dismiss 6000 ms. One of the two motions B134 keeps. Mock's -130 px is a magic number; derive from measured height. Tap opens the approval sheet |
| 3 | Badge | `.tabbar .badge::after` | `--p-att` (`#FFF1A10D`) — mock `#ff453a` retired (§1.4) | none | `--p-chip-r` 8 = half the 16 dp height, so it renders a pill | padding `space_2` x `space_6`; height 16; anchored to the tab icon's top-right, offset `space_6` right / `space_4` up (mock's `right: 24%` breaks under font scaling) | `Mono.Agent` (10 sp / 600 / mono) / `--p-hero-ink` (`#FF06150C`), 8.79:1 | Shown only when the `NeedsInput` count is non-zero; >=100 renders `99+`. Content description "N sessions need you" |
| 4 | Toggle | `.toggle` | track off `--p-ink3` at 40% (`#6662666D`, over `--p-card` -> `#FF303236`, 1.48:1 — the mock's own ratio); track on `--p-hero` (`#FF53CE7C`); thumb `--p-ink` (`#FFF7F8F8`) both states | none | **exception: pill** — radius = half the track (14) and half the thumb (12). Substrate's ladder has no pill step and a squared track reads as a checkbox | track 46x28, thumb 24, inset `space_2`, travel 18; touch target >=48 with the visual unchanged (PB-DS-12) | none | On = `--p-hero`, not `--p-ok`: after B134 `--p-ok` carries `ReadyForReview`, and a control's on-state is not a status. `.chip.on` is the precedent — hero fill is what "engaged" looks like in this skin. 150 ms; covered by the `ANIMATOR_DURATION_SCALE` check B134 requires |
| 5 | Grab handle | `.sheet .grab` | `--p-ink3` (`#FF62666D`), 3.17:1 on `--p-elev` — see §1.2 for the 55% fallback | none | `--p-dot-r` 4 on a 5 dp box -> full round cap (PB-DS-4's degeneracy, second instance) | 36x5; centred; `space_14` below | none | Decorative while dismissal is scrim-tap and the third button. If drag-to-dismiss ships it needs a 48 dp target |
| 6 | QR frame | `.qr` | **exception: scannability** — `#FFFFFFFF` tile, `#FF000000` modules. The one legitimate palette exception | none | `--p-card-r` 9 (mock 12) | 180x180; quiet zone `space_24` — **raised from the mock's 12**, which is 2.2 modules against the QR spec's 4-module floor at this scale | none | **The grain overlay is suppressed over this tile** (second exception, same reason): 5% soft-light noise on a 29-module symbol is a scan risk. Do not invert or dim; raise screen brightness while shown. Content description "Pairing QR code" |
| 7 | SAS display | `.sas` | none | none | none | row, gap `space_14`; margin `space_10` top / `space_4` bottom | `Display.SAS` 34 sp — **the one style this document adds to PB-DS-2's 18** (§5). Ink: **exception** — emoji glyphs carry the platform emoji font's own colour, no token applies | Content description "Verification emoji". Cross-vendor glyph divergence is an open question (§8.4) |
| 8 | Empty state | `.empty` | none (the ground shows) | none | none | padding 48 (2 x `space_24`) vertical, `space_24` horizontal; compact variant `space_24` all round (mock 60/30 and 24) | `Body.Message` / `--p-ink2`, centred | PB-DS-9 requires empty *sections* to render as sections: the `.plabel` stays and this block sits under it. Dropping the section is the obvious implementation and is wrong for a triage surface |
| 9 | Composer | `.composer` | bar `--p-tabbg` (`#E008090A`) + 16 dp blur (mock 18; one shared blur constant with the tab bar); field `--p-well` (`#FF060708`) | bar: 1 dp `--p-hair` top rule; field: 1 dp `--p-hair` | field `--p-card-r` 9 (mock: an 18 pill) | bar padding `space_8` x `space_14`, gap `space_8`, bottom `tabbar_height` 74 (mock 82), height `composer_height` 52; field padding `space_8` x `space_14`, visual height 36, touch target 48; glyphs 26, stroke 1.8 | field `Body.Message` / `--p-ink`; placeholder `--p-ink2` — **not `--p-ink3`, which is 3.50:1 on the well and fails the 4.5:1 text floor**; ink2 gives 6.21:1 | Voice glyph `--p-ink2`; stop glyph `--p-err`. Both 48 dp targets, both keep the mock's content descriptions. The field is a *well*, inverting the mock's lighter-than-its-bar field: `--p-well` is the token for recessed input, and against a translucent bar a `--p-card` field would barely separate |
| 10 | Quick-reply chips | `.qchips button` | `--p-card` (`#FF0F1011`), opaque — mock's 0.9 dropped (§2) | 1 dp `--p-hair` | `--p-chip-r` 8 (mock 14) | padding `space_8` x `space_12`; row gap `space_8`, row padding-x `space_14`, bottom `qchips_bottom` = `tabbar_height` + `composer_height` = 126; targets >=48 | `Label.Chip` / `--p-ink2` (mock 500 weight -> 600) | **This is the `.chip` component in a `floating` variant, not a new component** — identical but for the fill's opacity and the absence of an on-state. Horizontal scroll, no scrollbar |
| 11 | Machine row | `.mrow` | `--p-card` | 1 dp `--p-hair` (the mock's rows have no border at all; §6) | `--p-card-r` 9 (mock 14) | padding `space_12` x `space_14` (mock 13/14); top-line gap `space_8` (mock 9); meta `space_4` below | name `Title.Row` / `--p-ink` (mock 15/600); endpoint id `Mono.Agent` / `--p-ink3`; meta `Body.Secondary` / `--p-ink2` | `--p-card-fx` key-light. Presence dot 7 dp, `--p-dot-r`: online `--p-ok`, offline `--p-ink3` (mock `#636366`, dE ~ 1). **Flat in both states — no glow.** Nothing glows unless it is alive, and a reachable machine is not a running agent. Collision with `ReadyForReview` noted in §8.2 |
| 12 | Kill-switch panel | `.kills` | none (the ground shows, as in the mock) | 1 dp `color-mix(--p-err 36%, --p-hair)` = `#FF723B41` — Substrate's own `.prow.attention` border recipe with `--p-err` substituted | `--p-card-r` 9 (mock 14) | margin `space_8` top / `space_14` sides; padding `space_12` x `space_14`; gap `space_10` | title `Title.Row` / `--p-err`; subtitle `Body.Secondary` / `--p-ink2`; inline command `Mono.InlineStrong` / `--p-ink` | **AMENDED 2026-08-01: the trailing control does NOT ship, and this is not a styling decision.** This row said "trailing control is row 4", which is the toggle. `App.KillSwitchEngaged` is **read only by design** — `protocol/server.go handleRemoteSetControl` refuses the remote tier before the backend is consulted, on the stated grounds that *a remote device must never re-enable a switch its owner turned off*. A toggle here is a control that cannot act. The panel ships as state, and the phone's real destructive action is Revoke in row 13. Everything else in this row — the `--p-err` border mix, the geometry, the type — is unchanged and correct. Same class as ADR-007 B135's three rows: the drawing specified an affordance the product must never have. An opaque mix, not an alpha, so it composites identically over any surface. **A 2 dp `--p-err` rail was considered and rejected**: the rail is Substrate's marker for "this row needs you", and a settings container does not |
| 13 | Paired-device row | `.devrow` | `--p-card` | 1 dp `--p-hair` | `--p-card-r` 9 (mock 14) | padding `space_12` x `space_14`; gap `space_10` | name `Title.Row` / `--p-ink`; fingerprint `Mono.Agent` / `--p-ink3` | `--p-card-fx` key-light. **Revoke takes the `.a2-no` treatment at chip metrics**: fill `--p-err` at 13% (`#21FF6369`, over `--p-card` -> `#FF2E1B1C`), ink `--p-err` (5.3:1 on that fill), no border, `--p-chip-r` 8, padding `space_8` x `space_10`, `Label.Chip`, 48 dp target. The mock's outline-destructive is dropped (§2) |
| 14 | Activity row | `.arow` | `--p-card` | 1 dp `--p-hair` | `--p-card-r` 9 (mock 12) | padding `space_10` x `space_12` (mock 11/13); gap `space_10` | timestamp `Mono.Meta` / `--p-ink3`; body `Body.Message` / `--p-ink`; inline emphasis `Mono.InlineStrong` / `--p-ink` | `--p-card-fx` key-light. **The timestamp column is wrap-content, not the mock's fixed 52 dp** — a fixed column clips at the 1.3x font scale PB-DS-12 requires |
| 15 | Settings row | `.setrow` | `--p-card` | 1 dp `--p-hair` | `--p-card-r` 9 (mock 12) | padding `space_12` x `space_14`; gap `space_10` | label `Title.Row` / `--p-ink`; sublabel `Body.Secondary` / `--p-ink2` (mock 11.5 -> 12, and 12.5 since owner ruling R1 of 2026-08-09 put it on the body rung) | `--p-card-fx` key-light. Trailing control is row 4, or status text `Label.CardHead` / **`--p-hero`** — not `--p-ok`: "active" is a liveness statement, which is what hero means, and `--p-ok` now carries `ReadyForReview`. The whole row is one >=48 dp target when it carries a toggle **AMENDED 2026-08-09 (agents-tracker-2pnu F5 / agents-tracker-zecs): the STATUS-TEXT trailing form does not ship, and the toggle form is unchanged.** The kit shipped it as `statusLabel` and it acquired no production caller in any release. Its only candidate was inventory C6's `End-to-end encryption` row, which `SettingsPanelScreen` records as deliberately unbuilt — "the claim is true of the transport by construction, and that is precisely why it cannot be rendered as a live status: nothing on this handset READS it, so 'active' would be a word printed unconditionally". A factory whose only justification rests on a row the screen model refuses to build is retired rather than left for the next reader to rediscover. The ink rule above is preserved verbatim because it is the design record and it is what a future spend has to obey; the trailing SLOT is untouched (`settingsRow` still takes any `View`, and takes a toggle on every preference row and a `denyChip` on the pairing row) |
| 16 | Streaming caret | `.stream-caret` | `--p-hero` (`#FF53CE7C`) — the terminal-peek foreground token; the caret is a terminal cursor | none | **exception: 0** — a block cursor is square; any radius token rounds it | 7x14; baseline offset -2 | none | Blink 900 ms `steps(2)`, opacity 1 <-> 0.35. The one liveness animation B134 keeps; suppressed when `ANIMATOR_DURATION_SCALE == 0` |
| 17 | Lock screen | `.lock` | **exception: platform-owned surface.** Android draws the lock screen; the app supplies a `Notification`. No token applies to the gradient, the 74 sp/200 clock, the notification chrome or the hint | — | — | — | — | The mock's `.lock` block is simulation scaffolding and ships as nothing. What the product owns: the channel and its importance, the copy (title, body, the `Decrypted on-device - relay saw ciphertext` subtext), the monochrome small icon (the system tints it), the tap intent, and the lock-screen visibility — see §8.5, which is a decision someone must take before push ships |
| 18 | Pairing scaffold | `.pair` | `--p-bg` | none | none | padding `space_10` vertical x `space_24` horizontal; title margins `space_18` top / `space_8` bottom; body margin-bottom `space_18`; command well margin-bottom `space_16`; waiting line `space_12` top | step title `Display.NavTitle` / `--p-ink` (mock 21 -> 27, and 22 since owner ruling R3 of 2026-08-09: the pairing step has no nav header, so its title *is* the screen title, and it moves with the display rung); body `Body.Message` / `--p-ink2`, `maxEms=30` (mock `max-width: 30ch`); waiting `Body.Secondary` / `--p-ink2` | Command line reuses the `.cmd` mono well verbatim: `--p-well`, 1 dp `--p-hair`, `--p-card-r` 9, padding `space_10` x `space_12`, `Mono.Code` / `--p-ink` — so every mono block in the app is one component (mock used a card fill at radius 8). CTA is `.a2-ok` unchanged (`--p-cta-bg` / `--p-cta-ink` / `--p-cta-fx` / `--p-btn-r`), `Label.Button`, padding `space_12` x `space_24`, hugging width |
| 19 | Status bar | `.ptime` / `.sbar` | **exception: platform-owned.** The clock and the status glyphs are the system's; the mock draws them because a browser has no status bar | — | — | — | — | The product owns three things: `--p-bg` drawn edge-to-edge behind the bar, `appearanceLightStatusBars = false`, and the inset. **`screen_top` 54 is an iPhone notch constant** — on Android it must come from `WindowInsets.statusBars`, with 54 as the design-time preview value only. `screen_bottom` 76 is the same problem against the gesture-nav inset |
| 20 | Screen scaffold | `.pscreen` / `.screen` | `--p-bg` | none | none | padding top `screen_top` (or the real inset), bottom `screen_bottom` (or inset + `tabbar_height`); vertical scroll; **side air `space_12` per destination, spent exactly once (ADDED 2026-08-09, owner ruling on agents-tracker-nx44.10)** — the scaffold itself pads no side, and neither does a destination's own column: six components already hold themselves off the glass (`.nav` and `.sect` at 18, `.prows` at 12, row 12 at 14, row 22 at 18, row 8 at 24) and what has none is §4's notice line, `.acts2`, a loose CTA and row 9's field. The composing column gives those the step the Inbox's row container already spends, per child, via `ui/kit/ScreenColumn.kt`'s `screenAir` — a padding on the column would add 12 to the seven that already pay, which is agents-tracker-2pnu F2's doubling in a second pair of columns. **AMENDED 2026-08-09 (same ruling, second reading): the RULE IS ABOUT ELEMENTS AND NOT ONLY ABOUT TEXT, and this cell counted a padding as a margin.** It read "nine components ... (rows 11/15 at 14)": rows 11 and 15 spend `space_14` INSIDE the card, so the label clears the floor and the `--p-card` fill and its `--p-hair` border underneath do not. On the Inbox that never showed, because `.prows` insets the cards and the card keeps its own 14; on Settings, which places rows 11 and 15 on the ground, both ran edge to edge. They are in the second group — `settingsPanelView` spends the step on each row, once, and the card keeps its 14 inside, which is `.prows`' arrangement reproduced where there is no list. The `Approval sheet pull-quote` row's sheet is the same correction: `sheetSurface` rounds all four corners and the app hosts it inline, so it is a card in a column and not the `Docked bottom sheet` row's full-bleed form. What stays full-bleed, by name: the tab bar and row 9's composer bar (one construction — a fill and a 1 dp `--p-hair` rule across the whole width) and §4's sync STRIP ("radius none and no side inset") | none | Scrollbars off (`android:scrollbars="none"`, matching `scrollbar-width: none`). API 31+ overscroll is the stretch effect, which is acceptable; the pre-31 glow would not be, and `minSdk 33` retires it. The grain overlay (row 21) sits above it, non-interactive |
| 21 | Grain overlay | `.grain` | **exception: the noise raster is an asset, not a colour.** `feTurbulence` output is implementation-defined, so it is pre-rendered once at 140x140 and checked in (PB-DS-5) | none | none | 140x140 tile, full-bleed | none | `--p-grain` alpha under `BlendMode.SOFT_LIGHT` (unconditional at `minSdk 33`). *(AMENDED 2026-08-07, ADR-009 D3/D4.3: the cell read `--p-grain` 0.05; the token is now 0.04 and the tile is re-rastered warm-neutral. The NUMBER is deleted rather than restated, because a row that names the token and then writes its value out is a second copy of the origin -- `ui/kit/Kit.kt` cites `origin: --p-grain opacity` and the Go gate recomputes it from `tokens.json`.)* Applied over every surface **except the QR tile** (row 6): the overlay is the screen scaffold's foreground, and the pair-only screen that draws the QR replaces the scaffold outright rather than sitting inside it. Non-interactive |
| 22 | Read-only note | `.ro-note` | none | none | none | margin `space_10` top x `space_18` sides (mock 10/20); centred | `Body.Secondary` / `--p-ink2` | **`[Take control]` becomes a standalone tertiary button below the note, not an inline span**: an inline span cannot carry a 48 dp target (PB-DS-12), and the mock's inline button was painted in the retired doc-chrome accent. It takes `.a2-more` unchanged — `--p-card`, 1 dp `--p-hair`, `--p-btn-r` 9, `Label.Button` / `--p-ink`, padding `space_12`, min 48 |
| 23 | Focus ring | `:focus-visible` | `--p-hero` (`#FFC9A876`) — 8.74 / 8.22 / 7.69:1 on `--p-bg` / `--p-card` / `--p-elev` | 2 dp stroke | focused component's radius + 2 | offset `space_2` | none | Applies to every focusable (PB-DS-12). *(AMENDED 2026-08-07, ADR-009 D3: champagne, not linen. The Substrate cell read `--p-ink` (`#FFF7F8F8`) and rejected `--p-hero` on the ground that hero meant SELECTED; in Obsidian the accent means "you" and focus is one of the five things it says, so the rejection's premise is gone.)* Rejected: `--p-ink` and `--p-ink2` (neutrals — a ring in either reads as a heavier hairline over the warm ladder), all four status tokens (they mean state; `--p-att` value-aliases `--p-hero` by ADR-009 D6 and that is the alias showing, not the ring meaning a status). Argued in §1.1 |
| 24 | Disabled / stale CTA | `.sheet.stale .a-ok` | fill `--p-hair` (`#FF23252A`); ink `--p-ink3` (`#FF62666D`) — 2.66:1, below the UI floor by intent | none | `--p-btn-r` 9 | padding `space_12` | `Label.Button` / `--p-ink3` | **`--p-cta-fx` removed** — nothing glows unless it is alive. Not clickable, not focusable, so WCAG 1.4.3's inactive-control exemption applies. Paired with the stale note: `Body.Secondary` / `--p-att`, centred, `space_10` above. Argued in §1.3 |
| 25 | Scrim | `.scrim` | `--p-bg` at 70% (`#B308090A`; over `--p-card` -> `#FF0A0B0C`; `--p-ink` text behind it -> `#FF505151`) | none | none | full-bleed, below the sheet and above everything else | none | No blur — Substrate's only blur is the one pinned to `--p-tabbg`. Tap dismisses. For screen readers the *sheet* exposes the dismiss action; the scrim is not a focusable target. Argued in §1.2 |

---

## 4. Adjacent derivations the same screens need

Not in PB-DS-7's list of 24 and not in Substrate's 14, but the eight screens cannot be built without
them. Same cell rules.

| Component | Mock class | Derivation |
|---|---|---|
| Notice line | — (neither artifact draws one) | *(ADDED 2026-08-08, agents-tracker-ksvb.4.)* **The sentence a screen says about its own state**, and the one block in this product that was specified nowhere. It shipped as sixteen bare `TextView`s across eight screens carrying no `TextAppearance` at all — which is not the absence of a decision, because the platform default is ~14 sp: every warning, stale mark and refusal line in the app was set LARGER than every body style in §7's ladder, on top of the block it was qualifying. Type `Body.Secondary` / `--p-ink2`, which is row 22's pair and is chosen for row 22's reason: this is prose a user is meant to read, and `--p-ink3` is 3.17 to 3.50:1 on every surface here, under the 4.5:1 floor. **Error variant: `--p-err` and nothing else moves** — same style, same box — because what changes is who is speaking, not how loudly. A refusal the machine sent is a different kind of sentence from a state the screen is reporting, and ADR-009 D6 keeps `--p-err` for failure; the variant is a parameter and not a second component, for the reason §4's In-card CTA pair gives about `bloom` (the site differs, not the component). Surface, border and radius all `none`: the ground shows through, as it does for rows 8 and 22. **No margin, no padding and no gravity of its own**, which is the one place this differs from row 22 and the difference is deliberate. Row 22's note is centred and inset `space_18` because it sits under a full-bleed well and has to hold itself off the edges of one; this line appears in eight different stacks — under a nav header, above a mono well, between a disclosure and a redirect control, at the bottom of a pairing step — and a component that carried one screen's air would be wrong in the other seven. The air is the composing column's, exactly as it was when these were bare views |
| Drill-down nav header | `.navhead` | Padding `space_6` top / `space_18` sides / `space_12` bottom (mock 6/20/12), gap `space_10`. Back control: 24 dp chevron glyph, stroke 1.7, `--p-ink`, plus a label in `Body.Message` / `--p-ink2`, 48 dp target — **not the mock's 16/400 accent text** (§2). Title on the DISPLAY RUNG / `--p-ink` — **AMENDED 2026-08-09 (owner ruling R4, ADR-012 phase 2 P4): the title takes the same rung as a root screen's, which is 22.** It said `Title.Sheet` (mock 16/700 -> 15.5/650), which was a 43 percent drop for one navigation step, taken because `Title.Sheet` was the only style below the display size rather than because anyone chose it. A screen is a screen; depth is the back chevron's job. Right-hand action is a `floating` chip (row 10), not accent text |
| Row pressed state | `.srow:active` | `--p-elev` (`#FF141516`). Substrate's own words: elevation is one ladder step lighter, never a shadow — and a press is a momentary elevation. It is subtle (+5 units against `--p-card`, where the mock moved +16). Recorded alternative if review finds it invisible: `--p-ink` at 4% (`#0AF7F8F8`, over `--p-card` -> `#FF18191A`), which is the `--p-card-fx` key-light recipe applied to the whole fill. `--p-hair` was rejected — the row would merge with its own border |
| Approval card variant | `.card.approval` | Border 1 dp `color-mix(--p-att 36%, --p-hair)` = `#FF6D5220` (Substrate's `.prow.attention` recipe, unchanged). Head text `--p-att`; **head divider stays 1 dp `--p-hair`** — the mock's att-at-30% divider is dropped, one attention accent per card is enough. Footer padding `space_10` x `space_12`, gap `space_8`. Post-approval replacement text `Label.CardHead` / `--p-ok`, padding `space_4`, copy `approved - logged` |
| In-card CTA pair | `.card .cfoot button` | Two buttons, equal weight, gap `space_8`, padding `space_10`, `--p-btn-r` 9, `Label.Button`. Approve = `.a2-ok` **without `--p-cta-fx`**: the card sets `overflow: hidden`, so an 18 dp bloom inside it is clipped at the card edge and looks broken. The bloom belongs to the full-width sheet CTA. Deny = `.a2-no` unchanged |
| Status dots, B134 mapping | `.dot.g-*` | `NeedsInput` `--p-att` + glow `0 0 9 #B3F1A10D`; `Working` `--p-work` + glow `0 0 9 #8C00C2D7`; `ReadyForReview` `--p-ok`, no glow; `Completed` `--p-ink3`, no glow; machine online `--p-ok`, offline `--p-ink3`. All 7 dp, `--p-dot-r`. The mock's `pulse 1.6s` working animation does not ship (B134). Both glows are `Paint.setShadowLayer(9, 0, 0, colour)` on a software layer, the same conversion as `--p-cta-fx`. **AMENDED 2026-08-09 (owner ruling R8, ADR-009 D6's resolved OPEN CONFLICT): `Working`'s glow retires.** The two hexes above are the ORIGINAL directions artifact's `color-mix()` reading (70%/55%) and are left as the historical record; the owner-signed maquette draws only `NeedsInput`'s glow, as a literal `rgba(201,168,118,0.5)` — one glow, one meaning. `Working` renders flat now, same as `ReadyForReview` and `Completed`; its liveness is the workbar's alone |
| Docked bottom sheet | `.sheet` | `--p-elev`, 1 dp `--p-hair`, `--p-sheet-r` 14 **top corners only** (mock 22), padding `space_14` sides and top, bottom `space_14` + the navigation-bar inset (the mock's 34 was the iPhone home indicator). translateY 100% -> 0, 350 ms `cubic-bezier(0.32,0.72,0,1)`. Substrate's `.sheet2` is an inline card with four rounded corners because the directions page has no phone chrome to dock it to; the product ships the docked form |
| Approval sheet pull-quote | `.sheet` in `docs/research/obsidian-maquette.html` (frame 2) | *(ADDED 2026-08-07, ADR-009 D4.4 + obsidian-migration-plan O6.1.)* **What the sheet SAYS, in the order the maquette draws it.** The row above is the sheet as an object; this is its contents, and the whole derivation is an ORDERING. Substrate's `.sheet2` put an `h4` question first and the machine/project context under it; the maquette reverses them and grows the question into a pull-quote. A sheet is read top-down, and the first line a person meets should be *what they are being asked*, not who is asking — the context is what they check second, once they know the question is worth checking. **No new information: three parts before, three parts after.** Surface: `sheetSurface` unchanged (D4.4's one vertical gradient `--p-sheet-hi` → `--p-sheet-lo`, 1 dp `--p-hair`, `--p-sheet-r`, the strong `--p-lit-fx` edge). Padding `space_14` x `space_14`, the docked sheet's own value reused rather than re-derived (§2). **Context line** `Mono.Meta` / `--p-ink3`, uppercase — `isAllCaps` and not an uppercased string, `sectionLabel`'s own ruling, so the accessibility tree still reads a phrase. `Mono.Meta` is `.sheet2 .ctx`, which is literally this line one skin ago; the maquette adds the caps and the tracking is the style's. **Question** on the DISPLAY RUNG, spelt `Display.NavTitle` / `--p-ink`. **AMENDED 2026-08-09 (owner ruling R5, ADR-012 phase 2 P5): the size is ratified as it renders and the ROLE is renamed.** The question is the moment's headline — the blocking thing a person is being asked — so it takes the rung a screen title takes, deliberately. What this cell said before argued it from the ladder's contents instead: "That is what 'larger type, `--p-display-wt`' resolves to *in this app's ladder*: `Display.NavTitle` is the only style in the scale at `--p-display-wt` above `Title.Sheet`'s 15.5" — a consequence of the ladder rather than a decision about the sheet, and untrue the moment R1 moved `Title.Sheet` to 14. The maquette's own 19 px is still not a size this scale carries: it is drawn on a 300 px gallery phone. The type ladder stays Substrate's on purpose — `android/gate/s22b_designsource_test.go` records why re-pointing nineteen sizes at the maquette is not a token migration's business. **Gaps** `space_10` under the context line, `space_12` above the well and above the actions, `space_10` between the two actions. **The well and the actions are SLOTS**, not built here: `monoWell` and `ctaButton` already exist and `pairingStep` sets the precedent for passing a kit-built view in. No grab handle and no scrim — rows 5 and 25 belong to the docked sheet, not to its contents |
| Scanner reticle | — (the mock draws no scanner) | The framing square over the camera preview: where to point the phone and how close to hold it. Ink: brackets `--p-hero` (`#FF53CE7C`), the token `android/design-tokens.tsv` glosses as brand *and live*, and a viewfinder is the most literal live surface this app has. **`--p-ink` is rejected on scannability, not on meaning** — row 6 draws the symbol this frames as a `#FFFFFFFF` tile, so a white reticle over a code held to fill it is a white line on white. The four status tokens are out for §1.1's reason (they mean state) and `--p-ink2` / `--p-ink3` are 5.87 and 3.30:1 against a *surface*, before a photograph is behind them at all. Geometry: frame 180, which is the size row 6 draws this same symbol at, centred and clamped to the preview when the preview is the smaller of the two; stroke 2, `.prow.attention`'s rail weight rather than the 1 dp hairline, because what is behind it is a moving image and not a flat surface; arm 24, so each corner paints `space_24` along both its edges and the middle of every edge stays open — a closed rectangle is a border, and what says *aim here* is four corners. Radius `--p-card-r` 9: row 6's, because this frames row 6's tile. **It is not a control.** No fill, no click, no focus, and no touch target of its own — it is a `Drawable` foreground rather than a view precisely so it cannot acquire one, since a view over a live preview that could take a tap is what PB-SEC-12 clause 1 exists for. **And it does not move**: ADR-007 B134 keeps three motions and this is not among them. A decode-confirmation flash was specified with it and is not built — §8.9 |
| Sync status pill and strip | — (neither artifact draws either) | *(ADDED 2026-08-09, agents-tracker-nx44.2.)* **What the app says about whether the screen is current**, in the place a reader is already looking. It replaces a stack of up to four `Body.Secondary` sentences drawn above every destination — the transport's, PB-SYNC-7's write-hold, the machine's clock, the roster's completeness — which field test 3 photographed sitting over the nav title and pushing the whole app down. The status is RANKED (broken > quiet > syncing > live) and **live draws nothing at all**, which is the same conditional-notice discipline §4's notice line and row 11's presence line already hold. Two components. **The pill** is `.chip` (row 10's floating variant: `--p-card`, 1 dp `--p-hair`, `--p-chip-r` 8) with a leading 7 dp `--p-dot-r` mark and one upper-case word, padding `space_8` x `space_10` and gap `space_6` — row 13's revoke-chip metrics, reused rather than re-derived (§2) — `Label.Chip` / `--p-ink2` (6.21:1 on the chip fill), min 48 dp height because it is a control (PB-DS-12). The mark takes the STATUS tokens at their existing meanings: `--p-work` syncing, `--p-att` quiet, `--p-err` broken. **The word carries the state and the colour is the second signal, not the only one** — the three labels differ in every state, and the tokens are indicator colours held to WCAG's 3:1 non-text floor rather than to a text floor, so a word painted in one would be prose taking an ink measured as a dot. **Flat, no glow**: §4's B134 mapping gave NeedsInput and Working a glow because they say a session is ALIVE; ruling R8 (2026-08-09) narrowed that to NeedsInput alone, and none of these three states is that. **The strip** is the escalation the broken state adds: `--p-elev` OPAQUE with a 1 dp `--p-hair` rule along its BOTTOM edge — row 9's and C1.4's top rule mirrored, because a bar's rule goes on the edge that faces the content — padding `space_10` x `space_18`, `Body.Message` / `--p-ink`, ONE line with the platform's truncation mark, min 48 dp because it is also a control. Radius none and no side inset: it is full-bleed chrome across the top of the app, not a floating block. **It is IN LAYOUT above the nav row and not an overlay**, which is the whole of its geometry: an overlay cannot be made not to overlap, only positioned so that it usually does not, and what field test 3 shows is a transparent banner over a title. A sibling above the nav in the same column cannot overlap by construction; the cost is its own height, taken from the destination, for one state out of four. **The ink is `--p-ink` and not `--p-err`** — §4's notice line moves the ink for a refusal the machine sent, and this is a sentence a person must read to the end, `--p-err` on `--p-elev` is not a pair §9 declares, and the state is already in colour on the pill directly under it. One accent per statement |

---

## 5. Frame constants this document derives

PB-DS-1 declares three (`screen_top` 54, `screen_bottom` 76, `tabbar_height` 74). The overlay
positions need four more, each derived from the scale rather than transcribed from the mock's fixed
offsets (82 / 100 / 130 / 136), all of which were measured against an 82 dp tab bar that Substrate
shrank to 74.

| Constant | Value | Derivation |
|---|---|---|
| `composer_height` | 52 | 36 (field visual) + 2 x `space_8`. The field's 48 dp touch target fits inside it |
| `qchips_bottom` | 126 | `tabbar_height` + `composer_height` |
| `toast_bottom` | 92 | `tabbar_height` + `space_18` — preserves the mock's 18 dp clearance over a bar that is 8 dp shorter |
| `banner_top` | 60 | `screen_top` + `space_6` |

The chat list's trailing padding is `qchips_bottom` + the quick-chip row height + `space_8`, measured
at layout time; the mock's 130 dp spacer is a constant that breaks as soon as a chip wraps.

---

## 6. What moved when Substrate won

**Radii.** Every row in the app tightens. The mock's card family is 12-14; Substrate's is 9.

| Site | Mock | Substrate | Delta |
|---|---|---|---|
| Session / machine / device row, kill panel | 14 | `--p-card-r` 9 | -5 |
| Activity row, settings row, toast, QR tile | 12 | `--p-card-r` 9 | -3 |
| Sheet (top corners) | 22 | `--p-sheet-r` 14 | -8 |
| Push banner, lock notification | 18 | `--p-sheet-r` 14 | -4 |
| Quick-reply chip | 14 | `--p-chip-r` 8 | -6 |
| Composer field | 18 (pill) | `--p-card-r` 9 | pill lost |
| In-card and sheet buttons | 9 / 12 | `--p-btn-r` 9 | 0 / -3 |
| Toggle track, badge | 14 / 9 | pill exception / `--p-chip-r` 8 | see rows 3-4 |

**Padding.** PB-DS-1's rounding applies: 5->4, 7->8, 9->8, 11->10, 13->12, 15->14, 26->24. On top of
that this document moves three values that are off-scale entirely: the empty state's 60 -> 48
(2 x `space_24`), its 30 -> `space_24`, and the sheet's 34 bottom -> `space_14` plus the real
navigation inset.

**Borders.** Two changes, in opposite directions.

- *Rows gain a border.* The mock's `.srow`, `.mrow`, `.arow`, `.setrow` and `.devrow` have no border
  at all — they are bare `#1c1c1e` fills, and their depth comes from being lighter than the ground.
  Substrate's `.prow` has a 1 dp `--p-hair` border and the `--p-card-fx` key-light, because its depth
  comes from hairlines. Every row in this document therefore gains a border and an inset highlight it
  did not have.
- *Every 0.5 px rule becomes 1 dp.* Android's minimum stroke is 1 dp, and there is no sub-dp form
  worth having. The affected sites are the tab-bar top rule, the composer top rule, the banner border
  and the toast border. The doubling is partly offset by colour: `--p-hair` `#23252a` is *darker* than
  the mock's `rgba(84,84,88,0.5-0.6)`, which composites to roughly `#38383a` over these surfaces, so
  the perceived change is less than 2x. It is still visible on the tab bar and composer — two
  full-width rules stacked 52 dp apart — and it is accepted rather than worked around.

**Type.** See §7. Every mock size maps onto an existing Substrate style except one.

---

## 7. Type styles and the exception register

**Type reuse.** The design inventory lists 24 sizes the mock needs that Substrate never specified.
Twenty-three of them map onto the existing 18 styles; the mapping is below, and the only *addition* to
PB-DS-2's set is `Display.SAS`.

**THE SIZES IN THE `Takes` COLUMN ARE THE LADDER AS IT STOOD WHEN THIS MIGRATION WAS DECIDED, and
they are left as written (dated note, 2026-08-09).** This table records which app style each mock
size maps onto and what that move cost at the time; owner ruling R1 has since consolidated the
ladder onto five rungs, so a style's size is ADR-012 phase 2's rung table and not this column. The
`Takes` cells are still true about WHICH STYLE — that is what the table decides — and re-typing
sixteen numbers into a record of a past decision would make it a second, staler copy of the rung
table rather than the migration's own account.

| Mock size | Takes | Move |
|---|---|---|
| 21 (pairing title) | `Display.NavTitle` 27/650 | +6, and it becomes the screen title |
| 17 / 16 (sheet, drill-down title) | `Title.Sheet` 15.5/650 | -1.5, -0.5 |
| 15 (machine name, sheet CTA, pair CTA) | `Title.Row` 14/600 (rows) or `Label.Button` 13.5/600 (buttons) | -1, -1.5 |
| 14 (settings label, banner message, composer input, empty state) | `Title.Row` 14/600, or `Body.Message` 12.5/400 for prose and input | 0 / -1.5 |
| 13.5 / 13 / 12.5 (pairing body, activity row, card button, toast, machine meta, waiting) | `Body.Message` 12.5/400 or `Label.Button` 13.5/600 | <= -1 |
| 12 / 11.5 (ro-note, stale note, banner meta, cmdline, settings sublabel) | `Body.Secondary` 12/400 or `Mono.Code` 11.5/400 | 0 to +0.5 |
| 12/600 mono (status text, post-approval text, revoke) | `Label.CardHead` 10.5/600 mono, or `Label.Chip` 11/600 sans | -1.5, -1 |
| 11/500 mono (timestamps) | `Mono.Meta` 10.5/500 | -0.5. `Mono.Fine` 9.5 was rejected: PB-DS-12 already flags sub-10 sp |
| 10/500 mono (endpoint id, fingerprint) | `Mono.Agent` 10/600 | weight +100 |
| 10/700 (badge) | `Mono.Agent` 10/600 | weight -100 |
| 12/500 (quick chips) | `Label.Chip` 11/600 | -1, weight +100 |
| 74/200 (lock clock), 13/600 (status clock) | platform-owned, ship nothing | — |
| **34 (SAS)** | **`Display.SAS` 34 sp / 400 / sans — new** | PB-DS-2's style set must grow by one, and its bidirectional gate will fail until it does |

**Exception register.** Five, and no others exist in this document.

| # | Exception | Site | Reason |
|---|---|---|---|
| 1 | `#FFFFFFFF` / `#FF000000` | QR tile and modules (row 6) | Scannability. The only legitimate palette exception, named as such in PB-DS-7 |
| 2 | Grain suppressed | QR tile (row 6) | Same reason: 5% soft-light noise over a 29-module symbol is a scan risk |
| 3 | Pill radius | Toggle track and thumb (row 4) | Substrate's ladder has no pill step; a squared track reads as a checkbox |
| 4 | Radius 0 | Streaming caret (row 16) | A terminal block cursor is square |
| 5 | Platform-owned | Lock screen (17), status bar (19), emoji ink (7), grain raster (21) | The system draws it, or it is an asset rather than a colour. No token applies, and pretending one does would be an invention |

---

## 8. Open questions

Recorded rather than invented. Each has a recommendation; none is safe to guess silently.

**8.1 What does LIVE count?** The Substrate artifact renders `3 LIVE` over 1 `NeedsInput` + 2
`Working` + 1 `Done`, and it omits the `ReadyForReview` section entirely — so the artifact cannot
answer whether Ready counts. *Recommendation:* `NeedsInput + Working`, which reproduces the artifact's
arithmetic exactly and matches the plain reading of "live" as work in flight. A session waiting on a
human is not running. If Ready is included, the header count and the tab badge start overlapping in a
second place and the §1.4 separation weakens.

**8.2 `--p-ok` now means two things on the Inbox screen. STILL OPEN, and now permanent unless someone
acts.** B134 decision 1 was ruled FOR the rebinding on 2026-08-01 (ADR-007 B137), so the collision
below is no longer a side effect of a contested decision — it is a property of the shipped palette,
tracked as `agents-tracker-k9k`. The ruling records it as a cost it did not settle. B134 moved
`ReadyForReview` to `--p-ok`,
and Substrate's `.chip .pd` machine-presence dot was already `--p-ok`. On the Inbox both are visible
at once: a green 5 dp dot inside a scope chip (machine online) and a green 7 dp dot at the head of a
row (ready for review). *Assessment:* low risk — different sizes, different containers, and the chip
dot is always immediately left of a machine name. *But it is a real collision introduced by B134 and a
reviewer should confirm it.* Re-binding presence to `--p-hero` does not help: hero `#53ce7c` and ok
`#4cc38a` are neighbours in the same green family. The only clean fix would be dropping the presence
dot for a text state on the chip, which changes the chip's structure.

**8.3 Grab-handle weight.** Flat `--p-ink3` at 3.17:1 (chosen) versus `--p-ink3` at 55% at 1.81:1
(the mock's weight). The choice depends on whether drag-to-dismiss ships. Cheap to flip, and the
derivation for both is in §1.2.

**8.4 The SAS emoji do not look the same on both ends.** The pairing step says *your Mac shows the
same four symbols*, and it is comparing Noto Color Emoji on Android against Apple Color Emoji on
macOS. U+1F52C (microscope) and U+1F30C (milky way) in particular are visually different pictures of
the same codepoint. A verification UI that asks a human to compare two pictures is weakened by this.
*Recommendation:* label each glyph with its Unicode name beneath it, so the comparison is over names
and pictures rather than pictures alone. This needs a decision before pairing ships, and it is a
security-relevant one — the SAS is what makes the channel authenticated.

**8.5 Lock-screen notification visibility.** The mock's lock notification shows the project name
(`quanthome/api needs your decision`). At `VISIBILITY_PUBLIC` that leaks project names to anyone
holding the phone, which sits oddly beside the same notification's own subtext,
`Decrypted on-device - relay saw ciphertext`. *Recommendation:* `VISIBILITY_PRIVATE` with a public
form that says only `swarm needs your decision`. This belongs to whoever owns the push copy, not to
the design system, but it is a visible-surface decision and nobody else is holding it.

**8.6 Scope-bar scrolling.** Substrate's `.chips` is a non-scrolling flex row; the mock's `.scopebar`
scrolls horizontally. With two machines both work; with four the non-scrolling row clips. Multi-machine
is a Phase C non-goal (requirements §7), so the flat row is fine for Phase B — but the component
should be built scrollable from the start, because it is a behaviour and it costs nothing.

**8.7 Two accessibility measurements this document cannot make.** First, the composer field at
`Body.Message` 12.5 sp is the smallest editable text in the app; PB-DS-12 requires the layout to
survive a 1.3x font scale, and this is the row most likely to fail it. Second, the toast's 3200 ms
visual lifetime is shorter than a TalkBack announcement of its longest copy
(`Controller lease taken - generation 7 to 8`); the announcement must be a live region with a
lifetime of its own, not a side effect of the view being visible.

**8.8 One row of copy is void. CLOSED 2026-08-01 — it is deleted, not deferred.** The settings row
`Require Face ID to approve` / `Biometric gate on every approval sheet` post-dated its own deletion:
ADR-007 B133 removed phone-side user auth on the grounds that the trust boundary is the wire. **ADR-007
B135 deletes the row from this design, not only from the code.** A design that keeps drawing a control
the product removed reads as a gap, and the next person to build the settings screen would rebuild it —
which is how a deleted feature comes back with no one deciding to bring it back.

Two other settings rows go with it, for reasons that are not the same and are worth keeping apart:

- **Quiet hours is not deferred — it is not a feature.** There is no such preference anywhere in the
  product and none is planned. The row comes off the design rather than waiting for a field someone
  adds to satisfy a drawing.
- **The encryption-status row ships only when something computes it,** and its claim is narrowed to
  **phone to computer** — the Noise session runs handset to gateway and the relay sees ciphertext.
  Unqualified "end-to-end" invites the reading that the agent's own traffic or the daemon's storage is
  covered. Neither is.

**The class, because this document is where it will be read next: a screen can be pixel-accurate to
its design and still be lying.** All three of these would have rendered correctly and every one would
have been a green test over a value the product does not possess. The fences in `android/gate/` catch
a colour that entered without provenance; nothing catches a *claim* that entered without one. These
were caught by reading.

**8.9 The scanner's decode-confirmation flash is specified nowhere and is not built.** The reticle in
§4 was asked for with a brief flash on a successful decode, before the screen advances. It is in
neither the row nor the kit, for two independent reasons and either alone would be enough.

*It would be a fourth motion.* ADR-007 B134 decision 3 enumerates what moves — the sheet, the banner,
the streaming caret — and the list is exhaustive; `android/gate/s23_motion_test.go` enforces it by
refusing the animation vocabulary in every production Kotlin file but `ui/kit/Motion.kt`. A flash is
not a liveness signal the way the caret is: it is feedback on a completed action, which is the
decoration B134 removed. Adding one is an ADR, not a component.

*And it could not be seen if it were built.* `QrScanner`'s analyzer hands the payload on with
`activity.runOnUiThread { onPayload(payload) }`, and `PairingSurface.acceptScannedPayload` calls
`stopScanning()` on its second line — so the preview is hidden inside the same main-thread message
that decoded the code, with no frame drawn in between. Making a flash visible means posting the
hand-off behind a delay, which is latency added to pairing in order to animate how fast pairing was.
*Recommendation:* leave it out. If a confirmation is wanted, the honest one is the destination step
the payload already advances to — a screen the user reads rather than an effect they may miss.

---

## 9. Contrast measurements

Computed for PB-DS-12, which requires `--p-ink3`'s failures to be recorded with the surfaces they
affect. WCAG 2.1 relative luminance; 4.5:1 is the body-text floor, 3:1 the large-text and non-text
floor.

| Ink | on `--p-bg` | on `--p-card` | on `--p-elev` | on `--p-well` |
|---|---|---|---|---|
| `--p-ink` | 18.73 | 17.91 | 17.19 | 18.95 |
| `--p-ink2` | 6.14 | 5.87 | 5.63 | 6.21 |
| **`--p-ink3`** | **3.46** | **3.30** | **3.17** | **3.50** |
| `--p-hero` | 9.95 | 9.52 | 9.13 | 10.07 |
| `--p-att` | 9.34 | 8.93 | 8.57 | 9.45 |
| `--p-ok` | 9.00 | 8.60 | 8.25 | 9.10 |
| `--p-err` | 6.87 | 6.57 | 6.30 | 6.95 |

**`--p-ink3` fails the 4.5:1 body-text floor on every surface in the product** — 3.17 to 3.50:1. It
clears the 3:1 non-text floor everywhere. The affected sites are the section label, the agent name,
the sheet context line, the binding line, the inactive tab labels, the endpoint id, the fingerprint,
the activity timestamp, and the grab handle. Only the last is non-text. This is a property of the
pinned token, not of any derivation in this document; PB-DS-12 asks for it to be recorded, and it is.
The one place this document declined to spend it is the composer placeholder (row 9), where 3.50:1
against a well would apply to text a user is actively trying to read.

Non-text pairs decided here:

| Pair | Ratio | Floor | Verdict |
|---|---|---|---|
| Focus ring `--p-ink` on the three surfaces | 17.19 - 18.73 | 3.0 | Passes by 5x |
| Badge ink `--p-hero-ink` on `--p-att` | 8.79 | 4.5 | Passes |
| Toggle thumb `--p-ink` on the off track | 12.1 | 3.0 | Passes |
| Toggle thumb `--p-ink` on `--p-hero` | 1.88 | 3.0 | **Fails** — but the thumb's position, not its contrast, carries the state, and the track-to-surface transition is what changes. Accepted; noted so it is not rediscovered as a bug |
| Grab handle `--p-ink3` on `--p-elev` | 3.17 | 3.0 | Passes |
| Disabled CTA `--p-ink3` on `--p-hair` | 2.66 | 3.0 | **Fails by intent** — inactive control, WCAG 1.4.3 exempt (§1.3) |
| Revoke ink `--p-err` on its 13% fill | 5.33 | 4.5 | Passes |
| Toggle off track vs `--p-card` | 1.48 | — | Matches the mock's own ratio; the thumb does the work |

---

## 10. Self-review

Three checks, run against the finished table.

**(a) Bare hex.** Every literal in this document falls into one of three classes: the output of a
stated derivation (shown so the arithmetic is checkable), one of the five declared exceptions in §7,
or a citation of the retired value being replaced (§ "How to read a cell"). Three citations survive
inside table cells — the badge's `#ff453a`, the offline dot's `#636366` and the focus ring's
`#e2a33b` — each sitting directly beside the token that replaces it. No cell contains an authored hex. The QR's black-on-white is the only colour exception, as PB-DS-7 anticipated; the
other four exceptions are a suppressed effect, two radii and four platform-owned surfaces, none of
which is a colour.

**(b) Silently kept mock colours.** Five near-misses, caught and replaced:

- The badge's `#ff453a`. Transcribing it would have painted the product's attention state red while
  every other site of that state is amber. Replaced with `--p-att` (§1.4).
- The settings status `#30d158`. The obvious substitution is `--p-ok`, which after B134 means
  `ReadyForReview`. Replaced with `--p-hero`, because "active" is a liveness claim (row 15).
- `var(--accent)` `#e2a33b` in five places — the back button, the header action, the composer's voice
  button, the read-only note's inline action and the pairing CTA. This is the documentation page's own
  chrome and has no product counterpart. Removed entirely rather than substituted; those affordances
  take chrome instead of colour (§2), which also fixed three touch targets.
- The toggle thumb's `#fff` -> `--p-ink`; the offline `#636366` -> `--p-ink3` (dE ~ 1).
- The mock's translucency and blur on the banner, toast and quick chips. Initially transcribed as
  `--p-elev` at 95% plus a 20 dp blur, then dropped after computing what the composite actually does
  over Substrate's surfaces (§2). This was the largest single correction in the document.

**(c) Derivations that collide with a status colour.** Four found, three resolved and one left open:

- `--p-ok` on both the machine-presence dot and `ReadyForReview`, visible on the same screen. Left
  open at §8.2 — it is a genuine B134 side effect and it is not mine to close.
- `--p-ink3` on both `Completed` and machine-offline. Accepted: both mean "not active", the
  collision is semantically correct rather than accidental.
- The settings status text, which would have collided with `ReadyForReview` had it stayed `--p-ok`.
  Resolved to `--p-hero`.
- The kill-switch panel border, which was first derived as `--p-err` at 40% (`#66FF6369`, over the
  ground `#FF6B2D30`) and replaced with `color-mix(--p-err 36%, --p-hair)` = `#FF723B41`. Two reasons:
  it reuses Substrate's own `.prow.attention` border recipe with the error hue substituted, so the
  charged container reads as a sibling of the charged row rather than as a new invention; and an
  opaque mix composites identically over every surface, where the alpha version would shift if the
  panel ever moved off the ground.

One further note on what `--p-ok` still means. B134 rebinds it inside the `status.Group` mapping only.
Outside that mapping it keeps its original Substrate role — success, presence, and the diff `+` — which
is why the post-approval `approved - logged` text (§4) is `--p-ok` and not `--p-hero`. If that
distinction is not held, `--p-ok` becomes ambiguous everywhere; PB-TOK-8's checked-in mapping table is
the place to state it.
