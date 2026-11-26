# Rust M11 developmental qualification

These observations come from development builds on native macOS arm64 and
Linux x86_64. Release qualification must use the frozen candidate and
matching helper hashes.

## Native OpenCode and mixed workloads

The first matrix used Rust source `7202a48dd1eca0bb1c9f3ff422291ca7f871e74a`.
Both hosts used installed OpenCode 1.19.3 with isolated state and local
scripted inference. The fixtures checked denied IPv4 and IPv6 egress
before launching. The runners did not replace an installed service.

| Native host | Rust Swarm SHA-256 | OpenCode SHA-256 |
| --- | --- | --- |
| macOS arm64 | `fbc5fd0daee979eb91b388173a4a2ec60967d91572e9d6d01699c95e3833d3e0` | `6d3e6458fce4b649edf974c19959d5dbfe1e7f255a503dcfc01215b24ad3ea5b` |
| Linux x86_64 | `52c39df9b20aca208682656576be5bbc1656da1531e259f96bbc8fc5cc4c2401` | `179ca421d61eefeadfa0c50e084a6e79c1623734732f0f4593f81ccfd3ab7ee9` |

The mixed calibrations passed on both hosts. The scheduler claimed and
completed one real minute occurrence per host and released the workspace.
Each long-lived operator PTY received one echoed input and resize per
cycle, rejoined the same PID and birth identity after daemon crash, and
restored its lease. The private Discord receiver observed one distinct
publication per cycle after exact replay. Swarm and fixture processes
exited at cleanup.

| Host | Elapsed | OpenCode terminal receipts | Typed decisions | Daemon crash recoveries | Operator attach and Discord publication cycles |
| --- | ---: | ---: | ---: | ---: | ---: |
| macOS arm64 | 90.008 s | 23 | 10 | 5 | 23 |
| Linux x86_64 | 90.000 s | 17 | 8 | 4 | 17 |

The macOS native queue started 26 November at 00:43:29 UTC in
`/private/tmp/oc.ZCg3oy`. The separate mixed queues started at 00:55:50 UTC
on macOS in `/private/tmp/oc.w1HDRj` and 00:55:57 UTC on the Linux host in
`/dev/shm/oc.iSqUKous`. Both mixed queues have recorded successful real
minute completions, including deletion of the proved stopped scheduled
session while retaining schedule history. Read each queue's `status.json`,
its active stage's `heartbeat.json`, and its immutable `result.json` after
completion. The native queue sequences real 1h, 24h and 72h stages. Each
mixed queue runs a separate 24h stage.

The macOS OpenCode one-hour stage passed after 3,600.006 seconds with
341 terminal receipts, 138 decisions and 17 daemon crash recoveries.
Maximum dispatch was 2.162 seconds and maximum rejoin was 0.389 seconds.
Process cleanup passed. The result SHA-256 is
`b36a824fec606597a1f9175ff91410b5dd91a12f2d8dba67634838825010e0fe`.
The longer stages and final-candidate measurements remain open.

The first fixed Linux native rerun and the initial Linux mixed run later stopped when the shared test volume filled. Both Python runners reported `ENOSPC` while writing their results. Their last heartbeats recorded 81 and 30 inputs respectively, and both queue and stage process IDs were gone. The previous `running` status files had survived their failed replacements. Neither run counts toward an elapsed-duration gate. The queue now reserves space for its terminal report and checks the volume during each stage. A deterministic stage-registration fault killed the real test child and left the terminal status readable on both hosts. [Fault fixture](../../scripts/verification/opencode-rust/test_queue_failure.py)

Both replacement Linux queues started from source `cb630ec5b1131e4b4ae6d706a1132120e03a7dbd` using the exact signed Linux archive executable with SHA-256 `419a3fc6a369d8336f02ee5c20fce348a3c4aeca82efb5acd070e66c3c5a9579`. The Linux one-hour native stage completed 3,600.000 seconds with 343 terminal receipts, 138 decisions, 17 daemon kills and recoveries, and 414 loopback model calls. All fixture processes cleared at shutdown. Maximum dispatch was 0.463 seconds and maximum rejoin was 0.349 seconds. The immutable result SHA-256 is `c781d8e3cdcb6899b3a714ec7b7eda13baf753cc44e14d75e4a1807718b97ae1`. The native queue then entered its 24-hour stage. The separate mixed workload began successfully and keeps its own elapsed clock.

## Linux interruption and controlled recovery

The first Linux native queue stopped on input 75 after about 13 minutes,
with 74 terminal receipts, 30 typed decisions, three SIGKILL recoveries,
and successful process cleanup. Its raw result SHA-256 is
`72f6c8a8e3cc5749895b4552a1ddeefaf25f93e82e03ba26946941e520b24b5f`.
The compressed timing probe and other work added concurrent host load.
That overlap makes the original transport errno unobservable from the
generic bridge error.

The failed operation has a scoped daemon intent. The shim's last
dispatched operation is still input 74, there is no receipt file for 75,
the read-only OpenCode database has zero parts with marker 75, and no
model turn followed input 74. The only bridge call on that fresh prompt
before the receipt write is the read-only dispatch digest. The mutating
POST requires a durable uncertain receipt first. The evidence localizes
the failure to the preflight, with no native send or native rejection.

