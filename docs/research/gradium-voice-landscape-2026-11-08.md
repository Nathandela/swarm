# Voice orchestration for terminal Swarm: a technical landscape

*2026-11-08*

## Abstract

This survey examines speech interfaces for a coding-agent supervisor used locally, through desktop SSH, and through Android/iPhone SSH clients. It separates speech synthesis/transcription, conversation and tool orchestration, device media transport, and existing Swarm control. Those layers have different authority, recovery and latency contracts. A microphone accessible on a VM is not the microphone on the SSH client's device.

Current Gradium services provide realtime STT and TTS, including browser credentials, semantic turn signals and streaming controls. These capabilities can support several architectures, but they do not themselves provide an authenticated Swarm manager, durable task orchestration or mobile lifecycle guarantees. New experiments establish desktop browser/SSH feasibility and selected SDK/control contracts; the owner's earlier authenticated synthetic spikes add provider observations. Fresh release qualification, real microphones and physical phones remain open. The companion [architecture proposal](gradium-voice-manager-2026-11-08.md) supplies the requested decisions and roadmap separately from this neutral survey.

## 1. Introduction

The research question is how a human can speak an intent, delegate coding work, follow multiple workers and answer precise questions across all requested terminal contexts. Scope includes Gradium's actual current protocol/source, manager runtimes, worker control, media topology, privacy/cost and UX/reliability. Production implementation, purchased infrastructure, real speech recordings and phone certification were outside the actions performed in this investigation.

“Place” means one Swarm installation/machine. “Manager” means the persistent conversational coordinator. “Worker” means an ordinary Swarm-managed coding CLI. “Speech turn” means a committed human utterance, distinct from an STT segment or pause. “Operation” means an exact worker mutation with its own durable identity. “Barge-in” means interrupting spoken output; it does not inherently cancel a worker task.

Evidence came from official documentation, pinned upstream source, the current repository and isolated tests. Context7 was consulted for Gradium/frameworks and Codex, then checked against current first-party material and installed schemas. Retrieved stale Gradium guidance and an unrelated LiveKit search result were not treated as current contracts. Two Sol research lanes and one Astra lane performed independent investigation and cross-domain challenges.

## 2. Foundations

### Speech is a pipeline with separate clocks

A typical interaction crosses capture → framing → transport → endpointing/transcript commit → manager inference → tool dispatch/receipt → phrase synthesis → playback. Time to first audio is not time to a correct action. Latency must be measured at each boundary; published vendor inference numbers do not include the whole chain. Swarm's model can continue working asynchronously after speech ends.

Mono PCM16 consumes `sample rate × 2` bytes/second: 48 kB/s at 24 kHz, 96 kB/s at 48 kHz. Base64 expands these to approximately 64 and 128 kB/s before JSON/TLS overhead. Sample-rate labels must match bytes. Gradium supports explicit PCM rates and Ogg-wrapped Opus; common browser WebM Opus blobs are not the documented Ogg container. The direct protocol accepts non-Python clients. [STT reference][1]

### Endpointing is not consent

Gradium emits text segments, segment completion, semantic inactivity predictions and correlated flush acknowledgements. A segment's end and a predicted pause do not establish that the user has completed an actionable instruction. PTT provides an explicit boundary; natural conversation needs calibrated endpointing and late-result rejection. A stable logical utterance ID must connect final transcript, human scope and tool execution. [Turn-taking recipe][2]

### Terminal and browser boundaries

SSH transports terminal streams and forwards network sockets; microphone capture still runs on the client device. A browser needs a trustworthy origin and explicit capture permission. Same-device loopback HTTP can qualify as trustworthy; ordinary remote HTTP does not. Forwarding a VM's loopback endpoint to the client's loopback origin preserves the browser's local-origin context. This is an inference from OpenSSH and web origin semantics, demonstrated by this run's desktop probe. [OpenSSH][3], [Secure Contexts][4]

WebSocket provides ordered reliable TCP transport and fits SSH forwarding, but queued audio can become stale under loss. WebRTC adds media-specific congestion/jitter handling and ICE/TURN operation. A TCP tunnel of the signaling endpoint does not forward arbitrary WebRTC UDP media. These approaches differ operationally, not just in SDK syntax. [WebSocket RFC][5], [WebRTC overview][6]

### State and authority

The current Swarm client → daemon → shim → worker architecture already separates control from terminal bytes. Semantic send has exact operations and receipts, journal subscriptions provide snapshots/replay, and native capabilities differ by adapter. The manager must preserve those identities and distinctions. Submission, provider input completion, idle state, process exit and task correctness are different facts. The current pilot interface is caller-scoped and foreground-driven; it is not a persistent model wake service. [Pilot runbook](../operations/pilot.md)

## 3. Taxonomy of Approaches

