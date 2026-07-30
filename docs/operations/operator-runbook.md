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
sidecar, and `init` installs the supervision unit that starts it. Without it on `PATH`:

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

## 2. Point the machine at the relay

Stand the relay up first — `docs/operations/relay-runbook.md`. Then:

```bash
mkdir -p "$SWARM_DAEMON_STATE/remote"
printf '{"relay_url":"ws://127.0.0.1:9440"}\n' > "$SWARM_DAEMON_STATE/remote/relay.json"
```

The **gateway's** URL, not the phone's. Loopback cleartext, for the reason in the relay runbook
§0: `swarm-remote` dials with `relay.Dial` and applies no transport-security policy, so it can
neither pin a self-signed certificate nor refuse a cleartext hop. The phone gets the
`wss://<LAN-IP>:8443` address through the pairing QR.

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

**NOT EXECUTED HERE — this step needs a handset, and PB-E2E-5 (physical handset) is deferred.**
There is no phone and no camera in this project. What *is* executed, on every run of the suite, is
the whole flow against the real daemon, the real relay and the mobile façade:

```bash
go test -run '^TestPBE2E1_PairObserveLaunchTakeControlTypeRevoke$' -count=1 -v ./internal/skeleton/
```

```
--- PASS: TestPBE2E1_PairObserveLaunchTakeControlTypeRevoke (17.60s)
```

That covers pair → observe → launch → take control → type → revoke. It does **not** cover a camera
decoding a QR, real biometrics, real FCM delivery, Doze, or hardware-backed Keystore. An emulator
is not a handset and this is not one either.

After a successful pairing, `swarm remote devices` lists the device and `swarm remote status`
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

> **`swarm remote revoke` reports success for a device id that was never paired.** Observed:
>
> ```
> $ swarm remote revoke deadbeefdeadbeef
> revoked device deadbeefdeadbeef
> run `swarm remote pair` to pair a device again
> $ echo $?
> 0
> ```
>
> Nothing was revoked and the machine epoch did **not** rotate (checked: still `epoch:1/1`), so it
> is a harmless no-op — but it is a no-op that prints a success line naming the id you typed. During
> a device-loss incident, a mistyped or stale device id therefore produces exactly the output that
> tells you the lost phone is cut off. `swarm remote regrant` on the same id refuses properly
> (`no such device "deadbeefdeadbeef"; nothing to re-grant`), so the asymmetry is in `revoke`.
>
> **Always confirm with `swarm remote devices` after a revoke.** An empty list is the evidence; the
> success line is not.

Every purge failure inside `revoke` is a **warning, not a nonzero exit**, and that is deliberate:
the revocation itself is already durable by the time the purges run (the device is de-registered
and the epoch rotated), so failing the command would tell you the revoke did not happen when it
did, and leave you no forward step. Read the warnings — a failed relay purge means the mailbox and
push token are still at the relay and want a manual purge.

`swarm remote regrant <device-id>` re-issues a paired device's epoch grant. Use it when a device is
still trusted but has lost its grant; it is not part of the loss procedure.

## 6. Push configuration

Push is configured **at the relay**, not on the machine, by pointing `push_credentials` at a Google
service-account JSON document:

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

> **NOT EXECUTED HERE, and this bounds every push claim in this repository.** There is no Google
> account, no Firebase project and no `google-services.json` in this project. The FCM sender has
> **never run against Google**. Nothing in the test suite is evidence that a wake would be
> delivered to a handset; the tests drive a fake endpoint. PB-E2E-5 stays deferred.

Per-category push preferences (`push_prefs`) are set from the phone and are signed, machine-
authoritative and durable. A preference set while the handset is backgrounded — the normal state —
**is not retried**, and no façade surface reports that it did not land: the user sees their choice
on screen while the machine keeps the old one until they toggle again. Recorded in the S16
residuals; there is no operator action for it.

## 7. What has no runbook, because it has no implementation

- **Backup and restore of the relay store**, disk-full behaviour, log rotation, health checks,
  resource limits, cross-version compatibility — returned to Phase C by the §6.18 scope correction.
- **Re-pinning a fleet after a relay key rotation** — the pairing QR has no pin field and no room
  for one at its current size budget. Relay runbook §8c.
- **Multi-device and multi-machine** — v1 is single-machine and single-device by ADR-007 B1.
  Pairing refuses a second device outright.
