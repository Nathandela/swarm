# Relay runbook — standing up a TLS relay for the handset demonstration

**Scope: PB-OPS-1 and PB-OPS-5.** Enough to stand up a reachable relay with a pinned certificate
that a phone on the same LAN can talk to. **Production deployment, VPS provisioning, image
publishing, TLS renewal automation, backup/restore, log rotation and health checks are Phase C**
(§6.18 scope correction) and are deliberately absent.

> **AMENDED (wave R2, playbook §6.5):** backup/restore is no longer absent — it shipped with R2.
> `swarm-relay backup`/`swarm-relay restore` exist now; see §11. Everything else this paragraph
> lists as Phase C (VPS provisioning, TLS renewal automation, log rotation, health checks) is still
> exactly as absent from this document as it always was — only backup/restore graduated out.

Every numbered step below was executed on 2026-07-26 against this tree; the transcript is in
`docs/verification/remote-phaseB-s20-evidence.md`. Where a step **could not** be executed here it
says so at the step rather than reading as verified. §11 (backup/restore) is a separate, later
addition and says so at its own head rather than being folded into that transcript's date.

> **AMENDED BY ADR-016 (2026-08-15), target state — arrives with wave R2 (`ADR-016:5`):** This
> document stands up a relay with a self-signed certificate and an SPKI pin. Once R2 ships, that
> becomes the **expert `pinned_spki` policy** — the right choice for a self-signed or IP-literal
> relay, never the default — and the new **default** relay TLS policy will be `webpki`: ordinary
> Web PKI hostname validation against a real reverse proxy and a publicly trusted certificate, with
> no pin configured or consulted at all (documented for that state in
> `docs/operations/relay-vps-deploy.md`). **Today, before R2 ships, there is no
> `--relay-tls-policy` flag and the pin below is the whole of relay TLS verification on Android —
> read what follows as current, normal practice, not an expert appendix.** Once R2 ships, read it
> as the **expert appendix**: every step below about issuing, computing, provisioning, verifying
> and renewing an SPKI pin will apply only when you have deliberately chosen
> `--relay-tls-policy pinned_spki`, and a machine that has adopted `webpki` will still need to keep
> publishing a compatibility pin while any paired device has not migrated off it (ADR-016 W9) —
> `swarm remote init` refuses a pinless `webpki` profile until every paired device acknowledges.
> **Neither policy checks revocation.** `webpki` means chains to a trusted root, name matches,
> inside validity — and not that the certificate is unrevoked; a pin admits its one key forever,
> checking revocation even less. The honest mitigation is short certificate lifetimes (ADR-016 W2).

---

## 0. What you are actually standing up, and why it has two processes

`swarm-relay` serves **plain ws:// only**. `Server.Start` binds a `net.Listener` and sets
`s.url = "ws://" + addr` (`internal/remote/relay/server.go`); `Config.TLSMode` is parsed and then
read by nothing that serves; `Server.URL`'s comment says the cleartext is intentional because E2EE
does not depend on TLS.

`PB-NET-2` nevertheless refuses cleartext to anything but a **loopback IP literal**, and the phone
is not on loopback. So the demonstration is two processes:

```
  phone  --wss://LAN-IP:8443-->  TLS terminator  --ws://127.0.0.1:9440-->  swarm-relay
  gateway (same host) -----------------------------------ws://127.0.0.1:9440--> swarm-relay
```

The gateway (`swarm-remote`) reaches the relay over **loopback cleartext**, which is the
configuration that needs no pin and never leaves the host. Only the phone's hop is TLS.

> **The gateway CAN pin, as of ADR-007 B34/B37 — this note previously said it could not.** All
> three machine-side dial paths — the gateway sidecar (`cmd/swarm-remote`), the CLI's short-lived
> owner connection (`withMachineRelay`, which is what carries a revoke) and the daemon's pairing
> rendezvous — now dial through `relay.DialSecure`/`DialRawSecure` under `relay.MachineSecurity()`,
> so the cleartext refusal, the redirect re-check and the pin all apply. The pin is provisioned
> with `swarm remote init --relay-pin` (§4a) and is read from `relay.json` by one parser
> (`internal/remote/relaycfg`) that all three share, so it cannot reach some of them and not
> others.
>
> Two consequences for the topology above. **Loopback cleartext is still admitted** — a
> `ws://127.0.0.1:PORT` connection has no on-path position for an observer to occupy — so the
> gateway hop drawn above needs no change and no pin. **A `ws://` URL to anything else is now
> refused outright**, where it previously ran and merely went unverified.