A separate fault trial on the original Linux binary paused the
birth-proved TUI before preflight. Its first attempt failed at 3.192 s
with the same generic transport error. The status read reported
`unknown_operation`. A changed-body retry was refused. After the TUI
resumed, an explicit exact retry produced one terminal receipt, one
SQLite input row and one primary model request.

Rust source `ab99f53e55864555e594eb88a8d6e1358a4ba47a` distinguishes
transport timeouts and retries the read-only prompt preflight once.
On the fixed Linux build the same pause recovered at 4.517 s. A longer
pause exhausted both deadlines at 6.293 s and preserved the no-effect
state until an explicit exact replay. A separate fault on macOS exposed
a stopped-child lifecycle bug. The native kernel probe returned
`CLD_STOPPED` with `WEXITED | WNOHANG | WNOWAIT`. The previous shim
treated any nonzero `si_pid` as exit. The shared Rust wait check now
accepts only terminal exit codes. The optimized macOS binary from
`7c61e6f25b46361dde1e6a7addac94d544dfbcea` passed the installed
OpenCode fault trials at 4.460 s for one automatic read-only retry and
6.046 s for bounded exhaustion. The original TUI survived, and both
trials ended with exactly one correlated native input and receipt.
The macOS executable SHA-256 is
`ac9e3d74418956f0d076ff59d40f7064a02ba27a5f1449cf3841a2054974dac0`.

The full binary unit suite passed 103 tests with three pre-existing
external fixture tests ignored. Workspace Clippy passed with warnings
denied, both optimized target builds succeeded, and all 162 pinned
OpenCode bridge tests passed.

The replacement Linux native queue started 26 November at 01:04:10 UTC
in `/dev/shm/oc.tgFQ9wcB`. It uses the fixed source above and executable
SHA-256 `3d24c9d95d8409cdfecba71bd0feea2e10407fe87dfb441e2fe7c106005b5f62`.
The first stage is active. The stopped original queue and its evidence
remain in `/dev/shm/oc.8d7sRJtY`.

## Comparative measurements under development load

Each arm captured 25 cold dashboard paints at 50 real sessions, 1,000
post-warmup PTY keystrokes, and 60 seconds of daemon-plus-three-shim idle
CPU. Both arms were reaped and their scratch sockets removed. The external
first-paint clock includes the dashboard process launch. The older Go
in-process gate starts after handshake. These are descriptive comparisons
with concurrent development work. Quiet-host measurements against the
final candidate remain open.

| Host | Go and Rust cold first paint p95 | Go and Rust PTY echo p95 | Go and Rust idle CPU as percent of one core |
| --- | ---: | ---: | ---: |
| macOS arm64 | 124.932 and 76.308 ms | 0.193 and 0.109 ms | 1.691% and 0.234% |
| Linux x86_64 | 115.770 and 82.570 ms | 0.440 and 0.282 ms | 2.157% and 0.399% |

The Go source pin was `f26648d69086e329132ac7849954d2f29a895387`.
The macOS reference was a local optimized arm64 build with SHA-256
`5f92beebd77f4d49a45ca7989a9d88d42bc3192b623356661217f102366fe179`.
The Linux reference was the v0.29.22 x86_64 archive executable with
SHA-256 `5808d838158261470fb68cc97c12d041c4436a77a48746cd00ae75ffbec7f05d`.

The raw samples, p50/p95/p99, tail observations, attach-cycle resource
counts, and calibration results are in the local evidence directory
`/private/tmp/swarm-m11-7202-evidence`.

The driver now records explicit commit hashes for both arms. A native macOS calibration paired signed Go v0.29.24 with signed Rust `1ceed43c`, exercised real PTYs in both arms and reported zero survivors. The receipt SHA-256 is `0fd7f8332447c4e8cb01d13d4fbd9cc6bbcea26e5d8914c29eb73912b204a5da`. It used the short smoke sample during development load.

The full loaded macOS arm64 calibration paired signed Go v0.29.24 and signed Rust `52e77ee5`. Each executed 25 alternating first-paint trials at 50 sessions, 1,000 real PTY echo samples, 100 attach cycles and a 60-second idle sample. Other workloads were running. The one-minute load average rose from 5.79 to 8.26. Both arms exceeded the 100 ms first-paint budget. The reaper found zero survivors for either arm. Quiet-host qualification remains open. Raw receipt SHA-256 is `a47755253405e5a0ced0099fa83efe62f322c506e4cc1607c92e94fbce3bc979`.

| Native Mac metric | Signed Go v0.29.24 | Signed Rust `52e77ee5` |
| --- | ---: | ---: |
| First paint p95 at 50 sessions | 169.691 ms | 125.538 ms |
| PTY echo p95 | 0.091 ms | 0.121 ms |
| Idle CPU as a percent of one core | 1.596% | 0.264% |
| Daemon plus three shims at idle sample end | 76.578 MiB RSS | 21.109 MiB RSS |

The pinned Go v0.29.24 in-process N-1 test separately measured 2.175 ms p95 over 25 trials with 50 real sessions on native macOS arm64. Its timer starts after the socket connection and spans subscription, roster loading and first rendering. The external calibration's interval starts before the PTY launcher and ends after all 50 labels reach the terminal. The Go log SHA-256 is `e25a5896f13bf76d019cbf5154b5473474adf19eca420e3028b7608655dfb4aa`. [Go timing contract](https://github.com/Nathandela/swarm/blob/v0.29.24/internal/tui/firstpaint_gate_test.go)

