# Native managed update rehearsal

Built two applications stamped 0.1.0 and 0.1.1 from `016102ed75ef27c3434a30c44981bd98111f9a42` for each native test host. Each fixture contained the exact media and notices from the `cb630ec5` release rehearsal for the selected platform. The signed lists also named placeholder assets for unselected platforms. These feeds were disposable fixtures. The regression suite independently verified the Ed25519 signature and member hashes for all three original `cb630ec5` archives.

## Installed workflow

On both native macOS arm64 and native Linux x86_64, each installed `swarm upgrade` invocation used `PATH=/nonexistent` with no `SWARM_OPENSSL`. Offline bootstrap used the release verifier and a disposable Ed25519 key.

- Verified signed metadata with the default read-only verb and an explicit test feed. The check left the state directory and upgrade journal absent.
- Refused modified archive bytes and a mismatched channel manifest covered by the original signed checksum set. Neither result changed the selected generation.
- Refused the historical Python administrator when pointed at the native marker. The current packaging script exposes only build, sign, and verify.
- Staged 0.1.1 with the bundled Node, DAVE, cipher, and Opus self-check. An old daemon holding its lease deferred activation with exit 2.
- Activated after the fixture daemon exited. The same launcher selected the new app and matching media. Doctor read the activation receipt, and a direct mutation through the retired executable was refused.
- Restored the prior route before the new generation ran a writer. A repeat unattended attempt against the abandoned version returned held.
- Refused rollback after the new daemon ran. The selected new app and a file created after cutover stayed intact.

An installed online check against the unpublished native preview channel returned offline. The preview tag has a dedicated URL. The stable Go endpoint is outside that lookup. The project did not publish a feed for this rehearsal.

Disposable Caskroom, GOBIN and Cargo paths and a private dpkg database identified their respective owners and declined staging. The fixture host did not provide RPM. A native installation uses a new private prefix. Existing Go and Python-managed preview prefixes keep their owner and state; their operational first-hop route remains the separate [stopped-state rehearsal](rust-stopped-go-state.md).

`cargo fmt`, warnings-denied Clippy, 163 default workspace tests, five Python bootstrap/archive tests, and the independent managed marker-loss regression passed. Seven environment-specific Rust tests were skipped by default; the real-archive verification was run separately. Native activation remains a cold switch, with live writer and unfinished-external-effect deferrals. The `b5272a1f` follow-up passed the focused A→B→A regression. Staging attaches to the exact route generation, so rollback cannot revive the prior staged record even when it restores the same executable. The installed two-platform suite above used `016102ed`. Publication, active service convergence and promotion of the combined candidate require separate evidence.
