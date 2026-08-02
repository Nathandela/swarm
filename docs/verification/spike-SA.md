# Spike S-A — Transcript Derivation from Real claude/codex PTY-Grid Diffs

**Bead**: `agents-tracker-bot` · **Plan section**: D.18 R-SPK-A (`.claude/tmp/remote-control-implementation-plan.md`) · **Gates**: Phase 2 chat view (R-GATE.4), Phase 0 spike exit (R-GATE.1/2).

**Question this spike answers**: can clean, incremental, chat-shaped text be derived by diffing successive `internal/vt.Emulator` grid snapshots of a REAL `claude` / `codex` session, well enough to drive a remote/phone chat view — or must Phase 2 fall back to a raw journal + periodic snapshot view?

**Method**: real CLIs, real PTYs, real auth (no simulated output, no ollama stand-in per the task brief). All 9 driven runs below actually exec'd `claude` or `codex` under `github.com/creack/pty` and fed the real, unmodified `internal/vt` emulator.

## R-SPK-A.1 — harness (reused `characterize()` verbatim, added periodic snapshots)

`cmd/swarm-char/char.go`'s `characterize()` was reused for its PTY-spawn/query-reply pattern (`pty.StartWithSize`, `emu.SetReplyWriter(ptmx)`, `emu.Feed` in the read loop) — this spike did not write new PTY/spawn code. `characterize()` itself only records the whole-run capture and snapshots once at the end, so it could not directly answer this spike's question; R-SPK-A.1 explicitly asks for periodic mid-run snapshots, so a throwaway sibling driver (`characterizeWithSnapshots`/`runTurns`) copies that same spawn/read/feed loop and layers a **quiescence-triggered periodic snapshot recorder** on top: a scripted "turn" sends a keystroke burst, the driver waits until the PTY has been silent for a quiescence window (900ms, capped by a per-turn max-wait), then calls `emu.Snapshot()` and records it labeled by turn — giving N grid snapshots across a multi-turn conversation, not just one at the end. Because `characterize()` is `package main`-unexported and needs `internal/vt`/`internal/adapter` (Go's internal-package rule requires the importer to live inside the module tree), the driver had to physically sit at `cmd/spike-sa-harness/` inside the worktree while running; it was **never `git add`ed** and has been deleted from the worktree post-run. A full copy of its final source (`main.go`, plus a `validate/main.go` that round-trips fixtures through the real `adapter.Fixture.Validate()`) is preserved at `/Users/Nathan/.claude/jobs/6878515f/tmp/spike-sa/harness-src/` for anyone who wants to re-run this exact spike.

**Reproducible charSpec values** (all scenarios): `Cols=100, Rows=40, Timeout=60s`, `Cwd` = the harness's own working directory, `Env` = `charEnv()` (current env + `TERM=xterm-256color` guaranteed) — identical convention to `cmd/swarm-char`.

| Scenario | Argv | Turns (Data → PressEnter → wait-for-quiescence) |
|---|---|---|
| `claude-plain` | `claude --dangerously-skip-permissions` | startup; "What is 7×6?..." ⏎; "Now multiply by 2..." ⏎ |
| `claude-tool-scroll` | same | startup; "Use the Bash tool to run \`seq 1 150\`..." ⏎ |
| `claude-altscreen` | same | startup; `/mcp` ⏎; `Esc` |
| `codex-plain` | `codex -a never --sandbox workspace-write` | startup; dismiss-update-nag (conditional, see finding #3); "What is 7×6?..." ⏎; "Now multiply by 2..." ⏎ |
| `codex-tool-scroll` | same | startup; dismiss-nag; "Run \`seq 1 150\`..." ⏎ |
| `codex-altscreen` | same | startup; dismiss-nag; "Run \`printf ... \| less\`..." ⏎; "q"; "/model" ⏎; `Esc` |
| `codex-plain-noaltscreen` (bonus, not required) | `codex -a never --sandbox workspace-write --no-alt-screen` | same turns as codex-plain |
| `claude-plain-oldversion` (R-SPK-A.3) | old `claude` binary, same flags | same turns as claude-plain |
| `codex-plain-oldversion` (R-SPK-A.3) | old `codex` binary, same flags | same turns as codex-plain |

## R-SPK-A.2 — scored table

Scoring: **PASS** = the new lines a content-aware grid diff attributes to the turn are byte-exact and contain no duplication/garbling. **DEGRADED** = the visible new text is itself clean, but something the CLI produced is not recoverable from the grid at all without an extra interactive step. **FAIL** = a diff heuristic applied to the transition would misattribute non-chat content as chat text, or lose visibility into real history, without CLI-specific special-casing.

| CLI (version) | Scenario | Result | Why (raw diff for every non-PASS below) |
|---|---|---|---|
| claude 2.1.214 | plain 2-turn Q&A | **PASS** | Both turns are byte-exact appends between the last content and the fixed footer, chrome untouched. Requires a **full-grid content diff (LCS over line text), not a row-index or tail-append diff** — Claude Code repaints the whole frame every turn and pins its footer at the bottom, so new content is inserted mid-buffer, not appended at a growing tail. |
| claude 2.1.214 | tool-use scrolling (`seq 1 150` via Bash tool) | **DEGRADED** | The 150 lines of `seq` output are **never rendered in the grid at all** — Claude Code collapses the tool call to a one-line summary ("Ran 1 shell command"). The summary + "⏺ DONE" text that IS rendered is byte-exact, no dup. But no grid-diff approach can recover the actual tool stdout without an extra interactive expand the harness never performed. |
| claude 2.1.214 | `/mcp` overlay open + close (alt-screen-equivalent stress case) | **FAIL** | Opening `/mcp` deletes ~15 rows of the welcome banner/prompt and inserts an unrelated 20-row server-status panel; closing it (`Esc`) reverses that exactly. See raw diff below — a content diff with no overlay-awareness would either drop real prior chat history from the derived transcript (while the panel is open) or worse, insert the panel's own text ("Manage MCP servers", "14 servers", the per-server list) into the chat transcript as if the assistant said it. |
| codex 0.144.1 | plain 2-turn Q&A | **PASS** | Genuine linear/append terminal semantics — the top of the update-nag banner **scrolls off** (one line removed) while new content is appended at the bottom, exactly like a normal shell scrollback. `altScreen=False` throughout, in every codex scenario captured, **contradicting `codex --help`'s `--no-alt-screen` text** (which implies alt-screen-by-default) — see finding #5. A simple tail-anchored (longest-common-suffix) diff suffices here; simpler than claude's case. |
| codex 0.144.1 | tool-use scrolling (`seq 1 150`) | **DEGRADED** | Same shape as claude's case, different truncation style: codex shows first 2 + last 2 lines plus a `… +146 lines (ctrl + t to view transcript)` marker, rather than a full collapse. The visible text (marker included) is clean/no-dup, but 146 lines are unreachable from the grid without a scripted `ctrl+t`. |
| codex 0.144.1 | `less` pager (auto-quit) + `/model` overlay | **FAIL** (driven by the overlay half) | The `less` half alone is PASS-clean: codex's own agent detected the backgrounded pager waiting on input and **auto-sent "q" itself** (`↳ Interacted with background terminal · ... \| less` / `└ q`) before our scripted quit-keystroke ever arrived — no overlay was entered, nothing to diff wrong. But `/model` opens a genuine full-pane selection list (4 model rows + "Press enter to confirm or esc to go back") that mass-replaces the visible pane and reverts byte-identically on `Esc` — the same FAIL pattern as claude's `/mcp`, for the same reason. |

Raw diff, claude `/mcp` open (`docs/verification/fixtures/spike-sa/claude-altscreen.dump.txt`, `startup -> turn1-mcp-overlay`):

```diff
- ⚠ 3 MCP servers need authentication · run /mcp
+▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔
+   Manage MCP servers
+   14 servers
+     User MCPs (/Users/Nathan/.claude.json)
+   ❯ context7 · ✔ connected · 2 tools
     ... (16 more inserted panel lines) ...
-                                                                                  ◉ xhigh · /effort
-─────────────────────────────────────────────────────────────────────────── remote control -SWARM ──
-❯
-────────────────────────────────────────────────────────────────────────────────────────────────────
-  Fable 5 | (no repo) out
-  ⏵⏵ bypass permissions on (shift+tab to cycle)
+   ※ Run claude --debug to see error logs
+   https://code.claude.com/docs/en/mcp for help
+   ↑/↓ to navigate · Enter to confirm · Esc to cancel
```

and `Esc` reverses it exactly (`turn1-mcp-overlay -> turn2-close-overlay` diff is the mirror image, ending byte-identical to `startup`).

Raw diff, codex `/model` open (`docs/verification/fixtures/spike-sa/codex-altscreen.dump.txt`, `turn2-quit-pager -> turn3-model-overlay`):

```diff
-› q
+  Select Model and Effort
+  Access legacy models by running codex -m <model_name> or in your config.toml
+› 1. gpt-5.6-sol (current)  Latest frontier agentic coding model.
+  2. gpt-5.6-terra          Balanced agentic coding model for everyday work.
+  3. gpt-5.6-luna           Fast and affordable agentic coding model.
+  4. gpt-5.5                Frontier model for complex coding, research, and real-world work.
+  Press enter to confirm or esc to go back
```

Full turn-by-turn text + diffs for every scenario are at `docs/verification/fixtures/spike-sa/*.dump.txt`; the exact per-turn `vt.Snap` JSON is at `*.snapshots.json.gz` (gzipped, decode with `gunzip`).

## R-SPK-A.3 — cross-version spot-check (both CLIs, not skipped)

A second version of **both** CLIs was readily available and used — not NOT-RUN:

- **claude**: primary PATH install is 2.1.214; `npm install @anthropic-ai/claude-code@2.1.196` into a scratch dir got a real older build running in ~5s. Re-ran the exact `claude-plain` turns against it. Result: **same structural pattern** (alt-screen from frame 1, repaint-per-frame, pinned footer, clean append diff) — PASS holds across versions. The only content difference was incidental account state (a "70% of weekly limit" usage banner appeared in the footer area in the older run; not a rendering-pattern difference).
- **codex**: primary PATH install is 0.144.1; `npm install @openai/codex@0.143.0` got a real prior stable release running. Re-ran the exact `codex-plain` turns against it. Result: rendering pattern held (non-alt-screen, linear append, same update-nag UI), **but** the old client could not actually use the current backend model — every prompt returned a structured API error (`"The 'gpt-5.6-sol' model requires a newer version of Codex..."`) instead of a real answer. That error still rendered through the same clean append-only structure (prompt line, then a new `■ {...}` block, no garbling/dup), so the **rendering fidelity finding is unaffected**, but this run could not exercise a real multi-turn conversation on the old client — noted as a genuine limitation of this spot-check, not silently glossed over.

## Key operational findings (beyond the scored table)

1. **Burst keystrokes with an embedded `\r` get swallowed as a paste, not submitted.** First probe attempt sent `"prompt text\r"` as one PTY write; Claude Code's TUI treated the co-arrival of text+`\r` in one read tick as a multi-line paste and inserted a literal newline into the input box instead of submitting — the prompt sat there unsent, and a second turn's text got appended into the SAME unsent multi-line draft. Fix: send the text, sleep 150ms, then write `\r` as its own separate write (`pasteGuard` in the harness). **This is directly relevant to the product's own remote-keystroke-forwarding feature**: any phone/relay path that batches "type X then press Enter" into a single forwarded write risks hitting the identical CLI-side paste heuristic and silently failing to submit.
2. **Neither CLI hands a spawned interactive program real terminal control.** Three independent attempts, none produced a genuine nested alt-screen: Claude Code's Bash tool silently substituted a non-interactive `Read` call instead of running `printf | less`; Claude Code's `!<cmd>` raw-bash-passthrough ran `less` as an async background job ("Running…") that never received our keystrokes and had to be left to time out; codex's Bash tool ran `less` but its own agent detected the pager waiting on input and auto-sent "q" itself. Good news for a chat-view: a genuine terminal takeover by a child process is structurally guarded against by both vendors' agent tool-use layers, so it is not the alt-screen risk to design for — the CLIs' **own overlay panels** (`/mcp`, `/model`) are.
3. **codex's startup "update available" nag is non-deterministic in form across otherwise-identical invocations.** Some runs show an interactive numbered menu ("› 1. Update now ... Press enter to continue") where a blind Enter would trigger an unwanted `npm install -g @openai/codex` (which then failed on an unrelated `EACCES` on a root-owned `~/.npm` cache — confirmed harmless, `codex --version` unaffected afterward); other runs show a passive already-dismissed info box with no menu at all, where the same blind dismiss keystroke leaked into chat as a literal "3" (codex's model asked "Could you clarify what '3' refers to?"). Fixed by gating the dismiss keystroke on the grid actually containing "Press enter to continue" (`turn.RequireGridContains`) before sending it — any real automation driving codex headlessly needs the same guard.
4. **This machine's Go toolchain silently produces amd64 binaries under Rosetta on Apple Silicon** (`go env GOARCH` = `amd64`, host is arm64). A harness built this way spawning `codex` (an npm-wrapped Node CLI) failed every time with `Missing optional dependency @openai/codex-darwin-x64` — macOS's architecture-inheritance rule makes a Rosetta-translated parent's child processes (through universal binaries like `node`) also run as x86_64, and only the `-arm64` optional dependency package was actually installed. Rebuilding with `GOARCH=arm64 go build` fixed it immediately. **This is a real, reusable finding for the project**: if `cmd/swarm-char` or the shipped daemon is ever built the same (unqualified) way on this or a similarly-configured Apple Silicon dev machine, spawning any npm-wrapped Node CLI it characterizes would break the same way. Worth a build-pipeline check (`go env GOARCH` on every dev/CI Apple Silicon runner).
5. **codex's own `--help` claims alt-screen-by-default** ("Runs the TUI in inline mode" is what `--no-alt-screen` *disables*, implying ON by default) but every codex capture in this spike — with or without `--no-alt-screen` — showed `altScreen=False`. Root cause not chased further (out of this spike's scope); reported as-observed rather than asserted.
6. A prompt-wording bug on this spike's own part (`Run exactly: seq 1 150 . Then say DONE.`) got the stray period parsed as a literal shell argument by codex's model (`seq: invalid floating point argument: .`, `less: . is a directory`) — Claude's model tolerated the same phrasing fine. Fixed by putting the command in backticks with unambiguous punctuation; not a CLI/rendering finding, noted only so the fixed scenarios above are understood as the corrected versions.