The matching Rust post-dial test uses the real owner, subscription, board initialization and first rendered frame. Its final `16f256c4` release-mode harness rendered all 50 labels in each of 25 fresh trials on both native hosts and left zero fixture processes. It measured 1.110 ms p95 on macOS arm64 and 5.428 ms on Linux x86_64 under concurrent load. Receipts SHA-256 are `b37c47dba79333813967b1a0ca2dddc5835cff75ec0e36c35213d402a78de2c4` on macOS and `2d0a276b98cc232355d84774f17dc3f420d6e8c071a4f42d115c8ad5f6e13a4f` on Linux. The external PTY gate retains its original 100 ms budget. [Benchmark commands](../../scripts/verification/parity/README.md)

| Evidence | SHA-256 |
| --- | --- |
| `performance-macos.json` | `c75ab9a2731e2075a88b5bd4221c3f1563b6bb487a1f6a65121d176d9aa3f9e5` |
| `performance-linux.json` | `e3d4cd363cd37e311a4782c6aeba0f2f670cf512d1d98887503aecaa996c7eef` |
| `mixed-calibration-macos.json` | `0e5ed26ea0aba26764cb556caa1c933623cc3b892189eb79a0224c2cb0aac71d` |
| `mixed-calibration-linux.json` | `a81d170abb80a73a0f191d33e177f25dc6cf62e465239a36cf1887791ebbd4ac` |

[Native fixture and queue commands](../../scripts/verification/opencode-rust/README.md) ·
[Performance commands](../../scripts/verification/parity/README.md)


## Long-run failures observed on November 26

The one-hour receipts above remain valid. Three longer development runs stopped before their 24-hour target. All three reapers reported successful cleanup. The original raw reports remain with their run roots. The digests below identify the terminal reports.

| Host and workload | Development source | Elapsed seconds | Completed native inputs | Daemon restarts | Terminal report SHA-256 |
|---|---|---:|---:|---:|---|
| Darwin arm64 native OpenCode | `7202a48d` | 15,518.45 | 255 | 12 | `16eddcf70479119775522cbe2af00aa7f6eb065bd2159c40e242af352e4a5888` |
| Darwin arm64 mixed | `7202a48d` | 17,915.24 | 389 | 38 | `532ccd8066aee01cf48abf8afbcfbc1b0ea1a56c01d433285371601b33d2310b` |
| Linux x86_64 mixed | `cb630ec5` | 13,133.01 | 286 | 28 | `d8cab0ae974bf916ea26c58e116b5782fce9aab4f79164cb4fadc15da2ae5e82` |

The native Mac run stopped waiting for the durable result of input 256. The provider had already completed that turn, including both permission and question steps. Its worker receipt still read `native_accepted`. In the Linux mixed run the 30-second durable wait expired on input 287. A read-only query against the stopped test database found exactly one matching native user row and one terminal assistant child with `finish=stop`. The model saw one request. The first explicit status query reconciled the missing completion from the native snapshot. These runs expose automatic receipt reconciliation when a completion event is lost.

The Mac mixed run stopped with a different outcome. Its 299th scheduled occurrence was exhausted after 298 earlier completions. The original schedule database recorded only a rejected Codex attempt, with no worker session or prompt delivery. Its detail was `provider command timed out` during pre-reservation discovery. The evidence distinguishes this from a verified native quota or a failed model turn. A new timed run is needed after the independent receipt and discovery repairs.

## Signed November 26 replacement runs

The corrected source `41f9aedd2bd12f22314177725b01106e65c40a2c` completed the native one-hour workload on Darwin arm64 and Linux x86_64. OpenCode 1.19.3 used the local loopback model. The Darwin executable SHA-256 was `308f0e740e2c413ffb457e9e41f051617848168d92c3a99fe4a9a95a57c26129`. The Linux executable SHA-256 was `10b7fd14e41cac33120a2544ea98efb8d42a4384575d3ef426c0f1cbdd36981c`.

| Host | Native receipts | Daemon restarts | Cleanup |
| --- | ---: | ---: | --- |
| Darwin arm64 | 328 | 16 | Passed |
| Linux x86_64 | 327 | 16 | Passed |

The Darwin run lasted 3,600.001 seconds, confirmed 132 decision cards and made 396 local model calls. The result SHA-256 was `263b3dcfc00dd8be2ded75b6158551f1ca1a3607b1eeb79fc6e4552cf8ffd1c0`. Its queue exited before registering a 24-hour stage. A manual restart with an inherited `PATH` found OpenCode 1.18.15. The version check rejected that process before workload execution. The Linux mixed run received a stop signal after 129 completed inputs and 99 scheduled completions, then reported successful cleanup. The independent older Linux native run at `cb630ec5` timed out waiting for input 310 and reported successful cleanup. These are separate outcomes. The 24- and 72-hour gates remain open.

Two subsequent Linux 24-hour runs on the same signed image stopped after an unconfirmed admission. The native workload found no row for the exact input ID and no model request. The mixed workload found one user row and one matching text part for its exact input ID, with no assistant child or model request. Both had already saved Swarm's dispatch intent and both reported cleanup. The observed network and SQLite evidence does not establish when either call lost progress. The Darwin native and mixed workloads continued through this checkpoint.

