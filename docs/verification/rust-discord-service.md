# Discord service installation and startup

`swarm discord connect --install` installs the same launchd or systemd user unit as Go. The command reports installation after the service manager accepts the definition. Its stdout reports the enrolled DM channel when one is available. A managed release records its stable launcher in the unit.

The service install and disconnect tests ran the actual CLI with an isolated HOME and a private replacement for launchctl or systemctl. They checked a successful install, a failed replacement restoring the original definition, permissions and active location, disconnecting a different place without stopping the installed job, removal of the selected unit, and refusal to replace an unrelated unit. Apple’s `plutil` and Ubuntu’s `systemd-analyze verify` both accepted the generated native definitions. A separate byte-level comparison used both renderers at the pinned Go revision with quoted paths, XML entities, a percent sign and a backslash. These rehearsals did not activate the account's installed production service.

On native macOS arm64 and Linux x86_64, a real supervisor first persisted the isolated operator environment and exited. An unattended connector started its enrolled supervisor from that environment while its caller pointed at a different private socket. Deliberately absent credentials kept the connector offline. A second invocation with an inconsistent state directory and socket refused the mismatch and left the known supervisor responsive. Both fixtures shut down the exact test owner over its negotiated connection.

For an ordinary Rust-owned guild thread, reconnect repair records one restatement notice per gap epoch if a qualified history page shows an admissible human message the connector did not previously know, or if the verified audience has lost history permission. Known ingress, compacted records, bots and control messages keep the thread quiet. If read authority becomes unavailable during the per-message lookup, repair preserves the prior cursor and epoch. A genuine deletion requires a fresh valid read scope. Publication rechecks the exact thread, parent and send permission. A lost POST acknowledgment keeps the original nonce and takes only exact REST or Gateway delivery evidence.

The two-thread authenticated loopback witness staged a notice before recording repaired input, then withdrew read authority during both a 403 and a 404 lookup. Neither control advanced the cursor, admitted the recovered message or made a POST. Restoring rights after reopen recovered the source and used the same notice nonce. Its lost acknowledgment was never replayed. The matching Go v0.29.25 cases passed in the reference checkout. The Rust fault log SHA-256 is `4868b8a9b680ac5c19082b12d3e87579d3370a63d187e0082469cd231bc873ef`. [Go gap notice reference](https://github.com/Nathandela/swarm/blob/44d796734fc60f6f9b9d6705dca44736f0c5741b/internal/discord/teammate_noise_test.go)

```sh
cargo test --locked -p swarm --bin swarm discord::unit
SWARM_NATIVE_UNIT_PARSER=1 cargo test --locked -p swarm --test discord service_activation_restores
cargo build --locked -p swarm
python3 scripts/verification/discord_service_bootstrap.py target/debug/swarm
```