## VERDICT

**PARTIAL** — chat view ships in Phase 2 for ordinary multi-turn conversational text (PASS in both CLIs, cross-version-stable), gated by a specific, mechanically-checkable degrade-to-journal rule for the two failure modes actually observed:

- **Overlay-transition rule** (covers both FAILs): a quiescence-window transition where a large, non-monotonic fraction of the grid's rows are simultaneously removed AND replaced (operationalized as: no simple common-prefix/suffix anchor covers the changed span, e.g. via the LCS diff's edit script touching a majority of rows in one window) is NOT appended to the chat transcript. The remote/phone view freezes on the last known-good transcript state (or shows a neutral "UI updated" placeholder) until a later window's diff matches back to that pre-transition baseline, at which point normal derivation resumes. This is deliberately CLI-agnostic (a size/shape heuristic on the diff itself, not a hardcoded list of overlay commands), because both `/mcp` and `/model` reverted byte-identically to their pre-overlay baseline — the one property the rule depends on.
- **Truncated tool-output rule** (covers both DEGRADEDs): when a rendered line matches a per-CLI truncation-marker pattern (Claude Code: `"Ran N shell command"`/similar collapse phrasing; codex: `"+N lines (ctrl + t to view transcript)"`), the chat view shows that summary text verbatim — it IS a clean, byte-exact derivation — but must not claim to have the underlying tool output; a "view full output" affordance defers to the hook/event capture path (spike S-B), not VT-diff reconstruction.