One Linux retry timed out in the installed OpenCode version check after 15 seconds and never started the workload. A second retry ran a byte-identical copy of that executable from private tmpfs and passed version verification. Its reserved session failed to bind. A read-only query found zero native sessions, messages, or parts. No model call or initial admission receipt was recorded, and cleanup passed. These are startup failures with separate receipts. The new readiness diagnostic records the last correlated state or the absence of a correlated response while retaining the existing poll budget.

A separate diagnostic run used `83ab0e25` and the same native OpenCode 1.19.3 bytes. It completed 165 seconds on Linux x86_64, with 14 verified input receipts, six decisions, 19 loopback model calls and successful cleanup. Maximum dispatch was 0.521 seconds. The diagnostic Swarm executable SHA-256 was `6690dc68952b21cd2e69018f27b21e36ffdcce4db7efac3e9bd832d810c6f3a2` and its result SHA-256 was `573c833ae900637ab4e639235c4305f95a9fd39046d92da4e9f244dbd55ec75c`. Longer qualification stays open. [Deterministic admission regressions](opencode-admission-boundary.md)

The signed development build from `75546e1d` passed archive verification on Darwin universal, Linux x86_64 and Linux arm64. The Darwin arm64 managed install in private scratch passed its packaged media check. The Linux native 24-hour workload used Swarm SHA-256 `2ec651ca00cd852e675ecd7f3a99a52aab1c066ab05723d0845598dd88254813` and a verified copy of the installed OpenCode 1.19.3 executable. The sixth operation stopped in `semantic_target` with a private socket EAGAIN at 2.013 seconds. The exact operation status was `unknown_operation`. Its count was zero native text parts and zero primary model requests. Five earlier inputs completed, and cleanup passed. The terminal result SHA-256 was `9504c3700aaa4f0195983b0341ce66722da641eb3b9d35699a3984ca62f40c75`.

The process-pause witness reproduced that pre-dispatch failure with `75546e1d` on both native hosts. The correction in `34e45433` retries one read-only handshake over a fresh socket after a timeout. It rechecks the same shim PID, kernel birth and token before either native command path proceeds. Pausing that exact shim through two handshake deadlines returned an unknown operation with zero native input and zero model requests. A separate explicit recovery used the same operation ID and completed with one native input and one primary model request. Automatic recovery within the second handshake completed with the same counts and a durable terminal receipt.

| Native host | Original refusal | Automatic recovery | Exhausted refusal | Original log SHA-256 | Corrected log SHA-256 |
| --- | ---: | ---: | ---: | --- | --- |
| Darwin arm64 | 2.017 s | 3.762 s | 4.012 s | `938caa753f3b7c8fe12cf946a037b58c66f76796a60968010fc191a1889a9adf` | `becf0a017a36a7c8222818c80486e9180acc77e94f87e53347956a0892bfce45` |
| Linux x86_64 | 2.026 s | 3.490 s | 4.033 s | `d3a18f866373c746b82556157e2a4dc6911c68d9a5aefa778aea1abff0685462` | `fc2d7455083fc907e58fcfaa42d8c762ae101c789e0b07c754b50d5654f778b8` |

The Linux trial of signed `ded21c92` handled ten native inputs and four decisions before a signal stopped the workload at 559.276 seconds. Cleanup passed. The signal source is unknown. The result SHA-256 is `582ba16f0dc6eb86590369d5c517b3b2bc557d416c89efed6ee5ca2486e92400`. The next trial under a transient user service stopped on the first new input with an exact `native_busy` refusal. The native database contained zero matching input parts and the loopback model received zero calls for that input. The result SHA-256 is `aae68a3854412889f00d5ac4bd66531913cc5f84fe4053859280f3f7ea8abbb8`.

The soak driver now verifies the terminal assistant child of the exact native opening message before admitting its first workload input. It also checks that the roster is idle. The initial launch receipt alone proves admission, and the roster can briefly report idle before that turn reaches completion. This change leaves later admission checks and deadlines in force. With the same signed executable and a three-second fixture delay in the opening model response, the 90-second phase completed 20 inputs, eight decisions and five daemon replacements on both native hosts. Each passed cleanup. Darwin used the first version of the barrier and returned SHA-256 `83e00c025772897bb51268eec245bdb82639f8708f15f13bb4deeea131325a6b`. Linux included polling for a delayed user row and returned SHA-256 `993a8f4d9a1fbe8bfc2debcef41d5e99fa7704444ac08424cf23d176bfb36b08`.

The next Linux native trial at the same signed source completed 23 inputs and one daemon replacement. Input 24 began approximately 1,417 seconds into the stage. Both read-only shim handshakes expired after 4.050 seconds. The status was `unknown_operation`, with zero matching native parts and zero primary model requests. The normal `swarm kill` command also timed out. The result keeps cleanup failed, while the independent transient unit and its control group were confirmed gone after namespace teardown. The terminal result SHA-256 is `8059bab185c19adbb274efc0613389623190322373df422a17f5c9ac027e9869`. Host pressure rose sharply in a later sample. The timing of that sample leaves the cause of the earlier stall open. Linux soak reports now capture pressure during samples and at the time of failure.

The separate Linux mixed run at `ded21c92` stopped on input 42 with the same pre-effect two-handshake timeout. It had completed 41 native inputs, 41 operator attaches, 41 synthetic Discord publications, 32 schedule ticks and four daemon replacements. Native input 42 had zero matching parts and model requests. Normal cleanup also timed out. Its terminal result SHA-256 is `a2b7f072896d811c176f820289239b3e6d7f02d9713f01d21d885d56e99059e9`. A controlled pause of the birth-verified shim in a separate Linux namespace reproduced both timeouts and collected six thread samples, including kernel stopped states. That fault witness recorded zero native effects and has SHA-256 `9c7957949a58b733a223d74fb568bf7bce0542e7f6bbe8bbe1e054def71b9354`. The forced pause verifies the new measurement path. The cause of the earlier stalls remains open.

