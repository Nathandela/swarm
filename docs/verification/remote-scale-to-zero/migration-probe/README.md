# Migration fence model

`migration_fence_model.mjs` is a deterministic Node model of the proposed
legacy-relay to Durable Object authority transfer. Run:

```sh
node migration_fence_model.mjs
```

It checks that a persisted legacy freeze prevents old append/revoke work during
export; an old authenticated connection remains fenced after cutover; rollback is
allowed only before the new writer accepts a write; and a cross-implementation
migration preserves mailbox incarnation while restore rotates it.

Limitations: this is not Swarm, Durable Object, database, or crypto runtime code.
It does not perform an actual export/import, authenticate a client, encrypt an
envelope, simulate crash persistence, or prove production atomicity. Its role is
to make the migration authority/rollback invariants executable; implementation
requires corresponding cross-runtime integration and fault-injection tests.
