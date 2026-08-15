# Operator runbook — the flows Phase B introduces

**Scope: PB-OPS-2.** Install, pair, revoke, kill switch, device loss, push configuration.

Every command below was run on 2026-07-26 against this tree, in a scratch state directory, and the
output shown is the output it produced. Where a step needs a handset it says so **at the step**,
and names the automated demonstration that covers it instead. The transcript is in
`docs/verification/remote-phaseB-s20-evidence.md`.

Set `SWARM_DAEMON_STATE` to work against a scratch directory instead of the real one:

```bash
export SWARM_DAEMON_STATE=/tmp/swarm-scratch/state
```

> **Every `swarm remote` verb that talks to the daemon will START one if none is running**
> (`dialClient` → `daemon.EnsureDaemon`, `cmd/swarm/main.go`). There is no "connection refused"
> to warn you that you are pointed at the wrong state directory — you get a fresh daemon there
> instead. Check `SWARM_DAEMON_STATE` before you run anything destructive.

---

## 1. Install

```bash
go build -o "$BIN/swarm"        ./cmd/swarm
go build -o "$BIN/swarm-remote" ./cmd/swarm-remote
go build -o "$BIN/swarm-relay"  ./cmd/swarm-relay
export PATH="$BIN:$PATH"
```

**`swarm-remote` must be on `PATH` before you run `swarm remote init`.** It is the gateway
sidecar, and `init` installs the supervision unit that will start it once a device is paired —
not before. Without it on `PATH`:

```
$ swarm remote init
remote init: no gateway supervision unit installed (exec: "swarm-remote": executable file not
found in $PATH); the gateway will not start on its own
machineid.Identity{hostname:"..." ... epoch:1/1}
$ echo $?
0
```

**That is a warning on stderr and an exit status of 0.** A scripted install that checks only the
exit status records a successful provisioning of a machine whose gateway will never start. With
`swarm-remote` on `PATH` the warning is absent and the unit appears:

```
$ swarm remote init
machineid.Identity{hostname:"..." ... epoch:1/1}
$ find "$SWARM_DAEMON_STATE" -type f
.../remote/machine.key
.../remote/units/com.swarm.remote.plist      # launchd on darwin
```

**CORRECTED 2026-07-31 (ADR-007 B132).** This section previously said `init` "installs the
supervision unit that starts it", and §3's pair step read as though the gateway were already
running. It is not: **the gateway starts only after a device is paired.** With zero paired
devices `swarm-remote` exits immediately, cleanly and by design — observed on real hardware
during the B132 bring-up:

```
swarm-remote: no paired device to serve (0 paired, want exactly 1): supervise: nothing to serve; gateway quiescent
```

That exit is status 0 on purpose, and the unit's restart policy (launchd `KeepAlive
{SuccessfulExit: false}`, i.e. restart on failure only) deliberately leaves a clean exit alone,
so after `init` the correct state is *installed and quiescent*. There is no step where you start
the gateway yourself: a successful `swarm remote pair` activates it (§3). One recovery to know:
if the gateway is down while a device IS paired — a transient `launchctl` refusal at pair time,
an upgrade from a build that installed no unit — re-run `swarm remote init`. It is idempotent
and converges the unit on the state the paired-device count implies; `swarm remote pair` cannot
do it, because pairing is refused while a device is paired.

## 2. Point the machine at the relay

Stand the relay up first — `docs/operations/relay-runbook.md`. Then:

```bash
mkdir -p "$SWARM_DAEMON_STATE/remote"
printf '{"relay_url":"ws://127.0.0.1:9440"}\n' > "$SWARM_DAEMON_STATE/remote/relay.json"
```

The **gateway's** URL, not the phone's. Loopback cleartext is safe here because the hop does not
leave the machine. The phone gets the `wss://<LAN-IP>:8443` address through the pairing QR.