`scripts/relay-tls-terminator.py` is the terminator used below. It is a stdlib TLS-to-TCP pipe
written so this runbook is executable on a machine with nothing installed; it has no access
control, no rate limiting, no supervision and no certificate reloading. **Do not deploy it.** In
Phase C this is a real reverse proxy.

---

## 1. Build the relay

```bash
go build -o ./swarm-relay ./cmd/swarm-relay
```

## 2. Issue the certificate — and keep the key, because the pin is over the key

```bash
openssl ecparam -genkey -name prime256v1 -out relay.key
openssl req -new -x509 -key relay.key -out relay.crt -days 90 \
  -subj "/CN=swarm-relay.local" \
  -addext "subjectAltName=DNS:swarm-relay.local,IP:<LAN-IP>"
```

Put the address the **phone** will dial in the SAN. A certificate with no matching SAN is
irrelevant to a pinning client — the pin replaces name verification — but it stops working the
moment anything on the path does verify normally, so get it right once.

> **`relay.key` is the thing you must not lose and must not rotate.** See §6.

## 3. Compute the pin

```bash
openssl x509 -in relay.crt -pubkey -noout |
  openssl pkey -pubin -outform der |
  openssl dgst -sha256 -binary | openssl base64
```

This is SHA-256 over the certificate's **SubjectPublicKeyInfo**, and it is the value that goes
into `relay.Security.PinnedSPKISHA256` (base64-decoded to 32 raw bytes). It is **not** the
certificate fingerprint, and it is not `relay.Security.PinnedCert` — see §6 for why that
distinction is the whole point of this section.

## 4. Write the relay config

```json
{
  "listen": "127.0.0.1:9440",
  "tls_mode": "on",
  "db_path": "relay.db",
  "sweep_interval": 30000000000,
  "quotas": { "mailbox_append_per_min": 600, "push_per_min": 600 }
}
```

- `listen` is **loopback**: the terminator is the only thing that should reach the relay directly,
  and the relay has no transport authentication of its own to put on a LAN interface.
- Durations are JSON **numbers in nanoseconds** (`time.Duration`), which is what
  `relay.LoadConfig`'s `json.Unmarshal` expects. `30000000000` is 30 s.
- `tls_mode` is recorded for the operator's benefit and is **not read by the serving path**. It
  does not turn anything on.
- **`mailbox_append_per_min` must be at least the shipped default of 600.** Quotas are
  operator-tunable and `mailbox_append_per_min` is the *only* cap that applies to live typing —
  `ops_per_min` explicitly excludes `mailbox_append` (`internal/remote/relay/config.go`). The
  client coalesces to ≤ 8 frames/s sustained, so 600/min is a deliberate 20% headroom; a lowered
  value silently breaks live typing rather than reporting a quota problem anywhere the user sees.
  Step 9 checks it.
- Omitting `push_credentials` is a **supported** configuration: the relay boots with no push
  transport and everything else is unaffected (PB-PUSH-5). See the operator runbook §6.
  > AMENDED BY ADR-015 (2026-08-15): omitting it is becoming the ONLY configuration — push moves to
  > the Swarm-operated gateway, and `push_credentials` survives only as the legacy relay-hosted
  > transport for the length of the `push_transport` compatibility window (playbook §12).

## 4a. Provision the machine with the relay and its pin

```bash
swarm remote init --relay-url wss://<LAN-IP>:8443 --relay-pin "$(
  openssl x509 -in relay.crt -pubkey -noout |
    openssl pkey -pubin -outform der |
    openssl dgst -sha256 -binary | openssl base64
)"
```

This writes `<stateDir>/remote/relay.json` at 0600 with `relay_url` and `relay_spki_pin`, which is
what all three machine dial paths read.

