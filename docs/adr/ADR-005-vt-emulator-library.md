# ADR-005: VT emulator library — `github.com/charmbracelet/x/vt`

**Status**: Accepted
**Date**: 2026-07-17

## Context

E2.1 is the risk gate for Epic 2 and the "highest-risk assumption in the plan"
(build-plan.md, Epic 2): that an existing Go VT library covers alternate-screen
emulation well enough to build on rather than writing an emulator from scratch.
ADR-002 already committed to in-shim VT emulation ("established Go library of the
vt10x class, evaluated at implementation"); this ADR records that evaluation and
picks the library.

Frozen needs (implementation-goals.md E2.2–E2.5, build-plan.md Epic 2 contracts):
pure-Go / CGO-free; permissive license; `Feed(bytes)` into a grid; read back
cells + SGR attributes + cursor + terminal modes; **primary and alternate**
screen buffers; `Resize`; sane UTF-8 and split-escape handling.

Four candidates were driven through one alternate-screen fixture in a throwaway
spike (scripted `CSI ?1049h` paint of bold-red text + cursor park + `?1049l`
exit, fed both whole and one byte at a time; plus a real `vim -u NONE` capture
in an 80x24 PTY). All findings below are from that spike, not documentation.

| Criterion | hinshun/vt10x | vito/midterm | **charm x/vt** |
|---|---|---|---|
| Alt-screen (scripted + real vim) | pass | pass | **pass** |
| Split-escape across Feed calls | pass | pass | **pass** |
| Cursor readback | yes | yes | **yes** |
| Attr readback | color only, **no bold flag exported** (bold folds into bright color) | Fg/Bg + IsBold/IsUnderline/… | **Fg/Bg + Attrs bits + underline style + hyperlink** |
| Alt detection / buffers | `Mode()&ModeAltScreen`, active only | `IsAlt` + `Alt *Screen` (**both** buffers) | `IsAltScreen()`, active only |
| Wide char (CJK/emoji) geometry | **wrong** (1 rune/cell) | **wrong** (1 rune/cell) | **correct** (width-aware spacer cells, grapheme clusters) |
| Resize | yes | yes | **yes** |
| Maintenance (last release/commit) | 2022-03-01 (dormant) | v0.2.4, 2026-03-04 | **2026-07-13 (active)** |
| External deps | **zero** | moderate | moderate |
| License / CGO-free | MIT / yes | MIT / yes | **MIT / yes** |

The decisive differentiator is wide-character cell geometry. Fed `你好世界`,
charm places `你` at column 0 with `Width=2` and an empty spacer at column 1,
keeping every following column aligned exactly as a real terminal would; hinshun
and midterm both pack one rune per cell, so anything after a double-width glyph
lands in the wrong column. Faithful snapshots (E2.3) and column-accurate grid
status heuristics (ADR-002; Epic 10) depend on that geometry being right for the
box-drawing, CJK, and emoji that Claude Code and Codex emit.

## Decision

Build the in-shim grid on **`github.com/charmbracelet/x/vt`**, pinned at
`v0.0.0-20260713092006-0d683c34c74b`, with its rendering core
`github.com/charmbracelet/ultraviolet v0.0.0-20260303162955-0b88c25f3fff`.

The `*vt.Emulator` API maps directly onto the Epic 2 `Emulator` contract:
`Write([]byte)` = `Feed`; `Resize(w, h)`; `CellAt(x, y) *uv.Cell` (grapheme
`Content`, `Style.Fg/Bg/Attrs`, `Width`); `CursorPosition()`; `IsAltScreen()`.
`vt.SafeEmulator` adds the mutex for the "single-goroutine Feed, Snapshot atomic
w.r.t. Feed" model. The production wrapper (Feed/Snapshot/Resize + hostile-escape
filtering + versioned snapshot bytes) is a later Epic 2 task; nothing here
freezes that surface.

## Consequences

