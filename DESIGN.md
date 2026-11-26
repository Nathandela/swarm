# Swarm design

A Swarm installation has one per-user supervisor, one PTY-owning shim per live worker, and a separately supervised Discord connector. The board is a client of the same owner protocol used by the CLI. A session's local ID, process incarnation, provider conversation and idempotent operation receipt each have distinct lifetimes.

The Rust design documents the ownership of those identifiers, their on-disk contracts and the points where a user action becomes a native provider effect. [Implementation design](docs/specifications/rust-migration/rust-design.md) · [Terminal runtime and controller leases](docs/specifications/rust-migration/terminal-runtime.md) · [Provider workflows](docs/specifications/rust-migration/workflows.md) · [Discord and calls](docs/specifications/rust-migration/discord.md)

The original Go product design is available at its [pinned reference revision](https://github.com/Nathandela/swarm/blob/f26648d69086e329132ac7849954d2f29a895387/DESIGN.md).
