# Dispatcher worker note qualification

The release candidate uses ordinary Swarm model defaults. The plan was reviewed independently by Sol and Astra at `2d59e59f` in [PR #86](https://github.com/Nathandela/swarm/pull/86).

## Isolated native rehearsals on 20 November 2026

Each run used a real native manager, provider accounts and supervisor, a temporary project, and a synthetic Discord sink. The worker alone read a random question marker and result. It retrieved the manager's exact answer by note ID inside its running turn. The manager parked while the worker remained live. The connector stopped, the worker recorded its queued completion receipt, and the connector restarted. The result reached the original discussion with the original job and attempt without a second human message.

| Worker | Question publication | Answer observed in worker | Manager parked | Completion after reopening |
| --- | --- | --- | --- | --- |
| Codex | 43 s | 13 s later | 2 min 22 s later | 14 s |
| Claude | 63 s | 27 s later | 2 min 8 s later | 26 s |

The Codex run produced publication `publication:5:81b24ddb8948403fa3409d1c20b8f6bd484808d2559efa28d21093eb1831fbbc`. The Claude run produced `publication:4:e755141183ba55cab5b416720b0da4a5c15c5d71f89e1b65fa6ad170923916f0`. These are connector publication IDs. Discord assigns its own message IDs on actual delivery.

A separate upgrade trial started a Codex worker on v0.29.5, replaced the supervisor, retained shim PID 2514881, and issued the first semantic send with a cold identity cache. Its exact native completion turn was `01a1fa59-31ee-7b80-be3f-7708775b80e4`. The fresh-session and expired-cache cases completed in turns `01a1fa59-68cb-7083-b397-a3d2fbc88086` and `01a1fa59-7dc4-72b0-be45-a27fc6084631`. Replaying each operation ID produced one native completion for its random marker. The generic receipt still reported submitted, so the native transcript supplied the execution evidence.

A fresh Codex session discovered the guide through top-level CLI help. A fresh Claude session discovered the bundled skill in a clean workspace and followed its reference to the command. Both named the two notification kinds and the reply command without receiving a skill path or subcommand in the discovery prompt.
