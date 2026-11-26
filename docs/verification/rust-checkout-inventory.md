# Checkout inventory verification

The local command reads selected or explicit supervisor and Discord state and Git worktree registrations. It does not bootstrap a service or open a database for writing. The report marks missing, changed, corrupt, locked or unsupported evidence as incomplete and returns exit 1 with its available associations. It provides evidence for retaining work. Git changes and other consumers require separate checks.

Use Go at `f26648d69086e329132ac7849954d2f29a895387` as the historical oracle.

```bash
cargo build --locked -p swarm
cargo test --locked -p swarm --bin swarm checkout::
python3 scripts/verification/checkout_inventory.py PINNED_GO target/debug/swarm
```

The fixture runs both real supervisors under private scratch directories. It compares live and stopped Go report objects to Rust, discovers a linked worktree containing a tab, space and quotes plus a nested path, and checks a prunable and an ambiguous registration. Distinct sibling repositories and supervisor directories verify that link traversal precedes parent components. It preserves exact session IDs and associates historical attempts, a queued receipt and the Go-readable export of an earlier Rust session. It checks that inspection leaves selected supervisor and Git file bytes unchanged. The connector fixture tests selected and explicit paths, parked manager proof, worker project association and pending ingress/publication projection without exposing private inputs. A separate bbolt test rejects a locked writer and confirms the completed read leaves the database bytes unchanged.
