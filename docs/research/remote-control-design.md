# Remote Control: Research and Design Proposal (v2, post-audit)

Status: research synthesis for the V2 remote-control epic, revised against the audit
committee's findings (docs/verification/audit-002-remote-control-design.md). Feeds ADR-007,
which system-spec.md F-2 requires before any implementation. Sources: two codebase surveys,
four web-research reports, a source-level read of Happy Coder, and the four-member audit.
Interactive UX mock: published artifact "swarm remote — interactive mock" (file
swarm-remote-mock.html), whose integration notes tag each element exists/gateway/protocol.

## 1. Goal and scope

A phone app to interact with swarm sessions from anywhere: see every session on every paired
computer, read live output, send input, answer permission prompts, interrupt, and spawn new
sessions. Fixed product decisions: push notifications are core; multiple computers; the UX
bar is Anthropic's Claude-app remote control; "extremely safe" is a requirement, not a vibe.

Scope honesty (audit item 12): "full interaction" is the product goal, not the Phase 1
deliverable. Phase 1 ships the provable core (read + input + push); structured chat and
one-tap approvals are gated behind spikes S-A/S-B/S-C (section 9); spawn-from-phone is gated
behind the launch-authority model (section 5.6). The Anthropic app achieves minutes-later
one-tap approval by owning the agent runtime; swarm drives third-party CLIs through PTYs, so
that UX must be earned by spike results, not assumed.

## 2. What swarm already has (and lacks)

Verified strengths the design leverages:

- Transport-neutral message schemas (F-2, tested by E6.8b) — a relay can carry them unchanged.
- Versioned, capability-negotiated handshake from message one (F-1).
- Namespaced session ids `<endpoint_id>/<local>` — multi-machine needs no schema break.
- Client-agnostic protocol server: a gateway dials the UDS as an ordinary client and gets
  list/launch/kill/delete/subscribe/attach for free.
- Lease-based attach with generation supersede — phone takeover of a session is existing,
  tested behavior (S2).
- Server-derived status Groups (NeedsInput/Working/ReadyForReview/Completed) — the phone's
  entire triage model, computed daemon-side already.
- Escape-filtered VT grid snapshots (N-6) — structurally safe for a remote renderer.

Gaps (all confirmed by source-reading auditors):

- G-1 No authentication beyond UNIX file permissions; every identity/pairing/E2EE layer is new.
- G-2 Live PTY bytes are unsanitized; only snapshots are filtered. The phone consumes
  snapshots and typed events, never raw bytes; the phone-side snapshot renderer must pass a
  sanitizer conformance test (it re-enters the N-6 trust boundary).
- G-3 No observer attach, and none is cheap: the shim serves exactly one connection (v1 shim
  pin), the daemon holds one upstream per session, supersede is close-and-reopen. Observer
  mode is a data-plane fan-out epic with new backpressure/eviction invariants.
- G-4 No event history: subscribe is live-only. Resume requires a durable, daemon-owned
  per-session journal (not gateway memory — it must survive daemon crash/upgrade, D-5) with
  an atomic snapshot-plus-cursor contract.
- G-5 No push pipeline, device concept, idempotency store, or remote audit log.
- G-6 Structured interaction content does not exist at ingest: hook parsing keeps top-level
  string fields only; adapters reduce permission events to a 4-value enum; tool_input
  (command, diff) is discarded today. The adapter boundary is frozen (Epic 9) — extending it
  is ADR-level work.
- G-7 The transcript file is raw, lossy PTY bytes (repaint-collapse, drop-under-pressure);
  ADR-002 explicitly rejected transcript replay for alt-screen TUIs. Chat-shaped text does
  not exist anywhere in the system today.

## 3. Prior art (condensed)

