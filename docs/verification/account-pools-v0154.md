# Codex enrollment preflight: v0.15.4

Status on 2026-10-04: **v0.15.4 published and deployed on the owner VM**. The official installed binary reached a real Codex device-code prompt and passed cancellation proof and Accounts navigation checks. OAuth was not completed. This record does not claim successful Codex account admission, live Codex quota acceptance, quota-triggered rotation, or discussion resume.

## Diagnosis and changes

Five previous Codex enrollment jobs ended with `worker-start-failed` before a worker job directory existed. The installed `@openai/codex` package was version 0.160.0, and `PATH` resolved to its `codex.js` npm launcher with mode 0775. The existing executable-permission guard rejects paths writable by group or other users. Provider authentication itself was not shown to have started.

The owner removed the launcher's group-write bit in place: mode 0775 became 0755, with the same inode and unchanged content hash. The installation fingerprint includes permissions and therefore changed; this does not prove unchanged CLI identity or successful discussion resume. Enrollment preflight reuses the existing permission guard before creating a candidate profile. It does not weaken the guard.

A second issue caused native Codex readiness to remain at `starting`: Codex 0.160 publishes `native.sock` as a symlink to a physical socket under a protected owner-only directory. A socket-only `Lstat` predicate sees the advertised symlink as a symlink, not a socket, and therefore reports a false negative. The fix validates only the expected deterministic alias, checks the protected directory and physical socket, then dials that socket. It does not increase the startup timeout.

The behavior matches the pinned [Codex Unix socket transport](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/app-server-transport/src/transport/unix_socket.rs) and [daemon directory implementation](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/uds/src/daemon_directory.rs): the app-server binds and protects the socket before publishing a symlink whose basename is derived from the canonical socket path. A separate no-auth probe observed the expected alias and socket metadata in 365 ms; the target directory was mode 0700 and the socket mode 0600. This read-only metadata check did not dial or validate the peer; the later real device-code flow provides the live connection evidence.

The pinned 0.160.0 app-server schema was generated under a new empty `CODEX_HOME`, without sign-in or provider requests. `LoginAccountResponse` requires `type`, `loginId`, `userCode`, and `verificationUrl`; the existing adapter spelling is correct. No device-code wire or schema change was needed.

The Accounts list now hides old terminal failed rows while keeping current sign-in states and custody-referenced jobs available. History starts collapsed. Press **h** in the list to show or hide past failures, then **Enter** to reopen an attempt. Inside the wizard, **Ctrl+X** requests cancellation and **Esc** leaves a live sign-in running. Cancellation remains visible while the daemon waits for stopped-writer proof.

## Focused and live evidence

The new native socket-alias test first reproduced the false negative, remaining in `starting` for 8.14 seconds. After the fix it passed in 0.20 seconds. Tests also reject unsafe aliases and cover cancellation fencing. The shared executable guard, enrollment, account-list filtering, preflight, and corrected backend-ready fixture checks pass locally. The red preflight failed at 6.64 seconds and the green preflight passed at 6.63 seconds.

The first probe against the repaired installed v0.15.3 launcher failed after 11.45 seconds with `native_login_failed`. The helper had not observed cancellation proof, so this is not a successful or safely cleaned-up sign-in result. A later host process scan found no remaining argv references to the isolated probe, and the helper was corrected to retain uncertain job state until cleanup evidence is available.

A corrected candidate v0.15.4 probe passed: the wrapper completed in 1.88 seconds and the focused Go test in 1.07 seconds. It launched the real Codex 0.160 device-code flow in an isolated profile and displayed a provider prompt. `Cancel` followed by `ReadProgress` proved the worker, runner, and native children had stopped before cleanup. The user did not complete OAuth; no account admission or live quota result was produced.

The first full normal suite failed after 490.42 seconds in the cancellation fixture with `unsafe_profile`; the other packages passed. That fixture bound a visible direct socket before restricting its mode. An independent diagnostic widened this interval and reproduced the same failure. With only the fake child's bind-time umask changed to 0177, the same diagnostic passed three race repetitions. Mode 0077 produces a 0700 socket and is insufficient for the native 0600 contract. Both fake native fixtures now publish 0600 at bind time and restore their inherited umask immediately. Production validation is unchanged. The original interleaving was not captured; the diagnostic establishes the mechanism, and final gates below must use the corrected test source.

## Gates and release identity