The native Linux replacement used signed `3d15e157`, executable SHA-256 `14adab5617c309de5b96a0c4c3907f0f0840151a726017e4339baacd2613739e`, and the same OpenCode 1.19.3 bytes. It recorded 11 native input receipts and received `SIGTERM` at 674.185 seconds. All six sampled shim threads were sleeping and recorded zero major faults. The stage cleaned up, the user unit was dead, and its control group was gone. The exact journal window shows the user manager recording the exit and CPU usage after cleanup. The source of the signal remains unknown. The terminal result SHA-256 is `01dfd0274c2137b82053a56b6107f79eab0e47b9aff75afd9da20020b8cc3270`. The queue did not reach its 24-hour target or start its conditional 72-hour stage.

A diagnostic queue on that same signed build captured only `TERM`, `INT` and `HUP` from the outer user service and its private stage. A disposable control recorded the sender PID in the same PID namespace. A signal sent directly across the namespace boundary reported `si_pid=0`. The first three inputs each matched one durable terminal receipt, one exact native user row and one linked terminal assistant child. Launch receipt SHA-256 is `01f726e74d66d4715471ef0883a40e636780db35dfa41509e966886d71de51e4`. Independent first-three receipt SHA-256 is `011758ae129c3aaf17700a6e0ee6488d4e3607ae3311797dd59276f266c4060b`.

That stage later received `SIGTERM` between inputs at 2,777.736 seconds, after 45 completed inputs and two daemon replacements. Its first signal was `SI_USER` with `si_pid=0` and `si_uid=65534`, the host's overflow UID. The later signals came from the in-namespace cleanup. Systemd recorded the exit and CPU use. The exact sender of the first signal remains unresolved. Cleanup passed. Terminal result SHA-256 is `eaddadce2faf4d13df951e2fd534033e31f03b599c2edbfc86a7f7fbfeb41ab9`. The scoped trace SHA-256 is `d5578cd40dfd44385115c2383a2a13bd9c9456b497106e2196ccda2d050b3fbb`.

A separate Linux mixed stage using signed `3d15e157` stopped after 22.9 seconds when exact native question readback returned HTTP 422. The first input already had one exact native user row, one text part, one linked assistant and one model request. The native question tool had started and was aborted during cleanup. The run produced zero completed native or mixed receipts. The isolated unit and fixture processes cleared. At failure, 10-second full pressure was 29.14% for memory and 29.86% for I/O. The result SHA-256 is `76a84f75015b42b08f9f7cbb53bb60fdb24bcd08e4c131295701c4b1b794df96`.

A deterministic bridge control returned HTTP 200 for a vanished question, HTTP 422 `native_shape` for a malformed binding, HTTP 422 `native_read_failed` for a failed SDK history read, and HTTP 422 `native_deadline` for a blocked SDK read after four seconds. A later read in the timeout control returned the original question with zero mutations. The signed workload discarded the rejection reason, so its exact cause remains open. No input was replayed.

`52e77ee5` retains a static reason from a read-only snapshot's HTTP 422 response after verifying the full expected bridge and native identity. The body limit is 8 KiB. Oversized, non-JSON, mismatched and unrecognized responses keep the generic error. Localhost tests exercise those controls. The exact embedded bridge fixture reproduced `native_deadline` in 4.005 seconds and recovered the same question on a later read. It made no mutations. Admission, replay and time budgets stay separate from this diagnostic.

The signed Linux `52e77ee5` mixed replacement verified its first three completed inputs independently in the native database. Each receipt matched one exact user row, text part and terminal assistant child. At the checkpoint it had five inputs, five attaches, five synthetic Discord readbacks and three scheduled completions. Memory and I/O full pressure at launch was 0.06% and 3.30%, respectively. Launch evidence SHA-256 is `34f54a163ab2e3c2ad29dfb72c069c693ea4f07cb14dbdbff2a555ec0079b4db` and the checkpoint receipt SHA-256 is `175347921900d1f8ccd7d685094815b5fee3f0cc1f39ed4ec94a48f5b8f5c5ae`.

A separate untraced native Linux queue started a 24-hour stage and a conditional 72-hour stage on the same exact signed `52e77ee5` release. At its first-three checkpoint, each durable receipt matched exactly one native user row, one text part with the expected hash and a linked terminal assistant child. Memory full pressure was 0.00% and I/O full pressure was 1.36%. Launch evidence SHA-256 is `5ee8004e91f469ce03629198f92802387aeb734b85be6376de6a6c14947e43f8` and the first-three witness SHA-256 is `306464457ea526d0852e7a9b414000ab32495bca1b1d39a71928c4dec00f3d3a`.

The matching signed `52e77ee5` mixed workload started its own 24-hour stage on native macOS arm64. It uses the exact OpenCode 1.19.3 executable and the frozen synthetic Claude and Codex fixtures. A read-only native database check matched the first three durable receipts to their exact user rows, text hashes and terminal assistant children. The checkpoint also observed three scheduled completions. Evidence SHA-256 is `83b53d879bb5ab77090ad613d9fc218166327832a7e314d27a51c7d2ebad5f4a`. The Mac mixed duration remains in progress.

