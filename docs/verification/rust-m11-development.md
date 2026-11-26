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
