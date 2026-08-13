# Provenance of the Claude Code interaction corpus

Every fixture in this directory is a **verbatim byte-for-byte copy** of a real recorded capture
from spike S-B (`docs/verification/spike-SB.md`), taken 2026-07-18 against real `claude` 2.1.214
(Opus 4.8, claude.ai OAuth) driven through the actual `swarm-char` binary — real PTY, real vt
emulator, real hook sink. Nothing here is hand-authored, reconstructed, or edited: the source
files are `docs/verification/fixtures/spike-sb/*.json` and `cmp` reports them identical.

| File | Source | Provenance | What it grounds |
|---|---|---|---|
| `claude-bash-pretooluse-no-escalation.json` | `docs/verification/fixtures/spike-sb/claude-bash-pretooluse-no-escalation.json` | copied | a Bash call that never escalated: `UserPromptSubmit` -> `PreToolUse` -> `PostToolUse` with real `tool_response.stdout`, plus a second prompt in the same session |
| `claude-bash-permissionrequest-run1.json` | `docs/verification/fixtures/spike-sb/claude-bash-permissionrequest-run1.json` | copied | the ONE genuine Bash `PermissionRequest` S-B captured — and it is a Bash-with-a-file-path, so it is spike S-C's carve-out (`mode: prompt_card`). Its `pty_capture` holds the real on-screen dialog the keystroke map is read off. |
| `claude-edit-permissionrequest-run1.json` | `docs/verification/fixtures/spike-sb/claude-edit-permissionrequest-run1.json` | copied | a Read tool run, an Edit `PermissionRequest` (native `mode: card`), and the `tool_response.structuredPatch` that becomes a `file_change` |

The **Provenance** column is BEAD nq0q's fence's own input (`internal/adapter/claude/provenance_test.go`):
`copied` rows are asserted sha256-identical to their declared Source; a `reconstructed` row (none
today) would be exempt, marked here rather than inferred.

Each file's `hook_payloads` also carries its turn's **`Stop`** body, recorded in the same runs and
copied with everything else: `claude-bash-pretooluse-no-escalation.json` holds two (one per
prompt), the other two hold one each, and every one of them carries a non-empty
`last_assistant_message`. That field is the `agent_message` the producer shapes under ADR-010's
2026-08-07 amendment (`Stop` as the fifth `capture=raw` row). **No fixture was added, edited or
reconstructed for it** — the bytes were already here, verbatim, and were simply not shaped.

`claude-edit-permissionrequest-run2.json` is deliberately NOT copied: S-B records it as having an
identical field set to run1 with only ids and the path differing, so it adds a second copy of the
same shapes and no new coverage. It stays available under `docs/verification/fixtures/spike-sb/`.

## What the corpus does NOT contain, and therefore what the producer does not shape

Recorded honestly rather than filled in from memory of the CLI's docs. Each is one recorded
payload away from being supported, and none is guessed at meanwhile (IS-TOOL-2's posture):

- **`PermissionDenied`** — never observed in any of S-B's ten runs, and Mirror M1.3
  (`agents-tracker-dwwv.2.3`) went looking specifically, against the real, installed claude
  2.1.231 (the same version M1.1's dialog fixtures were captured against): an interactive "No"
  keystroke on a real recorded dialog (twice), a `permissions.deny` rule under
  `--permission-mode manual`, and the same rule under `--permission-mode auto`. All four denied
  the tool for real (confirmed by the marker file never being created and, in the rule runs, the
  agent's own reply naming the block) and none produced a `PermissionDenied` hook body. `strings`
  on the installed binary explains why: the event's one real call site gates on
  `decisionReason.type=="classifier" && decisionReason.classifier=="auto-mode"`, and the binary's
  own embedded schema names the event "Emitted when a tool call is auto-denied WITHOUT an
  interactive permission prompt (e.g. auto-mode classifier, dontAsk mode, headless-agent
  auto-deny, or a deny rule)" — a family of NON-interactive paths, not the TUI dialog's own
  keystroke handling. `permissions.deny` is named in that description too, and did not reproduce
  it on this CLI version either, so a deny RULE alone is not sufficient at 2.1.231 (or the
  remaining trigger, `dontAsk` mode, is the one that actually carries it — untried). Full record,
  including the verbatim hook dumps of all four runs: `docs/verification/mirror-m1.md`'s M1.3
  section. Follow-up: `agents-tracker-hgyg`.
- **A `Write` `PostToolUse`** — S-B captured Write's `PreToolUse` (`file_path`/`content`) only, so
  whether Write's response carries a `structuredPatch` is unobserved.
- **`Grep`/`Glob`/`WebFetch` `tool_input`** — never captured, so §7's `search` and `fetch` action
  types have no evidenced key to read and those calls classify as `other`.
- **An exit code, a non-empty `stderr`, or an `interrupted: true` tool response** — the shapes
  exist in the recorded `tool_response` objects but only ever with benign values.
