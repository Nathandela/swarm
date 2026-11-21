# Personal and team dispatcher call qualification

Integration branch `integration/discord-dispatcher-calls`. The design was reviewed against the merged durable delivery fix at `b3ac49c`; the integration also includes personal DM continuity through `29345ee`. This record separates real provider and native observations from simulated Discord transport. No production bot, production server voice room, human microphone or human listener participated.

## What has run

|Boundary|Evidence|What the observation establishes|
|---|---|---|
|Single Gateway and connector|Local real WebSocket/HTTP, connector event loop, bbolt store, admitted discussion and synthetic native manager with injected media/speech endpoints. Personal and team routes; duplicate, stale, foreign and delayed publications, grant withdrawal, operator suspension, text continuation.|Both routes reach the existing discussion through the normal connector. Voice commands, playback and canonical text retain their original audience. The fixtures do not establish human-heard results. [Integration fixture](../../internal/discord/dispatcher_calls_integration_test.go)|
|Discord helper|Real pinned Node, Opus, DAVE and native MLS on macOS arm64, Linux arm64 and emulated Linux amd64. Synthetic transport encryption, membership changes, native join/welcome/commit, bidirectional encrypted media and removal. Wrong audience, media gaps, duplicate handshakes and helper failure tested.|Media processing and key transitions work locally with the selected native binaries. The library snapshot is pinned because the former stable binding did not independently bind an incoming welcome to the intended room. [Versions and evidence](discord-dispatcher-calls-media.md)|
|Existing dispatcher|Actual Codex account, private supervisor and fixture discussion. Seeded an unpredictable marker by synthetic text. Retrieved it by typed voice admission from the same native instance. Replayed before and after cutover.|One real native input and one canonical result. Postmerge personal VM canary took 8 seconds for the written seed and 5 seconds for voice-origin retrieval. No worker or Discord/speech traffic in this canary. [Native evidence](discord-dispatcher-calls-native.md)|
|Spoken result|Authenticated GPT-Live session, GPT-Transcribe, isolated Realtime readout, independent transcription of returned audio. Offline eSpeak synthetic input. Inputs in English; no raw human audio. Both one-turn paths and repeated turns exercised.|An earlier GPT-Live publication renderer twice read “The lantern is ready” for “The orange lantern is ready”. This failed in personal and team mode. The current isolated `gpt-realtime-2.1-mini` readout scopes and checks its completed output transcript before release. Independent output transcription recovered the distinguishing marker in all 20 repeated trials. [Hosted probe](../../internal/voice/callspeech/lane_hosted_test.go)|
|Native worker custody|Grant and attempt fixtures, actual VM MCP registration and controller dispatch. Two real Codex workers from separate voice-origin turns returned their verified markers in reverse order in a 79.62-second disposable rehearsal. Manual steering, native draft refusal, uncertain write and retry were tested separately.|A voice-origin worker whose launch is proved can return to the matching turn. A manual shell launch without individual turn attribution stays in the written outbox. [Native evidence](discord-dispatcher-calls-native.md)|

## Measured hosted speech timings

Two persistent sessions, one per mode, each carried ten requests. Each synthetic question asked for the blue notebook. The fixture backend immediately supplied a verified response containing “orange lantern”. A separate transcription of the returned PCM checked the marker every time. Team mode first sent an unrelated utterance under another speaker ID. Results below are measured from the last submitted PCM frame; the end of that file can include silence. P90 uses nearest rank. These measurements exclude Discord networks, human devices and native dispatcher think time.

|Mode|Result fidelity|End of submitted audio to recognized turn, median / p90|End of submitted audio to first result PCM, median / p90|Continuous session wall time|
|---|---:|---:|---:|---:|
|Personal|10/10|0.904 s / 1.060 s|2.438 s / 2.776 s|72.90 s|
|Team|10/10|0.473 s / 0.542 s|1.874 s / 2.037 s|79.43 s|