The frozen reviewed source is `41dc207c58fbc317d6e0acd1c938cd611684ce6e`, tree `da2bb9363029380b8a69a2aaa86f54465a2afd85`. All 1,651 tracked Go files were hashed and independently checked by Astra; canonical inventory SHA-256 is `bd5b3b645726b15e4392a0a514ff246b22143697d8581ef49772f65d3a41ff71`. The squash merge in [PR #45](https://github.com/Nathandela/swarm/pull/45) produced `cd0ae8986b74d2ce98d4e2e1c8124ef19146a342` with the same reviewed tree and every Go hash unchanged.

| Local gate | Result | Elapsed |
|---|---|---|
| build | pass | 3.42 s |
| vet | pass | 4.54 s |
| lint | pass | 10.16 s |
| docs | pass | 0.04 s |
| test | pass | 447.17 s |
| race | pass | 565.96 s |

Normal and race suites each passed 76 tested packages; eight packages had no tests. The final changed-file secret scan checked 12 files with zero findings. Independent Astra reviewed executable preflight, expected socket aliases and hostile alternatives, cancellation custody, collapsed/expanded history, focus and viewport behavior, and the corrected bind-publication fixtures.

All 30 branch/PR checks passed. Initial branch CI run 37219650812 failed before its unchanged shim survivor fixture sent SIGTERM because `CHILD_PID` was not observed within five seconds. The independent duplicate full CI run passed on the same source; ten focused uncached race repetitions also passed with unchanged assertions. Astra demonstrated a legal snapshot/live-output marker split that the fixture's separate searches miss, but did not establish the original CI interleaving. Original failure evidence was preserved. Exactly one unchanged failed-job rerun passed on attempt 2. Follow-up `swarm-8ip` tracks deterministic observation; no gate or assertion was bypassed.

The exact release tag **v0.15.4** resolves to `cd0ae8986b74d2ce98d4e2e1c8124ef19146a342`. [Release workflow](https://github.com/Nathandela/swarm/actions/runs/37221590952) attempt 1 passed all 17 jobs. [The release](https://github.com/Nathandela/swarm/releases/tag/v0.15.4) was published at **2026-10-04T18:00:29Z** with 10 assets. Signed staging verified clean-source build metadata and embedded commit/version for both binaries against that source, plus the archive's compatibility card. Android identity is version name **0.15.4**, version code **51**; this is not a Play Store publication claim.

The account compatibility axes remain `account_schema=1`, `account_jobs=1`, `account_recovery=2`, `account_worker=2`, `account_shim=2`, `account_config=2`, `account_inventory=1`. Shim wire and protocol remain 1, discussion schema remains 2.

## Installed acceptance and continuity

Both `/usr/local/bin/swarm` and `/usr/local/bin/swarm-remote` match the verified official stage; the responding daemon loads the installed Swarm hash and reports 0.15.4. All doctor checks pass and the pending-converge marker is absent. Activation installed both verified binaries and returned deferred (exit 2) while a discussion was working. The restart with the saved environment passed (exit 0), followed by successful convergence to current (exit 0). Fresh doctor and executable-hash observations prove the running version.

| Installed artifact | SHA-256 |
|---|---|
| swarm | `88ef96d90ba4b3839e8abe6639a80b91d95bcfe0dd97badc60eb566dc70ce8eb` |
| swarm-remote | `d35ec3aeea6ab42a416d36439ff1c1ec30ba325ce6b362f38ce6571c618b8aea` |

The official installed binary's isolated native Codex prompt and shutdown probe passed in 1.88 seconds. It used a fresh temporary profile, omitted the device URL/code from published output, and verified the worker, runner and every recorded native child had stopped before erasing temporary state. OAuth was not completed and no model turn was requested.

The owner-local daemon also advertised Codex device-code sign-in as available. The installed 80-column terminal flow passed: Options → Accounts, default collapsed failures, both connected Claude quota rows, **h** expand/hide history, select a failed attempt, **Enter** reopen, and **Esc** leave and return through Options to the discussion board. This navigation check did not start authentication, cancel production jobs or change account/rotation settings.

Across activation, all 155 existing discussions and the exact PID/start-tick identities of all 8 live shims were retained. Saved daemon environment, selected account identity/lifecycle/generation metadata, provider rotation settings and discussion bindings were preserved. This compares selected metadata, not credential or quota byte identity, and does not prove a discussion resumed or performed account rotation. The launcher permission repair changed the mode-sensitive CLI fingerprint despite preserving its bytes and inode.

Completed native OAuth/admission, live enrolled Codex quota, two-account quota exhaustion/rotation and continuation remain tracked in `swarm-2xm.2`. The existing 256-job cap and safe retirement of failed history remain `swarm-2xm.27`. The historical v0.15.2 quota-reader and v0.15.3 compact-label verification records retain their original evidence.
