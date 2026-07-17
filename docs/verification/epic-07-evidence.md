# Epic 7 — Evidence

**Epic**: TUI — general view + launch form (`agents-tracker-pzv`) — the production-grade user-facing slice
**Commits**: 6ed5d85 (implementation), 1f4b568 (review fixes).

## TDD evidence (GG-5)

Designer wrote the failing suite first (35 tests + 2 goldens); red log in [epic-07-red/tui-red.txt](epic-07-red/tui-red.txt) (undefined-only, confirmed genuine). Fixes wrote failing tests first (index-vs-identity red on the pre-fix logic).

## Criterion walk (E7.1 – E7.7)

| Criterion | Evidence |
|---|---|
| E7.1 router | general/launch/attach sub-models; only the router is shared shell; clean transitions + Esc backout |
| E7.2 general view | 4 groups (UPPERCASE, fixed order, empty omitted); all 5 V-4 row fields; notification banner on needs-input/ready-for-review transitions (inline mode; the transient in-view refinement is Epic 8's alt-screen work); goldens match ui-preview |
| E7.3 liveness (V-2/L1) | Subscribe event moves the affected row in place on the event (no polling) |
| E7.4 keyboard | wrap-select (↑↓/jk), Enter attach, Esc, `n`, Ctrl+X confirm — **resolves by SESSION IDENTITY** (kill running / delete completed per R-3); a concurrent event can never make it act on the wrong session |
| E7.5 launch form | cwd + `~` expansion + invalid-cwd refusal (L-3); agent picker greys not-installed AND out-of-range with install/upgrade hints (L-2); declarative options; initial prompt; worktree placeholder; submit refuses an unusable agent |
| E7.6 first paint (N-1) | eager List() in New(); 50 sessions at first render under budget |
| E7.7 stub-only | all tests against a fake Client; compile-time `var _ Client`; no live daemon |

## The blocking bug the review caught

Selection + the Ctrl+X confirm originally resolved by flat INDEX, so a concurrent status event that reordered groups could slide a different session under the cursor and make kill/delete/attach hit the WRONG one (data-destructive). Fixed: capture the target by ID on Ctrl+X, resolve against a fresh sessionByID lookup (no-op if it vanished or flipped kill/delete state); selection follows the same session by identity across regroups; Enter-to-attach is identity-safe. Five new tests cover index-shift, vanish-decoy, state-flip, identity-follow, and attach-by-identity.

## Review outcome (protocol step 5)

**Opus (independent): FIX REQUIRED → APPROVE.** The blocking F1 (wrong-session kill) confirmed genuinely closed on re-review; F2 (repaint slowed to 1s, general-view-only — N-3) and F5 (launch guard) fixed; F3 (transient in-view banner) is a legitimate Epic 8 deferral (conflicts with a frozen liveness assertion, needs alt-screen). One latent cosmetic (confirm-prompt render row when a session is removed mid-confirm) can't fire until Epic 8 adds removal — recorded on the Epic 8 bead.

## Dependency note

Bubble Tea **v2** + lipgloss/v2 v2.0.0 (exact) + teatest/v2 — the repo's charm x/vt stack (ansi v0.11.7) is incompatible with Bubble Tea v1; v2.0.1+ needs go 1.25, so pinned to v2.0.0 to hold the **go 1.24 directive**.

## Quality gates (GG-4)

gofmt · build · vet · `go test ./internal/tui/ -race -count=3` green; goldens are the acceptance record, matched to docs/design/ui-preview.html.