The two sessions used about 152 seconds of wall time. At USD 0.05 per active minute this implies roughly USD 0.13 for the GPT-Live component of this run. Transcription and isolated Realtime audio are billed separately. The test did not query an account invoice. A real call with worker delegation also uses the native agent account. [Pricing](https://developers.openai.com/api/docs/pricing)

To reproduce the billed rehearsal, set `SWARM_TEST_HOSTED_DISPATCHER_CALL_DIR` to the private voice settings directory for the authorized test project. Set `SWARM_TEST_HOSTED_DISPATCHER_CALL_TURNS=10` for the repeated fixture, or omit it for one request per mode. Run `go test ./internal/voice/callspeech -run '^TestHostedDispatcherSpeech$' -count=1 -v -timeout 16m`. The three gzipped 24 kHz mono PCM inputs were generated locally with eSpeak NG en-us at speed 153 and SoX. Human input and provider output are never saved by this test.

## Local turn-taking trials

Replayed the committed eSpeak personal, team and chatter fixtures at 20 ms per packet against baseline `ea295bc8` and the revised speech lane. Each run exercised two personal requests, alternating team speakers, unaddressed chatter, simultaneous speakers and an already playing publication. A local provider stub identified known PCM signatures and returned the prescribed text. Three repetitions admitted all six expected turns per mode per revision, with zero team turns or interruptions for chatter and overlap.

| Median host interval, six turns per mode | Baseline | Revised |
|---|---:|---:|
| Personal first voiced frame to stop callback | 16 ms | 60 ms |
| Personal last voiced frame to admission / first result PCM | 858.5 / 858.5 ms | 859 / 859 ms |
| Team first voiced frame to addressed stop callback | 2,598 ms | 2,599 ms |
| Team last voiced frame to admission / first result PCM | 437.5 / 438 ms | 439 / 439 ms |

Team interruption follows the full addressed request and recognition. A verified result that has yet to start also waits for room quiet while recognition is pending. In three single-speaker and three overlapping-speaker trials, baseline playback began at or before the final injected frame. Revised playback began 420–421 ms after that frame in all six. Recognizing an earlier addressed request during the later speech preserved the hold.

For personal interruptions, three repetitions injected constant-amplitude pulses at 0 and 10 ms offsets into 20 ms packets while playback was active. Baseline canceled on all six 20 ms transients. A two-consecutive-packet candidate canceled on three, all at the split offset. The selected three-packet rule canceled on zero. All six 80 ms and all six 240 ms pulses canceled with the selected rule, 49–61 ms after simulated onset. A 40 ms pulse canceled only at the split offset (3/6). This timing filter detects sustained energy, including brief vocalizations. A separately synthesized “uh-huh” (local macOS Samantha voice, 726 ms PCM, prescribed transcript) stopped and admitted a personal turn in all three trials per revision. Median stop was 20 ms before and 60 ms after. Neither revision interrupted or admitted the unaddressed team backchannel. The timing filter cannot determine the speaker’s intent.

Reproduce with `SWARM_TEST_LOCAL_DISPATCHER_TIMING=1 go test ./internal/voice/callspeech -run 'TestLocalFixtureTurnCycle|TestPersonalInterruptionPulse|TestTeamPendingPublicationYields' -count=3 -v`. Admission and isolated result rendering use immediate local callbacks. These measurements exclude network, hosted speech, native manager processing and device playback. [Local fixture trials](../../internal/voice/callspeech/turn_cycle_test.go)

## Review and release decision

Astra and Sol independently reviewed the complete plan before product work. Luna handled config and local operator controls, Sol handled media, Gateway, native admission and speech, and Astra reviewed the combined implementation in successive rounds. The first review identified pre/post-cutover authority, optional intents, consent expiry, provider self-answer, delayed worker attribution, final transcript loss, team interruptions and DAVE recovery. The next review found actual VM tool registration, the personal utterance gap window, abrupt helper exit, a publication arriving mid-speech, and a privilege denial after an earlier successful connection. See the corresponding regression tests and fixes on the integration branch.

A real test guild and consenting human participants are still required for the private-room and two-speaker acceptance procedure, physical barge-in and echo, hosted Discord rekey, device latency distribution and 30-minute call soak. Cloud speech and synthetic Discord/native evidence cover separate boundaries. Production release remains held until those boundaries have also passed together. [Acceptance matrix](../specifications/discord-dispatcher-calls.md)


### First-version handoff

Nathan deferred live Discord and human testing until after implementation. Astra’s fourth review closed the four remaining round-three implementation findings, including revocation at first observed helper loss, incomplete RTP spanning two clocks, recovered DM order and unattended VM tool authorization. The subsequent two-worker hosted run passed. That review also confirmed exact-word fallback. No live bot, deployment or release tag was used. The [operator setup](../ops/discord.md#opt-in-to-dispatcher-calls) and [first-call acceptance procedure](../specifications/discord-dispatcher-calls.md#integrated-contracts-and-acceptance-procedure) cover the next trial.

## Repository gates

The first-trial candidate passed `go build ./...`, `go vet ./...` and repository-wide golangci-lint with zero issues. The final serial `go test -p 1 -timeout 45m ./...` exited successfully with 97 passing packages and 8 packages without tests. Race detection passed in all 10 changed Go packages, including the CLI, Discord, both native adapters, schema, native manager and speech packages. The tests used shared Go caches and canonical scratch roots.

An earlier parallel run hit startup deadlines in the CLI and daemon packages and stalled in a test-supervisor fixture. The two packages passed on serial retry, the test-supervisor package passed in isolation, and the complete serial rerun passed. The macOS arm64 trial CLI and bundled, credential-free Node helper also passed their local version and dependency smoke checks. Live Discord qualification remains deferred at the owner's request.
