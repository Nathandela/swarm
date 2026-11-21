# Frozen dispatcher call qualification

The source under test was `87a8d600c54e501ae691cfbc78ed766741837a49`. The native checks used the Linux amd64 binary extracted from the GoReleaser snapshot. The Mac snapshot build, vet and lint exited zero. The unprivileged Linux test checkout used Go 1.26.9, Codex CLI 0.171.0 and an independent synthetic discussion per trial.

## Archive

All three CLI tarballs matched the generated SHA-256 manifest. Each contained a runnable Swarm executable, a v0.0.0 snapshot compatibility card with positive wire/protocol/schema versions, and byte-identical copies of the helper executable, media module, npm manifest and lockfile. The macOS executable contained x86_64 and arm64 slices.

| Archive | SHA-256 |
|---|---|
| Darwin universal | `297d473e9e9a24952701203f0f34b69267d888b2cff9cd03e90a52f251c860b3` |
| Linux amd64 | `072076dd22612062745c6c5cb27090281544c49ecdb4244a219508fc4ed1d2e8` |
| Linux arm64 | `58978393abc2f116fd502ef11322617e66b960b788bf876aa2b6f9cf4c5fa1c1` |

## Disposable native supervisor

The Linux host had 8 CPUs and about 9 GiB of available memory. Free disk space was 613 MiB on root and 926 MiB on data. The checkout, temporary profiles, supervisor sockets and state lived on RAM filesystems. A private PID namespace excluded existing supervisors. Native trials used one CPU equivalent and a 1,900 MiB memory limit. Only the fresh test profile was visible to native processes, and the harness removed its temporary authorization file at exit. The suite used separate copy-on-write Go caches, capped scratch, a 135% CPU quota and a 3,800 MiB memory limit.

| Trial | Observed result |
|---|---|
| Supervisor restart, 91.64 s | Supervisor PID changed, the waiting worker survived and the original admission stayed committed. Fresh input produced a distinct replacement manager in 5 s with an exact retirement receipt for the old manager. Both workers returned their exact markers in reverse order, with one native input and one canonical marked result each. |
| Connector runtime reopen, 88.67 s | Supervisor PID stayed the same. Reopening the connector ledger recovered the waiting worker. The second worker returned first. Both workers had a verified native turn and one exact publication each. |

The archived Linux CLI SHA-256 was `cc31ad4d784c952bb6dea4b0d554e090b1dabb88f8a92a97d2c3d69d01ecac3e`. The native test executable SHA-256 was `e7c4db49ec863a8496b573376ba6d33eea53db7a51e54fcdbe96ed3f43ea2a83`. Reproduction uses the [native canary settings](dispatcher-call-native-restart-cycle.md#reproduction) with the exact archived CLI and an isolated profile. The two interruption settings were `restart` and `connector`. These measurements used real native workers, synthetic Discord coordinates and a local publication sink. Actual Discord audio, listener observations and a published build require separate qualification.

## Whole-repository tests

The corrected Linux `go test -count=1 -timeout 35m ./...` exited **0**, with **96 packages passing**, from 16:04:29 to 16:27:54 UTC on 21 November 2026. The first pass had passed 95 packages; the mobile package could not find `gobind` inside the isolated HOME. The installed tool was verified against the frozen `golang.org/x/mobile` pin, `v0.0.0-20260709172247-6129f5bee9d5`, and staged only in the test profile. The corrected run used a fresh checkout.

The Linux `go test -race -count=1 -timeout 45m ./...` exited **1**, from 16:01:09 to 16:32:53 UTC. Its monitor identified failures in `internal/verify` (11.735 s) and `internal/voice` (19.550 s). The race-instrumented Discord package passed in 82.037 s. A failing test under the race detector does not by itself establish a detected data race. The detailed log remains at `tf_nathan:/run/user/1000/swarm-87a8d600.9CA5TyRg/race.log`. The monitor captured the exit status before a local macOS account lookup failure prevented a new SSH connection. Treat this gate as red until the cause is established and the complete check passes.

An earlier ordinary Mac pass timed out probing the OpenCode fixture. Three focused reruns of that test at the same commit passed in 1.148 s. The Linux full-suite result above is the qualification record.

## Private-listener window

The located personal connector profiles on the Mac and Linux host had no configured voice guild, room or linked discussion. Their existing application IDs alone cannot identify the intended test voice room. The archived helper passed its offline startup check with Node 24.17.0 on both hosts. Nathan was asked directly for the designated private guild voice-channel link and test bot mention or disposable profile path. The first spoken check is “Dispatcher, say violet harbor and wait.” Readiness starts when the serving connector matches the candidate build and authorized room. No Discord audio or listener observation was collected.