- **The pin is optional and mandatory in effect once set.** Omit `--relay-pin` and the machine
  behaves as it did before this section existed; supply it and a relay presenting any other public
  key is refused with `relay.ErrPinMismatch` on every machine dial path.
- **A pin on a `ws://` URL is refused at `remote init`**, not at the next dial. Cleartext presents
  no certificate, so the pin could never be checked, and a configured control that silently does
  nothing is the failure this section exists to avoid.
- **A malformed pin is refused at `remote init` too**, with `relay.ErrPinMalformed`, against the
  same parser every dial path uses — so the CLI cannot accept a value a dial would later reject.
  Paste the §3 output verbatim; a trailing newline is tolerated.
- `--relay-url` is capped at 39 characters (`pairing.MaxRelayURLLen`) because it is carried into
  the pairing QR verbatim. The pin is **not** in the QR and costs it nothing.

> **The handset gets its pin from pairing, and only from pairing.** `TrustRootSourceFor` makes
> Android pinning-only, so a handset that holds no pin refuses a `wss://` dial with
> `relay.ErrPinRequired` — but that is the state *before* pairing, not a permanent one.
> **AMENDED BY ADR-016 (2026-08-15), target state — arrives with wave R2 (`ADR-016:5`):**
> `TrustRootSourceFor("android")` still returns `TrustRootsPinned` — ADR-016 keeps it as the
> no-verifier floor — but once R2 ships and a machine has adopted the default `webpki` policy, a
> platform-delegated verifier is installed and Android is no longer pin-only in effect. **Today,
> before R2 ships, `pinned_spki` in all but name is the only policy there is**, so this paragraph
> and the pin channel it describes are simply how pairing works; once R2 ships they describe the
> expert `pinned_spki` policy specifically. The pin
> travels to the phone in the pairing exchange's msg2 as `MachinePayload.RelaySPKIPin`
> (`internal/remote/pairing/pairing.go`), fed from this machine's `relay.json`
> (`internal/skeleton/pairing_config.go`); the phone persists it (`internal/phonecore/state.go`,
> state schema v7 `relay_spki_pin`) and dials with it from then on (`mobile/relay.go`,
> `handsetSecurity`). The pairing dial itself is the one dial that cannot be pinned — it is the
> dial that *fetches* the pin — which is why `relay.PairingSecurity()` exists and why the QR
> carrying no pin is not a deadlock (ADR-007 B33/B34 for the channel, B45 for the bootstrap).
>
> **What remains open is ROTATION, not first use.** Once a handset holds a pin, there is no
> channel that can hand it a *different* one: §8c has the arithmetic for why the QR cannot carry
> it. A rotated relay key is recovered by pairing again, not by reconfiguring the phone. This
> section provisions the **machine** only.

## 5. Start both processes

```bash
./swarm-relay --config relay.json &
python3 scripts/relay-tls-terminator.py \
  --listen <LAN-IP>:8443 --target 127.0.0.1:9440 \
  --cert relay.crt --key relay.key &
```

## 6. Verify the pin against the LIVE endpoint, not against your notes

```bash
openssl s_client -connect <LAN-IP>:8443 -servername swarm-relay.local </dev/null 2>/dev/null |
  openssl x509 -pubkey -noout | openssl pkey -pubin -outform der |
  openssl dgst -sha256 -binary | openssl base64
```

This must equal step 3. Recomputing from the live endpoint rather than from the file is the point:
it is the only check that catches a terminator serving a different certificate than the one you
think you deployed.

## 7. Verify the relay is reachable *through* the terminator

A TLS handshake proving the terminator is up proves nothing about the relay behind it. Drive a
real websocket upgrade:

