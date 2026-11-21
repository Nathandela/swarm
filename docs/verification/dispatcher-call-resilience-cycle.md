# Dispatcher call resilience qualification

## Scope and release boundary

The input was the call branch at `ea295bc`. Upstream main and published v0.29.10 both resolved to `22f3090d` at reconciliation. Merge `fe2d7a60` retains the released dispatcher’s confirmed-delivery callback, origin and audience revalidation, and publication compaction alongside voice’s historical supervisor and call-generation grants. Three patches from the active-worker-wake branch were already patch-equivalent on main. Its separate rehearsal and investigation notes were not merged wholesale. The [plan and gate matrix](../specifications/dispatcher-call-resilience-cycle.md) identify the personal and shared windows required on candidate and published builds.

## Paired measurements

The local lane harness paired synthetic PCM onsets at packet boundaries and half-packet offsets. Active personal output stopped on 6/6 isolated 20 ms clicks at baseline, 3/6 with two consecutive voiced packets, and 0/6 with three. All 12 sustained intentional controls stopped in both baseline and selected candidate. Median local stop delay increased from 16 to 60 ms. This missed the original one-frame added-delay allowance. Team address recognition remained slower than personal energy detection. Solo and overlapping team turns now hold a queued readout until 420–421 ms after the final voiced frame. Already-playing audio continues through ordinary unaddressed room speech. See the [paired fixture results](discord-dispatcher-calls.md).

The separate read-only duration sweep used one synthetic English voice, two rates and two offsets. Longest consecutive voiced runs were 480–600 ms for “uh-huh”, 400–500 ms for “yes”, and 260–380 ms for “stop”. At a 400 ms gate all four “uh-huh” samples would interrupt and none of the four “stop” samples would. Raising the gate beyond 600 ms would reject all eight short intentional examples too. Personal backchannels can still interrupt. A confirmed tool question can legitimately need a short affirmative response.

A fresh paid baseline at ea295bc and the first integrated speech candidate at 484b9d30 each ran five personal and five team turns through the real speech project. Both scored 10/10 on independently transcribed result markers. The synthetic backend returned immediately. The clocks start at the last submitted input frame; this is neither native worker duration nor sound heard by a person. Runs were sequential, not randomized: provider and network variation prevent attribution of the timing difference to the code change.

|Route|Baseline admission median / max|Candidate admission median / max|Baseline first result PCM median / max|Candidate first result PCM median / max|
|---|---:|---:|---:|---:|
|Personal|1,260 / 2,100 ms|833 / 883 ms|2,710 / 4,020 ms|2,175 / 2,510 ms|
|Team|451 / 553 ms|451 / 483 ms|1,895 / 2,205 ms|1,825 / 1,989 ms|

## Degraded states

- A single zero PCM frame renewed a simulated 180-second idle age to 60 ms personal / 36 ms team. The repair counts delivered media separately from actual speech activity and gives the idle deadline its own cancellation path while native admission is blocked.
- Three race-instrumented fresh-generation fixture runs per route recovered the original logical discussion, rejected old playback and delivered distinct late worker text. The expanded reverse-order fixture admitted two new turns on the new generation, replayed an old receipt twice, spoke new results in order 2 then 1 and retained three distinct written replies.
- Real connector object recreation using a reopened persisted store revealed that completed personal DM repair failed to re-arm a previously eligible join. Re-authorization must follow positive repair evidence without bypassing pending owner input, a suspended call, changed room membership or expired consent. A separate red case reproduced a stale pre-expiry team consent reopening paid media. Retain both the genuine post-expiry request and stale-trigger controls.
- Native Codex with two disposable workers and no interruption returned their distinct markers in reverse order in 99.51 seconds. Cold connector runtime reopen preserved the first waiting shim and manager conversation, but the second worker’s execution host timed out; no verified final was produced. A real supervisor restart preserved endpoint and worker custody but reached uncertain input/custody and a paused scheduler. Those two native interruption routes remain unqualified. A safe hold does not establish recovery. [Native details and reproduction](dispatcher-call-native-restart-cycle.md)
- The mixed REST/Gateway receipt test passed across actual outbox compaction and store reopen. A changed replay conflicted and a distinct admission with identical text created an independent output. A successful spoken result alone establishes no Discord text receipt. Free-text paraphrases and genuine corrections require stronger evidence than string similarity to distinguish.

## Test and release handoff

Record the exact source and runner for each gate. Synthetic transport, hosted provider speech, real native account, actual guild audio and listener judgements are different observations. The public CLI archive is configured to carry the helper executable, codec module, npm manifest and lockfile beside the CLI. A release snapshot is not a published artifact.

The original author session attempted the requested native needs-input message and reply wait. Both reported a missing delegated-worker binding. The displayed correlation ID was `semantic:1795267184710:a674faff4a16e10a3d0558fde7ac7ad7`. An independently resolved namespaced roster ID allowed a read-only peek of this terminal. The live-window request was also delivered directly to Nathan. No production room or conversation was used.


## Resolving the pilot’s onset-budget miss

An intermediate design counted occupied 1 ms bins. A held-out offset showed that quantizing a partially occupied bin could still cost a second added packet. The final detector counts the exact above-threshold span within each bin. A threshold of more than 480 occupied samples at 24 kHz rejects a 20 ms pulse regardless of its alignment. These results cover a synthetic signal family, not arbitrary real noise or echo.

The Go race suite passed the full lane test and the held-out offset challenge. The independent Python reference exercised all 480 sample alignments: zero of 480 isolated 20 ms pulses stopped output, and 480/480 controls at 40, 80 and 240 ms stopped within one added frame. Two production-lane fixture observations stopped at 32 and 38 ms after first detected voice. For the six recorded synthetic English word/rate combinations at 20 offsets each, worst added delay was 20 ms for both “stop” and “yes” recordings; “uh-huh” could still interrupt. These local clocks exclude Discord transit, packetization at the client and speaker buffering. [Reproducible reference](../../scripts/verification/dispatcher_call_onset.py)

A slower team-path reversal was separately reproduced: an old addressed transcript cleared newer playback in 3/3 baseline scenarios. Interruption now checks the time the earlier speaker was actually voiced against the start of the active playback. The final lane suite includes positive and negative delayed-recognition controls. Full-utterance team addressing still took roughly 2.6 seconds from the first voiced frame of the fixture question to a hard stop. This is not evidence of meeting the physical 500 ms target for a team interruption.

A new status response identifies the **serving connector** and configured room independently of the status client's executable. The window script verifies the designated local profile before contacting its private socket, then matches the live application, guild, room, discussion, version, commit and PID. Its unit check passed. Collector output excludes transcripts and participant names. Record the launch PID and separately verify the signed archive; local status fields are not a human-heard receipt.
