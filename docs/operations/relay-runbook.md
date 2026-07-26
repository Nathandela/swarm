# Relay runbook — standing up a TLS relay for the handset demonstration

**Scope: PB-OPS-1 and PB-OPS-5.** Enough to stand up a reachable relay with a pinned certificate
that a phone on the same LAN can talk to. **Production deployment, VPS provisioning, image
publishing, TLS renewal automation, backup/restore, log rotation and health checks are Phase C**
(§6.18 scope correction) and are deliberately absent.

Every numbered step below was executed on 2026-07-26 against this tree; the transcript is in
`docs/verification/remote-phaseB-s20-evidence.md`. Where a step **could not** be executed here it
says so at the step rather than reading as verified.

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

> **The gateway cannot pin, and does not verify what you might assume.** `cmd/swarm-remote` dials
> with `relay.Dial` (`main.go`), not `relay.DialSecure`, so `relay.Security` — the pin, the
> cleartext refusal, the redirect re-check — is not applied on the gateway's connection at all. A
> `wss://` relay URL still gets Go's default `http.DefaultClient` TLS verification against the
> system roots, so a **publicly-trusted** certificate works and a **self-signed** one does not,
> with no pin knob to make it work. Keep the gateway on `ws://127.0.0.1:PORT`.

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
- **The relay's own store holds a push token per routing id in the clear** once push is configured.
  See `docs/operations/metadata-disclosure.md`.
