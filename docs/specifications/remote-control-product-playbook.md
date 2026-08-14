# Swarm Remote Control: implementation playbook

- **Status:** Owner-approved product direction; implementation plan, not yet an implementation claim
- **Decision date:** 2026-08-14
- **Tracker:** `swarm-e9w`
- **Audience:** implementers, reviewers, release operators, and physical-handset testers
- **Review:** revised after independent Terra implementation review and Sol architecture/security
  review
- **Supersedes:** no existing ADR by itself; the ADR amendments in section 3 must land before the
  corresponding behavior

## 1. Outcome

Swarm Remote Control makes the terminal and an Android phone two concurrent surfaces on the same
locally running coding-agent session. A developer can start a session through Swarm, continue from
the terminal or phone without transferring ownership, observe the same conversation and tool
activity on both, answer approvals from either surface, survive ordinary disconnects, and reconnect
from a different network.

The product is local-compute and self-host-first:

- Every agent, repository, PTY, tool, credential, and model subscription stays on the developer's
  computer.
- Every developer or organization operates its own publicly reachable relay. The relay is an
  untrusted ciphertext mailbox and rendezvous service, not an agent host.
- The Play Store application uses a minimal Swarm-operated push gateway solely to deliver
  authenticated, content-free FCM wakes. Session traffic never passes through that gateway.
- Identity is accountless. A phone and machine trust each other through QR pairing, SAS confirmation,
  and device keys; there is no Swarm user account in the first release.
- Claude Code and Codex receive complete structured-chat experiences first. A provider without a
  complete structured interaction adapter receives an honest status and terminal-fallback surface,
  not fabricated chat.
- One phone can pair with several computers in the first complete product. Several phones paired to
  one computer remain a later multi-device program.

The reference behavior is Claude Code Remote Control, but the implementation remains
provider-neutral and preserves Swarm's stronger shim lifetime and end-to-end mailbox encryption.

## 2. Locked product decisions

### 2.1 Verified repository baseline

This playbook starts from the 2026-08-14 repository, not from a greenfield design:

- The shim-owned PTY, daemon, `swarm-remote`, ciphertext relay, QR/SAS pairing, single-phone device
  registry, signed remote operations, remote launch policy, terminal watch/input path, and Android
  screen models already exist.
- The interaction schema, optional `InteractionSource`, Claude hook capture, transcript folding, and
  phone approval path exist. A phone composer and a complete Codex structured backend do not.
- Android is still composed around one `mobile.App`. Its build intentionally has no Google Services
  plugin or production Firebase registration, and current background behavior closes the socket and
  depends on a push path that has only fake-endpoint evidence.
- The relay-side FCM design assumes the relay operator holds the application's sender credential.
  That is incompatible with a public Play-signed app and is replaced by this program.
- Codex exposes an app-server/remote surface in the installed CLI, but two-client ownership and exact
  thread semantics have not been proven. Claude is the only current structured source. OpenCode and
  AGY do not satisfy the complete-chat contract.
- The current generic gates are not trustworthy enough to be a release baseline: the repository has
  reproducible PTY close/resize race evidence, gateway-shutdown stress failure, a miswired callback
  overflow conformance case, missing generic-lane mobile bind tools, and stale lint/dependency gates.
- The existing physical-handset record is not evidence for the target product: its required rows are
  unrun and describe the legacy push topology.

Historical verification files remain evidence about the commit they name; they do not prove the
future behavior in this playbook. Every new claim needs new evidence against a released artifact.

### 2.2 Product decisions

| ID | Decision | Consequence |
|---|---|---|
| RC-D1 | Only sessions launched through Swarm are remotely controllable in v1. | Swarm owns the PTY and lifecycle from process creation. Attaching arbitrary pre-existing CLI processes is out of scope. |
| RC-D2 | The relay is self-hosted per developer or organization. | The normal setup target is an always-on public host with WSS on port 443, usually a small VPS. Running only on a sleeping laptop is a development mode, not the promised cross-network product. |
| RC-D3 | There are no Swarm accounts in v1. | Pairings are device-local. Reinstalling the phone app or losing the phone requires revocation and re-pairing. No cloud backup or cross-phone restoration is implied. |
| RC-D4 | Claude Code and Codex are the first full structured-chat providers. | OpenCode and AGY remain launchable, but use the fallback surface until their provider adapter satisfies the complete-chat capability contract. |
| RC-D5 | Terminal fallback exists only for providers without complete structured chat. | A structured provider never exposes a terminal or visible lease in its normal phone experience. The fallback is sanitized, read-only by default, and raw control is an explicit temporary action. |
| RC-D6 | Swarm operates a minimal push gateway for the Play Store app. | The gateway maps opaque push addresses to FCM tokens and forwards authenticated fixed-shape content-free wakes. It receives no session traffic, relay credential, category, repository datum, or agent credential. |
| RC-D7 | End-to-end encryption remains mandatory for session traffic. | A relay compromise may disclose metadata, delay, reorder, duplicate, or drop traffic, but must not reveal or forge session content. |
| RC-D8 | Multi-machine is in the first complete product; multi-phone is later. | Android state becomes a collection of independent machine pairings. The machine's current single-device enrollment limit remains until ADR-011 is implemented as one coherent program. |
| RC-D9 | A fully authorized phone may launch a new Swarm session. | Remote launch is an RCE-class semantic operation over machine-defined workspace and provider presets. It never accepts arbitrary argv, environment, or an unchecked path from the phone. |
| RC-D10 | Normal Web PKI hostname validation is the default relay TLS policy. | E2EE and SAS remain the content trust roots. SPKI pinning becomes an optional expert policy with an explicit rotation mechanism, not the default pairing output. |

## 3. Required decision-record amendments

Implementation must not create a second, contradictory source of truth. Land the following ADR
changes before or in the first code commit that depends on them. Allocate the next available ADR
numbers at implementation time; do not reuse the planned paging ADR number.

R1 must also update every live downstream contract in this table. A historical verification record
is left intact and labelled historical; a normative or operational document is amended in place.

| Decision change | Sources that must move together |
|---|---|
| Play Store Android is the first client; iOS/APNs language is no longer the active target | `ADR-007-remote-access.md`, `system-spec.md`, `implementation-goals.md`, `remote-phaseB-requirements.md`, `operator-runbook.md`, `physical-handset-gate.md`, release/CI docs |
| Push moves from relay-owned sender credentials to the Swarm gateway | `ADR-007-remote-access.md`, relay and VPS runbooks, metadata disclosure, example relay config, Android build/runtime requirements, physical-handset gate |
| Incomplete providers receive terminal fallback | `ADR-009-structured-chat-interaction.md`, `ADR-013-mirror-capture-architecture.md`, `mirror-program.md`, Android verb/screen coverage tables and their enforcing tests |
| Web PKI becomes the default TLS policy | `ADR-007-remote-access.md`, pairing payload/state migrations, phone dial policy, relay/VPS runbooks, pin conformance tests |
| Multi-machine remains distinct from multi-phone | `ADR-011-multi-device-epochs.md`, mobile state and screen-coverage contracts, Android navigation requirements, physical-handset gate |
| Phone launch is a supported RCE-class action | `ADR-007-remote-access.md`, protocol and launch policy, mobile/Android coverage tables, activity-log and physical authorization tests |