```bash
python3 - <<'PY'
import base64, hashlib, os, socket, ssl
HOST, PORT = "<LAN-IP>", 8443
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
ctx.check_hostname = False   # the pin replaces name/chain verification; step 6 checked it
ctx.verify_mode = ssl.CERT_NONE
tls = ctx.wrap_socket(socket.create_connection((HOST, PORT)), server_hostname="swarm-relay.local")
key = base64.b64encode(os.urandom(16)).decode()
tls.sendall((f"GET / HTTP/1.1\r\nHost: swarm-relay.local:{PORT}\r\n"
             "Upgrade: websocket\r\nConnection: Upgrade\r\n"
             f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n").encode())
resp = tls.recv(4096).decode(errors="replace")
accept = base64.b64encode(hashlib.sha1(
    (key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
print(resp.split("\r\n")[0], "| accept ok:", accept.lower() in resp.lower())
PY
```

Expect `HTTP/1.1 101 Switching Protocols` and `accept ok: True`.

---

## 8. Certificate renewal — the step that decides whether the product survives 90 days

**On Android the pin is the whole of relay TLS verification.** `TrustRootSourceFor` returns
`TrustRootsPinned` for `GOOS=android` because Go's Linux root loader looks in
`/system/etc/security/cacerts`, which Android 14 emptied when the system CA store moved into the
Conscrypt APEX, and which never carried user or enterprise CAs. There is no fallback path: an
unpinned dial on a pinning-only platform is refused before a packet is sent.

> AMENDED BY ADR-016 (2026-08-15), target state — arrives with wave R2 (`ADR-016:5`): "the pin is
> the whole of relay TLS verification" is true **today**, while `TrustRootsPinned` is Android's
> only trust-root source. Once R2 ships, ADR-016 gives Android a fourth source,
> `TrustRootsPlatformDelegate`, used under the default `webpki` policy — the platform's own trust
> manager plus Go's own hostname check, with no pin involved at all. Until then the sentence above
> is unconditionally true; from R2 on it is scoped to the expert `pinned_spki` policy this document
> configures.

That makes certificate renewal a **product-availability event**, not a maintenance chore. Two
rules, and they only work together:

### 8a. Pin the SPKI, not the leaf

`relay.Security.PinnedCert` compares the **full leaf DER**. A reissue changes the serial and the
validity window, so the DER changes even when nothing else does, and every pinned handset stops
connecting. On the Let's Encrypt cadence that is every 60-90 days.

`relay.Security.PinnedSPKISHA256` compares SHA-256 over the SubjectPublicKeyInfo, which a reissue
does **not** change as long as the key is the same. Same security level: the digest still admits
exactly one public key, and a certificate holding any other key is refused exactly as it is under
the DER pin.

### 8b. Make the renewal REUSE the key — this half is not optional

An SPKI pin is a *necessary half* of surviving renewal and not the whole of it. **certbot
generates a fresh keypair on every renewal by default**, and a fresh key is a fresh SPKI, which
breaks an SPKI pin on exactly the cadence it was adopted to survive.

```bash
certbot renew --reuse-key          # or, in the renewal config:
# reuse_key = True
```

For the self-signed certificate of §2, "reuse the key" means: reissue with the **same
`relay.key`**, never `openssl ecparam -genkey` again.

```bash
openssl req -new -x509 -key relay.key -out relay.crt -days 90 \
  -subj "/CN=swarm-relay.local" \
  -addext "subjectAltName=DNS:swarm-relay.local,IP:<LAN-IP>"
```

Re-run step 6 after any reissue. The pin must be **unchanged**; if it moved, the key rotated and
every paired handset is about to go offline.

**And so is the machine, which is new.** Now that §4a provisions a pin the gateway, the CLI and the
pairing rendezvous all honour, a rotated key takes the *machine* down alongside the handsets: the
sidecar exits on a failed dial and `swarm remote revoke` cannot reach the relay to purge anything.
A rotation therefore needs `swarm remote init --relay-pin <new value>` re-run on the machine, and
the machine can be repaired locally where a handset cannot — which is an argument for §8b, not a
reason to relax it.

Both halves are pinned by test rather than left as prose —
`internal/remote/transport/pin_renewal_test.go`:

