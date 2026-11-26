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

Rust unit coverage includes ambiguous and unknown masks, a split live change into pixel encoding, string payload handling, resize cancellation, native release routing, and C1 SGR clicks. The initial tranche preserved the query and ownership-release ordering. The follow-up below qualifies the emulator reply.

## Mouse mode report follow-up

Nathan approved exactly one reply on 26 November 2026, with unsolicited and bracketed-paste input preserved. The current witness expects the exact 11-byte DECRPM reply `1b 5b 3f 31 30 30 30 3b 31 24 79` from both the detached and attached workers' virtual terminals. It also compares every byte received by the fixture after entering a query and forwarding ordinary native mouse input. On both native hosts the strict Go/Go, Go/Rust and Rust/Rust arms passed.

XTerm 372 on Linux x86_64, running on a private Xvfb display with TCP disabled, produced the same 11-byte report for restored normal mouse mode. Its bytes were saved verbatim and replayed through the active board PTY in fragments. Rust/Rust passed on Darwin arm64 and Linux x86_64 with exactly one worker report despite the physical terminal response. The Darwin Go/Go negative control forwarded the physical report too and the worker logged two copies, which the witness rejected. The independent capture and replay compose the actual XTerm report with the public Rust attach path.

```sh
# Run the probe inside a dedicated XTerm 372 to a new absolute file.
scripts/verification/parity/probe_terminal_reply.py "$report"
scripts/verification/parity/test_board.py --touch-back --terminal-mouse-reply "$report" \
  --supervisor "$rust" --board "$rust"
```

| Artifact | SHA-256 |
| --- | --- |
| Darwin arm64 Rust | `b84c2b67564bd612e5740d337f240a5bf222bc63b04e7146a177a69f3f8ba811` |
| Linux x86_64 Rust | `a85a20b49cf16650f04d17c68368076ae4f2af362a4745a3f0187e16bdf64444` |
| XTerm 372 DECRPM bytes | `7e740e618b402592ca5958698f6a1515ab7dcabad327392f049ef4e71bb18f73` |

The shim queue is bounded. The input filter consumes at most one response per accepted mouse DECRQM within a two-second window and retains unrelated, expired, and pasted reports. This slice qualifies the relevant mouse reports. Full emulator query parity, full Options cell parity, Manager Prompt, and Codex Context Guard settings are separate acceptance items.


## Approved input preservation replay

On 26 November 2026 the public PTY fixture replayed the captured XTerm 372 bytes at `dc269ed8` on the Darwin arm64 Mac and Linux x86_64 VM. While a real mouse-mode query was outstanding, it first pasted a complete copy of the report, then delivered the matching physical reply in two fragments, followed by another uncued copy. The worker received the virtual reply exactly once, the complete bracketed paste including delimiters, the unsolicited copy and the following ordinary input in order. Both runs restored the terminal and reported zero surviving children. The Darwin executable SHA-256 was `138c15eee8a2a4f966895ac20b23f56099992befda41e705739dd3c40f767f7a`. The Linux executable SHA-256 was `4e9df2147ca39dcb16a15add46772371768550c84f18bb569d8027b28e16523f`.