**The exact condition that would flip this verdict to FAIL**: this spike's scenarios were deliberately short (2-3 turns, one tool call) and **never once forced content to scroll past the fixed 40-row viewport within a single quiescence window** — every turn's new content fit inside the visible grid alongside everything before it. That is the core risk the whole spike exists to probe (can a periodic-snapshot recorder actually reconstruct history that scrolls off-screen faster than it can be sampled?) and it remains untested here. If a follow-up test with many more turns, or a single turn producing more than ~40 rows of REAL rendered content (not collapsed/truncated — this spike found both CLIs avoid ever rendering that much raw tool output, which incidentally shields tool-output specifically from this risk, but not necessarily long back-and-forth chat text) shows that a periodic snapshot cadence can miss content that scrolled off between two samples, the verdict flips to FAIL for any conversation length beyond what a snapshot cadence can guarantee to catch, and the chat view would need permanent journal+snapshot as its ground truth with grid-diff only as a live-tail optimization on top, never the source of truth. **The condition that would flip it to full PASS**: a CLI-agnostic overlay-detection heuristic proven reliable enough to retire the freeze/placeholder fallback, and a scripted "expand" step (ctrl+o / ctrl+t) proven to reliably recover full tool output before quiescence, eliminating both degrade rules above.

## R-SPK-A.5 — fixtures (adapter.Fixture shape, Validate()-clean)