**CORRECTED 2026-07-31 (ADR-007 B124).** This paragraph previously read that `swarm-remote` *"dials
with `relay.Dial` and applies no transport-security policy, so it can neither pin a self-signed
certificate nor refuse a cleartext hop."* **Both halves are false at HEAD, and the sentence argued
against configuring a protection that works.** `run()` dials with `relay.DialSecure` under the
configured `RelaySecurity`: ADR-007 B37 makes it **refuse `ws://` to anything but a loopback IP
literal**, and B34 makes it **carry the operator's SPKI pin from `relay.json`**. Both are fenced —
`TestPBOPS5_TheGatewayHonoursTheConfiguredPin` and
`TestPBOPS5_TheGatewayResolvesItsPinFromRelayJSON`. **Configure the pin; it is enforced.**

> **AMENDED BY ADR-016 (2026-08-15), target state — arrives with wave R2 (`ADR-016:5`):** "Configure
> the pin; it is enforced" remains exactly true **today** — there is no `--relay-tls-policy` flag
> yet, and the pin is the whole of relay TLS verification. Once R2 ships, the pin becomes the
> **expert** `pinned_spki` policy, opt in via `--relay-tls-policy pinned_spki --relay-pin ...`, and
> the new **default** policy will be `webpki` — ordinary Web PKI hostname validation against a real
> certificate, no pin configured or consulted at all. Even then, a machine that has adopted
> `webpki` must keep publishing a compatibility pin (`--relay-pin-compat`) while any paired device
> has not yet migrated off it (ADR-016 W9) — `swarm remote init` refuses a pinless `webpki` profile
> until every paired device acknowledges. See `docs/operations/relay-vps-deploy.md` for the target
> default flow and `docs/operations/relay-runbook.md` for when the expert policy will still be the
> right choice (self-signed or IP-literal relays). **`webpki` means chains to a trusted root, name
> matches, inside validity — and not that the certificate is unrevoked.** Neither the platform
> default trust manager nor Go's own verifier checks OCSP/CRL; the honest mitigation is short
> certificate lifetimes (ADR-016 W2).

**`relay.json` is read once, at sidecar start.** `swarm-remote` redials the relay on ADR-007
§6.0's backoff when the link drops (PB-NET-4) rather than exiting for the supervisor to restart
it, so an edit here takes effect when you restart the unit — not on the next network blip.

`swarm remote status` reports what it found:

```
$ swarm remote status
configuration: initialized (identity + relay)
remote control: OFF (device-derived; no devices paired)
paired devices (0):
```

`remote control: OFF (device-derived; no devices paired)` is the correct state before pairing: the
kill switch is derived from having a device, not from a stored flag.

## 3. Pair

```bash
swarm remote pair
```

prints the pairing QR and waits for the handset to scan it and complete the SAS comparison.

The SAS comparison is the only human step, and since ADR-007 B133 it is the only human-in-the-loop
security step in the product: pairing requires no biometric, no PIN and no unlock gesture on the
handset. Compare all six emoji — there is no longer anything behind this checkpoint.

**NOT EXECUTED HERE — this step needs a handset, and PB-E2E-5 (physical handset) is deferred.**
No real pairing has ever completed in this project: a real handset has reached the app (ADR-007
B131/B132), but not through this step. What *is* executed, on every run of the suite, is
the whole flow against the real daemon, the real relay and the mobile façade:

```bash
go test -run '^TestPBE2E1_PairObserveLaunchTakeControlTypeRevoke$' -count=1 -v ./internal/skeleton/
```

```
--- PASS: TestPBE2E1_PairObserveLaunchTakeControlTypeRevoke (17.60s)
```

That covers pair → observe → launch → take control → type → revoke. It does **not** cover a camera
decoding a QR, real FCM delivery, Doze, or hardware-backed Keystore. An emulator is not a handset
and this is not one either.

**AMENDED 2026-07-31 (ADR-007 B133).** Two prior statements are corrected above rather than
silently rewritten. First, this section read "there is no phone and no camera in this project";
a real handset has since reached the app (B131/B132) without completing a pairing, so the step
still needs a handset and stays NOT EXECUTED. Second, the not-covered list above included "real
biometrics". All phone-side user authentication has been removed from the product, so PB-E2E-5
has NARROWED: real biometrics left its scope because the feature left the product — real camera,
real FCM, real Doze and hardware Keystore attestation stay deferred and stay in the gate
(`docs/operations/physical-handset-gate.md`).