Both signed Linux stages ended within 14 seconds of each other during a shared pressure spike. The native stage ended at 1,720.733 seconds after 28 completed inputs. Two read-only handshakes for input 29 expired in 4.030 seconds, with zero matching native parts and model requests. The mixed stage ended at 3,077.458 seconds after 66 completed inputs, 66 attaches and synthetic Discord readbacks, and 50 scheduled completions. Input 67 used its original operation and recorded a bridge timeout. The independent database query found one exact native user row, one matching part and one assistant child. The status probe also timed out, with zero primary model requests observed at that checkpoint. No input was replayed.

At the two failures, 10-second full memory pressure was 52.20% and 56.88% and full I/O pressure was 60.96% and 63.87%. The timing does not establish a root cause. Both fixtures retained failed normal cleanup in their reports. The two user units later showed inactive, dead and empty control groups. Terminal SHA-256 is `fefb572e227a03d74888f08a30ef465062881a5ed37e4d340782fe0066e1f90c` for native and `123bcbf046727c17b216a986b74ed69c7cc191b37257db32f39291ab56fad405` for mixed. The paired collection SHA-256 is `9f988e1ee16ae32170b2751c28e96a2fee32bf23c202bcbdcb97d9b4c9713f9b`. The Linux 24- and 72-hour gates remain open.

An offline read of the stopped native databases verified that mixed input 67's exact user row, matching text part and linked assistant appeared 61, 69 and 93 milliseconds after the recorded intent. The assistant had no completion timestamp, finish reason or error. The final durable result remained `outcome_unknown` with `bridge_timeout`, and the model request count was zero. Native input 29 had no durable intent. The independent readback SHA-256 is `a2d403c1b4b305430fd36e5eb8025223b48ee30886fdbbd15b9592453f62ac71`.

A single replacement Linux native queue started with the same signed `52e77ee5` product and the independently identified fixture revision `5c6b7af2`. That fixture recognizes the CLI's nonzero ambiguous response only after matching the exact operation, session and input hash in its durable record. It uses a read-only status request and receipt observation within the original dispatch budget and skips the duplicate-submission control for that ambiguous input. Running one queue avoids overlapping the replacement with a second mixed workload. At launch, full memory pressure was 0.00% and full I/O pressure was 1.30–1.56%. An independent native database read matched all three of its first completed receipts to the exact user row, text hash and terminal assistant child. Launch manifest SHA-256 is `f9f33d8265a4f8a00edf108d53057a3960cdc7f7546e10d5616a1a3dc7a27487` and the three-receipt proof SHA-256 is `4af3b74a40422957a0bdfacdffd2e6f99a9463499a234c0d0a2417c6c2b0e31f`. The Linux replacement and four Mac queues were still running at this checkpoint.

Rust now persists exact native admission as soon as the authenticated `/message` read proves the user row and input. Terminal history is checked on a later observation, with the existing requirements for the same input and qualifying assistant outcome. The private socket regression withholds the first snapshot and verifies that status makes only the message read, saves `native_accepted` without terminal proof, and later observes the exact completed reply. The full 252 default tests passed on macOS arm64 and Linux x86_64, with 22 opt-ins outside that run. Log SHA-256 is `dcd3f2ede21637064450f121478819958344ae84012ba7b0afe7830938345fd4` on macOS and `6584cded7c23790f6e9cbc0f2fdcd61ff52cebd7022ddf2531c7e39745ef0ae9` on Linux. The installed macOS OpenCode 1.19.3 loopback also verified matching history, decisions, restart, exact retry and resume. The running duration stages keep their frozen binaries and fixture hashes.

The combined OpenCode, timer probe and retained guild source `797dad1b` passed 253 default tests with 22 opt-in cases on each native host. The nine exact Go ledger opt-ins also passed on both. The OpenCode installed macOS receipt SHA-256 is `2a7d6b59a32acc13b29ccdef643ca755f3e0e55bcf89aa36883d81c30ccb6993`. The integrated workspace log SHA-256 is `704d6c0d411a8ddb3533c3c58627b332e9f323b5e00edffa670a8481ab0e91de` on macOS and `10803f1d9ddbfa638d65f2fd2a8852cfa55a8c029f27955aa7dcb4e5eede9510` on Linux.

Integrated source `6ea5ddfe` adds the timer confirmation, saved job detail, canonical retained guild preparation and template catalog. Both native hosts passed 258 default tests with 22 opt-in cases. The nine pinned Go ledger tests also passed on both and the five signed-reference guild paths passed on Linux. The workspace log SHA-256 is `c67cb93ca2fb7516b49241cde987d85c1c140ce439af7bace41fc3f2e059dd44` on macOS and `2646b5f286c483184ec29863bac533621fb6986b31779c4a369b8a99c873ffb0` on Linux. Linux signed-guild witness SHA-256 is `4fa7ddbab17512e0748236009ecd637530f21d0b1f5b2c1ea54645f787aabca0`.