| System | Model | Trust | Lesson |
|---|---|---|---|
| Anthropic Remote Control | Local agent; outbound-only HTTPS to their relay; phone is a window | TLS; relay sees plaintext; trusted devices/passkeys | UX benchmark: QR pairing, hostname-keyed list, two coarse push toggles, per-tool approvals, steer from any surface — but it owns the runtime (can suspend turns), swarm does not |
| Happy Coder (MIT, source-read) | CLI + Socket.IO relay, self-hostable | Zero-knowledge for content: tweetnacl Ed25519 auth, XSalsa20/AES-GCM payloads, relay stores ciphertext | Working E2EE-relay precedent. Two flaws we must not copy: QR carries the raw account seed (one shared key, no per-device revocation); push title/body transit Expo/APNs in plaintext. One pattern to note: remote mode drives the Agent SDK's structured stream instead of scraping the PTY |
| Omnara | Relay + multi-surface dashboard | TLS to their cloud | CLI-wrapper archived Feb 2026 — screen-scraping agents is unmaintainable. Caution transfers to our grid-to-chat derivation, not just wrappers |
| Coder AgentAPI (MIT, Go) | Terminal emulator parses TUI screens into typed messages (HTTP/SSE) | Local | Validates typed events as the wire product; also proof that screen-parsing is per-CLI work |
| VibeTunnel / ttyd / tmate | Raw terminal forwarding | Varies | The road not taken for the default UX |
| Codex in ChatGPT | Supervision surface in ChatGPT; QR-paired Mac | OpenAI cloud | "Not a raw terminal — the artifacts that matter"; queue tasks from phone |

Convergent patterns: outbound-only bridge; typed events over raw bytes; sessions outlive
clients; QR pairing; the trust fork is trusted-relay vs zero-knowledge-relay.

## 4. Connectivity options

The daemon side is identical in all options: a remote component dials outbound. Only the
transport and trust model differ.

### Option A — Self-hosted E2EE relay (recommended, conditionally)

Small relay on a VPS. Daemon and phone dial out TLS/443 (WebSocket, multiplexed); payloads
E2E-encrypted so the relay stores/forwards ciphertext; QR pairing; push forwarded as
ciphertext. Honest costing (audit item 7): the relay is not stateless — it holds mailboxes,
cursors, a device registry, APNs tokens, rate limits, and abuse controls; it is a small
stateful service you operate. And E2EE hides payloads, not metadata (presence, timing,
sizes, routing pairs, push cadence) — section 5.8.

- Pros: works from anywhere (outbound 443); native push; untrusted relay for content by
  construction; Happy is a working precedent; single coherent architecture.
