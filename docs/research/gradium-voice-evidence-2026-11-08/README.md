# Voice manager research evidence

Date: 2026-11-08. Swarm source base: `b609dfa64ee0619271a3b2b6d0187f608da9c3eb`.

This is a research package, not product tests or a voice implementation. No real microphone was recorded. The new browser probe uses Chromium's fake capture device and muted playback, in an isolated profile. New provider probes sent no keys, audio or speech. At the owner's request, prior authenticated synthetic-provider evidence from another VM session was also inspected read-only, as distinguished below. The new SSH fixture bound both ends to loopback, created no remote files and exited after explicit completion; it also has a 30-second upper lifetime. Live Swarm control was not exercised by the fixtures.

## Retained results

| Artifact | Exact scope |
|---|---|
| [browser-results.json](browser-results.json) | Chromium 145.0.7632.6: localhost and 127.0.0.1 trusted origins; synthetic track 48 kHz, processing 24 kHz; 24,064 samples in 188 AudioWorklet blocks. Nonloopback HTTP negative control had no mediaDevices/getUserMedia. |
| [ssh-results.json](ssh-results.json) | Actual `tf_nathan` forward: nonce roundtrip, forwarded browser page/module, secure context and local fake-mic processing. 769 ms observed tunnel startup plus probe request; this is **not** model, media-network or conversation latency. No audio uploaded. |
| [sdk-contract-result.json](sdk-contract-result.json) | Eight fake-transport checks compiling exact upstream Tts/Stt class bodies. No complete SDK/dependency import or live service timing guarantee. |
| [auth-probe-result.json](auth-probe-result.json) | Token endpoint and EU STT/TTS returned 401 without authentication. HTTP/TLS elapsed values are not inference latency. WebSocket upgrade was not accepted. |
| [prior-vm-observations.json](prior-vm-observations.json) | Sanitized observations and source hashes from the owner-identified prior live spike. Includes contradictory token reuse, small synthetic TTS timings, fake-browser limits and a new read-only tail-energy measurement. Raw private project artifacts are excluded. |

Read-only VM environment inspection found Linux, installed `/usr/local/bin/swarm` 0.25.1 built with Go 1.26.10, Python and Node. `/dev/snd` exists; this does not grant access to a remote client's microphone. Initial sandboxed SSH could not authenticate; the scoped read-only escalation succeeded. Initial Chromium launch was blocked by sandboxed macOS IPC; the same isolated fake-device probe passed with approved scope.

The local installed native-runtime check found Codex CLI 0.167.0 and Claude Code 2.1.319. `codex app-server generate-json-schema` with and without `--experimental` established that `dynamicTools` is absent in the stable ThreadStartParams and present only in experimental output. Schema generation did not start a model turn, test inference access, prove native account support, or validate manager tool isolation. Generated schema bundles are not committed.

## Reproduce browser and SSH foundations

Use an existing Python Playwright installation and matching Chromium. Nothing in these probes installs dependencies. Run from any directory with this folder's path substituted:

```sh
python3 docs/research/gradium-voice-evidence-2026-11-08/browser_probe.py
python3 docs/research/gradium-voice-evidence-2026-11-08/ssh_probe.py
```

The SSH probe intentionally uses the owner's existing `tf_nathan` alias, BatchMode and ordinary host-key verification. It requires authorized access and Python on that VM. It starts a bounded in-memory HTTP server, a specific local SSH forward, and a fake-device Chromium process; all owned local processes are stopped/waited in `finally`. It does not inspect or mutate production Swarm sessions. The remote timeout is the fallback if the client dies. Ports are transient and intentionally absent from retained results. SSH server forwarding policy may refuse this route on other hosts.

These scripts are limited research fixtures, not a browser server to deploy. They do not implement production endpoint authentication/Origin defense, PCM16 conversion, media upload, VAD, provider clients or action authorization. Do not use them as that implementation.

## Prior authenticated VM evidence