The exact repository file list is captured in the R1 change after an `rg` sweep; the table is a
minimum, not permission to leave another current source contradictory.

### 3.1 Capability-driven terminal fallback

ADR-009 currently says there is no terminal grid anywhere in the app and that the phone never authors
raw keystrokes. Amend it narrowly:

- `structured_chat` sessions keep ADR-009 exactly: transcript, tool cards, approvals, composer, no
  terminal surface, and no visible take-control ceremony.
- `terminal_fallback` sessions may render the machine-sanitized terminal snapshot already produced at
  the trusted boundary. Raw PTY bytes, ANSI control sequences, OSC payloads, and an on-phone VT parser
  remain forbidden.
- The fallback opens read-only. The user must explicitly enter temporary control; backgrounding,
  leaving the screen, transport loss, expiry, kill switch, or revocation ends it.
- No content may be promoted from the terminal fallback into structured chat. The adapter must earn
  structured mode by satisfying the capability contract in section 7.

Amend ADR-013 and the Mirror program to replace "status card only" with "honest status plus terminal
fallback" for incomplete providers, while retaining the rule that terminal scraping never produces
structured interaction items.

### 3.2 Split self-hosted relay from Play Store push

ADR-007 and the current operator runbook put FCM service-account credentials on `swarm-relay`. That
cannot be the public setup for a Play-signed application: the application's Firebase sender
credentials cannot be distributed to every relay operator.

Record a separate push-gateway decision:

- Android registers its FCM token directly with the Swarm push gateway.
- Android generates an installation signing key in Keystore. Registration submits its public key and
  returns only an opaque installation id; signed proof of the private key controls FCM-token rotation
  and machine-address allocation/revocation.
- A separate per-machine allocation returns a random opaque push address, a submit capability exposed
  only in that response but reusable until address revocation, and a machine-revoke capability. A
  failed or abandoned pairing deletes the allocation; the gateway expires an unbound allocation
  after ten minutes in every case. Suspected capability exposure revokes the complete old address,
  allocates fresh capabilities, and binds them through an authenticated pairing-update; there is no
  in-place verifier swap that could leave both generations usable.
- The phone conveys the address, submit capability, machine-revoke capability, and phone-generated
  wake key inside the authenticated pairing exchange. The machine persists them before confirming
  pairing; the push gateway never receives the wake key.
- `swarm-remote`, not the untrusted relay, submits an authenticated content-free wake directly to the
  push gateway under a durable retry obligation associated with the corresponding mailbox event.
- The push gateway resolves the opaque address to an FCM token and forwards the envelope unchanged.
- Phone-side removal may revoke with the installation credential. Machine-side device revocation
  uses the machine-revoke capability and retries deletion durably after local epoch rotation.
- A user may disable the gateway, but the app must then state "foreground updates only"; it must not
  imply reliable background delivery.

### 3.3 Self-hosted TLS policy

Lock RC-D10 in an ADR: use normal Web PKI hostname validation by default. Preserve E2EE and SAS as
the content trust roots and treat TLS as metadata protection. A relay therefore needs a domain whose
certificate SAN matches the configured host; an IP address or self-signed certificate is a
development setup unless the user deliberately enables an expert trust policy.

Existing pinned clients migrate only after an authenticated machine profile advertises Web-PKI
support and the phone has successfully validated the same configured hostname. Failure retains the
old pin and gives a repair path; it never silently disables validation. Optional pinning requires
overlapping current/next pins and an authenticated rotation protocol before it may claim automatic
renewal. Test Caddy renewal with the same key and a new key, hostname/SAN failure, downgrade, rollback,
and N/N-1 clients.

### 3.4 Multi-machine is not multi-device

Record multi-machine as an Android/client-state change: one phone stores several independent machine
pairings. Do not weaken `Registry.AddSole`, share inbound sequence spaces between phones, or partially
implement ADR-011. Multiple phones controlling one machine remain refused until all ADR-011
milestones land together.

## 4. Normative user experience

### 4.1 Onboarding

The normal route is:

1. The developer deploys `swarm-relay` to an always-on host through the supported deployment bundle.
2. `swarm relay doctor <url>` proves DNS, WebSocket upgrade, TLS policy, protocol compatibility, and
   an ephemeral authenticated mailbox round-trip. The R2 provisioning check separately proves
   restart, backup, and restore persistence.
3. The developer runs `swarm remote init --relay-url <wss-url>` once on each computer.
4. The developer opens the Android app and chooses **Add computer**.
5. `swarm remote pair` displays a QR code and manual fallback. The phone and machine compare the SAS;
   the terminal requires local confirmation.
6. The new computer appears without replacing existing pairings.
7. The first screen shows its sessions and a plain connection state; it never presents a successful
   pairing as proof that push or background delivery works.

Long relay URLs must have a supported path. If the QR cannot fit an ordinary 80x24 terminal, the CLI
offers a PNG/browser rendering and a manual relay-URL plus short-code flow. The current 39-character
limit must not be disguised as "terminal too small."

### 4.2 Machine and session navigation

The app has a machine switcher and an aggregate inbox:

- Each row names the machine, reachability, last successful sync, and count of sessions needing input.
- Session identity is always `(machine_id, session_id)`; a display title is never an authority.
- When foregrounded, the app connects to every paired machine within a documented concurrency cap so
  the aggregate inbox is live. Connections beyond the cap use a deterministic least-recently-viewed
  policy and visibly show their last-sync age.
- A notification open first validates the wake and resolves at most its opaque machine pairing. Relay
  reconciliation then selects the authoritative session/interaction and deep-links there. If a
  target is gone, the app opens the machine inbox with an honest explanation; it never derives a
  session/card target from FCM data.
- Removing a computer from the phone and revoking a phone from a computer are different operations
  with different copy and consequences.

### 4.3 Remote launch from the phone

**New session** is available only on a selected, online machine whose paired phone has the `full`
authorization tier and whose remote kill switch is on.

- `swarm remote init` publishes machine-authored launch presets. A preset contains a stable opaque
  id, display name, provider, canonical allowed workspace/worktree root, fixed environment policy,
  and allowlisted options. The phone may choose a preset and initial prompt; it never supplies argv,
  environment variables, an arbitrary filesystem path, or dangerous permission-bypass flags.
- The confirmation sheet shows machine, provider, resolved workspace display path, worktree behavior,
  and initial-prompt presence. Confirm creates one signed `session_launch` operation.
- The daemon re-resolves and authorizes the preset before composing argv, hands the same resolved
  path to the shim, uses the existing two-phase reservation, and records the remote action.
- Launch uses the same `pending`, `sent`, `refused`, `uncertain`, and `outcome_unknown` vocabulary as
  composer operations. Reconciliation by operation id is mandatory; a network retry never spawns a
  second process.
- A terminal-launched Swarm session appears remotely without a new pairing. A remotely launched
  session appears immediately in the local session list and is attachable from the terminal.
- An exited session is historical. Remote resurrection/resume is not in the first complete product;
  the app offers a policy-checked new launch, not a guessed provider resume.

### 4.4 Structured-chat session

A complete structured session shows:

