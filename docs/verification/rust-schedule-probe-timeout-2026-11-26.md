# Scheduled provider version timeout recovery

The macOS arm64 mixed run of `7202a48d` completed 298 of its 299 schedule claims.
The final claim was due at 05:54 UTC on 2026-11-26. Its sole Codex attempt was
rejected with `provider command timed out`. The saved occurrence has no session
ID, prompt write, or completion. Codex launch preparation calls the five-second
`--version` probe before reserving a Swarm session. The retained evidence
identifies the failed boundary. It does not show whether the child stalled or
lost CPU time while concurrent work was running. During the four preceding
operator samples the daemon held four threads and seven descriptors. The run
did not record host-wide scheduler pressure or sample the failed probe itself.

The provider runner now releases its own stdout writer immediately after spawn
and reads data buffered at exit before parsing the result. A timed-out read-only
version command is reaped, including its owned process group, before one fresh
probe. The executable identity must still match across both probes. Each probe
keeps the original five-second budget, or ten seconds for Hermes. Version results
are fresh because a wrapper can change its downstream target without changing
its own inode. A second timeout remains a pre-reservation refusal. The retry stays inside the original durable provider attempt. The scheduler reconciles
an ambiguous native start without repeating or falling back from that launch.

The isolated fixture suspends the first version process, overlaps a second
caller with the same durable operation ID, and proves that both callers recover the
same run with one native turn. It then suspends both version attempts and proves
that there is no new reservation or prompt write. The archived `7202a48d`
executable under the same fault stopped at the first timeout with the same
rejected attempt detail as the original run. The corrected executable passed
both arms.

```sh
python3 -u scripts/verification/parity_schedule.py --probe-timeout \
  --candidate /absolute/path/to/swarm --provider /absolute/path/to/offline-codex-fixture
```

On macOS arm64, `a8dc4439` also passed the cold generated-timer fixture and
a 155-second mixed calibration with installed OpenCode 1.19.3 and its loopback
model. The calibration completed 2 scheduled occurrences, 22 operator attaches,
22 private Discord fixture publications and 5 daemon restarts. Cleanup passed.
The Swarm executable SHA-256 was
`57b0d8afb217b31dc76e88541509f51067863b1ef19fde12d7621d61d5bfd4e3`.
This run covers the short mixed calibration. The sustained 24-hour gate is pending.