After the owner pointed to the existing session, this investigation read its roster entry, visible screen, relevant native discussion, plan and identified spike sources/tool results. It did not send the agent a message, alter its worktree, search for another key or rerun its provider calls. The prior project continues independently. None of its private code, raw discussion, account balances or project-specific product decisions is committed here.

The manifest records script content hashes and the narrowly applicable results. The original owner session is the source of that evidence; the sanitized manifest alone cannot independently reproduce or authenticate the historical provider run. A fresh credentialed rerun is still needed. The provider specialist independently checked that the token helper used the query token without an API-key fallback, both setups received `ready`, and a bogus setup failed. This is stronger than a mere successful WebSocket open, and conflicts with documented single-use behavior in that one observed run.

The prior Chrome harness used `--use-fake-ui-for-media-stream`, `--use-fake-device-for-media-stream`, `--use-file-for-fake-audio-capture`, and `--autoplay-policy=no-user-gesture-required`. It tested STT followed by TTS, never simultaneously. No normal permission/autoplay flow, real mic, acoustic playback, AEC, background or phone claim follows from it. Its 169–181 ms TTS values measure first network audio after text, excluding the manager and audible onset.

The new independent VM analysis read two already-generated synthetic WAVs using Python's `wave` and `array` modules. For each absolute PCM16 threshold (0, 32, 128), it counted consecutive terminal samples no louder than that threshold and divided by 24,000 samples/s. Results are in the manifest. Roughly 437–455 ms below threshold 32 confirms buffer-end timing can be materially biased, but does not measure human/semantic end-of-speech.

Other prior experiments have specific limits: longer idle tests collided with free-tier concurrency; a reconnect helper didn't verify the first message was `ready`; 600 ms tail cases never reached the flush branch; mixed credit deltas do not isolate billing. They are not promoted into confirmed provider contracts. Numeric/name roundtrip errors motivate a human-listened Swarm identifier corpus rather than a claim about which speech model failed.

## Reproduce SDK contract inspection

Inspected Gradium Python source pin: `e354e178990a00f1251fb82375a9b55e3bb319d9` (2026-09-04). Obtain that exact source in scratch, then pass its `gradium/stream.py` to:

```sh
python3 docs/research/gradium-voice-evidence-2026-11-08/sdk_contract_check.py /path/to/pinned/gradium/stream.py
python3 docs/research/gradium-voice-evidence-2026-11-08/uncredentialed_probe.py
```

The contract probe checks setup/json_config, explicit sample format, base64 roundtrip/size, flush correlation and receive-event preservation, ready metadata, EOS/error, multiplex IDs, variable audio chunk decoding/timing and stream cleanup. Test ordering is a fixture assumption; preserving the fake sequence does **not** prove the deployed provider emits that sequence. The copy retained here takes the source path as an argument and was rerun against the same pin.

Other inspected pins: Gradbot `db64d7703b478f85476adab8bb8c9a8db617dbf0`; Pipecat `3dbb0c8445c0ddc2276d566140fe8d12f1d61e5b`; LiveKit agents `1e352e413a2441b2c9cedcfe8f72392b2365b802`. Downloaded STT files matched the pinned source: Pipecat SHA256 `8aab6dbae22d84c4cebba82a0f5f85e0b9fdf9e0a33c36cc85f607574a260252`, LiveKit SHA256 `6582b134b3eb11e893435c16b130cfac085b09ee2062557ed2439dc1face85bc`.

Pipecat's post-flush 100 ms aggregation file last changed on 2026-10-08, before the November 3 Gradium model replacement. This is historical source evidence, not a demonstrated current service defect. The landscape links exact source URLs and distinguishes current provider documentation from framework implementation.

## Existing Swarm control verification

The control specialist ran existing tests using package TestMain/testbin supervision and shared Go caches. No live worker was launched. Exact focused command:

```sh
go test ./cmd/swarm ./internal/protocol -run 'TestPilot(Semantic|Guidance|SendUses|WorkerText|WatchOnce|AwaitRejects)|TestOwnerSemantic|TestSemanticClient|TestNativeDecisionOwner|TestNativeRequestRead|TestNativeHistoryUses' -count=1 -timeout=120s
```

