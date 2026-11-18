# ADR-038: Native locked-screen voice over an optional per-place manager

## Status

Proposed implementation architecture. Owner requirements were settled on 2026-11-08; no implementation acceptance or release qualification is claimed. Companions: [implementation plan](../specifications/voice-manager-plan.md), [independent review](../verification/voice-manager-plan-review.md).

## Date

2026-11-08. Speech provider decision amended 2026-11-17.

## Context

The owner requires local terminal, desktop SSH, Android/Termux and iPhone-equivalent use, including speaking with the phone locked. Setup offers Codex and Claude, the manager is unrestricted, and users provide a separate OpenAI API project key for speech. Existing app/design should be reused. This supersedes the research's foreground-only browser, Codex-first and restricted-manager defaults.

The Android Activity currently releases its remote surface on backgrounding and has no microphone foreground service. Remote-v2 hosting/device gates remain open. No iOS app exists. Swarm shims already preserve native CLI sessions independently of the terminal and daemon.

## Decision

1. An optional per-place service owns scheduling, speech and compact recovery state. Managers are ordinary visible Swarm-owned sessions: existing Codex thread ownership and qualified interactive Claude native mod. Both are required before release; no silent model API credential or SDK/account substitution.
2. Manager tools remain unrestricted under owner scope and mandatory native policy. Typed Swarm actions retain exact targeting/reconciliation. This guarantee does not cover arbitrary shell effects; restart never blindly reruns uncertain work.
3. The selected place calls the global OpenAI API using its user's speech key. GPT-Live-1 streams 24 kHz PCM and continuous captions, and GPT-Transcribe finalizes an explicitly bounded utterance. Live commentary can be paraphrased and has no per-phrase audio completion event. The native manager owns actions, canonical text and exact confirmations. Swarm does not persist audio by default. Provider data handling follows the project's applicable controls. [Live WebSockets](https://developers.openai.com/api/docs/guides/voice-websockets?api=live) · [GPT-Transcribe](https://developers.openai.com/api/docs/models/gpt-transcribe) · [OpenAI data controls](https://developers.openai.com/api/docs/guides/your-data)
4. Desktop uses a local browser panel and OpenSSH forwarding. Phones own native audio and SSH, independently of any terminal app. A narrow shared Go/gomobile client is the target, conditional on actual iOS binding/signing/locked-device proof.
5. Extend the existing Android app and Slate kit; add a separate user-started voice service. Build a thin native iOS shell sharing wire, design tokens and assets. No UI framework migration or full iOS dashboard port.
6. Active native voice is an explicit exception to Android background-disconnect rules. Preserve existing dashboard background/raw-control severance. Implement the exception through ADR-007 remote B16, connectivity policy, manifest and lifecycle gate amendments; do not merely stop calling EnterBackground. No boot/push/force-stop microphone restart or silent-audio keepalive.
7. Lock alone preserves active native voice. Calls, unsafe route changes, revoked permission and process death have explicit pause/end behavior. Short network recovery may resume the same active session after control reconciliation; stale audio is not replayed as commands.
8. SSH is first and requires no hosted Swarm service. HTTPS/overlay/relay deployment is later and has separate auth/device gates. Existing relay pairings and keys are not silently migrated into SSH.
9. Versioned capability proofs and actual end-to-end gates bind both managers and every requested client route. Desktop previews/emulators do not close phone requirements. Actions remains release-tag-only.

## Consequences

Positive: reuse shim survival, native accounts, action receipts, Android components and token origin. The human retains one manager conversation across devices; SSH avoids hosted dependency and another terminal app's lifecycle.

Negative: native background audio, iOS build/signing/distribution, device SSH enrollment and physical qualification are substantial work. Unrestricted effects can bypass the action ledger. Provider-native capabilities differ. BYO speech accounts retain their own limits/privacy policies.

## Alternatives Considered

Browser-only phones and Termux-owned tunnels do not establish the selected locked-screen contract. A tool-only manager contradicts owner scope. A second native thread or generic SDK lifecycle risks violating current session/account ownership. Completing all remote-v2 hosting first is unnecessary for SSH voice. New media frameworks or duplicate platform SSH libraries require measured justification.

## Verification

The plan defines V01–V30, C1–C10 and P0–P12, including cross-component and physical gates. Independent plan review records findings/revisions. The standalone speech pipeline has its own [operation guide](../operations/voice.md) and [verification record](../verification/voice-manager/openai-live.md). Native manager integration, desktop/SSH and locked phone operation require their own gates. [Historical Gradium evidence](../research/gradium-voice-evidence-2026-11-08/README.md) records the prior provider experiments.
