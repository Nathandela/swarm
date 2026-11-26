# Checkout ownership inventory

Run `swarm checkout inventory` to discover checkouts referenced by local Swarm sessions and Discord discussions. It also includes the current directory and registered worktrees of each discovered Git repository. Give paths to narrow discovery or to inspect a directory, file, or missing checkout that has no recorded session.

```sh
swarm checkout inventory
swarm checkout inventory --json /path/to/repo /path/to/external-checkout
swarm checkout inventory --supervisor-state /path/to/other/supervisor --discord-state /path/to/connector /path/to/repo
```

The command reads the selected local supervisor, known installed and active teammate connectors, and their current and previous supervisor roots. The two state flags are repeatable. Use them for an additional local installation or an archived state root you intend to inspect. No remote machines or other users are queried.

A path row shows the recorded Git repository, worktree, branch and revision when available. The linked session rows give the requested and provider working directories, whether Swarm recorded worktree isolation, and descendant or resume relationships. Hidden attempts remain visible. A live row has a matching shim process identity at inspection time. A parked discussion has durable discussion evidence. A retained parked source has an independently confirmed retirement record and can still be relevant to resume. A resume candidate has a recorded provider conversation eligible for further checks. The inventory does not launch a resume or verify current provider credentials.

Unresolved receipts include durable supervisor operations, pending worker notes, connector ingress or publication work, and native recovery obligations. A transport receipt may already be complete while the native outcome is still unresolved. Unlinked receipts are included because they could refer to a checkout whose session or source could not be verified.

A report with unresolved evidence gaps exits with status 1 and still prints its findings, including in JSON mode. Unknowns cover missing or changing state, unverified physical worktree registrations, lost native work and unavailable ledgers. A complete report means the selected sources were readable within the inventory's bounds. It is a snapshot and does not determine that an unreferenced checkout is safe to remove. Check Git changes, local files and other consumers separately.

The command does not start a daemon, connector, or discussion controller. It reads existing Git and Swarm state without writing it.

New isolated launches use the primary checkout's `.swarm/worktrees` directory for each Git repository and start from the source checkout's HEAD. Bare-backed and separate-metadata repositories can use the Git common directory as the placement anchor. Existing nested registrations stay in place. If a deletion reports a nested checkout or uncertain ownership, its path and Git registration remain available for inventory and manual recovery. [Placement and recovery evidence](../verification/nested-worktree-placement.md)
