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

Publication and installation are pending at this candidate commit. The release
will be published by the existing GitHub Actions workflow, then its signature,
archive hashes, compatibility card and build metadata verified against the tag.
The post-publication record will include the exact workflow and commit.

The Mac installation must retain Homebrew ownership. The VM uses the existing
signed stage/activation path. Both binaries are upgraded together; live session
identities, ordinary rollback state and pending convergence markers are retained.
An unattended restart may safely defer while sessions are active. Installed
binary versions and running daemon versions will be recorded separately; no
forced native-agent restart is part of this rollout.

## Support boundary

This is a terminal navigation release. Actual Termux and iOS terminal trials are
still open in `agents-tracker-nzo8.5`; it does not claim handset qualification,
thumb accuracy, phone-visible latency or universal swipe translation. The
[accepted plan](../specifications/mobile-terminal-touch-plan.md) requires both
actual clients before those compatibility claims. Local PTY/render timings and
hosted release gates do not substitute for that physical evidence. The native
Android remote app and hosted services are not deployed by this task.