### Positive
- Only candidate with correct double-width/grapheme cell geometry — the property
  that makes snapshots and grid heuristics trustworthy on real CLI output.
- Richest attribute readback (fg, bg, bold/faint/italic/underline/reverse,
  underline style, hyperlink) — enough to serialize the full snapshot (E2.6).
- Most actively maintained candidate, lowering bit-rot risk over the project's
  life; part of the charm ecosystem swarm already tracks (Bubble Tea, Epic 7).
- MIT, CGO-free; cross-compiles CGO_ENABLED=0 for the E1.2 target matrix.

### Negative
- No stable tag: x/vt is versioned by commit inside charm's `x` monorepo and its
  package doc still carries an in-progress note, so HEAD churns. **Mitigation**:
  pin the exact pseudo-version; this file's spike test guards against regressions
  on any bump; the later `Emulator` wrapper isolates callers from upstream drift.
- Adopting it raises the module's `go` directive to **1.24** (a transitive charm
  dep requires it) and pulls a moderate dependency graph (x/ansi, ultraviolet,
  uniseg, uax29, colorprofile, go-runewidth, x/sys). All pure-Go and MIT.
- We read back the active buffer, not both simultaneously (unlike midterm). The
  active grid is always correct and primary restores on alt exit, which is all
  attach/snapshot needs, so this is not a functional gap.

## Alternatives Considered

- **`github.com/hinshun/vt10x`** — passes the alt-screen fixture and has zero
  external deps, but dormant since 2022, exposes no per-cell bold/attribute flags
  (bold is folded into a bright color), and models no double-width geometry.
  Rejected: correctness and maintenance both lose to charm.
- **`github.com/vito/midterm`** (v0.2.4, dagger/bass) — the strong runner-up:
  actively maintained, exposes both screen buffers directly and rich per-cell
  `Format` flags, passes the fixture. Rejected only on wide-character geometry
  (one rune per cell, so wide content misaligns) plus a heavier go.mod
  (bubbletea, creack/pty, containerd/console).
- **`github.com/ActiveState/termtest`** — an expect-style test harness wrapping
  `github.com/ActiveState/vt10x` (a hinshun fork); not an independent grid
  emulator and inherits vt10x's grid limitations. Rejected: wrong layer.
- **`github.com/danielgatis/go-vte` / `go-ansicode`** — a low-level ANSI parser
  state machine, not a grid emulator (it is midterm's parser layer). Using it
  means writing the grid ourselves — exactly what E2.1 exists to avoid.
  Rejected: not a grid.

Reversal cost is low: the chosen library sits behind the Epic 2 `Emulator`
wrapper, so swapping it later touches one package.

## Known limitations

- **Grapheme clusters split across PTY reads may render as separate cells.**
  charm x/vt flushes its pending grapheme buffer at every `Write` boundary, so a
  multi-rune grapheme cluster (base rune + combining mark, or a ZWJ emoji
  sequence) that straddles two `Feed` calls commits as separate cells instead of
  one. Buffering the trailing runes inside our wrapper is rejected: it would
  delay echo of the last typed character, which attach/live-view cannot accept.
  Product impact is minor — real PTY reads rarely bisect a cluster, and the next
  paint (or a redraw) reconciles the grid — so we accept it rather than diverge
  from upstream. The E2.5 fuzz property is scoped accordingly: on valid UTF-8,
  `whole == split` is required except when the split byte offset falls strictly
  inside a grapheme cluster (checked with `rivo/uniseg`); on malformed UTF-8 it
  is not enforced, because charm's byte-level error recovery (consuming stray
  control/continuation bytes into replacement runes, then clustering them) makes
  cross-`Write` behavior implementation-defined and not analyzable from the byte
  stream. Any panic or deadlock still fails, and real buffering regressions still
  surface on the well-formed inputs the fuzzer explores. Revisit if charm gains
  cross-`Write` grapheme merging, at which point the scoping can tighten back
  toward unconditional.
