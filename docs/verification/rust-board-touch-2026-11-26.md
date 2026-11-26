# Attached CLI button qualification

Pinned Go source is `f26648d6`. The public witness uses the `swarm` board, Options, owner, shim, and an offline fixture provider. Its PTYs are 80×24 and resize to 30×2 during the held gesture. An ordinary leased worker receives a 23-row area while the 24th row holds attach chrome. The fixture state is disposable and network-denied.

```sh
scripts/verification/parity/test_board.py --touch-back --supervisor "$go" --board "$go"
scripts/verification/parity/test_board.py --touch-back --supervisor "$go" --board "$rust"
scripts/verification/parity/test_board.py --touch-back --supervisor "$rust" --board "$rust"
```

All three arms passed on each native host. The witness starts with the preference off and verifies raw native mouse delivery, then enables it using Options arrows and Enter. Native press and release still reach the worker, including a release over the footer. A footer drag cancels without leaking its bytes. A split footer click detaches. A split native mode query observes restored native modes before the terminal receives the query, and native mouse delivery resumes. A second worker starts without native mouse modes and exercises lease-preserving shrink and grow during a gesture. A stale release stays attached and the next independent click returns to the board. Quitting the board restores the complete original `termios` settings. Fixture cleanup reported zero surviving processes.

| Native host | Go SHA-256 | Rust SHA-256 |
| --- | --- | --- |
| Darwin arm64 | `5f92beebd77f4d49a45ca7989a9d88d42bc3192b623356661217f102366fe179` | `4ff0de228a1d9e15c33fdcdff1ed94abd2042da64110b8da43a531ae4d4711ea` |
| Linux x86_64 | `5808d838158261470fb68cc97c12d041c4436a77a48746cd00ae75ffbec7f05d` | `39667cc4deba84a7e1f6a66992d297817fd0b9d1c72a4db7e44bf623bb8dbf55` |

Rust unit coverage includes ambiguous and unknown masks, a split live change into pixel encoding, string payload handling, resize cancellation, native release routing, and C1 SGR clicks. The Go shim produces a DECRQM reply in the bare transport PTY. The Rust shim in this tranche sends the restored native query through to the outer terminal and currently has no emulated reply in a bare PTY. The witness records that exact known difference. It does not qualify emulator reply parity, the Manager Prompt or Codex Context Guard owner settings, or full Options cell parity.