Integrated source `1a818e2c` passed 261 default tests with 22 opt-in cases on both native hosts. It includes signed-source Go v0.29.25 opener-derived Discord thread names and recognition of an exact settled Go gap receipt. The authenticated loopback suite verified that a withdrawn opener or permission after the initial channel readback leaves zero POSTs. A definitive HTTP 400 can use the original default title only after a fresh exact source and audience check. Malformed durable fallback coordinates remain untouched. Workspace log SHA-256 is `215cf2b0365e3fb492ab72cc0fc26d0116b7caa925bc339fd44119c877015d48` on macOS and `dc801bdb767333cf23f9c4911e083caed46b40758717f1653c412bef20ff678b` on Linux. Strict Clippy passed.

Integrated source `f4999691` passed 264 default tests and builds on both native hosts, with 22 opt-in cases and strict Clippy. It adds the personal template dashboard and exact creation retry protection. The retained controller custody keeps Go's original lock, credential and socket; Go reacquires the lock even while a duplicate descriptor is open. The isolated signed Go v0.29.25 and Codex witness performed one Go-owned model request with the original operation after Rust stopped. Workspace log SHA-256 is `13eadb6d8a6518c88b11cd8d7a25b44f3c01b476662cf561af65a7b5e93030ce` on macOS and `689eea112d334ee15f646dc901ee33f25ed412dd6c73f3ee1b43456fdb62ddaa` on Linux. The controller witness log SHA-256 is `efcfd0193884c7089c706b3c03e4dd895cda325fe3103d6a706b51ab3c50d03d`.

Integrated source `e43f114f` adds the ordinary guild restatement notice and holds repair across lost read authority. Both native hosts passed 266 default tests with 22 opt-in cases. Workspace log SHA-256 is `d334cc9994c45aa1cbbdb0d999cba1b8e8c9fa2c8b37949dd58af3cd4f8204c7` on macOS and `1ad75e41f01bfd440a5892816284d5088585403d8ca2419d1ed6adeed47df8a7` on Linux. At the latest duration check, the signed sequential Linux fixture had 86 completed inputs and four rejoins after 5,242 seconds. The oldest Mac mixed fixture had 763 completed inputs and 601 scheduled completions after 36,086 seconds. Both were still in their 24-hour stages.

Integrated source `e30e6dd0` adds the complete template snapshot stream. Both native hosts passed 268 default tests with 22 opt-in cases. macOS also passed the two Go mailbox opt-ins, the pinned Node 24.17 media permission test, the local TLS classification test and the transformed OpenCode admission cases. The JavaScript suite passed 162 cases and delegates three transformed-bridge cases to the Rust opt-in. Workspace log SHA-256 is `434725e2727ab2b7bbe31d661a8c0b57b2d63910566cc0c3eebfd0efda2e1106` on macOS and `992a0b9399ada81914fc05a2cf09227caff45846dc7777bd177cae869a9fa667` on Linux. The macOS offline opt-in log SHA-256 is `2490c97f253c032e4a1bc64463e94dfe34fd1eec3bf8644179f7ca8c657cdc53`.

Additional `e30e6dd0` opt-ins passed on both native hosts. The nine original Go Discord ledger cases used the exact v0.29.25 Go source with the fixture overlay and native architecture on each host. Native Linux also passed both Go mailbox cases, the pinned Node helper, TLS loss classification, transformed OpenCode admission, and the older disposable signed archive check. The first Linux helper setup put its package under a group-writable directory, and the production path validation refused it. After staging the package with private modes, the helper passed. The TLS, OpenCode and archive cases ran in the isolated loopback namespace. The Linux logs SHA-256 are `80de9699d1b1d56d2f851ff8302a77b0700e2c44cc00fb2552d7dcec7385a2b6` for the initial ledger and mailbox checks and `be3f3071ec6d44bee65c2be614ca997264128ead95af74e32b8ad6c300332111` for the four passing reruns. The native Mac ledger log SHA-256 is `fa63cc70b61221c3326b1a57e11c481ac2cc8048fca6b735fd45ae8cf35b1442`.

The Mac `e30e6dd0` opt-ins passed the owner protocol with both signed Go v0.29.25 and Rust and the arm64 fake Codex built from the pinned Go source. Installed native Claude 2.1.329 passed the worker and enrolled manager loops. The scheduled Claude rerun observed one native composer turn, a scoped completion tool and its final response, exact replay, and no Enter into a newly raised trust dialog. SHA-256 of the owner, worker and manager, and scheduled logs are respectively `8e2de755f48d31d9d967819df71d9c5a2a753a14585c98184d0cbf82971b2307`, `b17b061d7e0ebe2c2c1e2248e35ab68e151d2f6c7c5cec44adf38c6f4fe3f4ff51` and `e6f2217e29dbaa6d2274ee77ac35eeb5ef7262d8c6dd73f56f6a731e17de6af6`.

Integrated `f9dd8ca4` and `a01ffc53` each passed all 268 default tests on macOS arm64 and Linux x86_64. Formatting and warnings-denied Clippy passed. The first revision adds one read-only history page for an exact retained Go guild native call, with a new proof immediately before response disclosure. The second adds the bounded original Go VM reply check and its separately enabled reference fixture, bringing the opt-in count to 23. The `a01ffc53` default log SHA-256 is `0fb0e5c4b23ff148409693ac5f0923752603cafc2c5261d7a88938a0e4655982` on macOS and `70e4cfdbab4b1a946488a82daa4fcfdaf43128b0b6e1373e0077d18e83d3330d` on Linux. [Retained Go boundaries and witnesses](rust-discord-retained-state.md)