| Approach | Device media | Conversation/runtime | Main new operational surface |
|---|---|---|---|
| Direct Swarm service with speech proxy | Thin browser → Swarm → Gradium | Existing native CLI runtime plus Swarm tools | Optional Go service and media/state lifecycle |
| Direct browser speech with token broker | Browser → Gradium; scoped control → Swarm | Same independent manager | Credential issuance and two synchronized connections |
| Pipecat pipeline | Browser/native transport → frame pipeline | Python orchestration plus chosen LLM/tools | Python deployment and pipeline integration |
| LiveKit room/agent | Browser/native WebRTC room | Agent runtime plus chosen LLM/tools | Rooms/auth/media server/TURN/agent operation |
| Native companions with shared manager | OS capture/playback client | Manager remains machine-side | Per-platform audio/session/distribution work |

Gradbot is an additional implementation reference for quick experiments rather than a distinct transport class. A voice feature built solely inside the remote TUI lacks a client microphone path; a terminal extension/local helper would change it into one of the client-mediated classes above.

## 4. Analysis

### 4.1 Direct Swarm service with speech proxy

**Theory & mechanism.** A same-origin companion captures audio and exchanges it with a scoped Swarm service. That service owns Gradium sessions, final utterances and manager scheduling, then executes validated operations through the existing local daemon. It preserves one browser connection and server credential custody.

**Literature evidence.** Gradium documents JSON/base64 speech messages, setup/ready/EOS/error lifecycle and optional multiplexing. The normal STT stream is not a resumable command log. TTS graceful end is not cancellation; aborting local playback and rejecting old generations remain application responsibilities. [Lifecycle][7], [multiplexing][8]

**Implementations & benchmarks.** The pinned Python SDK's exact Tts/Stt class bodies passed eight fake-transport checks. A local and actual SSH-forwarded Chromium page processed synthetic audio at 24 kHz from a 48 kHz track. No audio upload, model response, PCM16 encoder or real acoustic test was performed. Swarm's focused existing pilot/native custody tests passed. [Evidence package](gradium-voice-evidence-2026-11-08/README.md)

**Strengths & limitations.** This aligns with Swarm's Go service model and existing local authority, while adding a VM media detour and application-owned media/endpointing work. On poor links, ordered audio can accumulate; bounded queues and drop semantics are essential. It does not solve phone background execution.

### 4.2 Direct browser speech with token broker

**Theory & mechanism.** An authenticated backend exchanges its permanent key for a short-lived token documented as single-use. The browser connects directly to Gradium and separately to the scoped manager/control service. This can shorten the media route while keeping the long-lived key server-side.

**Literature evidence.** Tokens have server-reported expiry and documented single-use semantics; reconnect should obtain a new token. Public documentation does not establish endpoint/voice/spend restrictions or a fine-grained token revocation API. Speech credentials and Swarm operation authorization are separate. [Browser WebSockets][9]

**Implementations & benchmarks.** This run made no fresh authenticated call. At the owner's request, prior same-day VM scripts and retained results were inspected: one token produced `ready` on two TTS sockets, contradicting documented single-use behavior; a new token worked after two seconds and failed after six. Three synthetic TTS runs reported 169–181 ms after first text. Prior Chrome STT/TTS used fake-WAV capture with permission/autoplay bypasses, sequentially rather than duplex. These are limited prior observations, not independent reruns or a proxy-versus-direct comparison. [Evidence manifest](gradium-voice-evidence-2026-11-08/prior-vm-observations.json)

**Strengths & limitations.** Less media transit through the VM can help some geographies. The browser must coordinate speech and control lifetimes, obtain fresh credentials and reconcile stale transcript/playback generations. A token holder can consume speech resources within whatever scope the provider actually enforces. Client-provided finalized text has the authenticated user's input scope, just as typed text does; server transcription is not proof of a human speaker.

### 4.3 Pipecat pipeline

**Theory & mechanism.** Pipecat models media and model work as frames moving through explicit processors. This supplies reusable interruption, context and transport machinery while application tools retain Swarm authority.

**Literature evidence.** Current source contains Gradium STT and TTS. The pinned STT path aggregates for 100 ms after `flushed`, citing possible trailing text. Its last file change predates Gradium's November 3 model replacement, so this is a historical integration workaround, not proof of a current provider-ordering defect. TTS interruption drops a local audio context without sending provider cancellation. [Pinned STT][10], [pinned TTS][11]

**Implementations & benchmarks.** Source snapshots were compared against pinned GitHub revisions. No full Pipecat process or deployed transport was run. The repository's integration summary is insufficient to infer every current source capability.

**Strengths & limitations.** Frame composition can reuse mature conversational behaviors. It adds a Python runtime, adapter lifecycle and integration boundary to a Go product. Framework defaults/rate conversion and finality workarounds must be qualified against the actual provider model rather than assumed correct because the framework supports Gradium.

