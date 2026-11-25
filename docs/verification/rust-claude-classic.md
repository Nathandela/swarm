# Rust M5 Claude classic slice

The [Claude workflow matrix](../specifications/rust-migration/workflows.md) remains the acceptance boundary. The shipping Go reference is `f26648d69086e329132ac7849954d2f29a895387`.

| Operation | Evidence |
|---|---|
| Classic version admission | The Rust tests cover the 2.0.0 floor, malformed and prerelease banners, and flag-shaped model values. The offline runtime fixture refuses three unsupported banners before creating a roster row or worker. The installed macOS Claude CLI reported 2.1.329 from a disposable home with no credentials and network denied. |
| Launch | The candidate resolves the executable through the session PATH, checks file identity before and after the version probe, and checks again in the shim before exec. The fixture consumes the emitted inline hook settings in a real child. The prompt and environment are delivered transiently. The runtime fixture checks that neither prompt nor environment canaries reach durable JSON. |
| Typed hooks and identity | The fixture executes the configured command inside the provider environment. PermissionRequest reaches the needs-input group, an unknown Notification subtype preserves that state, a wrong conversation ID is refused, and Stop reaches ready-for-review. The shim publishes only status and the first canonical conversation identity seen in its bound directory. A unit test exercises a late child stop during a new parent turn. |
| Restart | The runtime fixture kills the daemon and replays the reliable operation across replacement. The worker PID, shim birth identity, reliable receipt and PTY remain the same. |

```sh
cargo test --locked -p swarm --bin swarm providers::claude
cargo build --locked -p swarm
python3 scripts/verification/parity_runtime.py --candidate "$PWD/target/debug/swarm"
```

The fixture exercises the classic adapter without any provider inference. Native trust and permission UI, retained raw captures, ordered replay of intermediate hook events, exact resume and takeover, semantic broker and manager turns still need qualification. Current observation polling can coalesce intermediate states.
