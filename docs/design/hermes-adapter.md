# Hermes CLI adapter contract

**Status:** binding implementation contract, based on pre-implementation
characterization on 2026-08-29. The shipped code and its verification evidence
take precedence if this document later drifts.

**Target:** NousResearch Hermes Agent `0.20.6`, source revision
[`aff5125f8edf5095aef5d3d79bbbb101c95b9413`](https://github.com/NousResearch/hermes-agent/tree/aff5125f8edf5095aef5d3d79bbbb101c95b9413).

This integration adds Hermes as a production Swarm CLI without adding another
process owner or wire protocol. Swarm's existing shim owns a PTY, Hermes's
classic interactive CLI runs inside it, and the engine derives status from the
emulated grid. The adapter remains a stateless strategy object under T-1.

## Frozen decisions

1. Swarm drives `hermes chat --cli`, even when the user's Hermes environment or
   configuration selects the newer TUI. `--cli` is an internal invariant, not a
   user option. It gives the adapter one characterized screen protocol.
2. Hermes must already be installed and discoverable on the session `PATH`.
   Swarm detects it with `hermes --version`; it does not install, upgrade,
   authenticate, or configure it. The upstream installation guide remains the
   source of truth: <https://hermes-agent.nousresearch.com/docs/getting-started/installation>.
3. Swarm owns the fresh process's initial working directory and any Swarm
   worktree. The adapter never emits Hermes's `--worktree`; every resume emits
   `--no-restore-cwd` as the
   documented upstream opt-out and for forward compatibility. Hermes `0.20.6`
   has a later resume-path bug that ignores the opt-out in practice and returns
   the process and terminal tools to the recorded session cwd. Swarm cannot make
   its selected resume cwd authoritative on that release.
4. The v1 signal source is a dedicated `grid: hermes` heuristic. Hermes hooks
   are profile/configuration based and can execute arbitrary subprocesses with
   the user's credentials; they are not a per-invocation authenticated source
   satisfying T-2. The Gateway and ACP are separate structured transports, not
   terminal adapters. All three are deferred.
5. The characterized minimum version is `0.20.6`. The initial ceiling remains
   open (`9999.0.0`) so a new release is detectable, but any upstream screen or
   identifier change requires re-characterization rather than a relaxed parser.
6. macOS support for this integration is Apple Silicon only. Linux support
   targets amd64 and arm64. `darwin/amd64` is unsupported for Hermes even though
   Swarm may continue publishing that artifact for other agents. Unsupported
   means no Hermes acceptance guarantee; the adapter does not add a platform
   branch to daemon core merely to hide a manually installed binary. This follows
   the [upstream platform tiers](https://hermes-agent.nousresearch.com/docs/getting-started/platform-support).

These decisions preserve T-5: the implementation needs an adapter package,
engine signature, registry entries, and tests, but no daemon-core, protocol, or
TUI behavior specific to Hermes.

## Adapter contract

Identity and detection:

| Field | Value |
|---|---|
| Adapter name | `hermes` |
| Binary | `hermes` |
| Version probe | `hermes --version` |
| Supported range | `0.20.6` through the conventional open ceiling |
| Signal declaration | `heuristic` with descriptor `grid=hermes` |

The fresh command is composed in this stable order:

```text
hermes [--profile PROFILE] chat --cli
  [--provider PROVIDER]
  [--model MODEL]
  [--reasoning LEVEL]
  [--toolsets CSV]
  [--skills CSV]
  [--yolo]
  [-q INITIAL_PROMPT]
```

The resume command is:

```text
hermes [--profile PROFILE] chat --cli
  --resume SESSION_ID --no-restore-cwd
  [--provider PROVIDER]
  [--model MODEL]
  [--reasoning LEVEL]
  [--toolsets CSV]
  [--skills CSV]
  [--yolo]
```

The adapter returns argv elements, never a shell command. Newlines, spaces, and
flag-looking text in the initial prompt therefore remain one literal argument.
Empty values emit no flag. `yolo` defaults to false and must have a warning label
because it bypasses Hermes approval prompts.

The declarative option schema is:

| Key | Type | Meaning |
|---|---|---|
| `profile` | string | Hermes profile; emitted on fresh/resume only when explicitly supplied |
| `provider` | string | Hermes provider override |
| `model` | string | Hermes model override |
| `reasoning` | choice | `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, or `ultra` |
| `toolsets` | string | comma-separated Hermes toolsets |
| `skills` | string | comma-separated Hermes skills |
| `yolo` | bool | explicit approval bypass; false by default |

Blank provider/model values deliberately leave selection to Hermes's profile
configuration. Model enumeration is not part of v1: Hermes providers can be
dynamic, and reading provider configuration would make the adapter impure.

## Conversation identity and resume

Classic Hermes `0.20.6` creates identifiers shaped as:

```text
YYYYMMDD_HHMMSS_[0-9a-f]{6}
```

The adapter recognizes only that 22-byte form. The Gateway's different ID shape
is not accepted speculatively in the classic-CLI adapter. The timestamp portion
must also parse as a real local wall-clock calendar time; a lexically plausible
value such as month 13 or hour 29 is not a native Hermes identity.

Hermes implements the adapter package's optional pure
`ConversationIDValidator` extension with that same strict parser. Generic live
capture consults it before write-once persistence, and saved/external resume
consults it before binary resolution or argv composition. Invalid extractor
output, corrupt persisted metadata, and an externally supplied identity that is
not native to Hermes therefore fail closed without adding a Hermes switch to
daemon core or widening the frozen base `Adapter` interface.

Extraction order is:

1. the last complete graceful-exit block, consisting of
   `Resume this session with:`, an immediately following
   `hermes --resume SESSION_ID`, and a source-shaped unbordered summary within
   the next eight lines: `Session:` carrying the same ID, optional `Title:`,
   then contiguous, grammar-valid `Duration:` and `Messages:` fields;
2. otherwise, an unambiguous startup-banner identity in a bordered `Session:`
   row, locally corroborated by a contiguous bordered row containing either
   `Nous Research` or Hermes's first-run `no model configured` text within the
   four preceding rows (repeated occurrences of the same ID are allowed);
3. an unambiguous rendered-grid startup marker as a fallback when raw-tail
   history no longer contains it.

A match requires a full identifier and a non-identifier terminator. EOF directly
after a candidate is inconclusive, preventing a partial PTY write from being
persisted. A lone bordered `Session:` row is not enough: model output can
reproduce it, so the nearby outer-panel branding is part of the proof. The
daemon's live scan can capture the corroborated startup banner before a crash;
when extraction first runs over a completed tail, the fully corroborated exit
block carries Hermes's most current advertised identity. Swarm's existing
write-once persistence prevents later model prose from replacing an identity
already captured live; the exit preference therefore matters only when no ID
was persisted from the earlier live banner scan.

“Corroborated” here means source-shaped, not cryptographically authenticated.
The strict banner/summary grammar plus early write-once capture sharply reduces
accidental or ordinary quoted-text matches, but terminal output remains an
untrusted transport. A deliberately exact forged structure emitted before any
valid live capture is a residual limitation of heuristic identity extraction.

`--no-restore-cwd` remains mandatory in Swarm's resume argv because it is
Hermes's documented opt-out and may work in a future release. It does **not**
keep `ResumeSpec.Cwd` authoritative in Hermes `0.20.6`. The early dispatcher
honors the flag in
[`hermes_cli/main.py`](https://github.com/NousResearch/hermes-agent/blob/aff5125f8edf5095aef5d3d79bbbb101c95b9413/hermes_cli/main.py#L3212),
but resumed-agent setup later calls `_restore_session_cwd` unconditionally in
[`hermes_cli/cli_agent_setup_mixin.py`](https://github.com/NousResearch/hermes-agent/blob/aff5125f8edf5095aef5d3d79bbbb101c95b9413/hermes_cli/cli_agent_setup_mixin.py#L458).
That helper changes both the process cwd and `TERMINAL_CWD`. Consequently, v1
resume returns to Hermes's recorded session directory; the launch form's cwd is
only the initial spawn directory. Profiles isolate Hermes configuration and
session state, so a caller resuming a named-profile session must explicitly
supply the same `profile` option.

That explicit qualifier matters: `persist.Meta` records launch options, but the
current `SessionView`/TUI one-key resume sends only `resume_from`; it does not
replay the source's saved options. The adapter correctly places `--profile`
when it is supplied to `Resume`, but it cannot recover the profile by itself.
Consequently, default-profile resume is live-proven, while one-key TUI resume of
a non-default-profile Hermes session can search the wrong profile store and fail
to find the native ID. Automatic profile carry needs a generic persisted
resume-option contract and protocol decision outside this adapter slice.

Hermes's classic ID is not a UUID. Swarm's generic external
`resume_conversation_id` gate first accepts canonical lowercase UUIDs only, then
applies the adapter's native validator. A classic Hermes ID fails the first
gate, while a UUID fails the Hermes gate; direct external Hermes adoption is
therefore intentionally unavailable in v1. Hands-off-handoff identity/history
paths are likewise UUID/provider-layout constrained. Hermes resume is available
through `resume_from`: the source must be a Swarm-saved Hermes session whose
captured native ID passes the Hermes validator. Direct external-ID adoption and
Hermes-as-source hands-off handoff require a separate generic identity-contract
expansion.

There is one explicit v1 limitation: Hermes can rotate its session ID inside a
running process through features such as `/new`, branching, and continuation or
compression paths. Swarm's native conversation identity is write-once. The
initial banner therefore guarantees an identity for crash-resume, but v1 does
not claim that it always follows Hermes's latest mid-process rotated identity.
That behavior needs separate characterization and likely a structured identity
update contract before it can be promised.

## Status classification

The engine reads the emulated grid, never raw bytes (T-3). It examines bounded
terminal chrome near the bottom of the screen and applies this precedence:

| Corroborated screen shape | Turn | Interaction |
|---|---|---|
| Selection or slash-confirmation navigation plus matching `⚠` state composer and terminal rule geometry | idle | permission |
| Selection/free-text navigation plus matching `?` or `✎` state composer and terminal rule geometry | idle | prompt |
| Synchronous-command hint plus matching spinner composer and terminal rule geometry | active | none |
| Hermes `⚕` generation status carrying its characterized interrupt/queue placeholder and terminal rule geometry | active | none |
| Bare composer, or valid profile prefix plus an upstream prompt suffix, in terminal rule geometry | idle | none |
| Anything else, including a partial redraw | inconclusive | inconclusive |

Modal states outrank stale busy/composer chrome. Matching a phrase alone is not
enough: agent output is untrusted and can quote any marker. The classifier must
validate the matching navigation/modal-composer/border shape, status-row shape,
or ordinary composer-plus-border structure, and must ignore matching prose
outside the bounded chrome region.

The accepted profile prefix mirrors Hermes `0.20.6`:
`[a-z0-9][a-z0-9_-]{0,63}`. A generic one-token prefix is too broad: the live
fixture replay exposed a partial border redraw, `──❯`, that would otherwise be
misread as an idle profile composer while Hermes was still working.

The accepted prompt suffixes are the classic renderer's `❯`, `>`, `$`, `#`,
`›`, `»`, and `→` variants. Geometry follows Hermes's responsive breakpoint:
wide surfaces require both the preceding top rule and terminal lower rule;
below 64 columns the source hides the lower rule, so the recognized state row
must itself be the last nonblank row. These terminal constraints prevent a
complete-looking block quoted in model output from overriding the real input
surface below it.

An inconclusive live frame preserves the last committed state under T-4. This is
important because prompt_toolkit repaints the status row in pieces; it is safer
to retain `active` briefly than to announce a false ready state.

## Typed transport decision

Hermes exposes richer integration surfaces documented at
<https://hermes-agent.nousresearch.com/docs/developer-guide/programmatic-integration>.
They are intentionally not hidden behind this adapter:

- profile hooks require persistent user configuration, consent, and executable
  allowlisting, contrary to T-2's per-invocation/global-config constraint;
- the TUI Gateway uses JSON-RPC over stdio or WebSocket and does not implement
  Swarm's current structured-backend lifecycle;
- ACP is also a distinct stdio JSON-RPC protocol.

Gateway support is the preferred later route for structured remote chat,
streaming events, approvals, and clarification. It requires its own ADR covering
transport ownership, readiness, authentication, cancellation, and capability
mapping. It must not enlarge this terminal-adapter slice.

## Acceptance boundary

Production acceptance requires:

- conformance, deterministic argv, defensive version parsing, immutable option
  returns, no-I/O, strict ID-extraction tests, and saved-ID validation before
  resume argv composition;
- adversarial grid tests plus replay of normal, approval, and clarification PTY
  captures with no false-idle transition during a turn;
- registry/picker, generic launch composition, capability, fresh-launch, and
  saved-source `resume_from` integration tests, with the Hermes `0.20.6`
  recorded-cwd limitation asserted rather than an authoritative-cwd claim;
- a native `darwin/arm64` real-Hermes smoke and Linux build/test matrix for
  `linux/amd64` and `linux/arm64`;
- GG-5 failing-first evidence and the complete GG-4 gates;
- a T-7 system-spec update naming Hermes once the production adapter ships.

No acceptance statement may imply that Linux was live-characterized merely
because the Go code cross-builds, or that Gateway/ACP capabilities are present
because upstream documents them.