| Behaviour | Test |
|---|---|
| A DER pin is broken by a same-key reissue | `TestPBOPS5_DERPinIsBrokenByRenewal` |
| An SPKI pin survives a same-key reissue | `TestPBOPS5_SPKIPinSurvivesRenewalWithTheSameKey` |
| An SPKI pin refuses a different key (security level held) | `TestPBOPS5_SPKIPinRefusesAnUnrelatedCertificate` |
| **An SPKI pin is broken by a renewal that ROTATES the key** | `TestPBOPS5_SPKIPinIsBrokenByARenewalThatROTATESTheKey` |
| Either pin alone satisfies a pinning-only platform | `TestPBOPS5_AnSPKIPinAloneSatisfiesTheAndroidPinRequirement` |

### 8c. Key rotation is a re-pin, and there is no channel for it today

If the key must rotate (compromise, algorithm change), every handset needs the new pin. **The
pairing QR has no pin field** — its payload is `version | flags | relay_url_len | relay_url |
rendezvous_id | pairing_secret | machine_static_pub?`
(`internal/remote/pairing/qr.go`) — and `MaxRelayURLLen` is 39 characters because the whole symbol
must still draw in an 80x24 terminal at QR version 6. A 32-byte pin is ~43 base64 characters and
pushes the symbol to version 7, which no 24-row terminal can show. **This is not a one-line
change**, and it is recorded as an open item rather than described here as a procedure.

> AMENDED BY ADR-016 (2026-08-15), target state — arrives with wave R2 (`ADR-016:5`): the
> QR-channel gap above still holds — the pin is still never in the QR — but "no channel for it
> today" will no longer be the whole story once ADR-016 W5 ships. **Until then, this section's
> opening sentence stands exactly as written: there is no channel, full stop.** Once R2 ships, W5
> will add an authenticated current/next pin overlap (`PinnedSPKISHA256Next`, to be provisioned
> with `--relay-pin-next`, a flag that does not exist yet): each paired phone will echo the pin set
> it holds on reconcile, and only after every device has acknowledged will the relay's key actually
> rotate. Re-pairing every device will no longer be required for a *planned* rotation on the expert
> policy; it will remain the only recovery for an unplanned one (compromise, a missed overlap
> window).

**NOT EXECUTED HERE:** every claim in §8 about ACME clients is about `certbot`'s documented
default, not about a run against Let's Encrypt. This project has no ACME account, no public DNS
name and no handset. What *was* executed is the reissue-and-recompare loop of §2/§3/§6 against a
self-signed certificate, and the five tests above.

---

## 9. Check the append quota was not tightened below the default

```bash
python3 - <<'PY'
import json
DEFAULT = 600   # relay.DefaultConfig().Quotas.MailboxAppendPerMin
got = json.load(open("relay.json")).get("quotas", {}).get("mailbox_append_per_min", DEFAULT)
print(got, "OK" if got >= DEFAULT else "TOO LOW: live typing would be refused")
PY
```

## 10. Tear down

```bash
kill %2 %1     # terminator, then relay
```

`swarm-relay` closes its store on `SIGINT`/`SIGTERM`. A clean run leaves an empty log.

---

## 11. Backup and restore the store

**Added by wave R2 (playbook §6.5), 2026-08-15 — not part of the §0-§10 transcript above.**
`swarm-relay` is a subcommand-dispatching binary now: `backup` and `restore` sit alongside the
no-subcommand serve behaviour every earlier section uses, reading `db_path` from the same
`--config` file.

```bash
# Stop the relay first (§10) -- see "why STOP THE RELAY FIRST" below.
./swarm-relay backup --config relay.json /path/to/relay-2026-08-15.db

# ... time passes, the store is lost or corrupted ...

./swarm-relay restore --config relay.json /path/to/relay-2026-08-15.db
# Now start the relay again (§5) against the restored file.
```

**The subcommand must come before `--config`, not after.** `./swarm-relay --config relay.json
backup <dest>` — flags first — is a usage error, not a backup: `swarm-relay` with no subcommand
serves (that is exactly the shape `deploy/relay/swarm-relay.service`'s `ExecStart` uses, since
serving needs no subcommand), so `--config relay.json` alone parses as "boot and serve," and a
`backup`/`<dest>` typed after it is now rejected rather than silently ignored.