- Ordered user messages from terminal and phone with source attribution.
- Assistant prose rendered as safe Markdown.
- Tool cards with kind, input summary, running/completed/failed state, timestamps, and bounded output.
- File-change, plan, status, approval-request, and approval-resolution items from the shared schema.
- A composer whose placeholder reflects idle versus active-turn behavior.
- Stop/interrupt and approval controls as signed semantic operations, never guessed keystrokes from
  the phone.

The transcript is one logical conversation on both surfaces. "Sent from phone" is attribution, not a
separate thread. Terminal-originated prompts must enter the same ordered item stream.

### 4.5 Composer delivery states

Every phone-authored message has one of these visible states:

| State | Meaning | Allowed transition |
|---|---|---|
| `draft` | Local text only. | `pending` or deletion |
| `pending` | The app is online and is acquiring/using the provider's message primitive. | `sent`, `refused`, `uncertain`, or `outcome_unknown` |
| `sent` | The daemon accepted the operation against the expected turn and attributed the injected/native message. | Terminal item acknowledgement may fold into the same item. |
| `refused` | The daemon definitively rejected it, for example stale turn, revoked device, offline target, or unsupported action. | User edits or retries as a new operation. |
| `uncertain` | Connectivity disappeared after send began and no authoritative outcome has arrived yet. | Operation-status reconciliation changes it to `sent`, `refused`, or `outcome_unknown`. |
| `outcome_unknown` | A crash crossed an irreversible provider side-effect boundary and Swarm cannot prove whether the provider accepted the text. | Never auto-retry. The user inspects the transcript and deliberately creates a new operation if needed. |

An uncertain, unknown, or offline draft is never automatically replayed. Live input must not become
an unseen queue merely to make the UI look successful. There is one active turn per session instance:
the first accepted idle-start wins; a mid-turn send is accepted only when the provider advertises
native steering, and otherwise receives a stable refusal instead of waiting invisibly.

### 4.6 Approvals and interruption

- Approval cards are first-answer-wins between terminal and phone.
- `Approve` means the machine applied the decision, not that the tool necessarily completed.
- Resolution is observed from the provider/session and attributed to `owner` or `phone`.
- Claude's grid-gated keystroke application remains fail-closed and version-characterized until a
  supported native channel replaces it.
- Codex approvals use the app-server RPC; no Codex approval is typed into the PTY.
- Stop is a signed semantic interrupt when the provider capability record says `interrupt=true`.
  Fallback shows the same button only under that capability; otherwise it hides/refuses it with
  "This provider version has no safe remote interrupt." It never guesses a control key.

### 4.7 Terminal-fallback session

Fallback is capability-driven, not user-selected for a structured provider:

- The header states why structured chat is unavailable, including provider and detected version.
- The terminal body is the machine-sanitized snapshot and incremental safe rendering, never raw ANSI.
- The initial mode is read-only. It remains useful for monitoring without any lease.
- **Control terminal** is explicit, visually persistent, and capped by the existing signed 15-minute
  horizon. The daemon may grant less; there is no silent renewal.
- Only the active fallback screen may send raw input. The app does not buffer or replay it.
- The computer TUI and phone may both remain attached, but the app warns that simultaneous typing can
  interleave.
- Leaving the screen or backgrounding stops local input immediately and sends a best-effort signed
  end. The daemon ends on receipt; an abrupt connectivity/process loss ends at the missing-keepalive
  deadline, no later than 30 seconds. Expiry, revocation, kill switch, or session replacement severs
  it synchronously at the daemon. Capabilities are immutable for a session instance; an adapter
  upgrade takes effect only on a newly launched/resumed instance, so no live surface silently changes
  mode.

### 4.8 Connectivity, sleep, and notifications

- `online`, `reconnecting`, `offline`, `repair required`, and `machine unavailable` are distinct
  states. "Machine asleep" may be inferred only from authenticated presence/liveness evidence, not
  from a generic timeout alone.
- Laptop sleep pauses the agent; Swarm promises reconnection after wake, not computation while asleep.
- Android foreground uses a live wait-driven connection. Background, Doze, standby, and battery saver
  close it and rely on FCM.
- The high-priority data wake has one app-wide collapse key and a five-minute FCM TTL. Its correctness
  purpose is to prompt a generic local notification, not to run an unrestricted background socket.
  A bounded Android-approved worker may be an optimization, but release correctness depends on
  foreground reconciliation after tap/open.
- Lock-screen notifications are generic. If the wake key is unavailable while locked, Android stores
  the envelope and performs no routing/action until unlock and authentication. After validation, the
  app resolves at most the machine, reconciles its self-hosted mailbox, and only then opens the
  authoritative pending session/card.
- Android force-stop is an explicit exception: FCM/background work is not promised until the user
  manually opens the app. The next open must reconcile without data loss and explain the stale period.
- A disabled or unreachable push gateway is a visible degraded state in Settings and machine health.

### 4.9 Forget, revoke, and device loss

These are separate actions; copy must not blur them:

- **Forget this computer on this phone** first revokes its push address with the installation key,
  then deletes machine keys/cache after warning that the computer still authorizes the old device id
  and must revoke it before re-pairing. If gateway deletion is unavailable, the safe default waits
  and retries. **Force forget** requires a second confirmation, deletes machine secrets, and retains
  only a bounded opaque deletion tombstone under the installation key until success or the gateway's
  180-day expiry; the copy names the possible residual generic-notification window.
- **Revoke this phone from this computer** first rotates/removes machine authorization and refuses
  future commands, then durably requests relay cleanup and push-address deletion. Partial cleanup is
  shown and retried; local authorization is not restored because an untrusted service is offline.
- **Phone lost or app reinstalled** is recovered from the computer with `swarm remote revoke`, then a
  fresh pairing. There is no account recovery. Re-pairing without revoking the old device is refused
  by the single-device registry.

An honest relay acknowledges mailbox deletion, but confidentiality does not depend on deletion: a
malicious relay may retain old ciphertext, and epoch rotation prevents it or the revoked device from
opening future traffic. One machine's removal/revocation cannot alter another pairing.

## 5. Target architecture

```text
terminal TUI ─┐
              ├─ local UDS ─ daemon ─ shared session tap ─ shim-owned PTY ─ agent CLI
Android app ─ self-hosted WSS relay ─ swarm-remote ────────┘
      │                 ciphertext mailbox only
      └─ FCM token ─ Swarm push gateway ◀─ authenticated content-free wake ─ swarm-remote
```

The two remote paths have deliberately different trust and data scopes:

| Path | Operator | Carries | May observe |
|---|---|---|---|
| Session relay | User/organization | E2EE journal, commands, terminal fallback snapshots, pairing rendezvous | Routing ids, timing, sizes, presence, retention metadata |
| Push gateway | Swarm | FCM token mapping and authenticated fixed-shape content-free wake | Opaque push address, source IP, timing, fixed/padded size, FCM outcome |
| FCM | Google | Authenticated fixed-shape content-free wake | App/project routing, device token, opaque address, clear wake counter/time, timing |

The push gateway is provisioned with no relay URL, relay-auth key, pairing secret, session key,
device command key, or daemon credential. Network reachability to a public relay is assumed, and the
security review tests a compromised gateway colluding with a compromised relay. E2EE and signed
commands—not network separation—prevent content access and command forgery. A compromised gateway may
spam, suppress, or correlate wakes; it cannot produce a wake authenticator, submit a valid command,
or read a conversation.