- Cons: you operate an internet-facing service; you own two crypto protocols (section 5.2);
  relay down = no remote (mitigation is Option D's direct path, not "a second relay").
- Feel: open the app anywhere and sessions are there in about a second; push arrives with
  the app killed; no VPN icon; one QR per machine, once.

### Option D — Tailscale transport + blind push broker (the honest challenger)

Phone talks directly to each daemon over the tailnet (tsnet embedded in the daemon; WireGuard
E2EE; identity, ACLs, and revocation delegated to Tailscale). A minimal stateless broker
holds only APNs credentials and forwards opaque wake-up pushes triggered by the daemon.

- Pros: dramatically less custom security code (no relay protocol, no mailbox store, no
  Noise handshake — WireGuard is the transport security); revocation and device identity are
  a mature product's problem; the broker is tiny and holds no content.
- Cons: requires the Tailscale app and an account (or self-hosted Headscale); iOS VPN
  reconnect lag on app open; history/resume still needs the daemon-side journal and a sync
  protocol on top of a raw net.Conn; presence ("machine asleep?") is inferred, not brokered;
  push content beyond "wake up" still needs an async envelope scheme if it carries anything.
- Feel: at home or with the VPN warm, instant and direct; opening the app cold away from home
  adds a visible beat of VPN negotiation; push still lands (broker), but deep-linking into a
  session waits on the tunnel.

### Option B — Pure Tailscale (rejected for the core requirement)

No push path at all: nothing can wake the phone. Fails "push is core" on its own.

### Option C — WebRTC data channels (deferred indefinitely)

Signaling + TURN infra is a superset of Option A's, for latency the terminal-text workload
cannot perceive; data channels die in iOS background.

### Recommendation

Option A remains the primary recommendation because it is one coherent product surface
(presence, mailbox, push, pairing in one place) and the only shape that matches the
"Claude-app seamless" bar cold-start-from-cellular. But per the audit, the recommendation is
conditional: ADR-007 must include an explicit A-vs-D comparison with the relay's true
operational cost on the table, and Phase 1's transport module stays behind an interface so D
can be adopted or added as the direct path without rework. If operating an internet-facing
relay proves unwanted in practice, D is the fallback with the better security-effort ratio.

## 5. Security design

Threat model: the phone commands processes that edit code and run tools on personal
computers; the relay is on the public internet; a stolen phone or a compromised relay must
not become code execution or data exfiltration. Each rule below becomes an ADR clause and a
test target.

### 5.1 Trust boundaries

The relay is untrusted for content and integrity (can drop, never read or forge); Apple/APNs
is untrusted entirely; the phone is semi-trusted (it holds keys but must be revocable and
biometric-gated); the gateway is the only component parsing attacker-influenced bytes and is
privilege-separated from the daemon (5.7).

### 5.2 Two crypto protocols, not one (audit item 8)

- Live transport: mutually-authenticated interactive handshake with forward secrecy and
  replay protection between phone and gateway (Noise XX via flynn/noise, or WireGuard when
  riding Option D). Protects the streaming session.
- Async envelope: sealed-box-family public-key encryption to each device's long-term key for
  everything that must be readable while the peer is offline — mailbox'd events and APNs
  payloads (decrypted by the iOS Notification Service Extension). Requires defined key
  epochs, history-access-across-rotation, and revocation semantics.
The ADR specifies both, including how a device's long-term key is provisioned at pairing and
stored (Secure Enclave).

### 5.3 Pairing (audit item 11; protocol pinned)

User flow: (1) `swarm remote pair` on the machine shows a single-use QR, 60 s TTL; (2) phone
scans it and displays four verification emoji; (3) the machine TUI shows the same emoji and
asks `Allow "<device>"? [y/N]`; (4) done — no further ceremony ever.

Protocol: at `swarm remote init` the machine generates a long-term identity keypair
(Keychain/0600); the phone generates its device keypair in the Secure Enclave. The QR
carries a random 32-byte single-use pairing secret that never touches the relay — the
camera is the out-of-band physical-presence channel. Phone and machine meet through an
opaque ephemeral rendezvous mailbox on the relay and run Noise XX with the pairing secret
mixed in as PSK: the relay carries the handshake but cannot MITM it. The emoji SAS is
derived from the handshake transcript (match = no MITM exists); the mandatory local desktop
confirm is the independent second gate that defeats a photographed/leaked QR (an attacker's
attempt surfaces as an unexpected prompt and fails closed; the secret burns on first use or
TTL). Outcome: mutual static-key pinning — authenticated first contact, stronger than TOFU.
Live sessions thereafter run Noise XX over pinned keys (mutual auth, forward secrecy,
replay protection); offline mailbox events and push payloads are sealed-box encrypted to
the device long-term key (5.2). Pairing mode listens only while the command runs,
auto-exits on TTL or first completion, and is rate-limited daemon- and relay-side.

Deliberate contrast with Happy (source-confirmed): their QR transfers the raw account seed
(single shared key, unrevocable if photographed) and push title/body transit Apple in
plaintext; ours transfers a disposable secret, keeps per-device revocable keys, and pushes
ciphertext only.

### 5.4 Device lifecycle (audit item 11)

Per-device long-term keypairs and names; a paired-devices list in TUI and app; revocation
available locally (TUI) and remotely (from another paired device, relay-mediated) — because
device loss without remote revocation equals remote code execution via 5.6. Revocation
rotates the machine's epoch key such that revoked devices can decrypt nothing new; mutating
operations require an on-device biometric/passcode gate.

### 5.5 Approval integrity (audit item 4)

An approval references an immutable (machine, session, agent-instance, request-id,
content-hash) tuple, expires, and is rejected by the daemon if stale or mismatched. Approvals
are never translated into blind keystrokes. Notification-launched sheets re-sync
authoritative state before the Approve button enables. "Approve-and-remember" is excluded
until a separately designed policy model (tool, argument pattern, directory, session,
duration, device) exists. The delivery mechanism itself is spike-gated (S-C, section 9).

### 5.6 Remote launch authority (audit item 6)

Remote launch is remote code execution and is treated as the highest-privilege verb:
allowed-cwd roots configured on the machine; dangerously-skip-permissions and
full-access-sandbox launch options refused from remote, hard-coded; no phone-supplied
environment (launch env comes from the daemon's own policy — also the correct fix for the
ADR-006 billing-env class: the desktop TUI forwards its shell env today, a phone has none to
forward); worktree isolation as the remote default; per-device capability policy (a device
may be read+approve only); an explicit confirm step on the phone.

### 5.7 Gateway isolation (audit item 14)

The gateway runs as a supervised sidecar process, not inside the daemon: it is the single
component exposed to remote input, and it must not share an address space with the process
that owns PTYs and spawns agents. It speaks the existing protocol to the daemon over the UDS
like any client. In-process is the exception that ADR-007 would have to justify.

### 5.8 Metadata honesty (audit item 10)

The relay sees: which machines and devices exist, connection/presence timing, message sizes
and cadence, push timing. Apple sees push routing and timing. The ADR carries this exposure
statement, retention limits and log scrubbing for the relay, optional padding/batching
mitigations, and drops any "leaks nothing" claim for managed hosting.

### 5.9 Idempotency and replay (audit item 5)

Mutating ops carry durable request ids; the daemon persists executed ids with cached
outcomes across restart; raw input frames are never auto-retried; approvals additionally
bind to the live request identity (5.5). Reconnect delivery is at-least-once with
daemon-side dedupe, exactly-once in effect.

### 5.10 Audit and kill switch (audit items 13, plus sleep blind spot)

Every remote-originated mutation is appended to a signed local activity log (device id,
action, session, request id, outcome) — called what it is; tamper-evidence beyond local
signing requires off-machine anchoring and is optional. `swarm remote off` (CLI + TUI)
severs the gateway and refuses remote leases without needing phone or relay; auto-off when
no device is paired. The relay emits a "machine went silent" push when a daemon disappears
mid-run (laptop sleep is a first-class state on the phone, not an error).

## 6. App platform

Recommendation: native SwiftUI, iOS-first — with the tradeoff stated plainly (audit): the
genuinely hard pieces (NSE ciphertext decryption, later Live Activities) require native code
under Expo too, so Expo's advantage is Android reach, not less native work; its cost is the
last 10% of feel. For a solo maintainer already carrying daemon+relay+crypto, one premium
client beats two adequate ones; the protocol work is client-agnostic, so an Android thin
client remains possible later. Hard dependency, not open question: an Apple developer
account ($99/yr) for APNs + NSE from Phase 1. Live Activities are deferred until the core
loop is proven on-device. PWA rejected (push/background too weak).

## 7. Architecture

```
 phone (SwiftUI)                relay (VPS, untrusted for content)         each computer
 +-----------------+           +---------------------------+      +----------------------------+
 | inbox / detail  |  wss      | presence, ciphertext      | wss  | swarm-remote (sidecar)     |
 | approvals, peek |<--------->| mailboxes, APNs forwarder |<---->|  pairing, device registry, |
 | NSE decrypt     |  E2EE     | rate limits               |      |  noise transport, envelope |
 +-----------------+           +---------------------------+      |  crypto, push triggers,    |
                                                                  |  capability policy         |
                                                                  +-----------|----------------+
                                                                              | UDS (existing protocol)
                                                                  +-----------v----------------+
                                                                  | swarm daemon               |
                                                                  |  + durable event journal   |
                                                                  |  + idempotency store       |
                                                                  |  (+ observer fan-out, ph.3)|
                                                                  |  sessions / shims / PTYs   |
                                                                  +----------------------------+
```

Placement corrections from audit: the durable per-session event journal and the idempotency
store live in the daemon (on disk, meta.json-adjacent, surviving crash/upgrade per D-5); the
sidecar gateway holds no state a restart can lose except live connections. Resume contract:
"snapshot as of cursor N, then events after N", atomic, with retention/compaction defined in
the ADR.

### 7.1 Wire product: typed events, honestly sourced

- session_state — existing SessionView (status dims + server-derived Group). Real today.
- grid_snapshot — existing escape-filtered VT snapshot. Real today; phone renderer must pass
  the sanitizer conformance test (G-2).
- journal events — Group transitions, lifecycle, presence: derivable today from subscribe,
  made durable by the daemon journal (G-4). Powers inbox, activity feed, and push triggers.
- transcript_delta — NOT promised. Spike S-A must prove clean incremental text is derivable
  from real Claude Code and Codex output (VT-grid diff heuristics, per-CLI). Until then the
  chat view renders journal events + grid snapshots; the terminal peek is the ground truth.
- interaction_request — NOT promised. Requires new nested hook-payload capture, adapter
  boundary extension (own ADR), and spike S-B on real permission-prompt payloads; S-C must
  resolve the delivery mechanism (opus C3): whether a minutes-later approval can be applied
  to a synchronous in-PTY prompt at all — candidate mechanisms: adapter-mediated hook
  gating (if the CLI supports a blocking permission hook with adequate timeout), MCP
  permission-prompt tooling, or honest fallback = notification deep-links into terminal peek
  with take-control.
- Phone-to-daemon ops: input (lease-holding), interrupt, kill, launch (5.6), approve (5.5)
  — all idempotency-keyed (5.9).

### 7.2 Multi-machine model

Each machine's sidecar registers under its endpoint id; the phone pairs per machine; the app
merges everything into one inbox (namespaced ids make this trivial); machines carry
hostname, presence (online/asleep/offline), and agent counts.

### 7.3 Observer attach (audit item 1)

Re-priced as its own epic: multi-subscriber fan-out at shim or daemon, per-observer
backpressure and eviction (S9 analog), snapshot boundary per observer, interaction with
controller supersede, new invariants + tests. Scheduled Phase 3; until then, "peek" takes
the controller lease (existing supersede) — which is also what continuous transcript
derivation would need anyway, another reason S-A gates the chat view.

## 8. UX design (mock: "swarm remote — interactive mock")

Screens unchanged from v1 (they survived audit; the mechanism behind two of them is what got
re-gated): pairing/onboarding (QR + desktop confirm), triage inbox (the four Groups as
sections, machine switcher, one-line need summaries), session detail (chat when S-A lands;
journal + snapshot cards until then; composer with voice, queue-while-busy, persistent Stop),
approval sheet (request-bound, expiring — contingent on S-B/S-C), terminal peek (snapshot +
live tail; take-control = lease supersede), machines (presence incl. asleep, paired devices,
revoke, kill switch, activity log), activity feed, settings (two coarse push toggles, quiet
hours, biometric gate).

## 9. Phasing (rebuilt per audit item 12 and opus M4)

- Phase 0 — ADR-007 (identity, pairing, two crypto protocols, relay trust, journal/cursor
  contract, idempotency, launch authority, A-vs-D decision) + Apple developer account.
- Spikes (parallel with Phase 0, before UX commitments):
  - S-A transcript derivation: can clean incremental chat text be derived from real Claude
    Code + Codex PTY/grid output, robustly across versions?
  - S-B interaction capture: can hook payloads (tool_input command/diff) be captured and
    normalized per adapter without breaking the frozen boundary's contract?
  - S-C approval mechanism: can a minutes-later remote decision be applied safely to the
    CLI's synchronous prompt (blocking hook? MCP prompt tool?), or is deep-link-to-peek the
    honest v1 approval?
- Phase 1 — the provable core: sidecar gateway, relay, pairing + device registry, two-scheme
  crypto, daemon journal + idempotency store, inbox (session_state), remote grid attach with
  input (existing lease), interrupt/kill with confirm, push on Group transitions (ciphertext,
  NSE). One phone, one machine, then multi-machine. This is a complete, safe, useful product
  even if every spike fails.
- Phase 2 — what the spikes earned: chat transcript view (S-A), structured approval sheets
  (S-B+S-C), launch-from-phone under the 5.6 authority model, remote revocation UX.
- Phase 3 — observer attach epic (7.3), activity-feed depth, voice, quick replies, quiet
  hours, optional tsnet direct path (Option D as accelerator), Live Activities.

All phases: TDD with evidenced failing-first runs, invariant-style tests for 5.x rules,
cross-model review (security-critical), evidence files under docs/verification/. Adversarial
test list from the audit (replay/reorder/dup, stale approvals, revocation, QR theft,
cross-machine substitution, daemon/shim crash during remote ops, cursor compaction, hostile
PTY content at the phone renderer, APNs duplicates/expiry, concurrent desktop/phone control)
is the acceptance floor for Phase 1.

## 10. Open questions for ADR-007

1. A vs D final call (4): relay operational appetite vs Tailscale dependency.
2. Sealed-box scheme details: key epochs, history across rotation, multi-device fan-out.
3. Journal format and retention (reuse transcript-style rotation? compaction policy).
4. S-C outcome shapes the entire approval UX — earliest possible spike.
5. Relay implementation: minimal purpose-built Go binary vs adapting happy-server (MIT).
6. Distribution: personal TestFlight vs App Store (affects APNs custody and the blind push
   gateway seam from the audit).
