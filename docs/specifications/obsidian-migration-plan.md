# Obsidian migration plan — Substrate to Obsidian, at full quality

**Status: PLAN. Phases O0-O1 in flight this session; nothing beyond O1 is executed yet.**

## The decision

[ADR-009](../adr/ADR-009-obsidian-visual-direction.md) replaces the Substrate skin with
Obsidian: warm near-black material, one champagne accent meaning "you", one earned specular
moment, glass simulated by composition (blur banned). The regime — token pipeline, gates,
spacing scale, Group binding, programmatic Views, dark-only — is deliberately kept. This plan
is the exact route from the shipped Substrate app to a fully Obsidian one, with the quality
bar raised on the way (contrast gate, motion register, haptics, mono bundling, brand assets).

## Ordering rule — non-negotiable

**ADR → maquette → requirements → tests → code.** Tests encoding Substrate values are correct
as written for a superseded decision; they are rewritten deliberately, RED-first, quoting the
old assertion, after ADR-009 and never bent to pass (CLAUDE.md, implementation-goals GG-5).
The maquette sits before requirements because it is the design source the requirements pin:
no token value exists until it exists in the maquette.

## Explicitly KEPT — a strict reading would wrongly rebuild these

| Kept | Why |
|---|---|
| The one-origin pipeline: `internal/design/tokens.json` → `android/design-tokens.tsv` → `values/*.xml`, gated both ways | The migration is a value flow through it, and it is the proof the migration is complete. |
| PB-DS-1's 2dp spacing scale, all ten steps, three frame constants | Obsidian changes material, not rhythm. The maquette must lay out on this scale. |
| PB-TOK-8's Group→token mechanism and the review-green / done-grey assignment | Only values move (ADR-009 D6). |
| Screen inventory, UX flows, status taxonomy, approval-sheet semantics | This is a reskin plus quality wave, not an IA change. |
| Dark-only (light mode stays Phase C), no dynamic colour | ADR-009 scope. |
| The dot as a true circle, 7dp; kit reuse discipline; no-literal gate PB-DS-11 | Unchanged. |

## Out of scope, tracked separately

Blockwork's typed-block transcript (needs a core protocol ADR), Daylight's light/dark
inversion (Phase C), Android 16 Live-Update progress chip (own bead, not blocked by any phase
here), tablet layout (3yi0).

---

## Phase O0 — decision of record

1. ADR-009 written, indexed, pushed. **(done this session)**
2. The amendments ledger executed against `remote-phaseB-requirements.md`: PB-TOK-2 cell,
   PB-DS-5, PB-DS-7 (focus ring resolves), PB-DS-8, PB-TOK-5/8 notes — exact edits listed in
   the ADR's ledger. Each edit cites ADR-009.
3. **Exit**: `git grep -l "Substrate (d1)" docs/specifications` shows only historical
   narrative, no live decision cell; all four quality gates still green (no code touched).

## Phase O1 — the complete maquette (the design source)

**Deliverable: `docs/research/obsidian-maquette.html`** — checked in, published as an
artifact for phone review, owner-signed before O2 starts. This is the rebuilt design for every
frame, every control, and the mark, at token fidelity:

