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

The classic fixture covers PTY behavior. The native broker and scheduled ACP results below use their own versioned probes.

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

The scheduled relay displays the pinned Go permission menu and accepts the normal number followed by Enter. Behind that display, the shim offers an opaque request and fresh display identity only when the complete current menu is present. The board flushes previously buffered tty input and any local draft after rendering, acknowledges the display on the current attach lease, and correlates its numbered response. Raw input, pasted numbers, an empty late Enter, and an old lease, display, or request cannot approve.

The same `swarm peek` and `swarm send --text 1` commands work from a foreground terminal job and from successive pipe subprocesses. The CLI acknowledges the complete menu after flushing output. The one-use approval is tied to the current session/request and live parent process, including the parent's birth and process session. A tty additionally binds its foreground device. A send must originate from a job started after acknowledgment. Missing or partial peek, a different parent, a prestarted process, a separate raw Enter, and a second request all refused the old authority. Explicit 0 can safely cancel a pending request.

The paired fixture exercised the unchanged menu and numbered selection through the Rust board PTY, Rust foreground shell, and Rust and pinned Go pipe processes. It also tested exact stale lease, display and request frames. The official macOS arm64 agy 1.5.17 binary received option 1 through the real Rust board, recorded its scoped MCP receipt and reached ready under a synthetic loopback model and scratch-only/no-egress guard.

Native delivery requires a durable worker cutover in the active, owned personal connector's private `rust-text-v1.json`. The connector stores the exact normalized composer under `worker_inputs[operation]`, with the provider, source message, and session incarnation. The source must match the selected installation, owner and DM channel, remain at the original content revision above the consent floor, and remain marked sending for that operation. An excluded session cannot receive a new effect. Manager cutovers cannot authorize an ordinary worker. The shim rereads the ledger under the same lock the connector uses to commit edits, then holds that lock across the provider call.

The private connector fixture refused missing cutover, manager cutover, forged audience, mismatched text and incarnation, out-of-bound source, exclusion, expiry and changed installation. It withdrew one message during the provider metadata probe and verified the shim recorded a rejection with zero provider message effects. The exact worker cutover delivered once and the receipt survived daemon restart. The fixture writes the private ledger directly. The separate ordinary-worker connector producer owns live ingress.

The worker effect takes the same supervisor and connector directory locks as configuration changes, followed by the ledger lock. The guard spans the provider call and its receipt. In the fixture, a real `discord disconnect` returned busy during a held native write and succeeded after release. A previously negotiated client then tried a late cutover and the daemon refused it after that acknowledgment. The first effect in an empty DM enrollment succeeded with its empty consent baseline. Worker delivery negotiates `worker-discord-origin-v1` independently from manager origin support.

### Installed ACP provider

On November 26, the official agy 1.5.17 binaries completed a real scheduled ACP turn against Rust revision `33392148be92524d4ce26217a08a7429f400d5eb` on macOS arm64 and Linux x86_64. Each native provider contributed the original opening marker to a loopback model request. The operator saw the provider request permission for `mcp_swarm_schedule_complete` and selected Allow. The scoped MCP bridge returned a receipt. Swarm verified the matching closing summary and marked the original run ready. Each host recorded three synthetic model calls.

The native binaries came from the platform manifests named by Google's installer. The fixture verified each vendor SHA-512 before extracting it to scratch. It used privately generated Gemini settings and isolated network access. [Official distribution](https://antigravity.google/docs/getting-started?tab=cli) · [macOS manifest](https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/darwin_arm64.json) · [Linux manifest](https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_amd64.json).

| Native host | agy executable SHA-256 | Rust executable SHA-256 |
|---|---|---|
| macOS arm64 | `e6cabc85878e5beaaac911993751abe314f6c1d89ebfd30a4fc578b7703638eb` | `0c11e6bf517675ba0c17f8fa8ab2710322eab7e4a853ba935e57d8fdbf347361` |
| Linux x86_64 | `f3f384bd2c15a74a036ebc3b0a2f48695cc6e56523ba0f9524fb6e4c4adb3baa` | `7acaeb0bb737b8908893a591355eec3683fb964ff6f0cdf3670cae0f9a2b5cc0` |

```sh
python3 scripts/verification/rust_agy_schedule.py /path/to/swarm /path/to/agy-1.5.17
sh scripts/verification/provider_native_linux.sh "$STAGED_FIXTURE" /path/to/agy-1.5.17 rust_agy_schedule.py
```

On Linux, stage `swarm`, `rust_agy_schedule.py` and `native_fixture_sandbox.py` together. The shared launcher exposes the vendor executable read-only and enables only the new namespace's loopback interface.
