# Discord service installation and startup

`swarm discord connect --install` installs the same launchd or systemd user unit as Go. The command reports installation after the service manager accepts the definition. Its stdout reports the enrolled DM channel when one is available. A managed release records its stable launcher in the unit.

The service install and disconnect tests ran the actual CLI with an isolated HOME and a private replacement for launchctl or systemctl. They checked a successful install, a failed replacement restoring the original definition, permissions and active location, disconnecting a different place without stopping the installed job, removal of the selected unit, and refusal to replace an unrelated unit. Apple’s `plutil` and Ubuntu’s `systemd-analyze verify` both accepted the generated native definitions. A separate byte-level comparison used both renderers at the pinned Go revision with quoted paths, XML entities, a percent sign and a backslash. These rehearsals did not activate the account's installed production service.

On native macOS arm64 and Linux x86_64, a real supervisor first persisted the isolated operator environment and exited. An unattended connector started its enrolled supervisor from that environment while its caller pointed at a different private socket. Deliberately absent credentials kept the connector offline. A second invocation with an inconsistent state directory and socket refused the mismatch and left the known supervisor responsive. Both fixtures shut down the exact test owner over its negotiated connection.

```sh
cargo test --locked -p swarm --bin swarm discord::unit
SWARM_NATIVE_UNIT_PARSER=1 cargo test --locked -p swarm --test discord service_activation_restores
cargo build --locked -p swarm
python3 scripts/verification/discord_service_bootstrap.py target/debug/swarm
```