1. **A machine-readable `:root` block** carrying all 35 tokens verbatim (the gates' future
   read target, same arrangement Substrate's directions HTML used).
2. **Every screen, rebuilt**: Pair-only, Pairing (camera + QR reticle + code fallback + SAS
   compare + numbered guidance), Triage Inbox (all three sections, scope chips, stale notice),
   Approval sheet over the inbox, Session Detail (transcript, stop/kill), Terminal Peek
   (amber-phosphor), Machines, Activity, Settings (toggles, paired devices, legal), Launch
   form, plus the Toast and Push-banner moments. Same live content across frames.
3. **The component sheet, every kit primitive in every state**: CtaButton (approve / deny /
   open-session / disabled), DenyChip, FilterChip (selected/idle), Toggle (on/off/disabled),
   StatusDot x4 Groups, PresenceDot x3, Badge, Toast, MonoWell, TextField (idle/focused —
   the new champagne focus ring), SectionLabel, NavHeader + Drill, TabBar, WorkingBar,
   EmptyState, ScanReticle, SAS display, KillSwitchPanel, ReadOnlyNote, PairingStepRow,
   SettingsRow, MachineRow, ActivityRow, SessionRow (x4 Groups, including the lit slab),
   key-light and grain swatches, the sweep (animated in the maquette, annotated one-shot).
4. **The mark**: Solid Wedge re-rendered — champagne wedge + cursor block on warm obsidian —
   as adaptive app icon tile, monochrome/themed-icon variant, store icon 512 composition, and
   feature-graphic composition. Geometry unchanged; only material moves.
5. **Exit**: owner approves the maquette (or edits it — the maquette is the negotiation
   surface; cheaper to argue in HTML than in Kotlin). Approval recorded on the bead.

## Phase O2 — token migration through the pipeline

1. RED: the three pins fail honestly — skin-name pin sees `"obsidian"`, colour count sees 19,
   design-source banner test sees the maquette. Failing output in the RED commit body.
2. `tokens.json`: 27 value changes + 4 new tokens + `skin`/`source` fields, per ADR-009 D3.
   TSV: 4 new rows (`--p-lit-fx`, `--p-sweep-fx` as effects; `--p-sheet-hi`, `--p-sheet-lo`
   with new `swarm_sheet_gradient_top/bottom` resources). Resources regenerate.
3. Authorized rewrites, quoting old assertions: skin pin, PB-TOK-5 count, design-source
   reader (now parses the maquette's `:root`).
4. **NEW contrast gate, RED-first** (ADR-009 D8.1): APCA over the join, against the **per-role
   floors of D8.1's Amendment (2026-08-07, measured calibration)** — `--p-ink` >= 90,
   `--p-ink2` >= 45, `--p-ink3` >= 24 (the named deviation, with its two standing rules),
   `--p-hero-ink` on the champagne fills >= 55, `--p-hero` as text >= 50, `--p-err` as text
   >= 38 (watch item for O7), indicators >= 3:1 WCAG. The blanket "Lc 75 body / Lc 60 large"
   this line used to cite was set before measurement and failed the shipped Substrate palette
   too; the amendment records the ceiling proof that made it unsatisfiable. Lands here so the
   new values are guarded at birth.

   **The ladder rule, corrected — this line contradicted itself and the contradiction shipped.**
   It used to end: *"If any Obsidian pair fails, the token moves (ladder is tunable), never the
   threshold."* Every threshold moved and no token moved, so the sentence was false about the
   change made directly above it. The rule that actually holds, and the one D8.1's amendment
   states, is narrower: **once a floor is set, a pair that later drops below it moves the TOKEN,
   never the floor.** Setting the floors in the first place was a one-time calibration against
   the first contrast measurement this repository has ever taken, and it is recorded as such
   rather than as an application of the rule it broke.

   **STANDING OWNER QUESTION, not resolved by any implementing session.** Two of the calibrated
   floors sit below the rung the gate's own `apcaRungs` table quotes: `--p-ink3` at 24 is under
   APCA's Lc 30 "absolute minimum for any text", and `--p-err` at 38 is under its declared 45.
   ADR-009's status line reads "Accepted (owner decision)", and lowering a quality bar the owner
   signed is the owner's call. The ceiling proof that retires Lc 75 for the CTA label is sound
   and independent of this — no ink on `#c9a876` can exceed |Lc| 59.73 — but it does not extend
   to ink2, ink3 or err, whose floors were set at what this palette happens to achieve. The two
   conditions attached to the ink3 deviation (never the sole carrier of required information;
   the O7 device glance pass) are stated in the ADR and the first of them is a claim about
   screens with no test behind it.
5. **Exit**: all four quality gates green; app builds and renders fully warm with zero Kotlin
   changes beyond the theme; device screenshot set (same shots as Substrate's PB-E2E-2 set)
   attached as evidence `docs/verification/obsidian-o2-evidence.md`.

## Phase O3 — material kit

1. Key-light strengthens to 0.10; promoted slabs take `--p-lit-fx` 0.22 (SessionRow learns
   the lit state from its Group — model-named, test from `resolve()`, not hand-fed state).
2. Approval sheet takes the `sheet-hi → sheet-lo` gradient; radii already live via O2.
3. Grain re-raster: warm-neutral 140x140 at 4%, checked in beside the old one's test.
4. Focus ring: champagne 2dp, wired through FocusRing.kt; the `#e2a33b` orphan dies.
5. **Exit**: gates green incl. PB-DS-5 effect tests re-parameterised; screenshot diff vs O2
   shows only the intended deltas; evidence file.

## Phase O4 — motion register

1. Motion.kt: new curve + durations as named constants, `origin: ADR-009` (entrance 200ms
   cubic-bezier(0.22,1,0.36,1) travel<=4dp; nav 300ms; toggle 150ms kept).
2. The specular sweep: one-shot animator in the kit, driven by Group promotion to NeedsInput;
   the four constraints (one-shot, single-concurrency newest-wins, kit-only construction,
   reduced-motion collapses to nothing) each get a test — the sweep gate (ADR-009 D8.2).
3. Press-feedback audit: every interactive kit control shows visible response <= 120ms;
   asserted where testable, hand-verified where not (listed in evidence).
4. **Exit**: gates green; reduced-motion device pass recorded; evidence file.

## Phase O5 — typography and mono

1. Bundle JetBrains Mono (Regular + Medium), wire into the mono type styles; `tnum zero calt`
   font features on all machine-data styles. `--p-mono` token updates (one value change
   through the pipeline).
2. Verify the box-drawing defect dies: the terminal peek frame aligns (the recorded 18%
   fallback-width mismatch), asserted with a measured-width test.
3. Display weight/tracking values already live via O2; this phase visually verifies the 19
   styles against the maquette.
4. **Exit**: gates green; APK size delta recorded (two weights, subset if needed); evidence.

## Phase O6 — signature moments and haptics

1. **The pull-quote approval sheet** (adopted from Press, per ADR-009): the sheet's headline
   IS the blocking question — larger type, `--p-display-wt`, above the mono command well;
   the well keeps the literal command. No new information, reordered hierarchy.

   **STATUS: PARTIAL, and this item is what keeps O6 from being closed.** `approvalSheet`,
   `ApprovalSheetScreen` and `ApprovalSheetPanel` are built, tested and reachable from nothing:
   a grep over `android/app/src/main/kotlin` finds zero references to any of the three outside
   their own two files, so no user reaches the sheet on any run. It also strands one of the six
   signals item 2 commissions — `Haptics.Signal.SHEET_SETTLE` has no call site, because there is
   no sheet to settle. **The blocker is the protocol, not the skin**: `mobile/app.go` exports no
   approve, deny or answer verb, so there is no facade surface to ledger in
   `android/unbound-verbs.tsv`, and the way a blocked session is resolved from this phone today
   is take-control plus send-line. `docs/verification/obsidian-o6-evidence.md` item 4 discloses
   this and names two further wire gaps in `ApprovalSheetPanel`'s KDoc. **An unreachable
   composition cannot be validated by anything except its own unit tests**, so the exit criterion
   below is not met for this item and the phase is recorded as partial until a protocol decision
   lands. That decision is a core ADR, not this plan's to make.
2. **Haptics vocabulary**, `ui/kit/Haptics.kt`, six signals, rhythm-differentiated, fired
   locally on tap (never on server ack): needs-you two-pulse, sent single sharp, completed
   soft thud, failed double low, sheet-settle thud, scroll ratchet tick. Composition API with
   pre-API-30 fallback; system haptic-disable honoured; a gate asserts no
   `VibrationEffect` constructed outside Haptics.kt.
3. **Predictive back** honoured on drill-downs (scale to 90%, 8dp margin, 35% crossfade),
   within the existing programmatic-Views nav.
4. **Exit**: gates green; hand-feel pass on device recorded; evidence file. **Item 1 does not
   meet it** (see its status note); O6 is PARTIAL, and O7 must not treat it as closed.

## Phase O7 — brand, QA protocol, release

1. **Mark re-render**: Solid Wedge champagne-on-obsidian from the maquette → launcher adaptive
   icon + themed icon, store icon 512, feature graphic 1024x500; `docs/design/store-assets/`
   and `docs/ops/play-assets/` both updated (the kb4q lesson: they must stay identical);
   store-listing.md table updated.
2. **Screenshots**: the paired-app set (h4yg) taken on the real handset in Obsidian.
3. **The device QA protocol**, run and recorded:
   - dimmed-state pass: every screen at 30% brightness — ladder steps still distinct;
   - ASBL pass: static inbox for 5 minutes — champagne still reads;
   - smear pass: fast transcript scroll — no near-black ghosting artifacts worth a token nudge;
   - glance pass: inbox comprehension at a half-second look (which sessions need me?);
   - reduced-motion + font-scale 1.3x passes.
4. versionCode/versionName bump, `bundleRelease`, publish internal via
   `swarm-publish --key secrets/play-service-account.json`.
5. **Exit**: 0.3.0 (or next) live on the internal track in full Obsidian; evidence file
   closes the epic.

---

## Dependencies

O0 → O1 → O2 → {O3, O4, O5} → O6 → O7 (O6 needs O3+O4; O7 needs everything; O5 can land any
time after O2). One bead per phase under the epic; each phase ends with the four quality
gates green and small commits pushed.

## Risks

| Risk | Mitigation |
|---|---|
| Warm ladder crushes on a cheap OLED panel | O7 dimmed-state pass; the ladder is the declared tunable — lighten `--p-card` one notch, never abandon the direction mid-migration. |
| An APCA failure surfaces late | It cannot — the contrast gate lands in O2 with the values. |
| Sweep reads as decoration, not signal | The four constraints are gates, not guidelines; if it still reads wrong on device, the sweep is deleted (the lit key-light alone carries promotion) — a one-line kit change, recorded on the bead. |
| Store-brand divergence confuses testers | Accepted for the closed track (ADR-009); O7 closes it. |
| Gradle/emulator QA flakiness during the many-screenshot phases | Serialize Gradle (recorded hazard); screenshots from one build, timestamps verified. |