All 9 driven runs produced a fixture, decoded from real JSON and round-tripped through the **actual** `internal/adapter.Fixture.Validate()` (not a hand-rolled check):

```
$ /Users/Nathan/.claude/jobs/6878515f/tmp/spike-sa/validate docs/verification/fixtures/spike-sa/*.fixture.json
claude-altscreen.fixture.json          VALIDATE OK cli=claude version=2.1.214 (Claude Code) scenario=claude-altscreen           capture_bytes=9018
claude-plain-oldversion.fixture.json   VALIDATE OK cli=claude version=2.1.196 (Claude Code) scenario=claude-plain-oldversion   capture_bytes=11240
claude-plain.fixture.json              VALIDATE OK cli=claude version=2.1.214 (Claude Code) scenario=claude-plain             capture_bytes=8221
claude-tool-scroll.fixture.json        VALIDATE OK cli=claude version=2.1.214 (Claude Code) scenario=claude-tool-scroll       capture_bytes=11116
codex-altscreen.fixture.json           VALIDATE OK cli=codex  version=codex-cli 0.144.1     scenario=codex-altscreen          capture_bytes=127346
codex-plain-noaltscreen.fixture.json   VALIDATE OK cli=codex  version=codex-cli 0.144.1     scenario=codex-plain-noaltscreen  capture_bytes=71783
codex-plain-oldversion.fixture.json    VALIDATE OK cli=codex  version=codex-cli 0.143.0     scenario=codex-plain-oldversion   capture_bytes=47422
codex-plain.fixture.json               VALIDATE OK cli=codex  version=codex-cli 0.144.1     scenario=codex-plain             capture_bytes=72837
codex-tool-scroll.fixture.json         VALIDATE OK cli=codex  version=codex-cli 0.144.1     scenario=codex-tool-scroll       capture_bytes=80686
```

Files under `docs/verification/fixtures/spike-sa/`:

| File pattern | Content |
|---|---|
| `*.fixture.json` | `adapter.Fixture` (schema v1), the R-SPK-A.5 deliverable — whole-run `PTYCapture`, `Validate()`-clean (proof above) |
| `*.snapshots.json.gz` | R-SPK-A.1's periodic-snapshot companion data: every turn's `vt.Snap` JSON + label + timestamp, gzipped (not Fixture-shaped — Fixture's schema has no multi-snapshot field, so this rides alongside rather than inside it) |
| `*.dump.txt` | Human-readable rendered grid text per turn + the line-diff between consecutive turns for every scenario — the primary evidence artifact for the scored table above |

Throwaway harness source (not merged, not committed): `/Users/Nathan/.claude/jobs/6878515f/tmp/spike-sa/harness-src/` (`main.go` the driver, `validate_main.go` the fixture round-trip checker).
