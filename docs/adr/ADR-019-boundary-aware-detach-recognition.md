# ADR-019: Detach recognition becomes boundary-aware — the solo-read test (D4) is superseded

- Status: Accepted
- Date: 2026-08-26
- Supersedes: the D4 RULED solo-read rule recorded in ADR-006 / bead agents-tracker-rs8
- Affects: `internal/attach` (the input pump), A-1

## Context

The attach passthrough recognized the detach key (Ctrl+q, 0x11) only when a raw read
of the controlling terminal yielded that byte ALONE:

```go
if n == 1 && buf[0] == detachKey { signalDetach(); return }
```

D4 chose the solo-read test on the reasoning that the pump has no bracketed-paste
state machine, so scanning every read for the byte risked detaching mid-paste; a
missed detach "under an input flood" was judged rare and recoverable by pressing
again. The decision invited revisiting **on field evidence**.

### The field evidence

Nathan reported that Ctrl+q intermittently fails to return to the board — sometimes
recovering after clicking around, sometimes only after killing swarm and starting a
new session.

The premise behind D4 was wrong: bytes sharing a read with the keypress are not a
rare flood, they are the steady state of an attach. Agent CLIs enable terminal input
modes that the passthrough forwards verbatim to the user's terminal, and never
disables. Probed directly (`claude` under a PTY, and again through a live swarm
attach):

| Mode | Meaning | claude | opencode |
|---|---|---|---|
| `CSI ?1003h` | report every pointer **movement** | yes | yes |
| `CSI ?1006h` | SGR extended mouse encoding | yes | yes |
| `CSI ?1004h` | focus in/out events | yes | — |
| `CSI ?2004h` | bracketed paste | yes | yes |

So for the whole life of an attach to a Claude Code session, the user's terminal is
streaming mouse-motion and focus reports into swarm's stdin. Any one of them landing
in the same `read()` as Ctrl+q silenced the detach — and forwarded the 0x11 to the
agent instead, so the user's escape attempts vanished into the CLI.

Measured on the real binary driven through a PTY (`v0.12.3`):

| Condition | Detach missed |
|---|---|
| idle terminal, solo press | 0 % |
| focus event immediately ahead of the key | 10 / 10 |
| pointer moving, ~40 reports/s, busy session | 6 % |
| pointer moving quickly (1–4 ms between reports) | ~33 % |
| two presses in the same kernel read | 38 / 40 |

Under heavy agent output the window widens: presses separated from a stray byte by
2 ms still missed. The user-visible behaviour is exactly "sometimes it does not
respond" — and "clicking at random places" makes it worse, not better; what helps is
the pause afterwards, during which a press finally lands alone.

## Decision

The detach key is recognized **wherever it lands in a read**, subject to two gates
that preserve everything D4 was protecting:

1. **Not inside a string sequence's payload** (OSC/DCS/APC/PM/SOS) — the one place a
   terminal can legally hand us a C0 byte that is not a keypress. `outParser` —
   already written for the output side's re-assert boundary — supplies the position,
   so no new parser is introduced.
2. **Not inside a bracketed paste.** Between `CSI 200~` and `CSI 201~` every byte is
   pasted data and is forwarded, detach byte included. The markers are matched on a
   6-byte sliding window so one split across two reads still flips the state.

A CSI/ESC/nF sequence is deliberately **not** a gate. Its bytes are drawn from
0x20-0x7e, so a C0 byte arriving while one is open is a keypress the user made DURING
the report, not part of it. Gating on full GROUND was tried first and lost exactly
that press whenever a report straddled a read boundary — 1 miss in 12 under a
continuous motion stream, which is the case the change exists for.

Bytes ahead of the key in the same read are real input and are still forwarded. The
key itself is never forwarded, and bytes trailing it are dropped — the attach is over.

## Consequences

- Ctrl+q returns to the board on the first press regardless of what the terminal is
  reporting. The `?1003h` motion stream stops mattering.
- Pasting text that contains a literal 0x11 still reaches the agent as data.
- ~45 lines in `internal/attach/attach.go`; no new dependency, no protocol change.
- `TestDetachKey_WithinMultiByteReadIsForwardedNotDetach` (the D4 pin) is replaced by
  `internal/attach/detachscan_test.go`, which pins the new rule and both gates.

## Not decided here

Two adjacent defects were found in the same investigation and are filed separately;
neither is a cause of the detach miss:

- **Leaked terminal input modes.** swarm never disables the `?1003h` / `?1004h` /
  `?1000h` / `?1006h` modes the agent turned on — not on detach, not on quit. The
  board goes on receiving motion reports, and a user who quits swarm is left with a
  terminal that emits garbage on mouse movement.
- **A kqueue fd leak of one descriptor per attach/detach cycle**, from bubbletea's
  `ReleaseTerminal` cancelling its cancelreader without closing it while
  `RestoreTerminal` allocates a fresh one. Measured 3 -> 17 over 15 cycles. Driven to
  exhaustion under a low `ulimit -n`, `RestoreTerminal` fails, the error is swallowed
  by `_ = hand.Restore()` in `NewAttachRunner`, and the board is left permanently
  without an input reader — dead until swarm is killed.