## 6. Component contracts

### 6.1 Shim and session tap

- The shim remains the only owner of the PTY and provider backend child processes.
- Daemon restart, terminal closure, and `swarm` upgrade do not end the agent session.
- One ref-counted tap fans PTY output to terminal and phone observers without eviction.
- PTY close, resize, input, and subscriber teardown must be serialized and race-detector clean.
- A provider backend such as Codex app-server lives and dies with the shim, not the daemon.
- Every complete-chat structured source shares that survival boundary. Claude hooks post to a
  per-session shim-owned socket; the shim durably sequences and spools each accepted raw event before
  acknowledging it, and the daemon drains the spool idempotently. A native backend's event cursor is
  persisted at the same boundary.
- Daemon unavailability neither fails a provider hook nor loses an accepted item. An unrecoverable
  spool/cursor gap emits an exact `structured_gap` boundary, disables `structured_chat` for that
  session instance, and forbids a fabricated completion.
- The spool is owner-only, bounded, crash-atomic, and compacted only after the daemon durably folds an
  acknowledged sequence. Disk-full or corrupt-spool behavior must let the agent continue locally,
  record the earliest provable gap, and show the surviving transcript read-only before offering an
  explicitly degraded fallback when supported.

### 6.2 Provider adapter

Extend the adapter seam with explicit, independently testable capabilities:

```text
InteractionSource  -> normalized item stream and history cursor
MessageSink        -> start/steer a turn with expected-turn enforcement
ApprovalSink       -> answer a provider approval or report unsupported
LifecycleSink      -> interrupt and provider-native status
TerminalFallback   -> sanitized observer plus optional live-only input
```

An adapter publishes an authoritative capability record at session launch. Capabilities are stamped
with provider name, detected version, adapter revision, and any runtime probe result, and are
immutable for that session instance. The phone renders from that record; it never infers support
from whether a transcript happens to be empty. A runtime integrity failure may only degrade the
record through an explicit daemon-authored capability event such as `structured_gap`; it cannot
upgrade a fallback session in place.

### 6.3 Protocol

Keep four concepts distinct: local transport-protocol features, paired-device authorization tier,
remote semantic profile, and per-session provider capabilities. The asynchronous E2EE mailbox has no
local `hello`, so the machine sends a sealed `RemoteProfileV1` during reconciliation. It names accepted
action/body versions, interaction-schema version, `TerminalView` version, and session-capability
record version. Every durable semantic mutation binds the selected profile and, where relevant, the
session instance and signs a canonical hash of its full body; symmetric sealing alone is not command
authorization. Generation-bound raw input and keepalive are the explicit per-frame-signature
exception described below.

Add or finalize these semantic operations:

- `launch_presets(machine, profile)` returning stable ids plus a policy revision
- `session_launch(machine, operation_id, profile, preset_id, preset_revision, initial_prompt?,
  expires_at)`
- `composer_send(machine, session_instance, operation_id, profile, expected_turn,
  expected_input_revision?, text, expires_at)`
- `operation_status(machine, operation_id)`
- `turn_interrupt(machine, session_instance, operation_id, expected_turn?, expires_at)`
- `interaction_history(machine, session_instance, before_item, limit)`
- `interaction_detail(machine, session_instance, item_id)`
- `session_capabilities(machine, session_instance)` as daemon-authored state
- `terminal_watch` / `terminal_unwatch` only when `terminal_fallback=true`
- `terminal_control_begin(machine, session_instance, operation_id, profile, expires_at)`,
  `terminal_input(machine, session_instance, control_generation, bytes)`, and
  `terminal_control_keepalive(machine, session_instance, control_generation)` /
  `terminal_control_end(machine, session_instance, control_generation)`

Durable semantic operations use the existing two-phase discipline: `prepared -> executing ->
applied | refused | outcome_unknown`. The record is fsynced before a side effect. A restart never
repeats an `executing` provider side effect whose result cannot be proven; it resolves to
`outcome_unknown`. Outcomes remain queryable for seven days and at least as long as the associated
session history. Fault-injection tests stop the process before/after prepare, provider write/RPC,
capture correlation, and sealed reply.

`composer_send` validates machine/session instance, device authorization, expiry, selected remote
profile, expected turn, provider capability, and provider-specific input preconditions. For Claude,
`expected_input_revision` is mandatory. The shim first acquires the one transaction lock shared by
every owner/remote input writer; while holding it, it snapshots and checks the characterized input
region is empty and the revision still matches, then writes the entire text-plus-submit framing
without releasing the lock. A pre-existing/changed terminal draft is refused and any concurrent
writer waits. Race tests park owner typing exactly between check and write. The resulting
`UserPromptSubmit` is attributed to the phone only when normalized text and the durable correlation
record match exactly; otherwise attribution is `mixed` or `unknown`, never falsely `phone`.

`session_launch` resolves only a machine-authored preset at the signed revision; a changed revision
receives `stale_preset` instead of silently launching different policy. It uses the existing
reservation to make a replay return the same session/outcome. `operation_status` never authorizes a
retry. Unknown actions, profiles, body versions, session instances, or capabilities receive sealed
stable refusals.

Terminal observation uses a versioned `TerminalViewV1`: full coalesced snapshots with monotonically
increasing revision, session instance, rows/columns, UTF-8 text, and a reset/resync marker. Prefer
full coalesced snapshots over an invented patch language in v1. Size/line/rate bounds are declared in
the profile; a slow observer drops superseded snapshots and receives the newest complete revision.
The trusted machine renderer strips control sequences and supplies replacement glyphs for invalid
Unicode. Watch grants no input authority.

`terminal_control_begin` is a signed semantic operation bound to the selected remote profile, paired
device signing key, and fallback session instance. It mints one non-transferable generation bound to
that sender. `terminal_input` and keepalive are not individually signed: like the existing lease
design, they ride only the E2EE frame's authenticated sender/sequence and that device-bound confirmed
generation. This is the sole exception to full-body signatures, never a bearer token another device
can use.

The daemon-clock deadline is no later than the signed 15-minute horizon; bytes are live-only and
never queued. The active screen sends a liveness keepalive at most every ten seconds; missing
keepalive severs the generation within 30 seconds and never extends the signed horizon. Wrong
profile/sender/generation/instance, replayed sequence, read-only tier, background-triggered end,
revocation, kill switch, and expiry fail closed. Owner-terminal typing remains possible and may
interleave as the UX warns.

### 6.4 Phone core and multi-machine manager

Refactor the current singleton `mobile.App` composition into:

- `MachineClient`: one pairing, key store, relay destination, cursors, read models, connection loop,
  command sequencer, and operation journal.
- `MachineManager`: durable registry of machine descriptors, lifecycle orchestration, aggregate event
  stream, connection cap, and add/remove/select operations.
- `PushRegistration`: application-level installation credential plus one independent opaque wake
  address, wake key, submit capability, and machine-revoke capability per machine.

Each `MachineClient` keeps independent keys and sequence spaces. No content key, relay identity,
cursor, operation id, or state directory is shared between machines. Existing single-machine state
migrates transactionally to the first registry entry and remains rollback-readable until the new
registry is durably committed.

### 6.5 Self-hosted relay distribution

