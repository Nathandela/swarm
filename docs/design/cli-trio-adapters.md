# CLI Trio Adapter Design: agy, opencode, vibe

Status: strategy/exploration phase output (2026-07-18, issue agents-tracker-5gv).
Every claim below marked "verified" was established empirically on this machine
against the installed binaries; provenance is the recorded fixtures under
docs/verification/cli-trio-exploration/ and the temporary test packages
internal/adapter/trioproto/ and internal/engine/trio_exploration_test.go (both
to be deleted when implementation starts).

## 1. Scope and spec note

Target CLIs: **agy** (Antigravity CLI, Google — native Go/Bubble Tea binary,
multi-backend: Gemini, Claude, GPT-OSS), **opencode** (anomalyco/opencode,
Bun/TypeScript, client-server), **vibe** (mistralai/mistral-vibe, Python/Textual).

system-spec.md T-7 currently names the v1.1 lineup as "Gemini CLI, OpenCode,
AGY". This design supersedes that: agy IS Google's successor to Gemini CLI
(Gemini CLI stops serving non-Enterprise requests 2026-06-18), and vibe is a
net-new addition. T-7 must be amended when implementation starts.

## 2. What was verified per CLI

| | agy 1.1.4 | opencode 1.17.9 | vibe 2.15.0 (2.21.0 latest) |
|---|---|---|---|
| Version banner | `1.1.4` | `1.17.9` | `vibe 2.15.0` |
| Probe latency | ~50-400ms | ~300ms warm, **1.8s loaded** | ~900ms warm, **2.0s+ loaded** |
| Launch argv | `agy [--model M] [--mode X] [--prompt-interactive P]` | `opencode [--model p/m] [--agent A] [--prompt P]` | `vibe --trust [--agent A] [P]` |
| Model flag | yes — accepts `agy models` display names (verified live) | yes (`provider/model`) | **none** (config/env `VIBE_ACTIVE_MODEL` only) |
| Resume argv | `agy --conversation <uuid>` (verified: context retained) | `opencode --session ses_<id>` (verified via `run -s`) | `vibe --trust --resume <uuid>` (verified: context retained) |
| Id in PTY stream | yes — exit screen prints `agy --conversation=<uuid>` | yes — exit screen prints `Continue  opencode -s ses_<id>` | **never** (id only in `~/.vibe/logs/session/*/meta.json`) |
| Busy signal on grid | trailing `⣷ Generating...` line — stock heuristic classifies **active** (verified) | spinner + "Thinking" mid-grid, never last line — stock heuristic **blind** | braille wave near header — stock heuristic **blind** after startup |
| Idle signal on grid | `>` inside bordered box; border/footer is last line — **unknown** | bottom status bar is last line — **unknown** | footer bar is last line — **unknown** |
| Typed-event potential | `--output-format stream-json` (print mode), hooks in `.agents/hooks.json` | first-class HTTP+SSE server (`opencode serve`, `GET /event`, `session.status` busy/idle/retry, permission/question request objects) | `vibe-acp` binary (Agent Client Protocol, JSON-RPC) |
| Launch gotchas | first-run workspace-trust prompt (persisted in `~/.gemini/antigravity-cli/settings.json` `trustedWorkspaces`); background auto-updater bumps version silently; needs non-zero PTY winsize + capability-query replies (swarm-char/shim already provide both — verified) | ~1.5s cold start; terminal capability queries at startup (handled by internal/vt — verified) | blocking update modal when stale unless `VIBE_ENABLE_UPDATE_CHECKS=false` (env-only); `--trust` required for unattended launch (per-invocation, not persisted — safe); no isatty guard (interactive mode must always get a real PTY) |

Storage (context only; pure adapters never read it): agy = SQLite per
conversation under `~/.gemini/antigravity-cli/conversations/`; opencode =
SQLite `~/.local/share/opencode/opencode.db`; vibe = JSONL + meta.json per
session dir under `~/.vibe/logs/session/`.

## 3. Adapter designs (prototyped and conformance-proven)

Each is a stateless pure strategy object per the frozen contract; prototypes in
internal/adapter/trioproto/ pass `adapter.Conformance`, real-binary
`adapter.Detect` via `detect.Host`, and extraction against the recorded
captures. Highlights:

- **agy**: options = model (string, Suggest = the 8 `agy models` names), mode
  (choice: accept-edits/plan), dangerously-skip-permissions (bool).
  InitialPrompt via `--prompt-interactive`. Extraction marker `--conversation=`
  with control-byte-aware token termination (a raw tail butts `\x1b[K` against
  the uuid — caught by the capture test; whitespace-only termination is a bug).
  SupportedVersions Min 1.1.0.
- **opencode**: options = model, agent (free strings). InitialPrompt via
  `--prompt`. Extraction = LAST `ses_<alnum>` token (length- and
  terminator-guarded); last occurrence because transcripts can mention child
  session ids. Declares the SSE event triple (busy/idle/permission) as
  wire-later `event` sources, codex-precedent. Min 1.0.0.
- **vibe**: options = agent (choice: default/plan/accept-edits/auto-approve).
  `--trust` always in argv (documented per-invocation-only). InitialPrompt
  positional. ExtractConversationID honestly returns false — capability matrix
  records ConversationID: false; resume works only via ids swarm captured at
  launch time (not extractable after crash). Min 2.0.0.

Registry: add the three to `constructors` AND `production` in
internal/adapter/registry/registry.go — the only mandatory core-adjacent edit
(T-5 confirmed to hold in practice by a full coupling-point sweep).

## 4. Status-signal strategy

