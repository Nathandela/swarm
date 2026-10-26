# Responsive terminal navigation release v0.23.0

The owner authorized publication and installation on the Mac and `ssh tf_nathan`
on 2026-10-25, after the implementation's clean local handoff. The latest live
published release was verified as v0.22.1; this additive navigation release is
v0.23.0.

## Candidate and qualification

The candidate contains the reviewed responsive terminal dashboard: viewport
scrolling independent of selection, collapsible groups, compact rows, explicit
attention reveal, captured New/Open/More controls, height-aware forms and
release-bound pointer actions. Wheel reports now scroll the viewport; an
Offscreen session command first reveals its target and consumes that action.
Raw attached-agent input ownership remains unchanged.

Runtime source is unchanged from main `9c279e8a`. See the
[implementation qualification](mobile-terminal-touch/implementation.md) and
[manifest](mobile-terminal-touch/qualification.json) for the broad normal/race
suites, independent Sol/Astra reviews, supplemental 5,400-geometry Unicode
checks, renderer negative controls and exact local performance measurements.
The release preparation adds documentation only. No protocol, shimwire or
persisted-schema change requires draining sessions.

The existing tag-only workflow must pass its reusable CI, runtime-service,
Android artifact and container gates before publishing signed archives and
updating the Homebrew cask. No gate or workflow trigger is changed.

## Publication and rollout

[v0.23.0](https://github.com/Nathandela/swarm/releases/tag/v0.23.0) is published
from `74504f5500a19e8d374009be1a16ef043ca628ec`.
[Release workflow 40199968287](https://github.com/Nathandela/swarm/actions/runs/40199968287)
passed all 17 jobs, including native Linux race/performance, macOS, local-service,
Android artifact, container and packaging gates. The signed checksums validated
against the compiled trust slot; a tampered-checksum negative control was rejected.
Both deployment archives matched those signed hashes, their compatibility cards
matched the source constants, and both binaries carried the exact release version,
commit and toolchain. Darwin build metadata was inspected for x86_64, the local
installation target. Container metadata was checked for tag/commit consistency;
its OCI attestation was not independently verified and no service was deployed.
[Sanitized rollout evidence](mobile-terminal-v0230-release.json) records the hashes,
workflow, installed versions and explicit limits.

| Host | Installed binary pair | Running daemon | Captured live shim identities |
|---|---|---|---|
| Mac | 0.23.0, Homebrew receipt/links and both hashes verified | 0.22.0; safe convergence deferred | 7/7 unchanged |
| `tf_nathan` | 0.23.0, signed stage/activation and both hashes verified | 0.22.0; safe convergence deferred | 25/25 unchanged |

Both unattended convergence attempts returned 2 because sessions were working.
The exact kernel PID/birth identities of the captured persisted shims and both
daemons remained unchanged; this is continuity evidence, not a physical PTY or
native-agent health trial. No busy restart was forced. Existing updater timers
remain available, and runtime convergence stays open in `agents-tracker-pnaq`.
The VM retains `pending-converge=v0.23.0` and its original `v0.22.0` rollback slot;
those recovery files were not cleared merely to remove warnings. Doctor reports
zero failures on both hosts, with the expected installed/running version mismatch
and pre-existing environment/storage warnings. Sol independently rechecked both
installed versions, all four binary hashes, the Homebrew links and doctor results.

Homebrew successfully upgraded Swarm and recorded its 0.23.0 receipt, then returned
1 during an unrelated `pkgconf` reinstall because the installed CLT does not support
macOS 27. The same error occurred after refreshing Brew metadata. The signed hashes,
receipt and actual installed versions were verified independently. That existing
host-tooling problem remains `agents-tracker-fi0w`; no SDK/CLT repair was attempted.
Collateral cleanup, autoremove and dependent checks were disabled for the targeted
upgrade. Native histories, credentials and unresolved upgrade state were retained.

## Support boundary

This is a terminal navigation release. Actual Termux and iOS terminal trials are
still open in `agents-tracker-nzo8.5`; it does not claim handset qualification,
thumb accuracy, phone-visible latency or universal swipe translation. The
[accepted plan](../specifications/mobile-terminal-touch-plan.md) requires both
actual clients before those compatibility claims. Local PTY/render timings and
hosted release gates do not substitute for that physical evidence. The native
Android remote app and hosted services are not deployed by this task.

## Task artifact retirement

After the sanitized evidence was recorded, task subprocesses had exited and both
private verification roots were retired, including captured identity reports,
disposable helper modules/binaries, downloaded archives and thin Mach-O probes.
The Mac task removed 137244719 logical file bytes; the VM task removed 3268608
allocated bytes. Shared Go/Homebrew caches, installed pairs, existing timers and
managed rollback/pending-convergence state remain intact.