Ship a supported deployment bundle, not only a prose VPS runbook:

- Versioned OCI image for `swarm-relay` on amd64 and arm64.
- Docker Compose bundle with Caddy, persistent volumes, health checks, restart policy, and automatic
  TLS.
- Generated high-entropy relay operator secret/instance identity for diagnostic/admin authority; it
  is not a substitute for Web-PKI server authentication.
- Explicit backup and restore commands for the bbolt store, with restore compatibility tests.
- Bounded logs with body redaction, resource limits, mailbox quotas, and disk-space alarms.
- A trusted-proxy policy that accepts forwarding headers only from the Compose loopback proxy, then
  keys source quotas by the validated external address. Adversarial tests prove spoofed headers and
  one client cannot consume another client's budget.
- `swarm relay doctor` that uses an operator-created, short-lived diagnostic capability to create an
  ephemeral route, round-trip random encrypted bytes, delete it, and print actionable DNS/TLS/WSS/
  storage results. The normal public protocol gains no privileged unauthenticated doctor endpoint;
  restart/restore checks are separate operator steps.
- Upgrade documentation and N/N-1 protocol compatibility; no "latest" image in generated config.

The relay remains usable without push credentials because push no longer lives there.

### 6.6 Push gateway

Define a small versioned HTTPS service with five logical operations:

| Operation | Caller | Purpose |
|---|---|---|
| Register installation | Android | With valid app-attestation evidence, exchange an FCM token and Keystore-generated installation public key for an opaque installation id; allocate no machine address yet. |
| Allocate address | Android | Authenticated by the installation key; return `{push_address, submit_capability, machine_revoke_capability}` once for one pending pairing. The submit capability is reusable until the whole address is revoked. |
| Rotate token | Android | Replace the FCM token without changing every machine pairing. |
| Revoke address | Android or `swarm-remote` | Delete one address using the installation credential or its distinct machine-revoke capability. |
| Submit wake | `swarm-remote` | Present the unguessable submit capability and a bounded `WakeV1`. |

Required controls:

- TLS, strict request/body limits, fixed or padded wake shapes, per-address and per-source quotas.
- Store only token mapping, installation public key, opaque address, hashed capability verifiers,
  creation/last-use timestamps, and minimum delivery diagnostics. Submit and revoke capabilities are
  distinct; installation-control requests require a signature over method, path, body hash, nonce,
  and expiry.
- Encrypt FCM tokens at rest and exclude tokens, capabilities, and payloads from logs and traces.
- Delete revoked/expired registrations; expire unbound allocations after ten minutes, delivery
  diagnostics after seven days, and inactive installation mappings after 180 days unless the app
  refreshes them. Publish these periods before the first real token is collected.
- `WakeV1` is fixed at one reviewed size and carries only version, opaque push address, monotonically
  increasing per-machine wake sequence, issued-at, five-minute expiry, nonce, and an AEAD
  authenticator over empty plaintext using the phone-generated wake key. Canonical AAD is the
  domain-separated tuple `(swarm-wake-v1, version, push_address, wake_seq, issued_at, expires_at,
  nonce)`; the nonce is unique under the wake key. The phone verifies the tag before trusting any
  header field and atomically persists the accepted per-address high-water before routing. Header
  mutation, nonce reuse, rollback, and tag/sequence race tests are mandatory. The clear
  counter/time/address metadata is documented.
  It carries no session, interaction, cursor, provider, category, repository, prompt, tool, or
  approval locator, even encrypted.
- The phone durably replay-checks sequence and expiry before routing. If locked key policy prevents
  immediate verification, an envelope can cause only generic notification; it cannot select a
  machine, deep-link, connect, or mutate state until unlock and validation.
- The gateway exposes delivery health but never claims that FCM acceptance proves handset display.

Before or atomically with mailbox publication, `swarm-remote` durably records a coalescible wake
obligation keyed by `(push_address, wake_seq)`. The minimal gateway has no hidden delivery queue: it
returns success only after FCM accepts the byte-identical request. Timeout, transport failure,
non-2xx, and non-final FCM responses leave the obligation retryable. Gateway/response loss after FCM
acceptance may cause a duplicate, which the phone's authenticator/high-water rejects harmlessly.
Restart retries the byte-identical wake until success or declared expiry. Failure never rolls back or
hides the published mailbox event, and FCM acceptance still does not prove handset display.

No-account abuse protection starts with unguessable capabilities, quotas, token validity, bounded
registrations per installation, IP/global admission limits, and a closed beta. Play Integrity/App
Check is required as a Play-app authenticity/abuse signal before public availability, but must not
become an identity account. Debug builds use a separate non-production gateway/project.

### 6.7 Android runtime

- Apply the Google Services plugin and ship a real Firebase configuration tied to the Play-signed
  package.
- Make FCM token acquisition, rotation, deletion, and push-address registration production code,
  not a fake-only conformance path.
- Keep foreground connection lifecycle in the Go/mobile core; marshal callbacks to Android's main
  thread through a bounded, observable queue.
- FCM wakes persist the envelope and post the smallest legal generic notification. They do not carry
  commands or require an unrestricted background connection; authoritative reconciliation begins on
  foreground/tap unless a separately proven Android-approved worker optimization runs.
- The aggregate UI reads immutable snapshots from `MachineManager`, not mutable singleton globals.
- Process death, app upgrade, backup exclusion, and Keystore invalidation have explicit recovery
  screens per machine.

## 7. Complete-chat capability contract

A provider may advertise `structured_chat=true` only when all mandatory rows pass against a recorded
provider version:

| Capability | Mandatory behavior |
|---|---|
| User messages | Terminal- and phone-originated prompts enter one ordered stream with stable ids and source attribution. |
| Assistant messages | Final prose is captured without terminal scraping. Streaming deltas are optional, but their availability is declared honestly. |
| Tool lifecycle | Start and terminal outcome are represented; missing full output is marked truncated/degraded, never invented. |
| Turn lifecycle | Idle, active, waiting, completed, interrupted, and failed states are provider-authored or adapter-proven. |
| Composer | Idle start and, where supported, mid-turn steer have explicit outcomes and expected-turn enforcement. Unsupported steering is refused with legible UX. |
| Interrupt | A semantic interruption reaches the current turn and is observed. |
| Approvals | Pending requests re-deliver after reconnect; resolutions dismiss on every surface; first answer wins. |
| History | Cold-open reconstructs the retained conversation in order and exposes an honest retention floor. |
| Version skew | Unknown provider versions fail to the terminal fallback or a status-only refusal, not optimistic structured mode. |
| Survival | Daemon restart preserves every structured event accepted by the shim/backend. A proven gap is marked at its exact boundary and degrades the session; it is never silently bridged. |

Claude may satisfy complete chat without token-live assistant deltas; tool/status activity must make
work visible and final prose arrives at `Stop`. Codex is expected to add true deltas through
app-server.

## 8. Provider implementation plans

### 8.1 Claude Code

Build on the shipped hook capture and approval path:

1. Finish safe Markdown, tool cards, incremental item mutation, scroll preservation, composer
   delivery states, and working indicators from Mirror M2.
2. Move hook ingestion to the shim-owned durable spool and prove daemon-outage survival before
   claiming complete chat.