> **Samsung handsets: Auto Blocker blocks `adb` entirely (observed on a Galaxy A26, Android 16 —
> ADR-007 B132).** With Auto Blocker on, both USB debugging and wireless debugging are blocked:
> `adb` cannot see the device at all, and the USB-debugging toggle in developer options is
> unavailable. Any step that reaches the handset through `adb` — installing a test build,
> `adb reverse` for pairing without a shared LAN, logcat — hits this first, and any Samsung
> tester will meet it. Remedy: Settings > Security and privacy > Auto Blocker, then turn off
> either the master toggle or just the USB-cable sub-setting.

After a successful pairing, the gateway is activated — it was quiescent until now (§1), and no
second command is needed — `swarm remote devices` lists the device and `swarm remote status`
reports remote control ON.

## 4. Kill switch

```bash
$ swarm remote off
remote control disabled
$ swarm remote on
remote control enabled
```

Both work with the daemon down (they start one) and both are idempotent. `swarm remote off` is the
**working fail-safe**: it calls `SetRemoteControl(false)` → `severRemoteControl`, independently of
epoch rotation and of the device registry. That independence matters — a revoke that fails partway
(rotate fails, or the registry write fails before the rename) can leave the device live and
un-severed, and `swarm remote off` still cuts it off. **If a revoke reports an error, run
`swarm remote off` immediately and sort the revoke out afterwards.**

**AMENDED 2026-07-31 (ADR-007 B133).** The trust boundary is now the wire between the phone and
this machine, and all phone-side user authentication has been removed: no biometric, PIN or
prompt stands between whoever holds the unlocked phone and take-control, type, kill or launch.
The accepted residual risk is that **a stolen unlocked phone gives its holder full control of
agents that edit code on this machine, and the only surviving mitigation is `swarm remote off`
and `swarm remote revoke`, issued from this machine.** These commands were the outer of two
layers; they are now the only layer, and they are load-bearing in a way they were not. If there
is any doubt about where the phone is, run `swarm remote off` first and investigate second.

## 5. Revoke, and device loss

```bash
swarm remote revoke <device-id>
```

The command is the **middle** of a four-step recovery, not the end:

1. `swarm remote off` — if the device is lost and you want it cut off *now*, before anything else.
2. `swarm remote revoke <device-id>` — de-registers the device, rotates the machine epoch key,
   stops the gateway, purges the relay-side mailbox and push token, purges the outbound custody.
3. **`swarm remote devices` — verify.** See the warning below.
4. `swarm remote pair` — pair the replacement handset. Until you do, the machine has no device and
   `remote control` reads OFF.

> **A device id that was never paired is refused, not reported as revoked.** `swarm remote revoke
> <unknown-id>` exits nonzero, writes nothing to stdout, and prints
> `remote revoke: device_revoke: no such device "<id>"; nothing to revoke`. Nothing is touched on
> the way out: the machine epoch does not rotate and the outbound journal is not purged. So a
> mistyped or stale id during a device-loss incident fails loudly instead of printing the line that
> says the lost phone is cut off. `swarm remote regrant` refuses the same id in the same shape;
> both are fenced by `TestRemoteRevoke_UnpairedIDIsRefused` and
> `TestRemoteRevoke_UnpairedRefusalMatchesRegrant`.
>
> **Always confirm with `swarm remote devices` after a revoke.** An empty list is the evidence; the
> success line is not.

**The relay half of `revoke` decides the exit code** (ADR-007 B120 F3). Exit 0 is a claim about the
relay — that the handset keeps neither connectivity nor a drainable mailbox — so it is made only
once the relay has acknowledged the purge, and the confirmation says which state you are in:

| exit | the line to read | what it means |
|---|---|---|
| `0` | `relay state purged: its mailbox, its push token and its route are gone from the relay` | the handset is cut off now |
| `1` | `remote revoke: the relay REFUSED to purge …: <relay's own reason>` | the relay answered and declined |
| `1` | `remote revoke: the relay was not reached, so its half of this revocation is PENDING: …` | the machine never reached the relay |

