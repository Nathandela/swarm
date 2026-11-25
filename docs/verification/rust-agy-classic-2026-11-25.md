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

## Native agentapi registration and delivery

The Rust shim opens a registration route for agy 1.4.0, 1.4.5, and 1.4.7 when an enabled local connector was selected at launch. The registration must carry the worker's own tool metadata, exact bootstrap command, workspace, executable identity and loopback agentapi endpoint. Native conversation metadata must confirm the conversation and project. The shim holds the credentials in memory. Registration does not change the agent's tool permission mode.

The pinned Go path requires a message origin for this broker. The Rust daemon likewise requires negotiation and validates the origin against the installed connector and the installation bound to the worker. `swarm message` without an origin continues to refuse. A submitted receipt confirms the provider echoed the exact recipient and text. The operation records the exact origin, session and provider conversation. A wrong native echo leaves an unresolved operation and fences any later sends. The terminal receives no fallback input.

The local fixture exercised the shipped Rust `spawn` launch, native registration role, owner-daemon semantic request, and `swarm message --status`. It refused forged project, tool metadata, endpoint, workspace, origin, installation switch and replay with altered intent. It confirmed exact Unicode and newline delivery once, retained the receipt and live source over a daemon restart, and preserved ambiguity without replay. The fixture implements the measured agentapi shape. Positive delivery with installed agy and the public Discord publication path remain separate from this fixture.

```sh
python3 scripts/verification/rust_agy_native_input.py target/debug/swarm
```

The separate retained Go `TestNativeAgyModalAdmission` passed with the installed 1.4.7 binary and the pinned arm64 helper under the no-egress scratch guard. The installed provider registered via its real tool context, acknowledged the native message, and supplied its marker to the synthetic model. The pending permission-probe command did not execute. This confirms the retained native interface independently of the Rust fixture.

## Scheduled ACP

For agy 1.5.12 and newer, an agent-default scheduled job opens one native ACP v1 conversation through the shim's private PTY relay. The opening prompt is durably claimed for its exact attempt and native conversation before the relay submits it. The provider inherits its ordinary environment and authority. The only Swarm receipt credential forwarded to ACP is the scoped MCP bridge configuration. The relay cancels permission requests during startup and from unrelated conversations. A request for the active conversation waits for the operator's numbered choice.

The paired fixture ran against both Rust and the pinned arm64 Go binary. It verified that the prompt entered the exact native conversation once, an explicit terminal denial cancelled the matching permission request, and the real MCP bridge recorded the scoped receipt. A valid bounded final assistant response completed the occurrence. The same receipt with only earlier or wrong-conversation assistant text left it incomplete. Both binaries refused the 1.5.11 fixture and unsupported observe authority without launching a worker.

The installed 1.4.7 binary was also refused before any worker existed by both Go and Rust. The standalone probe used disposable directories and the scratch-only, no-egress OS guard. The supported ACP path was measured with the paired fixture because the installed binary is below its floor.

```sh
python3 scripts/verification/parity_agy_acp.py target/debug/swarm /path/to/pinned-go-swarm
```
