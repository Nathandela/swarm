# CLI Trio Exploration Evidence (2026-07-18)

Raw characterization fixtures for the strategy phase of the CLI adapter design
(docs/design/cli-trio-adapters.md, issue agents-tracker-5gv). Integration
targets are agy and opencode; vibe was evaluated and DROPPED (2026-07-18, see
the design doc appendix) — its fixture and drive script are retained here as
the record of that evaluation.
Recorded with the repo's own harness (`swarm-char -adapter reference`, PTY
100x30, 45s bound) against the locally installed binaries: agy 1.1.4,
opencode 1.17.9, vibe 2.15.0.

Each fixture drove a full interactive turn: startup, typed prompt
("Say OK and nothing else." + Enter at t=6s), generation, idle, scripted quit
(agy/opencode: double Ctrl+C; vibe: double Ctrl+D). vibe ran with
`--trust` and `VIBE_ENABLE_UPDATE_CHECKS=false`.

Consumed by the TEMPORARY tests (deleted with this phase's scaffolding once
implementation starts):

```
SWARM_TRIO_FIXDIR=docs/verification/cli-trio-exploration \
  go test ./internal/adapter/trioproto/ ./internal/engine/ -run 'TestTrio|TestExtract' -v
```

Key facts these captures prove (details in the design doc):
- agy prints `agy --conversation=<uuid>` and opencode prints
  `Continue  opencode -s ses_<id>` on their exit screens (id extraction works);
  vibe never emits its session id into the PTY stream.
- The stock last-line grid heuristic sees agy's `⣷ Generating...` (active) but
  classifies all three CLIs as unknown at idle, and opencode/vibe as unknown
  even while busy — their bottom bars own the last grid line.
- The fixtures contain the account identity line agy renders (owner's own
  Google account email, as shown in its TUI header).
