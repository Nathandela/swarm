# Codex enrollment preflight: v0.15.4

Status on 2026-10-04: **source and release verification pending; v0.15.4 is not published or deployed**. The isolated native device-code prompt and cancellation proof passed, but OAuth was not completed. This record does not claim successful Codex account admission, model rotation, or live Codex quota acceptance.

## Diagnosis and changes

Five previous Codex enrollment jobs ended with `worker-start-failed` before a worker job directory existed. The installed `@openai/codex` package was version 0.160.0, and `PATH` resolved to its `codex.js` npm launcher with mode 0775. The existing executable-permission guard rejects paths writable by group or other users. Provider authentication itself was not shown to have started.

The owner removed the launcher's group-write bit in place: mode 0775 became 0755, with the same inode and unchanged content hash. The installation fingerprint includes permissions and therefore changed; this does not prove unchanged CLI identity or successful discussion resume. Enrollment preflight reuses the existing permission guard before creating a candidate profile. It does not weaken the guard.

A second issue caused native Codex readiness to remain at `starting`: Codex 0.160 publishes `native.sock` as a symlink to a physical socket under a protected owner-only directory. A socket-only `Lstat` predicate sees the advertised symlink as a symlink, not a socket, and therefore reports a false negative. The fix validates only the expected deterministic alias, checks the protected directory and physical socket, then dials that socket. It does not increase the startup timeout.

The behavior matches the pinned [Codex Unix socket transport](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/app-server-transport/src/transport/unix_socket.rs) and [daemon directory implementation](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/uds/src/daemon_directory.rs): the app-server binds and protects the socket before publishing a symlink whose basename is derived from the canonical socket path. A separate no-auth probe validated the alias and observed the owner-protected socket in 365 ms; the target directory was mode 0700 and the socket mode 0600.

The pinned 0.160.0 app-server schema was generated under a new empty `CODEX_HOME`, without sign-in or provider requests. `LoginAccountResponse` requires `type`, `loginId`, `userCode`, and `verificationUrl`; the existing adapter spelling is correct. No device-code wire or schema change was needed.

The Accounts list now hides old terminal failed rows while keeping current sign-in states and custody-referenced jobs available. History starts collapsed. Press **h** in the list to show or hide past failures, then **Enter** to reopen an attempt. Inside the wizard, **Ctrl+X** requests cancellation and **Esc** leaves a live sign-in running. Cancellation remains visible while the daemon waits for stopped-writer proof.

## Focused and live evidence

The new native socket-alias test first reproduced the false negative, remaining in `starting` for 8.14 seconds. After the fix it passed in 0.20 seconds. Tests also reject unsafe aliases and cover cancellation fencing. The shared executable guard, enrollment, account-list filtering, preflight, and corrected backend-ready fixture checks pass locally. The red preflight failed at 6.64 seconds and the green preflight passed at 6.63 seconds.

The first probe against the repaired installed v0.15.3 launcher failed after 11.45 seconds with `native_login_failed`. The helper had not observed cancellation proof, so this is not a successful or safely cleaned-up sign-in result. A later host process scan found no remaining argv references to the isolated probe, and the helper was corrected to retain uncertain job state until cleanup evidence is available.

A corrected candidate v0.15.4 probe passed: the wrapper completed in 1.88 seconds and the focused Go test in 1.07 seconds. It launched the real Codex 0.160 device-code flow in an isolated profile and displayed a provider prompt. `Cancel` followed by `ReadProgress` proved the worker, runner, and native children had stopped before cleanup. The user did not complete OAuth; no account admission or live quota result was produced.

The first full normal suite failed after 490.42 seconds in the cancellation fixture with `unsafe_profile`; the other packages passed. That fixture bound a visible direct socket before restricting its mode. An independent diagnostic widened this interval and reproduced the same failure. With only the fake child's bind-time umask changed to 0177, the same diagnostic passed three race repetitions. Mode 0077 produces a 0700 socket and is insufficient for the native 0600 contract. Both fake native fixtures now publish 0600 at bind time and restore their inherited umask immediately. Production validation is unchanged. The original interleaving was not captured; the diagnostic establishes the mechanism, and final gates below must use the corrected test source.

## Gates and release identity

Android release identity is prepared as version name **0.15.4** and version code **51**. Frozen source commit/tree/inventory, complete build/vet/lint/normal/race gates, independent Astra review, PR and merge identity, release tag/workflow, publication time/assets, and deployment evidence are **pending**. Do not infer them from focused tests or the device prompt. The v0.15.2 quota-reader and v0.15.3 compact-label records remain the evidence for those releases; this draft does not supersede them.