`cmd/swarm` passed in 2.137 s. Protocol test bodies passed, but sandboxed mandatory cleanup enumeration failed with `operation not permitted`; the **whole command failed**, retaining scratch `/private/tmp/s2953212733`. A scoped escalation reran protocol fully, including cleanup:

```sh
go test ./internal/protocol -run 'TestOwnerSemantic|TestSemanticClient|TestNativeDecisionOwner|TestNativeRequestRead|TestNativeHistoryUses' -count=1 -timeout=120s
```

That run passed in 1.753 s. The following seven existing synthetic tests passed fully in 16.674 s with mandatory supervisor cleanup:

```sh
go test ./internal/skeleton -run 'TestComposerOldShimRefusesBeforeBeginWithoutRawInputFallback|TestComposerFIFO_ReplacedSessionIsRefusedAtLaneHeadBeforeBegin|TestOpenCodeOwnerSemanticSendUsesDurableNativeCustody|TestOpenCodeOwnerUncertainSendNeverReposts|TestNativeHistoryCoreReadUsesBoundSnapshotWithoutPublishing|TestOpenCodeNativeDecisionLostReplyNeverRepostsAndCanReconcile|TestHermesSharedOwnerSemanticSendRestartAndStopIsolation' -count=1 -timeout=120s
```

These establish existing refusal, custody, stale-instance, history/no-publication and uncertain-outcome contracts. They do not test a new voice manager. Full build/vet/lint/test/race gates were not rerun for this documentation/research-only change. Production implementation waves require their appropriate full gates.

## Independent review disposition

Sol provider review checked current protocol/source, costs, privacy and measured-versus-proposed statements. Sol control review checked actual daemon/API/capability and recovery boundaries. Astra device/UX/security review checked all requested routes, foreground/background behavior, authority and shipping gates. Concrete review corrections incorporated into the proposal include:

- Float32 AudioWorklet output versus an application PCM16 encoder, which remains untested.
- Capture/playback on the local computer while proxy audio travels through VM and Gradium.
- Retry only uncommitted capture; reconcile accepted/uncertain turns and original action receipts.
- Explicit foreground lifecycle, server timeout if browser callbacks never arrive, and silent audio resume.
- Provider-session rotation across sustained-run gates; required language setting and non-ZDR retention distinction.
- Persistent event cursor is not persistent reply content; manager restart is not uninterrupted model reasoning.

This was research review, not approval of an implemented release. No live Gradium/account or physical-device quality certificate was produced.

Final provider Sol and device/security Astra reviews approved publication as research. They reviewed manager SHA256 `f8f87f951b051eeb15b359bb0ba47134ae6410eeec538433c894d38b0ace3888`, landscape `03b5ab9424b9682f2b8ea1449d446f15d2b615bd1fefb61498c4731c3960c53a`, and prior manifest `4f19c4ee11fb94990e6b2483773f7147fadcf764790ab7e72d8564df0b5d80b8`. The control Sol review approved the shared control/recovery design with its accepted-input retry clarification, which is incorporated. These hashes identify the reviewed documents, not a product build.

## Execution tracking

Research `agents-tracker-1wxi` is complete as research. Proposed implementation epic `agents-tracker-uv6m` remains open, with `uv6m.1` for owner decisions, `uv6m.2` for fresh provider/account qualification, `uv6m.3` for the reliable manager and desktop slices, and `uv6m.4` for mobile/hardening/release gates. Full IDs carry the `agents-tracker-` prefix. This backlog does not authorize production implementation or infrastructure purchases.

## Pending live qualification

The owner will provide a Gradium key after its console works again. No new credential was supplied to this run. Do not infer an outage from unauthenticated 401 results, or use them as an availability/latency benchmark. Earlier synthetic spikes partially demonstrate provider paths; remaining gates include fresh account tokens/setup and the single-use discrepancy, actual stream/finality races, validated PCM16 encoding and timing, corpus accuracy, spend metering/silence/rotation/cancellation, real speakerphone/AEC and physical Android/iPhone lifecycle. The proposal identifies each dependency and the choices needed before implementation.