**Why STOP THE RELAY FIRST.** bbolt gives exactly one process exclusive use of a store file at a
time — `Open` takes an OS-level `flock` for as long as the handle stays open, which is the relay's
entire run (`internal/remote/relay/store.go`'s `openStore`, `Options{Timeout: 0}`, waits forever
for it). `backup` and `restore` each probe that same lock, but with a short, bounded timeout
instead of waiting forever, so running either against a **live** relay fails cleanly and fast —
`relay: store is locked by a running relay` — rather than hanging your terminal or, worse, copying
a file mid-write. This is not a defect to route around: two OS processes were never going to share
one bbolt file, and a clean, immediate refusal is the honest behaviour when they try.

**What "consistent hot snapshot" means here.** `backup` does not `cp` the store file. It opens a
**read-only bbolt transaction** and calls `Tx.WriteTo` — bbolt's own documented mechanism for
producing a self-contained snapshot as of one transaction. That is what "hot" refers to: no
explicit flush, checkpoint, or graceful-shutdown step is needed beyond not having the relay hold
the file open, because bbolt's on-disk format is always internally consistent, even after a hard
kill. `backup` fsyncs the temp file before renaming it into place, so a crash or power loss
**immediately after** a "successful" backup cannot leave a truncated file at the destination
either — and the destination directory is fsynced too, once the rename lands, so the directory
entry itself is not lost to that same crash. `restore` does the same on its own side (temp file
fsynced before rename, directory fsynced after).

