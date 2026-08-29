# Hermes 0.20.6 characterization record

This is the small, sanitized record of the pre-implementation experiment. The
raw prompt_toolkit captures contain tens of kilobytes of repetitive repaint
traffic, so they are not duplicated under `docs/verification`. The exact payloads
were instead promoted into `internal/adapter/hermes/testdata` for production
replay. The original disposable files were retained during implementation under
`/tmp/swarm-hermes-investigation.MlRp4Y`; promotion changed only the JSON file's
final newline, not the encoded PTY payload.

## Provenance

- Hermes Agent version: `0.20.6`
- Source revision: `aff5125f8edf5095aef5d3d79bbbb101c95b9413`
- Source commit time: `2026-08-29T13:02:13+05:30`
- Python: `3.11.2`
- Test provider/model: local, non-billable OpenAI-compatible mock named
  `swarm-mock` / `swarm-test`
- Characterization host: Apple Silicon (`darwin/arm64`)
- PTY geometry: `100x30`
- Harness: the repository's real `swarm-char`, rebuilt natively for arm64

The installed source reported:

```text
Hermes Agent v0.20.6 (2026.8.27) · upstream aff5125f
Install method: git
Python: 3.11.2
OpenAI SDK: 2.24.0
Up to date
```

Two observed warmed/version-probe runs completed in approximately 0.67 s and
1.24 s, both below Swarm's five-second detection bound. This is an observation,
not a startup-performance guarantee.

Hermes was installed from the pinned checkout into an isolated temporary
virtual environment with:

```text
uv sync --frozen --no-dev --python 3.11
```

The host's older `uv 0.9.21` could not parse the checkout's current project/lock
metadata, so the disposable tool environment used `uv 0.12.7`. No package,
launcher, credential, or Hermes configuration was installed into Swarm or the
operator's normal home. This source-install detail does not become a Swarm
dependency: end users install Hermes by its upstream-supported method and Swarm
only probes `PATH`.

## Capture identity

| Promoted fixture | JSON bytes | JSON SHA-256 | Decoded PTY bytes | PTY SHA-256 |
|---|---:|---|---:|---|
| [`normal.json`](../../../internal/adapter/hermes/testdata/normal.json) | 34,795 | `f2749e2b1ff43cb3c81f649cb2c289a59a2c0f3e1c6fc840b64c862c9d9eab0f` | 25,999 | `94f4649e1e7e8856f4a04b5a4c409a08bb014c1c848fffeadb1b381fe9bf987c` |
| [`approval.json`](../../../internal/adapter/hermes/testdata/approval.json) | 39,714 | `48274a8abc4e02d02bbeb5ad63131aee8573d9accf9961381c658c2dd032f56b` | 29,692 | `ecf92ab0a228b6d4da82c2ac4a2288165a95306f9c18cdaaec3ab61993da558d` |
| [`clarify.json`](../../../internal/adapter/hermes/testdata/clarify.json) | 37,965 | `a35974eb00cebdb515bc7e10e19d0754de7f3fe60e0628b9ed2afa92c6e7ad02` | 28,382 | `4dcf08d6e6dd810de678e3ab8f920e79cc466c1520701128d68082871446af42` |

All three files use fixture schema 1, identify `cli: hermes` and
`version: 0.20.6`, and were captured before a production Hermes adapter existed.
A scan of both JSON and decoded PTY bytes found no email address or common API
key, bearer-token, password, or secret assignment shape. The excerpts below
omit transient paths and decorative ANSI sequences.

## Characterized terminal states

Fresh launch printed the session identity before the first turn:

```text
Session: 20260829_103232_1a7c23
```

In the captured grid, this row belongs to Hermes's contiguous bordered welcome
panel and follows a configured-model row containing `Nous Research`. Production
extraction uses that nearby branding (or the upstream first-run
`no model configured` row) as local corroboration; the text of a lone bordered
`Session:` row is not treated as native identity evidence.

