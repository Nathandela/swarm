# Rust agy classic launch qualification

Tested on macOS on 25 November 2026 against Go reference `f26648d69086e329132ac7849954d2f29a895387` (source-built binary SHA-256 `dd1c72a067ff7dbd51d31413a6d51be10c3fb381173145e52ab9d1a509ec354e`). The Rust runtime owned the daemon, shim, PTY and board. The Go executable was used only as the comparison arm.

| Observation | Result |
|---|---|
| Fixture reporting agy 1.3.9 | Both arms launched the same model and interactive prompt, read the bordered idle composer and resumed the terminated session using its captured UUID and model. Rust resumed through the dashboard's `r` key. |
| Fixture reporting agy 1.4.7 and advertising `--settings` | Both arms injected the status-line wrapper, preserved the operator's command and rendered the native reviewing state as a permission wait. The Rust shim refused a following callback for a different conversation, preserving the title, the original conversation and the wait. |
| Fixture reporting agy 1.4.7 without advertising `--settings` | Rust kept the classic argv and used terminal evidence. A running source could not be resumed and a structured message did not enter the PTY. |
| Actual installed Antigravity CLI 1.4.7 | Go injected `--settings`, and the provider rejected the flag and exited 2. Its captured terminal tail contained `flags provided but not defined: -settings`. The Rust binary reached the real provider's unsigned-in chooser through `spawn` and `peek`. The original home status-line command rendered without wrapper injection. The native login actions were left untouched. |
| macOS natural exit | The kernel returned EPERM when signaling an unreaped zombie-only process group. An isolated probe returned the sole leader for that group and returned two PIDs for a group retaining a live child. Rust accepts EPERM only after proving that the leader has exited and the group contains that one PID. The fixture compared natural exit and subsequent resume through both supervisors. |

The actual CLI binary had SHA-256 `b7a13e53c9f49921692676ef8edc2e60ceb67064c00bc732d9a364ec3ad75b32`. The actual-provider trial used private homes and workspaces with an OS guard that denied external network and writes outside the test root. The tested Go and Rust clients each used their own daemon and socket.

The status-line gate requires both a compatible version and a bounded help probe advertising the flag. This is an intentional divergence from the pinned Go version-only gate on the installed build above. The wrapper is observational and conveys no permission to submit work or approve a tool. Rust's classic transport refuses semantic submission without a qualified native input registration. The fixture also supplies unrelated provider API keys and checks that agy receives neither. It inspects the inherited environment of the preserved status-line command, and exercises malformed conversation fields through both the public hook and a direct framed callback. Rust preserved the joined title and permission state after those callbacks.

Go's optional native agentapi broker (versions 1.4.0, 1.4.5 and 1.4.7), authenticated native turns, and scheduled ACP (1.5.12 or newer) remain separate qualification items. The fixture confirms no positive broker or ACP behavior.

```sh
cargo build
python3 scripts/verification/parity_agy.py /path/to/pinned-go-swarm target/debug/swarm
```

A repeated run against the independent arm64 Go rebuild exposed callback reordering in the Rust arm. The shim accepted two status-line connections concurrently and processed a later conflicting UUID before the initial UUID. Rust now processes callbacks in accept order on a private agy status socket. The wrapper waits for a validated acknowledgment with bounded I/O. A fixture opens overlapping connections, holds the first frame for 150 ms, and requires acknowledgment of that frame and refusal of the conflicting second frame. Ten consecutive Go/Rust runs of the advertised-settings scenario passed.
