# Architecture decisions

The Rust application keeps the supervisor, PTY-owning shim and Discord connector as separate processes. The migration design records ownership of terminal state, provider operations, persistent records and release artifacts. [Rust design](../specifications/rust-migration/rust-design.md) · [Terminal runtime](../specifications/rust-migration/terminal-runtime.md) · [Discord](../specifications/rust-migration/discord.md)

The original numbered ADRs document decisions for the shipped Go system. Use the [pinned decision index](https://github.com/Nathandela/swarm/blob/f26648d69086e329132ac7849954d2f29a895387/docs/adr/README.md) when comparing historical behavior and state.