3. Add `composer_send`: verify `expected_turn`, an empty characterized input region, and
   `expected_input_revision`; take the shim-wide input transaction; write text plus submit framing;
   and correlate the exact subsequent `UserPromptSubmit` item back to the phone operation.
4. Implement history paging and bounded detail from Mirror M3. Transcript JSONL remains enrichment;
   killing the tailer must degrade detail, not the live feed.
5. Preserve grid-gated approval injection only for characterized Claude versions. Unknown dialog or
   version means a pending card with a refusal, never a guessed answer.
6. Retain the Channels path as a feature-gated successor only after a production support contract and
   version gate exist.

Claude exit demonstration: start through Swarm, type from terminal, continue from phone while tools
run, approve from either side, background the app, receive a real wake, reopen into the exact card,
switch networks, and finish from the terminal without session replacement.

### 8.2 Codex

Run the app-server feasibility gate in R1, before topology work:

1. Start `codex app-server --listen unix://<session-dir>/codex.sock` under shim ownership.
2. Attach the real Codex TUI with `codex --remote unix://<socket>` inside the shim PTY.
3. Connect a second observer and prove it receives the same thread's item events without stealing or
   mutating TUI ownership.
4. Prove `turn/start`, `turn/steer`, `turn/interrupt`, and approval replies affect that exact thread
   and close the terminal's corresponding state.
5. Record protocol fixtures and installed Codex version. If any leg fails, use rollout-file tail for
   mirror-only status/history and keep composer/approval in fallback; do not invent RPC support.

If the gate fails, RC-D4 is not silently weakened: the first complete release is blocked until the
owner amends that decision or a supported native route is proven. If it passes, make app-server the
session backend, batch text deltas at the adapter, normalize tool and file-change items, use native
approvals, and replace heuristic status. No Codex semantic
operation is implemented by terminal keystroke injection.

### 8.3 OpenCode and AGY

For the first complete product:

- Keep real launch, interrupt/kill, and honest heuristic status where already supported. Remote
  resume of an exited session remains out of scope across providers.
- Advertise `structured_chat=false`, `terminal_fallback=true` until the complete-chat table passes.
- Show provider/version and missing-capability explanation on the phone.
- Continue the bounded OpenCode SSE/HTTP and AGY hook/ACP probes after Claude and Codex gates are
  green. Partial structured sources may improve status cards internally but do not unlock chat.

## 9. Delivery program

Each wave begins with failing evidence, ends with independent verification, and produces a named file
under `docs/verification/`. A wave does not close because its code exists; its field behavior and
release gates must pass.

The dependency graph is explicit: R0 is the release baseline; R1 depends on R0 for exit but its
docs/spikes may start in parallel; R2 and R3 depend on R1 and may implement in parallel, while R3's
physical exit also requires the R2 public-relay path; R4 depends on R2+R3; R5 depends on R1+R4; R6,
R7, and R8 depend on R4 and their R1 feasibility/profile gates; R9 depends on every prior exit.

### Wave R0 — trustworthy baseline

**Purpose:** stop building the remote product on a release process known to be red.

Deliverables:

- Fix and race-pin PTY close versus resize in `internal/shim`.
- Make gateway cancellation return promptly; stress `TestPhonesim_LaunchE2E` rather than widening its
  timeout without diagnosis.
- Repair the callback-overflow conformance test so it subscribes to the event family it claims to
  overflow, then mutation-prove drop-oldest and the surfaced signal.
- Install pinned `gomobile`/`gobind` where the generic test lane invokes bind tests, or split those
  tests behind one deliberately exercised CI lane.
- Use a Go-1.25-compatible golangci-lint and refresh reviewed Gradle verification metadata.
- Gate release publication on all required CI jobs, including race, Android, and lint; a manually
  published tag while required CI is red is a release failure.

Exit evidence: three consecutive full required CI runs green, isolated race/stress reproductions
green, and no orphaned test processes.

### Wave R1 — decisions and capability skeleton

**Purpose:** make the new product rules executable before new UI accumulates around old assumptions.

Deliverables:

- Land the ADR amendments and complete the source-transition matrix from section 3 in the same
  change; no current normative/runbook source may retain the replaced behavior.
- Add sealed `RemoteProfileV1`, immutable session capability records, and downgrade/refusal tests.
- Add `session_launch`, `composer_send`, `operation_status`, `turn_interrupt`, terminal-view/control,
  and push-pairing shapes with refusal-only daemon handlers first.
- Introduce `MachineManager`/`MachineClient` interfaces with a single-machine compatibility adapter.
- Define push-gateway OpenAPI/schema, threat model including relay+gateway collusion, retention
  policy, `WakeV1`, wake-obligation state machine, and migration fixture.
- Run and record the Codex app-server two-client feasibility gate. A failure blocks RC-D4 pending an
  explicit owner decision; it does not silently move Codex to fallback.
- Rewrite `docs/operations/physical-handset-gate.md` for Android, gateway push, multi-machine, and the
  test ids used by every later wave.

Exit evidence: old clients degrade safely; unsupported semantic operations return sealed refusals;
structured versus fallback routing is provider-authoritative; the Codex decision is evidence-backed;
the current docs have one non-contradictory direction; and the new handset matrix is ready to execute.

### Wave R2 — self-hosted relay productization

**Purpose:** make cross-network setup repeatable without requiring an operator to reconstruct the
repository's runbooks.

Deliverables:

- Versioned relay OCI images and Compose/Caddy bundle.
- RC-D10 Web-PKI migration, optional expert-pin rotation, and downgrade-safe N/N-1 behavior.
- Health/readiness endpoints, doctor command, backup/restore, quotas, log rotation, and disk-full
  behavior.
- Trusted-proxy source identity and per-source/per-key quota isolation behind the shipped Caddy
  topology.
- Pairing flows for long endpoints and clear diagnostics for DNS/TLS/WebSocket/pin failures.
- Upgrade compatibility test across N/N-1 relay and client versions.

Exit evidence: a clean VPS can be provisioned from the released bundle, paired from a physical
Android handset on cellular, restarted, upgraded, backed up, and restored without losing an acked
cursor or exposing a payload.

### Wave R3 — real push and background vertical slice

**Purpose:** prove the feature that makes a phone useful before polishing chat.

Deliverables:

- Minimal push gateway and production Firebase project/configuration.
- Public minimum privacy notice, exact retention/deletion jobs, consent/degraded copy, abuse contact,
  Play Integrity/App Check policy, and credential-incident runbook before collecting a real token.
- Android installation registration, token rotation, per-machine allocation, both revocation paths,
  and pairing-failure cleanup.
- Per-machine wake key, address, submit capability, and machine-revoke capability transferred in the
  authenticated pairing transcript.
- Durable coalesced wake obligations and idempotent gateway submission with crash injection before
  append, after append, before submit, after gateway commit, and before local acknowledgement.
- Notification permission, generic lock-screen copy, durable replay guard, force-stop copy, and
  foreground reconciliation. Inspect the raw FCM request to prove the content-free schema.
- Visible foreground-only degradation when push is disabled or unhealthy.

Exit evidence: physical handset receives and opens a wake from background, Doze, app standby, and
battery saver; Wi-Fi-to-cellular and cellular-to-Wi-Fi handoffs recover; no fake endpoint or emulator
counts for the exit.