Finding: all three TUIs anchor a status bar / input-box border at the bottom of
the screen, so the stock last-line heuristic (internal/engine/heuristic.go)
sees: agy = active-while-generating but never idle; opencode and vibe =
permanently unknown. Timeline evidence: TestTrioExploration_HeuristicTimeline.

v1 recommendation (decision pending): **descriptor-driven bottom-region grid
rules**, read by the engine from the adapter's declared heuristic
SignalSources — e.g. `busy-contains` (agy "Generating...", opencode "Thinking",
vibe braille-wave frames) and `idle-prompt-in-box` (bare `>` line above a box
border within the bottom K lines). This is the architecturally sanctioned slot
("per-CLI grid rules are Epic 11 adapter work" — heuristic.go), leaves
claude/codex behavior byte-identical, and stays inside T-4 humility (unknown
remains the fallback). Alternative considered: widen the generic heuristic to
scan the bottom K lines for spinners — smaller change but touches all CLIs'
classification and risks false actives on scrolled agent output.

Typed events (v1.1+, per CLI, in order of payoff):
1. **opencode**: spawn-or-attach to `opencode serve` and consume `GET /event`
   SSE (`session.status` busy/idle/retry + permission/question requests, which
   map cleanly onto turn/interaction). Cleanest event source of the three.
2. **agy**: hooks exist (`.agents/hooks.json`, desktop-compatible format) but
   are file-configured, not argv-injectable — wiring them to `swarm hook`
   would mutate workspace config (violates the T-2 spirit); revisit if agy
   grows an inline-settings flag. Print-mode stream-json is not usable for
   interactive sessions.
3. **vibe**: `vibe-acp` (Agent Client Protocol) is the structured path but
   replaces the TUI (ACP client owns the UI), which conflicts with swarm's
   attach model; needs its own spike before any commitment.

## 5. Small core changes required (none break T-5)

1. **Raise `probeTimeout`** in internal/adapter/detect/detect.go from 2s to ~5s.
   Evidence: under load, vibe's probe hit 2.006s → `Version:""` → greyed
   picker; opencode 1.8s. Detection is already async with a "checking..."
   placeholder and generation-guarded staleness, so a larger bound costs no UX.
2. **swarm-char `-adapter` registry gap**: cmd/swarm-char/main.go's
   `adapterRegistry` never got real adapters registered (claude/codex fixtures
   were hand-curated). Add thin `func(adapter.Fixture) adapter.Adapter`
   wrappers so T-6 characterization is tool-assisted for the trio. (The
   fixtures here were recorded with `-adapter reference`, which works but
   derives a meaningless capability entry.)
3. **Decision needed — vibe env injection**: vibe's model selection and
   update-modal suppression are env-only (`VIBE_ACTIVE_MODEL`,
   `VIBE_ENABLE_UPDATE_CHECKS`). Options: (a) ADR extending the adapter
   contract with a pure `Env(spec) []string` descriptor the shim applies at
   spawn (adds a method to the frozen interface — cheap now with 5 impls, but
   needs an ADR); (b) ship vibe degraded (no model option; update modal visible
   to the attached user, who can dismiss it interactively); (c) shim
   unconditionally exports the vibe vars for every session (gross, rejected).
4. Optional cosmetic: internal/tui/launch.go `authLine()` is claude-only; agy
   (Google OAuth), opencode (opencode.ai account), vibe (Mistral key/OAuth)
   could get equivalent auth-source hints later.

## 6. Risks and open questions

- **agy auto-updater** mutates the binary between launches (1.1.3 → 1.1.4 mid
  research); version pinning is impossible → keep SupportedVersions ceiling
  open and re-probe per form-open (already the case).
- **agy workspace trust**: launches in an untrusted cwd block on a TUI prompt.
  The attached user can answer it (swarm sessions are interactive), so v1 can
  ship without special handling; do NOT auto-seed `trustedWorkspaces` (user
  config mutation).
- **vibe id extraction is impossible from the PTY** — resume-after-daemon-crash
  won't work for vibe sessions unless a future vibe release prints the id (or
  the ACP spike lands). Honest `ConversationID:false` is the v1 answer.
- **opencode `ses_` last-occurrence rule** could still grab a child-session id
  if a subagent id is printed after the exit screen; considered acceptable for
  v1 (exit screen is terminal output), revisit with real-world transcripts.
- **vibe staleness**: local install 2.15.0 vs 2.21.0 latest; re-verify the
  capture-derived markers after upgrading before freezing the real fixtures.
- Quit sequences for scripted characterization: agy = double Ctrl+C, vibe =
  double Ctrl+D, opencode = Ctrl+C (all verified working in the swarm-char
  drive scripts under docs/verification/cli-trio-exploration/).

## 7. Implementation plan (next phase, not this branch)

Per CLI, TDD, mirroring the claude/codex package conventions (conformance +
import-boundary + no-IO-source + fixture + capability tests):
1. Fix swarm-char registry (5.2) and raise probeTimeout (5.1) first.
2. Re-record clean fixtures per CLI with swarm-char (idle + turn + permission
   scenarios; upgrade vibe first), commit under internal/adapter/<cli>/testdata/.
3. Port each trioproto adapter into internal/adapter/<cli>/ with its full test
   suite (failing-first evidence per GG-5).
4. Registry entries (constructors + production) + amend system-spec.md T-7 and
   the architecture diagram; ADR if the Env-descriptor route (5.3a) is chosen.
5. Grid-rule extension per the chosen option in section 4, fixture-proven per
   CLI.
6. Epic evidence file under docs/verification/, audit pass, delete
   internal/adapter/trioproto/ and internal/engine/trio_exploration_test.go.