A nonzero exit never means nothing happened: the local half — de-registration, epoch rotation,
gateway stop, outbound custody — is durable before the relay is dialled at all, and the
`revoked device <id>` confirmation and the `swarm remote pair` pointer are printed on every path.

On both failing rows the handset still holds its relay mailbox, its push wake and its route.
**Nothing retries the purge**, and re-running `swarm remote revoke <id>` will not do it either —
the local record naming that routing id is already gone, so a second run is refused `no such
device`. The routing id is printed with the warning so the leftover state can be identified at the
relay. (ADR-007 D9's *"an offline-at-revoke machine defers the purge to reconnect"* is still
unimplemented; that deferral is the fix this row is waiting on.)

`swarm remote regrant <device-id>` re-issues a paired device's epoch grant. Use it when a device is
still trusted but has lost its grant; it is not part of the loss procedure.

## 6. Push configuration

> **AMENDED BY ADR-015 (2026-08-15):** This section previously said, without qualification, "Push is
> configured at the relay, not on the machine." That described the only transport that existed.
> ADR-015 moves the FCM sender off the relay entirely: `swarm-relay` ships with no push credential,
> no token map and no push transport; Android registers directly with the Swarm-operated push
> gateway; `swarm-remote` submits the wake to the gateway itself. What follows is the **legacy**
> relay-hosted transport, which a pairing keeps only for the length of its `push_transport`
> compatibility window (playbook §12) before migrating to `gateway`. A new deployment should not
> configure `push_credentials` at all.

Push was configured **at the relay**, in this legacy transport, by pointing `push_credentials` at a
Google service-account JSON document:

```json
{ "listen": "127.0.0.1:9440", "db_path": "relay.db",
  "push_credentials": "/etc/swarm/fcm-service-account.json" }
```

Three states, and the middle one is the one to know about:

| `push_credentials` | Behaviour | Verified |
|---|---|---|
| **unset** | Relay boots with **no push transport**. Wakes are dropped; every other path is unaffected. A supported configuration (PB-PUSH-5). | Executed: boots clean, empty log. |
| **set but unreadable/invalid** | **The boot FAILS.** | Executed: `swarm-relay: read push credentials: open /nonexistent/fcm.json: no such file or directory`, exit 1. |
| set and valid | The relay constructs the FCM v1 sender and injects it. | **NOT EXECUTED HERE.** |

The fail-closed middle row is deliberate: a relay that looked healthy while push was silently dead
would be discovered by a user who missed a hand-off, hours later, with nothing connecting the two.

> **NOT EXECUTED HERE, and this bounds every push claim in this repository.**
> AMENDED BY ADR-015 (2026-08-15): "There is no Google account, no Firebase project" is corrected —
> Firebase project `swarm-8404f` exists, the Android app `dev.swarm.phone` was registered on
> 2026-08-14, the FCM v1 API is enabled, the sender/project number is `733314021126`, and
> `google-services.json` is present locally, deliberately untracked. What has **not** changed: the
> FCM sender has **never run against Google**, no production token has been collected, and the
> Google Services plugin is not applied to a shipping build. Nothing in the test suite is evidence
> that a wake would be delivered to a handset; the tests drive a fake endpoint. PB-E2E-5 stays
> deferred.

Per-category push preferences (`push_prefs`) are set from the phone and are signed, machine-
authoritative and durable. A preference set while the handset is backgrounded — the normal state —
**is not retried**, and no façade surface reports that it did not land: the user sees their choice
on screen while the machine keeps the old one until they toggle again. Recorded in the S16
residuals; there is no operator action for it.

## 7. Launch policy configuration

> **AMENDED BY ADR-007 B144 (2026-08-15):** the Phase-2 deferral on phone-initiated launch is
> lifted. Live launch execution is a supported RCE-class action, not a restriction listed in §8.

