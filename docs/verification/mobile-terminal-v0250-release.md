# Swarm v0.25.0 — optional attached return button

Date: 2026-10-27. [Published release](https://github.com/Nathandela/swarm/releases/tag/v0.25.0), immutable source `6a6a432076a412d4326d57960810fe74f1876503`. The [machine-readable record](mobile-terminal-v0250-release.json) binds workflow, signed archives, build metadata, installation, process continuity and limitations.

The owner authorized implementation and, after clean gates, publication and installation on this Mac and `tf_nathan`. **Options → CLI button** enables `[swarm]` in the existing attached footer. It starts off. Matching press/release returns to the dashboard while the session continues; Ctrl+Q remains available. Terminal-wide capture can affect selection and scrolling. Older live shims lack the new mouse metadata and retain keyboard detach; updating binaries or the daemon does not retrofit those sessions. No native agent was restarted to demonstrate the button.

The [integration plan](../specifications/mobile-terminal-touch-plan.md#attached-swarm-button-integration-amendment--2026-10-27), [decision](../adr/ADR-035-mobile-terminal-navigation.md#attached-swarm-button-amendment--2026-10-27) and [source qualification](mobile-terminal-touch/implementation.md#attached-swarm-return-button--2026-10-27) record the supported envelope and causal tests. All **17** [exact-tag workflow jobs](https://github.com/Nathandela/swarm/actions/runs/40471411699) succeeded, including a fresh full race run, platform builds, lint, fuzzing, Android gates, container gates and publication. No separate hosted full-normal rerun is claimed. The earlier local full race remains recorded as failed; its two test-only observer corrections passed full affected normal/race checks and independent review before this exact-tag rerun. No gate was bypassed.

## Signed publication and installation

The independent verifier accepted the compiled Ed25519 trust key and rejected changed checksum data. All three CLI archives match the signed checksum file and source-generated compatibility card. Both universal Mac slices and both Linux architectures carry exact 0.25.0/commit/Go 1.26.10 build identities; both executable hashes were checked on each installed host. The container manifest's tag/commit matches; it is not covered by the binary checksum signature, and OCI attestation was not independently reverified.

| Host | Installed binary pair | Running daemon | Captured shim instances |
|---|---|---|---|
| Mac | 0.25.0, Homebrew receipt/links and signed hashes verified | 0.24.0; safe handoff deferred | 8/8 unchanged |
| `tf_nathan` | 0.25.0, signed stage/activation and both hashes verified | 0.24.0; safe handoff deferred | 31/31 unchanged |

Both unattended requests returned **2** because sessions were working. Both daemon PID/birth identities stayed unchanged and both saved owner environment files retained their digests. Doctor exited zero with no FAIL checks: Mac 7 OK/3 WARN; VM 3 OK/7 WARN. VM pending convergence is `v0.25.0`, with its original `v0.24.0` rollback retained. The Mac upgrade timer is loaded; the VM timer is enabled and active/waiting. `agents-tracker-ppbw.8` tracks quiet convergence. No working turn or native session was killed or drained.

The Mac cask upgrade installed Swarm successfully, then Homebrew exited 1 while attempting an unrelated `pkgconf` reinstall because the existing Command Line Tools do not support macOS 27. Both new binaries, symlinks and the receipt were verified independently. No CLT removal/reinstall or unrelated dependency repair was attempted; the existing `agents-tracker-fi0w` maintenance issue remains separate.

## Installed-byte runtime checks

On native Mac arm64 and Linux amd64, the approved smoke harness copied hash-verified installed CLI bytes into private supervised roots. It started private daemons/shims/PTYs with fake agents, verified keyboard and `[swarm]` return, reattached and advanced the same agent conversation. Both passed: Mac 2.375 s and VM 1.876 s. HOME/XDG/state/socket settings and credentials were isolated. Only private copies were registered for cleanup; the installed executable was never registered or swept. Actual installed-path version, hashes and doctor were checked separately.

The read-only identity proof uses kernel process births, refuses uncertain inspection errors unless signal zero independently proves ESRCH, and emits no Env/raw metadata. Its captures are sequential observations, not atomic roster transactions. Unchanged PID/birth tuples establish the captured shim instances, not native PTY responsiveness or provider health. After independent audit and committed evidence, task-owned Mac/VM rollout, smoke, review and test scratch was retired, including the private raw captures. Only sanitized counts and hashes are committed. Shared caches, older calibration artifacts and managed rollback/pending-convergence state remain intact.

## Remaining qualification

`agents-tracker-ppbw.4` retains actual Termux/iOS terminal and native-CLI qualification. No handset, universal touch support or phone-visible latency is claimed. Known mode/frame ambiguity and the early Escape-prefix timeout remain documented. No provider upgrade, remote gateway initialization, hosted deployment or Android publication was requested by the rollout helpers. Existing managed rollback/pending state and shared caches are retained.
