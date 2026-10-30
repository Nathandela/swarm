# ADR-034: One native login per provider

Status: Accepted

Date: 2026-10-22

## Context

Swarm needs to continue discussions when a subscription reaches its limit. A
discussion does not need an account assignment. Native clients already handle
authentication and renewal; Swarm needs to select a login and restart safely.

## Decision

Keep a saved credential store for each login and one selected login for each of
Claude Code and Codex. All managed discussions of a provider use that selection
and one stable native configuration/history directory. Ordinary native launches
continue to work when no login is selected.

Claude uses `CLAUDE_CONFIG_DIR` for the common context and
`CLAUDE_SECURESTORAGE_CONFIG_DIR` for the selected saved credential store. Codex
uses a common `CODEX_HOME`; after all writers stop, Swarm saves the latest outgoing
credentials and installs the incoming credentials. Native clients own refresh.
Swarm does not independently refresh a parked copy of an active grant.

A provider has at most one durable switch journal. It fences new launches and
new turns, waits for safe idle boundaries and terminal detachment, stops all
writers, changes authentication, and resumes exact native conversations. Native
identity, model, working directory, worktree and discussion lineage are preserved.
The switch never resends a failed prompt or tool operation. Permission answers
and cancellation remain available while a busy discussion is waiting.

Only authenticated, terminal provider evidence starts an automatic change.
Claude quota exhaustion requires the matching structured native transcript;
generic rate-limit prose does not establish a subscription limit. Native terminal
authentication failures mark a login as needing sign-in. Rejected destinations
are tried at most once per switch; the same journal retains every restart
obligation while choosing another eligible login. If none is available, the
provider waits visibly for renewed capacity or sign-in.

Enrollment uses the installed native CLI in a private temporary home. Device
codes and terminal output remain live owner-local presentation. The completion
marker prevents an old enrollment from replaying credentials over a newer native
refresh. Temporary credentials are erased only after writer containment and
durable acceptance.

The previous persistence format has a one-time offline importer. It preserves
credentials and referenced conversation history before removing obsolete fields.
It refuses unresolved writer or recovery ownership. The importer runs under the
daemon singleton lock before session loading. Shim wire version 2 describes the
new launch/control contract and prevents activation under an incompatible live
daemon. Compatibility-card input fields exist only for the offline importer;
new login state also blocks downgrades, including machines without sessions.

## Consequences

The runtime has no discussion-to-account table, balancing score, trial budget,
configuration projection, or custom OAuth refresh service. Switching can wait for
an attached terminal, a running tool, or a permission response. External native
clients must stop during the initial configuration/history import. Subsequent
native settings for managed discussions live in the common provider directory.

Source deletion does not authorize deleting user data. The importer retains
history and unrelated configuration; unsupported old state produces an explicit
refusal rather than an unverified conversion.

## Evidence and alternatives

Real Linux native A→B→A probes passed with Claude Code 2.1.308 and Codex 0.163.0,
including exact-conversation continuation and marker recall. Claude also renewed
an expired cached access credential. The Codex probe did not force a token
renewal. See [qualification evidence](../verification/provider-logins.md) for the
scope and remaining integrated release checks.

Upstream Codex documents native authentication and has confirmed a refresh-token
reuse window for concurrent native clients; overlapping refresh alone is not
evidence that Swarm needs a token broker. Claude exposes native failure hooks,
while reported quota transcripts show why generic rate-limit events need
additional evidence. Sources: [Codex auth](https://developers.openai.com/codex/auth),
[Codex maintainer discussion](https://github.com/openai/codex/issues/10332),
[Claude hooks](https://code.claude.com/docs/en/hooks),
[structured quota example](https://github.com/anthropics/claude-code/issues/97975),
[ambiguous rate-limit example](https://github.com/anthropics/claude-code/issues/98012).

Request proxies, active copies of the same grant, per-discussion account routing,
automatic model changes and prompt replay were rejected as unnecessary or unsafe.

## Amendment 1 (2026-10-30): owner return to native sign-in

The owner could activate, remove and cancel a switch, but could not clear a
provider's selected login. Automatic switching therefore could not be turned off,
and there was no way back to ordinary native sign-in.

Return to native sign-in is a deactivation journal with no destination. It reuses
the provider switch journal, so writers stop through the same safe-stop path. Once
they have stopped, Swarm saves the outgoing login's latest native credentials,
scrubs the Codex token fields from the managed `auth.json` (replacing an
unparseable one), removes the Claude account identity from the managed
`.claude.json`, and clears the selection. A credential-content capture failure
marks that login needs sign-in and the change still completes; storage faults are
retried. Discussions end rather than resume, so the journal needs no exact
conversation for them. With no active login but managed-bound discussions still
running, the journal is stop-only: capture is skipped, only those discussions
stop, and the scrub still runs.

Each discussion stays bound to the home that holds its history. A v0.25-migrated
discussion, bound by its environment alone, counts as managed-bound, and that
binding survives later meta writes. A managed-bound discussion never resumes
natively; a native-bound one resumes into the managed home only when the managed
copy is current, and a stale copy is refused. After the managed home has been
imported once, reactivation verifies only managed-bound history, and it is refused
while native-bound discussions of that provider are running. The TUI refuses
removing the active login locally.

Discussions end when the provider returns to native sign-in. Exact continuation
across homes is not provided. Hand-off, which still reads the old transcript, is
the supported path. While a deactivation is pending, a binary without this feature
refuses to load login state and fails closed. Once it completes, the remaining
state is ordinary login state.
