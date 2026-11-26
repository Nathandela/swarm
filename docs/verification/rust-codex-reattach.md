# External Codex reattach on the Rust owner

`swarm reattach --cli codex --session <ID> --take-over` acquires an ordinary stopped Codex CLI conversation from the default native profile. `--dry-run` only inventories native metadata. `--all` includes archived records for inspection. An actual archive requires native unarchive first. `--take-over` confirms the owner already stopped the source CLI. The native metadata cannot establish that an external process stopped.

The supervisor resolves the same owner-controlled CLI-root rollout as the client and pins its path, metadata, checkout, account and provider version. The shim verifies the reservation before app-server startup and again before resuming. It waits for the server to return the exact idle native thread before starting the viewer. Another live or unconfirmed Swarm writer for that conversation blocks a duplicate acquisition.

Installed Linux Codex 0.172.0 passed `parity_codex.py --native --loopback-model --reattach-check` on 2026-11-26. The fixture ran a separate real Codex TUI in a controlling PTY, got a synthetic local inference reply, stopped the original process, and then acquired its exact conversation. Dry run, missing takeover confirmation and archived import preserved an empty Swarm roster. The new viewer showed the old reply, the native rollout still recorded exactly one user turn, and duplicate acquisition was refused with no surviving fixture children.

This checkpoint qualifies Codex, default native profile, on Linux. The CLI, source filter and archived behavior follow the pinned Go operator route. [Go Codex reattach](https://github.com/Nathandela/swarm/blob/f26648d69086e329132ac7849954d2f29a895387/cmd/swarm/reattach_codex.go)