A phone-initiated launch is refused unless its resolved cwd equals, or lies within, an
operator-configured root. There is no default allow: a missing or malformed policy file fails
**closed** to deny-all.

```bash
cat > "$SWARM_DAEMON_STATE/remote-policy.json" <<'JSON'
{ "version": 1, "allowed_cwd_roots": ["/home/you/code/project-a", "/home/you/code/project-b"] }
JSON
chmod 0600 "$SWARM_DAEMON_STATE/remote-policy.json"
```

- **Today's mechanism is this file, hand-authored.** There is no `swarm remote` subcommand that
  writes it yet; `internal/skeleton/remote_policy.go` loads `<stateDir>/remote-policy.json` on
  assembly start (`R-POL.7`, fail-closed on missing or malformed) and exposes the roots on the
  `policy_query` reply (`R-POL.3`, `docs/specifications/protocol.md`) so the phone can show them
  before a launch is attempted.
- **A resolved cwd equal to, or nested under, one of `allowed_cwd_roots` is admitted; everything
  else is refused** — checked against the same fully-resolved real path the daemon hands the shim.
- **B144 lifted the phasing, not the restrictions.** Kill switch on; device capability permits
  launch; `dangerously-skip-permissions` and full-access options refused from remote, hard-coded;
  no phone-supplied env; worktree isolation by default; an explicit phone confirm — all still
  apply. This section configures only which roots a launch may resolve into.
- **Presets arrive with wave R5.** B144's supported shape beyond the roots above is a
  machine-authored preset at a signed revision — opaque preset id, provider, canonical allowed
  workspace/worktree root, fixed environment policy, allowlisted options — with `launch_presets`
  and `session_launch` replacing free cwd/argv/env from the phone, and a changed revision refused
  as `stale_preset` rather than silently launching different policy. Until R5 ships,
  `allowed_cwd_roots` above is the whole of the launch policy surface.

## 8. What has no runbook, because it has no implementation

- **Backup and restore of the relay store**, disk-full behaviour, log rotation, health checks,
  resource limits, cross-version compatibility — returned to Phase C by the §6.18 scope correction.
  > **AMENDED (wave R2, playbook §6.5):** backup and restore now have both an implementation and a
  > runbook — `swarm-relay backup`/`swarm-relay restore`, documented in
  > `docs/operations/relay-runbook.md` §11 and, for the systemd deployment,
  > `docs/operations/relay-vps-deploy.md` §13. Disk-full behaviour for the backup path is covered
  > there too; the rest of this bullet's list (log rotation, health checks, resource limits,
  > cross-version compatibility) is unaffected.
- **Re-pinning a fleet after a relay key rotation** — target state, arrives with wave R2
  (`ADR-016:5`): under the future default `webpki` policy there will be no pin to re-issue, since
  rotation becomes ordinary certificate renewal verified against platform trust roots. Under the
  expert `pinned_spki` policy the pairing QR will still carry no pin field, but ADR-016 will give
  that policy an authenticated current/next pin overlap (`--relay-pin-next`) that will not require
  re-pairing every device on a planned rotation — see relay runbook §8c. **Today**, before R2
  ships, the pin is the only relay TLS policy there is, and a fleet-wide key rotation has no
  channel of its own yet — every paired handset is re-paired, per relay runbook §8c.
- **Multi-device** — v1 stays single-device by ADR-007 B1. ADR-018 MM1 freezes `Registry.AddSole`
  and `BeginPairing`'s fast-reject by name, so pairing still refuses a second device outright.
  > AMENDED BY ADR-018 (2026-08-15): this used to be one bullet, "multi-device and multi-machine",
  > conflating two claims with different fates. Multi-device is the one still deferred; see the
  > next bullet for multi-machine.
- **Multi-machine** — no longer deferred. ADR-018/RC-D8 puts N independent machine pairings on one
  phone in the first complete product (wave R4); the machine-side single-device model above is
  unchanged (the daemon still believes it is paired to one phone). This runbook still documents a
  single-machine flow because the phone-side client-state work (`MachineManager`, ADR-018 MM3)
  has not shipped yet.