**A stale or concurrent temp file never blocks or corrupts a backup.** `backup` writes to a
uniquely-named temp file in the destination directory (`os.CreateTemp`, not a fixed `<dest>.tmp`)
before renaming it over `<dest>`, so a `.tmp`-shaped leftover from a previous killed `backup` is
simply irrelevant to the next one — no manual cleanup step, and no `file exists` error to work
around. The unique name also means two `backup` runs overlapping the same destination (nothing
serializes them; the source's read-only lock is shared) can never collide on the same temp file, so
neither can silently clobber the other's in-flight write.

**What `restore` checks before it touches anything.** In order: (1) the destination `db_path` is
not currently locked (the check above); (2) the candidate backup's own bbolt metadata — BOTH of
bbolt's meta pages, not just the first, since which one is active depends on which page the last
commit's transaction id landed on — is checked against its actual file size, refusing a
**truncated** file (an interrupted `scp`/`rsync`, or any copy that stopped early) before ever
memory-mapping it — opening a truncated bbolt file behaves correctly right up until something reads
a page past where the file was cut, which can crash the process outright rather than return an
error, so this check runs first and never touches the file beyond plain, bounded reads; (3) the
file opens as a valid bbolt database **with every bucket the
relay's store requires present**; (4) bbolt's own consistency check (`Tx.Check`) walks the whole
B+tree — key ordering, page reachability, double-frees — catching a backup taken by some other
tool or file-level corruption that (2) and (3) alone would miss. **Caveat:** bbolt has no per-page
checksum over the opaque bytes it stores, so (4) cannot detect a corruption confined entirely to
already-opaque ciphertext content (a single flipped bit deep inside one envelope, say) — only
structural damage to the store itself. Only once all four pass does `restore` write the restored
content into place, through a temp file in the destination's own directory renamed over the
original — so a failure at any point (including a disk-full write, or any of checks 1-4 failing)
leaves the previous file, if any, untouched rather than half-overwritten, whether that previous
file held real data or was itself empty.

**Restore compatibility.** A backup taken at the relay's current on-disk schema restores and
serves: every mailbox item, the storage cursor it was assigned, and the pairing graph
(`authorize_device` grants) are all present and usable immediately after `restore`, proven by
`TestRestore_RoundTripSeedBackupWipeRestoreServe` (`internal/remote/relay/backup_test.go`) — seed a
store through a real relay, back it up, wipe the live file, restore, and read the seeded item back
out through a freshly started relay.

**Restore is a revocation rollback — re-revoke anything revoked after the backup.** `restore`
replaces the *entire* store, and the revocation state (`swarm remote revoke`'s deleted pairing edge
and its ADR-007 B47 retired-ceremony tombstone) lives in that same store — there is nowhere else
honest to put it without the relay holding more than opaque ciphertext. Restoring a backup taken
**before** a revoke therefore undoes that revoke: the deleted pairing edge comes back, the tombstone
that refuses a replayed consent comes back with it removed, and a grantee that kept its old consent
bytes — or simply still holds the pairing — is accepted again, without the phone ever being asked.
This is inherent to any point-in-time restore, not a defect in `backup`/`restore` themselves, and it
is pinned as a test (`TestRestore_RollsBackARevocationPerformedAfterTheBackup`,
`internal/remote/relay/backup_revocation_rollback_test.go`) so it stays a recorded decision. **If you
restore a backup, treat every revocation performed after that backup was taken as undone** and
re-run `swarm remote revoke` for each one — this matters most for a lost or stolen phone, where the
whole point of the revoke was to keep it out.

**Disk-full behaviour.** A write failure partway through `backup` (disk full, quota, or any other
write error) is a clean error, and never leaves a corrupt or partial file at the destination path —
the temp file it was writing into is removed and the real destination is untouched. Exercised by
`TestBackup_DiskFullLeavesNoPartialFile`, which fails the underlying write deterministically via an
injectable writer seam (`backupCreate`) rather than a real full disk: this host has no simple
per-test tmpfs or disk quota available without elevated privileges, and a fault-injected write
failure exercises exactly the code path a real `ENOSPC` would hit.

---

## 12. `swarm relay doctor`: diagnose a deployment end to end

**Added by wave R2 (playbook §6.5), 2026-08-15 — not part of the §0-§10 transcript above.**
`swarm relay doctor <wss-url>` (a `swarm`-binary subcommand, not `swarm-relay`) runs every check
§§6-10 above walk through by hand — DNS resolution, the TCP+TLS handshake under the EXACT policy a
real machine dial applies (reporting which policy that was), the WebSocket upgrade, protocol
version compatibility, an authenticated mailbox round-trip, and the relay's own storage health —
in one command, against a relay that is already running:

```bash
swarm relay doctor --relay-pin "$(cat relay.pin.b64)" \
  --operator-secret-file operator.secret \
  wss://relay.example.com
```

Omit `--relay-pin` to dial under system trust roots (the ADR-016 `webpki` policy
`docs/operations/relay-vps-deploy.md` §11 steers toward); pass it to reproduce exactly what a
machine on the expert `pinned_spki` policy does — §3/§8a above compute the same value.

`--operator-secret-file` must point at the SAME file `operator_secret_file` names in the relay's
own config (`docs/operations/relay-vps-deploy.md` §14b) — the doctor reads it **locally** and
**mints** a short-lived (≤ 5 min), single-use\* diagnostic capability itself. There is no network
call that hands one out, so `swarm relay doctor` adds **no privileged unauthenticated endpoint**
to the public protocol (playbook §6.5) — this is the doctor rule §14a and §14b of the VPS deploy
doc already reference. Presenting that capability over the relay's ordinary authenticated
connection surface (`diag_open`) unlocks a new SCOPED op family — `diag_open`/`diag_status`/
`diag_append`/`diag_read`/`diag_close` — that can only create, use, and delete the **caller's own**
ephemeral diagnostic route: it can never read a real mailbox and never enumerates a routing id
(`internal/remote/relay/diag.go`; the adversarial fences are in `internal/remote/relay/diag_test.go`).
`diag_status` reports the SAME store-writable/free-disk verdict `/readyz` reports (§14a) — it exists
because a remote operator running this CLI typically has no `admin_listen` access, only the public
`wss://` one. Omit `--operator-secret-file` to run every step except the mailbox round-trip and
storage checks — useful when you only have network access to the relay, not its host. Both report
`skip`, not `fail`, when the flag is simply omitted, and a `skip` does not turn the exit code
nonzero — this is a legitimate, exit-`0` diagnostic run, not a degraded one. A flag that **was**
given but turns out broken (an unreadable or empty `--operator-secret-file`, a wrong secret, an
unreachable relay) still reports `fail` on both steps and a nonzero exit: the operator asked for the
check and it did not run.

\* "Single-use" is enforced in the relay process's memory only (`Server.diagUsedNonces`); a restart
forgets every spent capability. The blast radius of a post-restart replay is a fresh, empty,
per-connection diagnostic route — never real mailbox content — so this is accepted rather than made
durable; see the comment on `DiagnosticCapabilityTTL` in `internal/remote/relay/diag.go`. Each minted
capability is also bound to the ONE relay-auth identity `swarm relay doctor` generates for that run
(`RoutingID`-keyed) — an endpoint the capability is ever shown to (a typo'd URL, a hijacked DNS
record) cannot replay it against the real relay under an identity of its own, since it never holds
that ephemeral identity's private key.

Each of the six steps prints its own `ok`/`fail`/`skip` line with an actionable remedy; the command
exits `0` unless a step actually **fails** — a `skip`ped step (only Mailbox round-trip and Storage,
and only when `--operator-secret-file` is omitted) does not affect the exit code:

```
DNS resolution       ok   relay.example.com -> 203.0.113.7
TCP+TLS              ok   policy: system trust roots; issuer="R3" not-after=2026-11-01T00:00:00Z
WebSocket upgrade     ok   101 Switching Protocols
Protocol version     ok   negotiated version 1
Mailbox round-trip    ok   32 bytes round-tripped through an ephemeral, single-use diagnostic route
Storage              ok   store writable; 42817728512 bytes free (>= 1073741824)
```

Network-only (`--operator-secret-file` omitted), against the same healthy relay, still exits `0`:

```
DNS resolution       ok   relay.example.com -> 203.0.113.7
TCP+TLS              ok   policy: system trust roots; issuer="R3" not-after=2026-11-01T00:00:00Z
WebSocket upgrade     ok   101 Switching Protocols
Protocol version     ok   negotiated version 1
Mailbox round-trip    skip no --operator-secret-file given; pass the relay's operator secret file...
Storage               skip skipped: no --operator-secret-file given; pass the relay's operator...
```

A `TCP+TLS` failure under system trust roots is a **real** certificate problem (expired, wrong SAN,
untrusted issuer, or — most often — ACME never having issued; see §12's troubleshooting list in
`relay-vps-deploy.md`) — never a false failure from the doctor itself: this step builds and reports
the SAME `tls.Config` a real machine dial resolves via `relay.Security.Resolve`
(`cmd/swarm/relay.go`), naming the server it verifies against exactly as a real dial would rather
than aborting on a bare config before certificate validation ever runs.

---

## Things that will bite you, recorded because they already have

- **A relay upgraded in place over an existing store skips every pre-version mailbox record**
  (ADR-007 B28). The item layout gained a version byte, and the store fails closed on any record it
  did not itself write. **The cost is a small number of lost frames**, which the receive path
  already tolerates — the phone reseeds. The alternative was reading a legacy record as the new
  layout, which serves 32 bytes of ciphertext as a routing id and **wedges that mailbox for its
  entire retention window** with the relay reporting itself healthy. If you are upgrading a relay
  that has served traffic, expect a brief gap, not a stall.
- **The append and push rate windows are per-target and shared across senders** (ADR-007 B29,
  accepted for v1). In single-device v1 the only party who can append to a target is the owner's own
  phone, except in a fresh target's first-use window, where a stranger holding a new phone's
  relay-auth public key could burn its append budget and delay the epoch grant by up to a minute.
  Transient and self-healing. **The trigger for revisiting is explicit: multi-device, or any change
  that widens who may append to a target.** If you are reading this because you are making that
  change, B29 also states the constraint on the fix — do not key a rate map by attacker-chosen keys.
- **`tls_mode` in the config does nothing.** It is documentation.
- **The relay's own store holds a push token per routing id in the clear** once push is configured
  via the legacy relay-hosted transport (`push_credentials`, §4). See
  `docs/operations/metadata-disclosure.md`.
  > AMENDED BY ADR-015 (2026-08-15): scoped to the legacy transport, kept only for the length of a
  > pairing's `push_transport` compatibility window (playbook §12). Once a pairing migrates to
  > `gateway`, the relay holds no push token at all — the join key moves to the Swarm-operated
  > gateway (`docs/operations/metadata-disclosure.md` §3).