During generation, the stable status-row content was:

```text
⚕ ❯ msg=interrupt · /queue · /bg · /steer · Ctrl+C cancel
```

After the local mock streamed its response, Hermes printed `pong` and returned
to a bordered composer whose input row contains `❯`. On graceful `/exit`, it
printed:

```text
Resume this session with:
  hermes --resume 20260829_103232_1a7c23
Session:        20260829_103232_1a7c23
Title:          pong #3
Duration:       7s
Messages:       2 (1 user, 0 tool calls)
```

The approval capture rendered:

```text
Dangerous Command
↑/↓ to select, Enter to confirm
⚠ ❯
```

The clarification capture rendered:

```text
Hermes needs your input
↑/↓ to select, Enter to lock, Tab next question
? ❯
```

The arrows are visible grid text. Some plain `strings(1)` output loses them,
which is why production tests must replay the capture through `internal/vt`
instead of matching a printable-byte dump.

## Replay experiment

A disposable Go classifier replayed each decoded capture through the real
`internal/vt` emulator in 128-byte chunks. It prioritized modal navigation,
then the busy row, then a bordered composer. Observed outcomes were:

| Capture | Observed terminal outcome | False idle during observed work |
|---|---|---|
| normal turn | alternating active/inconclusive repaint frames, then idle/none | none observed |
| approval | initial idle, active/inconclusive repaint frames, then idle/permission | none observed |
| clarification | initial idle, active/inconclusive repaint frames, then idle/prompt | none observed |

This coarse replay proves marker feasibility, not byte-granular production
correctness. It was not accepted as a substitute for structural adversarial
cases and complete package-local fixture replay; the production suite now adds
both at a 16-byte evaluation cadence. Inconclusive repaint frames are expected
and safe because T-4 preserves the previously committed status.

## Final native fresh/resume smoke and cwd correction

A final native Apple Silicon acceptance run used the production adapter argv,
the default Hermes profile, and the local mock provider. The fresh prompt
`Reply exactly pong` received `pong`, then clean exit exposed session
`20260829_114328_1ee20e`. Resume printed:

```text
↻ Resumed session 20260829_114328_1ee20e "pong #7" (1 user message, 2 total messages)
```

and restored visible history rows:

```text
You: Reply exactly pong
Hermes: pong
```

A subsequent scripted prompt, `Reply exactly pong again`, also received
`pong`. This establishes fresh turn, clean exit, identity capture, retained
history, continued interaction after resume, and not merely successful process
startup.

The resume was spawned with Swarm cwd `/tmp` and:

```text
hermes chat --cli --resume 20260829_114328_1ee20e \
  --no-restore-cwd --provider swarm-mock --model swarm-test
```

Contrary to the earlier strategy-phase interpretation, it then printed:

```text
↻ Working directory: /private/tmp/swarm-hermes-investigation.MlRp4Y/home
```

and actually changed to that recorded directory. The earlier claim that the
scratch cwd remained authoritative was false and is superseded by this final
production-argv smoke.

The first resume banner initially showed `/private/tmp`, matching the requested
spawn cwd after macOS path canonicalization. The later `↻ Working directory:`
line and actual process state therefore prove a post-startup override rather
than an incorrectly composed `ResumeSpec`.

`swarm-char` derived the same capability entry on both fresh and resumed runs:

```text
cli: hermes
version: 0.20.6
hooks: false
resume: true
conversation_id: true
options: 7
signals: [heuristic]
```

Source inspection explains the result. `hermes_cli/main.py` honors
`no_restore_cwd` in its early workspace-binding path, but
`hermes_cli/cli_agent_setup_mixin.py` later calls `cli.py`'s
`_restore_session_cwd` without consulting the flag. That helper calls
`os.chdir(recorded)` and resets `TERMINAL_CWD` to the same path. Swarm will keep
emitting the documented flag for forward compatibility, but Hermes `0.20.6`
resume returns to its recorded cwd. The profile flag's placement and
profile-scoped behavior were confirmed from source and argv parsing, but the
live run used the default profile. Moreover, Swarm's current one-key TUI resume
sends `resume_from` without replaying the source session's saved launch options.
The adapter can carry an explicitly supplied `--profile`; automatic named-
profile carry is not established and can fail by searching the wrong Hermes
profile store.