### Wave R4 — multi-machine foundation

**Purpose:** remove singleton state before the final chat screens make it more expensive.

Deliverables:

- Transactional migration of current state into a machine registry.
- Independent `MachineClient` state/key/connection loops and aggregate event stream.
- Add/switch/remove computer UX and global inbox.
- Per-machine push address, notification routing, revocation, error recovery, and connection health.
- Bounded foreground connection concurrency and deterministic stale-state rendering.

Exit evidence: one handset controls three machines—two through the same organization relay and one
through a separate relay. Every pairing has independent device/relay keys, sequence spaces, state
directories, operation ids, and wake addresses; all three remain live; duplicate session ids and
display names do not collide; revocation isolates one pairing; process death restores all three.

### Wave R5 — phone remote launch

**Purpose:** finish the privileged lifecycle path before chat polish depends on sessions it cannot
create remotely.

Deliverables:

- Machine-authored provider/workspace launch presets and setup UX.
- Phone selection/confirmation, signed `session_launch`, stable refusal codes, activity logging, and
  operation-status reconciliation.
- Existing allowed-root/symlink/options/environment policy and two-phase reservation applied to the
  exact remote execution path.
- Terminal and phone lists converge on the same new session; kill switch, read-only/read+approve
  tiers, stale profiles, offline targets, and unknown presets refuse before argv composition.

Exit evidence: a physical phone launches each supported provider on an allowed preset over cellular,
the terminal attaches to the same session, and fault injection around reservation/spawn produces at
most one process with an authoritative or `outcome_unknown` result.

### Wave R6 — Claude complete chat

**Purpose:** deliver the first full reference experience on the multi-machine foundation.

Deliverables:

- Shim-owned durable hook spool and daemon-restart reconstruction.
- Mirror M2 and M3 work, structured composer with input revision/transaction, semantic interrupt,
  approvals, history, detail, honest source attribution, and post-reconciliation deep-links.

Exit evidence: the Claude exit demonstration in section 8.1 passes on a physical handset and the
terminal remains live throughout.

### Wave R7 — Codex complete chat

**Purpose:** prove the provider abstraction with the first token-live native backend.

Deliverables:

- The R1 app-server gate converted into a shim-owned backend, normalized incremental items, native
  message/steer/interrupt/approval, typed status, durable cursor, and compatibility fallback.

Exit evidence: terminal TUI and phone drive the same Codex thread concurrently; token deltas and tool
states arrive within the budgets below; answering an approval on either surface resolves the other.

### Wave R8 — incomplete-provider terminal fallback

**Purpose:** make OpenCode, AGY, and future providers useful without presenting terminal scraping as
chat.

Deliverables:

- Capability-routed `TerminalViewV1`, sanitized read-only rendering, explicit temporary control,
  live-only generation-bound input, immediate daemon-side release triggers, provider/version
  explanation, conditional safe interrupt, and interleaving warning.
- Adversarial ANSI/OSC/Unicode fixtures, slow-reader coalescing/resync, resize/input races, stale
  generations, relay replay/wrong signer, session replacement, disconnect/background, kill switch,
  and revocation tests.

Exit evidence: OpenCode and AGY can be launched and safely monitored/controlled from the fallback;
Claude and Codex expose no route to it when their structured capabilities are healthy.

### Wave R9 — closed beta and release

**Purpose:** validate the whole product outside developer fixtures.

Deliverables:

- Physical-handset matrix completed and recorded.
- Self-host install tested by people who did not write the runbook.
- Push-gateway privacy/retention/incident controls from R3 exercised at beta scale, with abuse
  dashboards and deletion sampling.
- Play closed-track upgrade from the current app, including state migration and notification opt-in.
- Release rollback, relay N/N-1, provider version skew, and device-loss drills.

Exit evidence: every release gate in section 11 passes and no P0/P1 residual is waived by prose.

## 10. Performance and reliability budgets

Measure these on the released Android build over a public self-hosted relay; loopback harnesses are
diagnostic only. Each report records app/machine/relay/gateway SHAs, handset/API/model, relay and
gateway regions, network carrier/type, sample count, failures/timeouts, and raw correlation ids.
Use same-device monotonic boundaries where possible and a measured clock-offset/error bound for
cross-device spans. Foreground latency requires at least 200 successful samples on Wi-Fi and 200 on
cellular after warm-up; background FCM requires at least 50 normal-background samples per handset.
Doze/standby/battery-saver results are separate distributions with at least 20 samples each and are
never excluded from the artifact. Provider emission delay begins/ends at named adapter timestamps.

| Measure | Required budget |
|---|---|
| Foreground phone send to daemon acceptance | p50 <= 150 ms, p95 <= 300 ms under ordinary broadband/cellular |
| Accepted interaction item to visible foreground update | p95 <= 300 ms, excluding provider emission delay |
| Network-restored to reconciled foreground state | p95 <= 5 s after Android reports usable connectivity |
| FCM submission to notification callback | Observed p95 target <= 5 s in normal background; not represented as an individual-delivery guarantee |
| App open from notification to reconciled target | p95 <= 2 s after process start on supported hardware/network |
| Duplicate semantic operations after retries/reconnect | zero |
| Automatically replayed uncertain messages or raw input | zero |
| Lost acknowledged interaction items within retained history | zero |
| Push plaintext session/content fields | zero, enforced by schema and payload inspection |

The 500 ms phone polling loop is removed in favor of bounded `MailboxWait`, with an explicit
compatibility fallback only for old relays. Backoff includes production jitter and is exercised with
the production random source enabled.

## 11. Verification and release gates

### 11.1 Required automated gates

- `go vet ./...`
- `go test -race ./...` with pinned gomobile tools available
- Focused stress counts for shim lifecycle, gateway cancellation, reconnect, operation idempotency,
  callback overflow, and multi-machine isolation
- Structured-spool daemon-kill tests at every Claude hook stage; input-transaction tests against
  owner typing/drafts; operation-journal crash tests at every side-effect boundary
- Android lint and all JVM/Robolectric tests against the real built AAR
- Dependency verification and Android artifact assertions
- `RemoteProfileV1`, signed-body, session-instance, downgrade, and unknown-action negative tests
- `WakeV1` plaintext deny-list/fixed-shape/replay/expiry tests plus wake-obligation process-kill tests
- Relay container scan, non-root execution, read-only filesystem except declared volumes, and
  backup/restore integration; trusted-proxy spoofing and quota-isolation tests
- Documentation manifest and ADR/spec consistency checks

Every required job must be a protected release prerequisite. Snapshot/release artifact generation is
not a substitute for test success.

### 11.2 Physical-handset matrix

At minimum test one current and one oldest-supported Android API level across:

- Camera QR and manual pairing
- Foreground, background, process death, and reboot; force-stop specifically proves no promised wake
  until manual reopen and complete reconciliation afterward
- Doze, app standby, battery saver, notification permission denied/granted
- Locked-device wake when the Keystore key is and is not immediately available; neither case routes
  or mutates before authentication
- Wi-Fi, cellular, Wi-Fi/cellular handoff, captive/offline recovery
- Laptop sleep/wake and relay restart during a pending approval
- Keystore-backed key creation, app upgrade, corrupted state, phone loss/revocation
- Three machines—two on one organization relay, one on another—concurrent session updates, duplicate
  session ids/display names, isolated revoke, and process-death restore