### 4.4 LiveKit room and agent

**Theory & mechanism.** A browser/native device joins a WebRTC room; a server-side agent handles speech, model and tool work. Media transport, room identity and coding permission become separate layers.

**Literature evidence.** The current Gradium plugin uses 24 kHz STT/48 kHz TTS and synthetic-silence/frame-countdown flushing. That differs from a fresh direct integration using native flush. LiveKit's room/media stack does not establish Swarm operation authority or native worker capability parity. [Pinned plugin][12], [official integration][13]

**Implementations & benchmarks.** Plugin source was pinned and checked; no room, TURN service, agent session or cloud account was provisioned. No packet-loss comparison or hosting cost was measured.

**Strengths & limitations.** WebRTC infrastructure addresses difficult mobile/media-network work and can support richer device/multi-participant behavior. It introduces media infrastructure, room credentials, deployment/billing and another service boundary. SSH forwards alone do not solve its media connectivity.

### 4.5 Native companions and local bridges

**Theory & mechanism.** OS-native clients own audio session, route, focus and background execution, while sharing the same manager/control protocol. A Termux local bridge is a narrower Android-specific variant.

**Literature evidence.** Android microphone foreground services require explicit permissions/type and permitted starts. iOS native recording has audio-session/lifecycle controls; Safari tabs, installed PWAs and embedded WebViews are not equivalent. Historical WebKit reports demonstrate divergent behavior, not a current universal prohibition. Termux's documented microphone command records to a file; it does not prove a duplex PCM stream or iPhone path. [Android][14], [Apple audio][15], [WebKit issue][16], [Termux source][17]

**Implementations & benchmarks.** No native voice client was built and no phone was exercised. Swarm's existing Android build and accepted remote replacement do not satisfy the still-open hosted/physical gates. Blink documents SSH forwarding/background shell features, but that is not evidence of unlimited tunnel survival under iOS suspension. [Blink][18]

**Strengths & limitations.** Native audio control offers platform-specific possibilities, particularly if foreground browser use fails the required workflow. It adds platform/distribution maintenance and still requires real calls, route changes, screen locks and suspension tests. A browser may satisfy foreground use without that work, but only actual devices can establish it.

## 5. Comparative Synthesis

| Dimension | Direct proxy | Browser direct | Pipecat | LiveKit | Native clients |
|---|---|---|---|---|---|
| Long-lived provider key | Machine service | Machine token broker | Pipeline service | Agent service | Prefer machine service/token broker |
| New runtime | Go service | Browser + Go service | Python | Agent runtime/media infrastructure | Each client platform |
| SSH-only desktop fit | Strong | Control tunnel still needed | Transport-dependent | Media not forwarded by ordinary SSH TCP | Local bridge required |
| Media loss/jitter handling | Application + TCP | Provider WS + application | Selected transport | WebRTC stack | Selected transport |
| Background phone evidence | Unproved | Unproved | Transport does not grant OS rights | SDK does not grant OS rights | OS capabilities still need qualification |
| Swarm safety/receipts | Must reuse broker | Same | Same | Same | Same |
| Actual tests in this run | Browser/SSH foundation | Unauthenticated reachability only | Pinned source inspection | Pinned source inspection | Documentation only |

Cost has independent speech, model and infrastructure terms. Gradium's published standard credits are characters synthesized plus three times submitted audio seconds; uploaded silence should be budgeted until billing tests prove otherwise. Connected wall time with no uploaded samples is a different quantity. Paid-plan data controls and commercial rights differ from the free tier. ZDR, residency and model-provider privacy are separate choices. [Pricing][19], [ZDR][20], [residency][21]

## 6. Open Problems & Gaps

Provider gaps include authenticated flush/event ordering after the recent model replacement, resolving the observed token reuse versus documented single-use contract, accepted model-specific settings, silence/rounding/idle billing, cancellation resource release, session rotation, languages/accents/identifiers and human end-to-end latency. The `default` model alias is not a frozen reproducibility boundary. The STT guide and settings currently disagree on the broad delay range; negotiate and test an overlapping documented value rather than deriving a universal range. [STT settings][22], [release notes][23]

Swarm gaps include owner create/resume operation identity and reconciliation, Claude/agy native sink selection currently coupled to message Origin, persistent manager scheduling, actual tool isolation and provider-specific approvals/interrupts/history. A durable event checkpoint does not preserve RAM-only replies after eviction/restart. `pilot` alone does not fix these gaps.

Device gaps include exact Android/Termux and iPhone/client selection, independent HTTPS versus tunnel lifetime, denied permissions, background/lock expectations, autoplay, AEC/echo, Bluetooth, calls and network handoff. Security/UX gaps include same-phone pairing, target ambiguity, accidental “yes”, stale finals, hostile origins, lost acknowledgements and malicious worker output. None is certified by a desktop fake microphone.