The ten opt-in ledger checks passed using the integrated Rust binary and fixtures rebuilt from the pinned Go source on both native hosts. The separate Linux signed Go v0.29.25 and restricted Codex witness passed all five history cases with both original databases byte-identical and zero Discord POSTs. One earlier run refused before reaching a delayed-edit barrier. The fixture now verifies the exact signed Go roster and idle native manager before each timed admission case. The next complete run reached every intended permission, stop, edit and closed-call boundary. Ledger log SHA-256 is `bf8b1d983e7bdb6dce8f63894239910b4f345e600704fe28763076736a7016ee` on macOS and `fc8f54299e86b98f34a1fad4b60c33fd9e5928a4fd4ef2dfff8ba10d908a2f4b` on Linux, including the first run's later history refusal. The passing five-case Linux history log SHA-256 is `efe426a3885bb0047fd60b05066bc7f25855d20da7786a393dc4144b2cfe0877`.

The replacement signed `52e77ee5` Linux native duration stage stopped on SIGTERM after 6,933.845 seconds, 114 exact native completions and five daemon rejoins. Cleanup passed. The harness observed 69.95% recent I/O stall and 11.37% recent memory stall. The signed Rust daemon and shim stayed below 8 MiB RSS each in this run, and the separate provider peaked at 1,031,659,520 bytes. The report identifies the signal and gives no sender attribution. It records a failed 24-hour gate. Private receipt SHA-256 is `3381d407585ac19925ca8be9da9c57b190fc1e777747f40597eb4b22ae76f2fd`.

Integrated source `aec3365e` passed 269 default tests with 25 opt-in cases on each native host. Formatting and warnings-denied Clippy passed. Both opt-in Go v0.29.26 file comparisons passed after integration. The retained attachment-only case carried the exact authorized bytes and nonce in one loopback POST, and Go reopened the acknowledged receipt. The second case compared worker capture and refusal against the tagged Go source. Both native hosts also repeated the ready-timer active and new Automations saves against the exact signed Go v0.29.26 executables at 80×24 and 40×8.

| Integrated `aec3365e` log SHA-256 | macOS arm64 | Linux x86_64 |
| --- | --- | --- |
| Default workspace tests | `3dc7e555b1990ad59119a89cb07b5a660d5a6e3ef4c6362bf255876fd115936c` | `26d61fa16247e7897db1bb8521aa33b68a0f2c2ceb2b0ab2c6d0a50453afa427` |
| Go v0.29.26 attachment opt-ins | `58b51cd0c22f1e222b1473b3bf4b8db65605ed65b987a8aa36906aae45a78793` | `167aaa07be31ee196c161925056a3d926918c92e1970a4270708cb7a7786a9ab` |
| Signed Go editor comparison | `ddcc82ff3c96dc33cfdd5ed2b67abff5c01473846630b7dbbcc7768ce0fc6c58` | `d9a481fe0a7368bba7729b522874fa9e5eba311ae5b392c04f9ddbb8f5e180d8` |

The integrated Automations row correction and its workspace regression passed 270 default tests with 25 opt-ins on both native hosts. Formatting and warnings-denied Clippy passed. Workspace log SHA-256 is `5b8d4821fec83f482f28f22bd0dedd29babe0bd5ff4648ec4a196cf02e622a7c` on macOS and `d7a21a58b356c958dc07ed88a5807c8840a323bcd2355d7a9af8d73d90611833` on Linux. [XTerm and tmux comparisons](rust-schedules.md#archive-confirmation-and-list-navigation)

A new isolated Linux development soak started from the frozen `aec3365e` executable with SHA-256 `4c58f86f5c33475b864c881b899c6a3201911de5f372fd3cb6b467380b662f60` and the same native OpenCode 1.19.3 fixture executable. Its private unit is running the 24-hour stage and has verified the first three inputs. The 72-hour stage is conditional on a completed, clean 24-hour result. This run qualifies the development build. The final signed artifact retains its own duration gate.

Integrated `f1b1e0e6` passed 277 default tests on macOS arm64 and 276 on Linux x86_64. Both suites list 25 opt-ins. macOS has one additional native case for binding the writable Bolt descriptor across a pathname replacement. Formatting, warnings-denied Clippy, the five packaging-script cases, and 162 JavaScript bridge cases passed. The native template retest initially waited on the old draft title. After switching to the typed editor title it exercised template copy and edit through the real dashboard, exact lost-ack recovery, truncation, both lock deadlines and a competing Go edit against signed v0.29.26 on both hosts. The integrated nine-arm inline timer fixture passed separately on both hosts.

| Integrated `f1b1e0e6` log SHA-256 | macOS arm64 | Linux x86_64 |
| --- | --- | --- |
| Default workspace tests | `1c0e3af721c38ec931762daf630acfeed396859acf72fd8721342e5a8765e05e` | `709e3483257350db0771fd0d0880880950eab9442c7be7390fcf7bde2489c774` |
| Signed Go v0.29.26 template and lock checks | `8a77de09dd1bb36ab86ad7151d9cbc575759ac9ac715cc9bc94954bfbb321e9c` | `8a77de09dd1bb36ab86ad7151d9cbc575759ac9ac715cc9bc94954bfbb321e9c` |
| Inline timer fixture | `a4e17be53fbe5bf28ecea5a959c5be08e900401c22d495cbcc366b785a6ca0ff` | `81863e1af53b301b8968eda499686b530efe1a73234160c5ce53b1f8813b51b4` |
