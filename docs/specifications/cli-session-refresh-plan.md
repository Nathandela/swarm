# Refresh sessions after an installed agent CLI upgrade

## Objective and scope

When the Claude Code or Codex executable selected by a session's saved environment
is upgraded, replace that session at a safe idle boundary and resume the same
provider conversation. Swarm does not download or install provider updates. A
busy or ambiguous session stays running. Installation changes are distinct from
swarm daemon upgrades, which preserve existing agent processes.

This plan follows an adversarial review of the existing credential recycler and
isolated binary-replacement experiments. Implementation uses the existing
supervisor, launch path, and lifecycle fences. It adds no dependency or alternate
process supervisor.

## Review outcome

The available GPT-5.6 reviewer and independent correctness reviewer agree on the
following release requirements. The configured Sonnet and Opus committee members
could not authenticate; the configured Gemini 3.5 member was unavailable. Their
approval is not claimed.

The initial local checkout was behind the installed release. Before publication,
the implementation was rebased onto v0.14.3/current main (`4c660259`), preserving
its newer Codex remote-resume handling, discussion projection, auth readiness and
archived-source cancellation. That upstream already supplies retained history and
the durable `Candidates` map; the final implementation reuses them rather than
introducing a competing replacement map. CLI refresh separately protects unsent
drafts, which account-change recovery may deliberately discard.

* A successful Launch means the shim is available, not that provider resume
  succeeded. The source row must remain available for recovery.
* A durable replacement ID and a search of both running and ended children close
  the launch-before-checkpoint crash window. No second automatic replacement may
  be created merely because the first replacement exited.
* Auth changes and executable changes need one lifecycle authority, including
  input fencing and crash restoration. Two autonomous recyclers are unsafe.
* The cached daemon-wide provider version is not session-version evidence.
* Codex's app-server and terminal have separate exec boundaries. Checks must
  cover both, not only the daemon's return from Launch.

## Launch evidence

Persist an additive optional CLIIdentity containing the selected executable path,
parsed release version, and filesystem fingerprint. Resolve and probe using the
session's filtered environment and working directory, never an unrelated daemon
PATH. Bound subprocess runtime and output. Reject incomplete, failed, malformed,
or unstable probes for automatic refresh. Ordinary launch remains available when
an identity cannot be established.

Use fingerprints before and after probing to detect installation changes during
the probe. The shim carries the intended identity, checks the installation across
actual process startup, and writes a separate observation only when the checked
identity remains consistent. For Codex this includes the backend and PTY startup
window; a degraded backend does not constitute a complete refresh.

These are installation observations, not cryptographic attestation of an arbitrary
wrapper's descendants. Preserve wrapper invocation semantics. Do not claim that a
mutable script's selected resources or an adversarial change-and-restore sequence
are fully covered by stat observations. Unstable evidence prevents automatic
completion rather than inventing a running version.

## Detection and eligibility

The existing watcher runs CLI detection serially alongside credential detection.
Require two consistent observations of a strictly newer ordinary numeric release.
Equal versions, downgrades, ambiguous prereleases, failed probes, unsupported
providers, and legacy unstamped sessions do not trigger destructive actions.
Installation polling is paced; pending work survives daemon restarts.

Eligibility requires a running session with recoverable conversation identity,
an idle turn, no outstanding interaction, and no worktree isolation. Existing
controller, composer, direct-input, ContextGuard, and owner-end guards are
rechecked inside the existing queue/lifecycle fence immediately before recording
the claim and signalling. An installation change during that final check holds
the attempt. First startup observation cannot kill reconciled sessions based on
possibly stale status.

## Durable lifecycle

Use the auth watcher's single serialized state owner and durable kill authority.
Add a per-source CLI refresh record for target identity, replacement identity,
retry timing/attempts, and an operator-visible reason. Coalesce an account change
and CLI upgrade affecting the same session.

The lifecycle is:

1. Observe and stabilize a newer target; retain the request while the source is
   busy or unsafe.
2. Under the existing input and owner lifecycle fences, revalidate source and
   installation, durably claim replacement, then request termination.
3. Wait for recorded source termination. Ambiguous delivery retains the claim;
   timeout is not permission to forget the obligation.
4. Search every child whose ResumedFrom names the source. Recover any replacement
   already launched, regardless of its process state. Otherwise launch one using
   the source's conversation ID, environment, cwd, options, name, tag and lineage,
   and the expected target identity.
5. Persist the created replacement ID before further processing. Pre-creation
   failures use bounded retries/backoff. A created but failed, lost, or ambiguous
   replacement is retained for manual recovery, never replaced in an endless
   automatic chain.
6. Complete only when replacement liveness and the shim's installation observation
   agree with the intended target. This establishes startup/version observation,
   not proof that every provider persisted in-memory resource survived resume.
   Keep the ended source row and its history after completion.

State writes use the existing sync/rename/parent-sync contract. Unreadable or
invalid settings/state fail toward inaction. An opt-out stops new destructive
work; already-owned recovery must remain recoverable. An explicit owner end that
wins before the refresh claim must never be resurrected.

## Operator interface

`swarm refresh` reports local refresh configuration and durable per-session
progress without starting a daemon or a provider process. `--json` exposes the
same facts. `--auto on|off` changes CLI refresh policy independently of credential
recycling. There is no force-refresh path that bypasses the idle/worktree gates.
Unknown observations and manual recovery are reported honestly.

Session capability version reporting uses session launch evidence when available;
the old provider-wide cache must not mislabel freshly launched sessions after an
update or label two different installations with one version.

## Implementation ownership

The launch workstream owns identity probing, metadata/config propagation, shim
observations, expected-target enforcement, and unit tests. The lifecycle
workstream owns detection, shared durable claims, retries/replacement recovery,
and state-machine tests. The independent verification workstream owns process
upgrade and crash/race integration tests. The integrator owns the operator
interface, session capability reporting, documentation, review, publication,
and installed-build verification. All work shares an isolated checkout; changes
are reviewed together before publication.

## Verification and rollout

Test atomic replacement and symlink retargeting, preserved saved PATH/cwd/env,
equal/downgrade/unknown versions, bounded hanging probes, stderr banners and
nonzero exits. Demonstrate an old mock process retains v1 while the replacement
starts v2 with the same resume ID and settings. Inject an update between Codex
backend and terminal startup and require an unverified outcome.

Exercise busy/controller/approval/composer deferral, owner end races, simultaneous
auth and CLI changes, disabled configuration, corrupt durable state, claim write
failure, ambiguous kill, and daemon restart at each durable boundary. Include
launch success before replacement checkpoint, already-ended replacement recovery,
failed launch backoff, and repeated ticks that must not create duplicate children.

Run targeted tests first, then complete Go build/test/vet/lint and relevant race
suites. Record any pre-existing or environment-only failures separately and fix
feature regressions before shipping. Review the integrated diff adversarially and
repeat relevant checks after corrections.

Push a normal, non-force commit to main after reconciling remote changes. Build a
uniquely stamped binary from the published commit, preserve a rollback copy of
the installed executable, atomically install, and restart only the swarm daemon.
Verify live session identities survive daemon replacement. Validate the installed
binary with disposable mock sessions and isolated state; do not upgrade provider
packages, alter real conversations, or stop a user's busy session as a test.
Legacy sessions remain unstamped and untouched. Record installed version/commit,
test evidence, and any remaining real-provider compatibility limits.
