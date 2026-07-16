# Epic 0 — Evidence

**Epic**: Epic 0: Agentic codebase foundation (`agents-tracker-lq1`)
**Scope**: docs and scaffolding only, per build-plan.md — no Go code.

## E0.6 — Backlog verification

Commands run: `bd list --status=open`, `bd list`, `bd blocked`, `bd show agents-tracker-lq1`, `bd list --status=closed`, `bd list --status=deferred`.

**Totals**: 15 issues total — 14 open + 1 in_progress (`agents-tracker-lq1`, Epic 0, claimed for this session). Zero closed, zero deferred. This matches build-plan.md's 15 epics (Epic 0 through Epic 14) exactly — no extra issues, none missing.

### Issue id <-> epic <-> dependency cross-check

Build-plan.md's "Depends on" line for every epic, checked against beads' `BLOCKS`/blocked-by edges:

| Epic | Issue id | build-plan.md "Depends on" | bd blocked-by | Match |
|---|---|---|---|---|
| 0 | `lq1` | — | (none; blocks `je2`) | yes |
| 1 | `je2` | Epic 0 | `lq1` | yes |
| 2 | `onj` | Epic 1 | `je2` | yes |
| 3 | `kw9` | Epic 1 | `je2` | yes |
| 4 | `b2l` | Epics 2, 3 | `kw9`, `onj` | yes |
| 5 | `s95` | Epic 4 | `b2l` | yes |
| 6 | `9k9` | Epic 5 | `s95` | yes |
| 7 | `pzv` | Epic 6 | `9k9` | yes |
| 8 | `ddp` | Epics 4, 6, 7 | `9k9`, `b2l`, `pzv` | yes |
| 9 | `5sx` | Epics 2, 4 (as libraries), 1 | `b2l`, `onj` | see note 1 |
| 10 | `8lr` | Epics 6, 9 | `5sx`, `9k9` | yes |
| 11 | `bf1` | Epics 9, 10 | `5sx`, `8lr` | yes |
| 12 | `0b9` | Epic 5, Epic 7 | `pzv`, `s95` | yes |
| 13 | `l56` | Epic 8, Epic 11 | `bf1`, `ddp` | yes |
| 14 | `54z` | ALL epics | `0b9`, `bf1`, `l56` | see note 2 |

(All issue ids are prefixed `agents-tracker-`, abbreviated above for width.)

**Note 1 (Epic 9)**: build-plan.md lists Epic 1 alongside Epics 2 and 4 as a dependency ("as libraries"). Beads does not encode a direct `je2` edge on `5sx`, but it is covered transitively: `5sx` depends on `b2l` (Epic 4) and `onj` (Epic 2), both of which chain back to `je2` (Epic 1). Epic 9 cannot become ready before Epic 1 closes either way. Not a mismatch — a transitive-reduction edge, not a missing one.

**Note 2 (Epic 14)**: build-plan.md says "ALL epics." Beads encodes only the three direct edges (`0b9`/Epic 12, `bf1`/Epic 11, `l56`/Epic 13). Walking the graph from those three transitively reaches all other 11 epics (10, 9, 7, 5, 8, 4, 2, 6, 3, 1, 0) with none omitted — verified by hand by following each `blocked by` chain in `bd blocked` output. Again a transitive reduction, not a missing edge: Epic 14 is correctly unlockable only after every other epic closes.

**Conclusion**: all 15 epics exist in beads with names matching build-plan.md; every stated dependency edge is present, directly or via verified transitive closure. No mismatches requiring a beads fix.

## Criterion walk (E0.1 - E0.6)

| Criterion | Requirement | Artifact |
|---|---|---|
| E0.1 | `AGENTS.md` finalized as a map: entry points, doc links, build/test commands, beads workflow, verification exit criteria | `AGENTS.md` (rewritten; kept the machine-managed beads-integration block, added project summary, entry-point table, build/test stub, verification pointer) |
| E0.2 | Manifesto vendored into `docs/governance/` with provenance | `docs/governance/MANIFESTO.md`, `docs/governance/PATTERNS.md` (verbatim from `Nathandela/agentic-codebase-manifesto` at commit `37dcf6814b2e47d7996aee95653fa34d21152dc6`), `docs/governance/PROVENANCE.md` (source + SHA + date) |
| E0.3 | CI skeleton exists, runs on push, lint/test may no-op until Epic 1 | `.github/workflows/ci.yml` — triggers on `push`/`pull_request`; `docs` job always runs and fails if `AGENTS.md`, `docs/INDEX.md`, or `docs/governance/PROVENANCE.md` are missing; `lint`/`test` jobs guard Go steps behind `if: hashFiles('go.mod') != ''` |
| E0.4 | ADR index in `docs/adr/`; `docs/INDEX.md` updated | `docs/adr/README.md` (new: table of ADR-001..004 + numbering/template convention for ADR-005 onward); `docs/INDEX.md` (AGENTS.md caveat removed, ADR index + governance + implementation-goals.md + audit-002 links added) |
| E0.5 | N-7 (host sleep pauses agents) documented where users will see it | `README.md` — new "Limitations" section |
| E0.6 | Beads backlog verified against build-plan.md (15 epics, dependency graph) | This file |

All six criteria have a corresponding artifact in the working tree.

## Independent review outcome

Reviewed by an agent that saw none of the implementation (orchestration protocol step 5). **Verdict: APPROVE.** Independently verified: vendored files byte-identical to upstream at the pinned SHA (current HEAD); all 14 beads dependency edges re-checked; all INDEX/AGENTS links resolve; CI YAML valid with correct guards; no emojis; no scope violations.

Findings and resolutions:
1. MEDIUM — build-plan Scope IN lists project rules in `.claude/CLAUDE.md`; not delivered, root `CLAUDE.md` was a stub. **Resolved**: root `CLAUDE.md` (the project-rules file this repo actually uses) finalized with Go build/test commands, architecture pointer, TDD + verification-exit-criteria + ADR conventions. Scope reconciliation: one project-rules file at the root instead of `.claude/CLAUDE.md` — same deliverable, standard location.
2. LOW — Epic 9 dependency paraphrase in this file. **Resolved**: wording corrected above.
3. LOW — ADR index named `README.md` where the vendored convention suggests `index.md`. **Accepted deviation**: GitHub renders README.md as the folder landing page; all links target it explicitly.
4. LOW — `golangci-lint-action@v6` without pinned `version:` becomes nondeterministic once Go lands. **Deferred to Epic 1** (noted on the Epic 1 bead).