- Remote launch from a machine-authored preset, disallowed root/options, launch crash recovery, and
  terminal attach to the remotely created session
- Claude and Codex terminal/phone co-presence and first-answer-wins approval
- OpenCode/AGY fallback control release on background/disconnect

The existing physical-handset gate must be rewritten to current product decisions and then executed;
old `[UNRUN]` rows cannot be cited as evidence.

### 11.3 Security review gates

- Relay cannot decrypt or forge a valid content envelope.
- A colluding compromised relay and push gateway still cannot decrypt content or forge a command.
- Push gateway cannot turn a wake into a daemon command and cannot resolve opaque addresses to a
  machine/session identity from stored fields.
- Submit and machine-revoke capabilities are distinct, machine-specific, unguessable, rate-limited,
  revocable, and absent from logs. The gateway never learns the wake authenticator key.
- `WakeV1` has empty plaintext and no encrypted session/card locator; clear address/counter/time
  metadata matches the disclosure and replay contract exactly.
- Cross-machine state confusion fails closed under duplicate ids, replayed frames, corrupted cursors,
  and restored backups.
- Terminal fallback admits no unsanitized control sequence and sends no bytes without a live confirmed
  control generation.
- Provider version skew falls back or refuses; it never guesses an approval key or RPC shape.
- Structured capture survives daemon outage; a proven gap visibly degrades instead of fabricating a
  complete conversation.
- Revocation stops commands immediately, rotates the epoch where required, asks an honest relay to
  delete old ciphertext without depending on it, and durably revokes the push address/capabilities.

## 12. Migration and compatibility

### Android state

The first multi-machine app version performs an atomic migration:

1. Read the existing singleton state and verify custody before modifying anything.
2. Create a machine registry entry keyed by the authenticated machine id.
3. Move/copy state into a per-machine namespace with keys remaining Keystore-wrapped.
4. Commit the registry last.
5. On failure, continue opening the old state read-only and offer retry; never produce two live send
   sequencers for one pairing.
6. Remove rollback compatibility only after at least one stable release train.

### Machine and relay

- Add remote behavior through sealed `RemoteProfileV1`; local protocol negotiation remains separate.
  Old clients see status or explicit upgrade requirements, not malformed chat.
- Keep current terminal watch/input bodies only under the legacy remote profile; new fallback uses
  versioned view/control bodies and confirmed generations.
- Persist one `push_transport` per pairing: `legacy_relay`, `gateway`, or `foreground_only`. A machine
  switches from legacy only after Android gateway registration, address allocation, authenticated
  pairing/update acknowledgement, and a successful gateway test wake. The state transition is
  atomic; rollback selects one transport, never both.
- During the one compatibility window, revocation attempts both legacy and gateway deletion
  idempotently, but wake sequence/deduplication and the selected state forbid double delivery. Test
  new app/old relay, old app/new relay, interrupted migration, token rotation, rollback, and revoke
  with either endpoint unavailable.
- Pairing schema additions are versioned and bind push address, submit/machine-revoke capabilities,
  wake key, remote profile, and session-independent machine identity into the authenticated transcript.
- Codex/Claude provider fixtures are version-stamped. A newly detected unsupported version starts in
  safe degraded mode until characterized.

## 13. Retention and cursor expiry

Defaults are product behavior, not placeholders:

| Store | Default retention and bound | Expiry behavior |
|---|---|---|
| Relay ciphertext mailbox | Delete on authenticated acknowledgement; hard-delete unacked items after 7 days, preserving the existing operator-visible cap. | Phone receives `cursor_too_old`, marks the transcript incomplete at an exact boundary, and rebuilds from machine history. |
| Machine normalized interaction history | Active sessions and unresolved approvals are protected; completed-session items remain 30 days, under a 250 MiB machine-wide cap with oldest-completed eviction. | History response returns the earliest retained item/time and an explicit gap; never silently starts later. |
| Machine provider-detail store | 7 days and 100 MiB; item summary/lifecycle remains with normalized history. | Detail reads return `detail_expired` while the card remains honest and usable. |
| Phone cache | 30 days and 250 MiB across machines, with per-machine namespaces and oldest-completed eviction. | Rehydrate from machine history; if both floors moved, render the exact unavailable interval. |
| Semantic operation outcomes | 7 days and no shorter than associated session history. | `operation_status` returns `outcome_expired`; the app never infers success or retries automatically. |
| Push gateway unbound allocation | 10 minutes. | Delete address and all verifiers; pairing must allocate again. |
| Push gateway delivery diagnostics | 7 days, no payload/capability/token. | Operational aggregates may remain only if irreversibly non-addressable. |
| Push installation mapping | Until revoke or 180 days without an authenticated app refresh. | Delete FCM token mapping and addresses; app shows foreground-only until it registers again. |

Quotas are configurable downward/upward by a self-host operator only where stated, but the app must
display the authenticated floor it receives rather than assume these defaults.

## 14. Operations and privacy

### Self-host operator responsibilities

The deployment guide states plainly that the relay operator owns:

- Public host, domain, DNS, TLS, storage, backup, update cadence, and availability
- Metadata visible to their relay
- Mailbox retention and log access
- Revocation cleanup and disaster recovery

Generated defaults are secure and bounded; "self-hosted" must not mean every user invents quotas,
systemd policy, certificate handling, and backup semantics independently.

### Swarm push-gateway responsibilities

Swarm publishes:

- Exact stored fields and retention periods
- FCM/Google metadata disclosure
- Token deletion and machine-address revocation behavior
- Availability/degradation status
- Abuse limits and appeal/recovery path for false positives
- Incident response for token or credential exposure

The privacy claim is precise: Swarm does not receive session traffic or plaintext wake content. It
does receive push-routing metadata, network metadata, and timing.

## 15. Explicit non-goals for the first complete product

- Attaching to an agent process that Swarm did not launch
- Remotely resuming/restarting an exited Swarm session; live running sessions remain attachable
- iOS
- Multiple phones/tablets paired to one computer
- Swarm accounts, cloud pairing backup, or account recovery
- Attachments and image/file upload from the phone
- Voice control
- A hosted multi-tenant session relay
- Session/card/category locators in FCM wakes, even encrypted
- Terminal-derived pseudo-chat
- Continued agent computation while the computer is asleep
- Guaranteed background delivery when the user disables the Swarm push gateway or Android
  notification/runtime permissions

## 16. Implementer handoff rules

For every wave:

1. Re-read the governing ADRs and this playbook; identify any contradiction before coding.
2. Create/claim Beads work with acceptance criteria tied to a wave exit.
3. Write failing contract or characterization evidence first.
4. Implement the smallest vertical slice across daemon, gateway, relay/push, phone core, and Android;
   avoid layer-complete work that no handset path invokes.
5. Run mutation or negative controls for security/load-bearing tests.
6. Record current-source and physical evidence under `docs/verification/`.
7. Run all protected gates and independent review.
8. Publish only after the wave exit is true on released artifacts.

When implementation reality disproves this plan, amend the decision record and this file in the same
change. Do not preserve an attractive roadmap sentence over observed provider, Android, or network
behavior.