## 7. Conclusion

The architecture space separates into media transport, manager runtime and Swarm tool authority. Current provider/browser features make several combinations technically credible. Their tradeoffs are primarily lifecycle, operational scope, data flow and where finality/recovery are enforced. The experiments narrow uncertainty around the desktop SSH/browser foundation and existing control primitives, while live speech and phone evidence remain open.

## References

[1]: https://docs.gradium.ai/api-reference/endpoint/stt-websocket
[2]: https://docs.gradium.ai/guides/recipes/turn-taking
[3]: https://man.openbsd.org/ssh.1
[4]: https://www.w3.org/TR/secure-contexts/#is-origin-trustworthy
[5]: https://www.rfc-editor.org/rfc/rfc6455
[6]: https://www.rfc-editor.org/rfc/rfc8825.html
[7]: https://docs.gradium.ai/guides/websocket-lifecycle
[8]: https://docs.gradium.ai/guides/multiplexing
[9]: https://docs.gradium.ai/guides/browser-websockets
[10]: https://github.com/pipecat-ai/pipecat/blob/3dbb0c8445c0ddc2276d566140fe8d12f1d61e5b/src/pipecat/services/gradium/stt.py
[11]: https://github.com/pipecat-ai/pipecat/blob/3dbb0c8445c0ddc2276d566140fe8d12f1d61e5b/src/pipecat/services/gradium/tts.py
[12]: https://github.com/livekit/agents/blob/1e352e413a2441b2c9cedcfe8f72392b2365b802/livekit-plugins/livekit-plugins-gradium/livekit/plugins/gradium/stt.py
[13]: https://docs.gradium.ai/integrations/agent-frameworks/livekit
[14]: https://developer.android.com/develop/background-work/services/fgs/service-types#microphone
[15]: https://developer.apple.com/documentation/avfaudio/avaudiosession/category-swift.struct/record
[16]: https://bugs.webkit.org/show_bug.cgi?id=239602
[17]: https://github.com/termux/termux-api-package/blob/master/scripts/termux-microphone-record.in
[18]: https://docs.blink.sh/advanced/advanced-ssh
[19]: https://gradium.ai/pricing
[20]: https://docs.gradium.ai/guides/zero-data-retention
[21]: https://docs.gradium.ai/guides/data-residency
[22]: https://docs.gradium.ai/guides/transcription-settings
[23]: https://docs.gradium.ai/guides/release-notes/2026-11

All web sources accessed 2026-11-08. Primary-source titles are linked at the claims above. Source pins: Gradium Python SDK `e354e178990a00f1251fb82375a9b55e3bb319d9`; Pipecat `3dbb0c8445c0ddc2276d566140fe8d12f1d61e5b`; LiveKit agents `1e352e413a2441b2c9cedcfe8f72392b2365b802`. Pins establish inspected code, not deployed service versions.

## Practitioner Resources

- [Gradium documentation index](https://docs.gradium.ai/llms.txt): current endpoint, data-control and release-note map; prefer current protocol references over old marketing snippets.
- [Gradium Python stream source](https://github.com/gradium-ai/gradium-py/blob/e354e178990a00f1251fb82375a9b55e3bb319d9/gradium/stream.py): concrete framing/stream behavior used in the eight contract checks.
- [Gradbot README](https://github.com/gradium-ai/gradbot/blob/db64d7703b478f85476adab8bb8c9a8db617dbf0/README.md): conversational reference explicitly positioned for experiments/prototypes; its production guidance points elsewhere.
- [Codex App Server](https://learn.chatgpt.com/docs/app-server): native structured manager candidate; local schema generation is required to distinguish stable from experimental fields.
- [Claude streaming input](https://code.claude.com/docs/en/agent-sdk/streaming-vs-single-mode): another persistent conversational runtime; independent Swarm integration/account qualification required.
- [Swarm evidence](gradium-voice-evidence-2026-11-08/README.md): exact probe scripts/results, source versions and deliberately unrun tests.

## Common Pitfalls

- Sending browser media labels that do not match actual sample rate/container.
- Treating a text segment, pause or speech token as a final authorized command.
- Confusing speech stop, manager interruption and worker cancellation.
- Replaying uncertain creates/messages after reconnect instead of reading receipts.
- Assuming all five adapters share native input, approval and Stop semantics.
- Treating a cursor as a durable transcript, or submission as task success.
- Exposing provider keys or the owner daemon through a browser endpoint.
- Assuming an encrypted private network makes remote HTTP a secure browser origin.
- Calling a desktop synthetic-mic test phone, acoustic or production qualification.
