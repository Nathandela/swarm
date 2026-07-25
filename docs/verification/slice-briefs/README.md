# Slice briefs — the orchestration record

These are the briefs handed to the agents that implemented Phase B, kept because they encode
cross-slice knowledge that is expensive to rediscover and is not derivable from the requirements
alone: which traps a slice will hit, which fences it will trip, what a previous slice already proved,
and which of its own requirements have been found unimplementable as written.

They are **not** specifications. `docs/specifications/remote-phaseB-requirements.md` is authoritative
and has been amended five times during implementation; where a brief and the spec disagree, the spec
wins and the brief is stale.

## Why they are here rather than in a scratch directory

The briefs for the remaining slices would otherwise have lived only in a job temp directory that is
deleted with the job. Losing them costs the accumulated context, not the instructions — anyone can
re-read a requirement, but the fact that (for example) an exit demonstration routed through the phone
simulator proves almost nothing, or that a latency harness never enters the code it appears to time,
was learned rather than looked up.

## What is here

| Brief | Slice | State |
|---|---|---|
| `s10-red-brief.md` | S10 — staleness, repair, epoch-grant delivery | SHIPPED, then re-audited twice |
| `s15-brief.md` | S15 — which tier seals which state | SHIPPED |
| `s17-brief.md` | S17 — the phone's push client | RED complete, GREEN owed |
| `s18-brief.md` | S18 — security hardening | not started |
| `s19-brief.md` | S19 — the exit demonstration | not started |
| `s20-brief.md` | S20 — closure, runbooks, the certificate pin | not started |

S16's brief is absent deliberately: that slice's scope changed three times while it ran (a
requirement was reassigned to it, one of its own was redefined, and a third state was added), so the
brief on disk is the least reliable document about it. Read the requirement rows and
`remote-phaseB-progress.md` instead.

## The parts worth reading even if you never dispatch an agent

- **`s19-brief.md`** carries the measured inventory of what the phone simulator skips — durable
  state, the durable send-seq reservation, the lease gate, coalescing, the reconcile gate, skew, the
  undelivered ledger — every one belonging to a slice that has already shipped. That list is what the
  exit demonstration has to cover, and it is why routing it through the simulator would prove almost
  nothing.
- **`s20-brief.md`** records that the relay certificate pin compares a full leaf certificate, so on
  Android — where the trust source is pinning-only — every certificate renewal breaks the handset.
  It also flags that the requirement's own proposed fix is incomplete: pinning the key hash survives
  renewal only if the key is reused, which most ACME clients do not do by default.
- **`s18-brief.md`** notes that a security assertion which *skips* is worse than one that fails, with
  two criteria already recorded in this project that read as green while proving less than they
  appear to.
