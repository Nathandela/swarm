# Isolated OpenCode native qualification

`opencode_native_loopback.py` runs a real standalone OpenCode TUI through a supplied Swarm binary. Its minimum stable version is 1.19.3; live qualification so far uses 1.19.3. It creates a fresh private XDG profile, project, state directory, and deterministic OpenAI-compatible HTTP fixture on loopback. The fixture supplies scripted text, safe `printf` tool calls, questions, and native task children. The primary, small/title, and child models all use the fixture provider. It does not seed user messages or mutate the native database.

Python 3, `lsof`, an executable Swarm binary, and stable OpenCode >=1.19.3 are required. `--expected-opencode-version` must equal the actual supplied binary's version; accepting a newer version does not qualify it without running the checks. The native provider package must already be available to OpenCode. Nothing is installed by the script. `HOME` is preserved; only the explicitly isolated test profile is configured. Existing run directories are refused. Keep the run path short enough for Unix sockets (the script checks the exact bound). Python optimization is refused because assertions enforce the gates.

Build the Swarm binary from the commit being qualified, then run:

```sh
python3 scripts/verification/opencode_native_loopback.py \
  --swarm /tmp/swarm-native \
  --opencode /usr/local/bin/opencode \
  --expected-opencode-version 1.19.3 \
  --source-head COMMIT_OF_THE_SUPPLIED_SWARM_BINARY \
  --run-dir /tmp/native-qualification-UNIQUE \
  --duration-seconds 3600 --max-iterations 500 \
  --interval-seconds 10 --restart-every 20 \
  --tool-finish stop --final-delay-seconds 2
```

Omit `--duration-seconds` for three root inputs and one native child. Use `--restart-every 1` to exercise two daemon restarts in that short run. `--tool-finish stop` deliberately emits ordinary tool calls with the provider's stop marker; receipts must remain admitted until the later tool-free assistant response proves completion.

Add `--check-arguments` to each planned command before starting a sequential
queue. This validates its arguments and socket path without creating a run
directory or starting workers. Intervals are bounded to 60 seconds. Valid
duration/interval/iteration plans include `3600/10/500`, `86400/60/2000` and
`259200/60/6000`; the iteration budget must leave enough time for the full
duration. Run the preflight regression checks with
`python3 -m unittest discover -s scripts/verification -p 'test_*.py'`.

The fixture profile selects `max` by default. Use `--variant default` for a second
short run with no native agent or persisted model variant selected. This checks
the native default representation; it does not add a Swarm variant bootstrap API.
The third input also verifies native newest-page, opaque continuation and exact
item-anchor history reads, separate from the live journal.

The script tests genuine initial input, semantic submission, public owner request cards and decisions, exact native message identities, completed-child reads, duplicate semantic operations, reducer completion, and current native activity after daemon SIGKILL/rejoin. It records concurrent permission/question requests and checks that active child scope settles. A transient write refusal permits a fresh operation only after a fully correlated, durable no-effect rejection. Unknown outcomes stop the workload; they are never replayed.

## Declared gates

Defaults are written to `declared-budgets.json` before launch. Override them explicitly for a different qualification target; do not change them after observing a run.

| Gate | Default | Measurement |
| --- | ---: | --- |
| Swarm RSS | 256 MiB per daemon/shim process | `ps` at busy, idle, and rejoin checkpoints |
| Swarm file descriptors | 128 per daemon/shim process | Numeric open descriptors from `lsof` |
| Swarm disk | 256 MiB | Private Swarm state tree plus daemon stdout/stderr log |
| Semantic/decision dispatch | 30 seconds | Owner protocol request through response |
| Daemon recovery | 15 seconds | SIGKILL through fresh owner observation of exact root/generation, known current scope and idle |

Native OpenCode RSS/FDs and SQLite/WAL/SHM sizes are reported separately. They are not covered by the Swarm process budgets. Goroutine counts and private queue depth are not exposed by the production API and are not qualified. Stream gap counts, native activity, identities, and restart outcomes are retained. RSS sampling does not establish a between-samples peak or an asymptotic memory bound.

An exceeded gate returns exit status 1 and preserves failure evidence. A negative control such as `--max-swarm-rss-mib 1` must fail against a real running Swarm process. Cleanup uses only this run's owner socket and its owned processes, then checks all owned shim exit records.

`result.json`, `finished.json`, and `metrics.jsonl` retain the source commit supplied by the caller, Swarm/native binary hashes, fixture source hash, configured limits, real elapsed time, native message IDs, and cleanup result. Keep the matching script revision. A duration qualification requires `pass: true` **and** `requested_duration_completed: true`; reaching the iteration cap early is not duration completion. The maximum accepted duration is 72 hours, but only actual measured runs qualify that duration.

These are synthetic upstream-native integration checks. They do not qualify DeepSeek/GLM provider execution or model quality, hosted relay/physical Android transport, native automatic compaction ownership transfer, or strict native turn cancellation.