## First-run and harness input

A real launch with an empty temporary Hermes home reached the classic banner and
interactive setup/idle surface, accepted terminal input, and exited cleanly with
Ctrl-D. Swarm does not need to seed auth or configuration for the process to be
attachable. This was a manual PTY observation rather than one of the three
retained fixtures; provider-specific onboarding remains owned by Hermes.

One harness detail was experimentally important: prompt_toolkit treated carriage
return (`\r`) as Enter, while a drive script that sent only line feed (`\n`) did
not submit the composer. The successful `swarm-char` recordings therefore use
terminal Enter semantics. This does not require a Swarm production special case;
attached input already carries terminal key bytes, but future characterization
scripts must not substitute newline for Enter.

## Architecture failure experiment

The host was arm64 while `go env GOARCH` initially selected amd64. An x86_64
`swarm-char` caused the universal Python launcher to run translated and reject
the arm64 native extension:

```text
pydantic_core.cpython-311-darwin.so: incompatible architecture
(have 'arm64', need 'x86_64')
```

Rebuilding `swarm-char` as arm64 made all three successful captures possible.
This was a harness/release-architecture failure, not an adapter failure. It is
the empirical reason that the Hermes macOS acceptance target is native
`darwin/arm64`, never a Rosetta-translated Swarm process.

## Commands already run

The strategy experiment ran the following checks before production lanes began:

```text
# Disposable adapter module: conformance, argv, version and ID unit tests.
go test -race ./...

# Existing Swarm adapter and engine baseline.
go test ./internal/adapter/... ./internal/engine

# Existing picker/detection baseline.
go test ./cmd/swarm \
  -run 'TestDetectAgentsWith|TestUnavailabilityReason|TestArchAugmentedReason'
```

All three commands passed. A direct extractor probe over the normal-turn raw
capture returned `20260829_103232_1a7c23`. The 128-byte VT replay described
above was also run over all three successful raw captures.

Broader `cmd/swarm-char` and `cmd/swarm` test attempts reached tests that bind
Unix/TCP sockets and failed with `bind: operation not permitted` in the managed
sandbox. The `cmd/swarm` attempt was stopped after 112 seconds of cascading
sandbox failures. Those attempts are neither a product regression nor green
evidence; the full suites remain mandatory in normal CI. No broad suite is
claimed complete by this characterization.

## What this record does not prove

- This pre-implementation record contains no Linux session. Subsequent
  real-binary acceptance smokes established both targets in the
  [main evidence](../hermes-adapter-evidence.md): arm64 native to a LinuxKit VM
  and x86_64 under Docker emulation on Apple Silicon. Those later runs do not
  turn the three retained PTY fixtures here into Linux captures or prove every
  Linux distro and packaging environment.
- No named-profile resume capture was retained.
- Swarm's one-key TUI resume does not currently recover the source's named
  profile option; this needs a generic persisted resume-option contract rather
  than an adapter-only flag change.
- No terminal-resize capture was retained.
- No real paid provider was called; model correctness and provider credentials
  are intentionally outside adapter acceptance.
- No hooks, Gateway, ACP, or Ink-TUI behavior was characterized for v1.
- The coarse replay is not a substitute for production adversarial and
  byte-stream fixture tests.
- Mid-process Hermes session-ID rotation was observed in source, not exercised
  in the retained PTY captures; v1 must not promise latest-continuation resume.
- `--no-restore-cwd` is ineffective in Hermes `0.20.6`'s final resumed-agent
  setup; Swarm cannot promise that its selected resume cwd survives startup.
