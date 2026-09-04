# Relay VPS deploy — Caddy in front, real TLS, systemd

**Tracks `agents-tracker-vzl`.** `docs/operations/relay-runbook.md` stands up a relay reachable
from one phone on a LAN, using a self-signed certificate and a bespoke Python TLS terminator it
explicitly says **not to deploy**. This document replaces that terminator with Caddy — a real
reverse proxy that gets a publicly-trusted certificate via ACME automatically — so the relay can
be reached as `wss://` from the public internet, not just a LAN.

**What this is not.** `PB-OPS-1` scopes "production deployment, VPS provisioning and image
publishing" to Phase C (`docs/specifications/remote-phaseB-requirements.md:659`), and that
section's own footnote is explicit about what stays out: *"Returned to Phase C: production
backup/restore, disk-full behavior, log rotation, health checks, TLS renewal automation, resource
limits, and cross-version compatibility"* (`remote-phaseB-requirements.md:664`). Nothing below
adds backups, log rotation, health checks, or monitoring. It stands the relay up behind a real
certificate and documents what `PB-OPS-1`'s acceptance criteria actually asks for: a `wss://` URL
fed to `swarm remote init`.

> **AMENDED (wave R2, playbook §6.5):** backup/restore graduated out of that Phase C list — see
> §13. The rest of the footnote's list (log rotation, health checks, TLS renewal automation beyond
> Caddy's own ACME default, resource limits, cross-version compatibility) is unaffected and still
> absent from this document.

> **AMENDED BY ADR-016 (2026-08-15), landed with wave R2 (`ADR-016:5`):** This paragraph previously
> named "a `wss://` URL and an SPKI pin" as the two things this document produces, because the pin
> was, at the time, still the only relay TLS policy that existed. ADR-016 now makes `webpki` the
> **default** policy — this document's own Caddy/ACME setup is exactly that default's normal shape,
> a publicly trusted certificate on a real domain — needing no pin configured or consulted at all:
> `swarm remote init --relay-url wss://<your-domain> ...` with no `--relay-tls-policy` flag now
> writes a `webpki` profile. §9, §9a and §11's pin material remain needed in two cases that use it
> **differently**, and the two are not the same policy: the **expert `pinned_spki` policy**
> (`--relay-tls-policy pinned_spki --relay-pin`), or a `webpki` machine still inside the ADR-016
> **compatibility window** (`--relay-pin-compat`, §11) while a paired device has not yet migrated
> off its pin. **The per-device acknowledgement gate that would refuse a pinless `webpki` profile
> until every paired device has migrated is not built yet** (tracked: bd
> agents-tracker-hggx.3.5.3) — publish `--relay-pin-compat` explicitly during the compatibility
> window rather than relying on a refusal that does not exist yet.

**Revocation is not checked.** `webpki` means the chain leads to a trusted root, the name matches,
and the certificate is inside its validity window — and **not** that it has not been revoked.
Neither the platform's default trust manager nor Go's own verifier performs an OCSP/CRL check; the
honest mitigation is short certificate lifetimes, which this deployment's ACME renewal cadence
already gives (ADR-016 W2).

**Why there are two processes.** `swarm-relay` serves **plain `ws://` only** — confirmed directly
in `internal/remote/relay/server.go`: `Start` does `s.url = "ws://" + ln.Addr().String()`
unconditionally, and `Config.TLSMode` (`internal/remote/relay/config.go`) is parsed but read by
nothing that serves. `docs/operations/relay-runbook.md` section 0 says the same. So a reverse
proxy terminating real TLS in front of a loopback-only relay is not one option among several — it
is the only way to get `wss://` at all.

**Public exposure is bounded, not trusted.** A relay identity remains free to mint, so per-identity
limits alone are not an abuse boundary. The shipped configuration therefore adds gateway-wide
admission: `max_durable_objects`, `durable_growth_writes_per_min`, `max_db_bytes`, global and
per-source connection bounds, plus `disk_free_min_bytes`. A growth transaction that crosses a
limit rolls back atomically while acknowledgements, purge, token deletion, and revoke remain
available to recover capacity. These controls bound storage/write exhaustion; they do not
authenticate strangers or make the hostname secret. ACME publishes the name in Certificate
Transparency, so keep monitoring and edge filtering in place and prefer a private network for
closed tests that do not require cellular reachability.

---

## 0. Prerequisites

- A VPS running a systemd-based Linux distribution (this doc assumes Debian/Ubuntu for the Caddy
  install step; adjust package commands for others — see §6).
- Root or sudo SSH access to it.
- A DNS name you control, able to point an `A`/`AAAA` record at the VPS's public IP.
- Inbound TCP 80 and 443 reachable from the internet (80 for Caddy's default ACME `HTTP-01`
  challenge; 443 for the relay traffic itself). Open both in the VPS firewall and in your cloud
  provider's security group/firewall rules if it has a separate one.
- A local checkout of this repo with a Go toolchain (`go build ./cmd/swarm-relay` must succeed
  locally before you ship the binary anywhere).

## 1. Pick the VPS architecture and cross-compile

`swarm-relay` is a static, `CGO_ENABLED=0` Go binary; `.goreleaser.yaml` already builds it for
`linux/amd64` and `linux/arm64`. Find out which one your VPS is:

```bash
ssh you@your-vps 'uname -m'
```

`x86_64` → `GOARCH=amd64`. `aarch64` or `arm64` → `GOARCH=arm64`. Then, from a checkout of this
repo:

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o swarm-relay ./cmd/swarm-relay   # or arm64
```

## 2. Copy the binary up

```bash
scp swarm-relay you@your-vps:/tmp/swarm-relay
```

## 3. Create the user, directories, and install the binary

Run on the VPS as root/sudo:

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin swarm-relay
sudo mkdir -p /opt/swarm-relay/bin /etc/swarm-relay
sudo install -o root -g root -m 0755 /tmp/swarm-relay /opt/swarm-relay/bin/swarm-relay
```

`/var/lib/swarm-relay` (the bbolt store's home) does not need to be created by hand — the systemd
unit's `StateDirectory=swarm-relay` (§5) creates and chowns it to the `swarm-relay` user the first
time the service starts.

## 4. Install the config

Copy `deploy/relay/relay.config.example` from this repo to the VPS as
`/etc/swarm-relay/relay.config`. It is real, valid JSON that `relay.LoadConfig` parses as-is — the
`_comment_*` sibling keys next to every field are ignored by `encoding/json.Unmarshal` (confirmed:
`LoadConfig` uses plain `Unmarshal`, not a decoder with `DisallowUnknownFields`), so the file you
ship is both the documentation and the thing that boots. The shipped values already point
`listen` at `127.0.0.1:9440` and `db_path` at `/var/lib/swarm-relay/relay.db`, matching the
Caddyfile (§7) and the systemd unit (§5) respectively — you should not need to change either
unless you have a reason to.

Read the `_comment_quotas` note in that file before you tighten any quota. Without more, this
deployment topology (Caddy proxying to a loopback relay) would collapse every client's per-source
rate window into one shared bucket, because the relay keys rate limits off the raw TCP peer
address it accepts (`defaultSourceKey`, `internal/remote/relay/server.go`). The shipped
`relay.config.example`'s `trusted_proxies: ["127.0.0.1/32"]` closes that: with Caddy's own address
listed there, the relay instead derives the per-source key from the last (rightmost)
`X-Forwarded-For` hop Caddy appends, so `max_concurrent_connections_per_source` and `conn_per_min`
bind per real client again, not per shared Caddy bucket (R2 `playbook 6.5`, the trusted-proxy work
ADR-018 names below). Clearing `trusted_proxies` reverts to the collapsed-bucket behavior — only
do that if this relay has no reverse proxy in front of it at all.
> AMENDED BY ADR-018 (2026-08-15): R4 fires the revisit this section used to defer — its exit runs
> two machines through one organization relay, where a shared per-source bucket would no longer be
> harmless. The R2 trusted-proxy work above (playbook 6.5) is what keys quotas by the validated
> forwarded address instead.

```bash
sudo chmod 0644 /etc/swarm-relay/relay.config
```

`0644` is deliberate and safe **for this file**: it holds addresses, timeouts and quotas, no
secret. **The shipped example carries no `push_credentials` key at all.** ADR-015 moves push off
the relay entirely — the relay's target design ships with no push credential, no token map and no
push transport; Android registers with the Swarm-operated push gateway and `swarm-remote` submits
the wake directly. A pairing whose `push_transport` has not yet migrated from `legacy_relay` to
`gateway` (playbook §12) still needs the legacy relay-hosted transport — add `push_credentials`
back to your copy of the config; see §4a if that is your situation.

### 4a. Legacy only: `push_credentials` during the ADR-015 compatibility window

> AMENDED BY ADR-015 (2026-08-15): push moves to the Swarm-operated gateway — the relay's target
> design carries no push credential at all. What follows is the legacy relay-hosted transport, kept
> only for a pairing whose `push_transport` has not yet migrated from `legacy_relay` to `gateway`
> (playbook §12). Do not provision it for a new deployment.

> AMENDED (2026-08-22, by the dated amendment inside ADR-015): one exception to the sentence
> above. The application-owner deployment — the relay operator who owns the app's Firebase
> project (`swarm-8404f`) — MAY provision `push_credentials` on a new deployment as a supported
> configuration, under the custody rules below unchanged, until the gateway path is end-to-end
> operable (the PG-MIG-2 sunset bead the amendment names, agents-tracker-yxpm). Every other new
> deployment: the sentence above stands — do not provision it; those handsets run
> foreground-only until the Swarm-operated gateway ships.

If you are still inside that window and do set `push_credentials`, the file it points at is a
private key: the Google service-account JSON contains an RSA private key, and anyone who can read
it can send push as your Google project — against this relay's own store, which (during the legacy
transport) holds a push token per routing id in the clear
(`docs/operations/metadata-disclosure.md`), that means waking any handset paired to it. Install it
owned by root, readable only by the service's group, and shred the world-readable copy you `scp`'d
up:

```bash
sudo install -o root -g swarm-relay -m 0640 /tmp/push-credentials.json \
  /etc/swarm-relay/push-credentials.json
shred -u /tmp/push-credentials.json    # the copy you scp'd up, which landed 0644
```

`root:swarm-relay 0640` gives the relay exactly what it needs and nothing more: the unit runs as
`User=swarm-relay` (§5) and only ever reads this file at boot (`os.ReadFile`,
`cmd/swarm-relay/main.go`), so group-read is sufficient, `root` keeps ownership so the service
account cannot rewrite its own credential, and `ProtectSystem=strict` in the unit already makes
`/etc` read-only to the process regardless. A mode too restrictive to read is **not** a silent
failure: a `push_credentials` that is set but unreadable fails the boot outright (`pushOptions`
returns the `os.ReadFile` error), so you find out from `systemctl status swarm-relay` immediately
rather than from a hand-off nobody was woken for.

Then point `push_credentials` at `/etc/swarm-relay/push-credentials.json`.

## 5. Install the systemd unit and start the relay

Copy `deploy/relay/swarm-relay.service` to `/etc/systemd/system/swarm-relay.service`, then:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now swarm-relay
sudo systemctl status swarm-relay
```

`swarm-relay` closes its store cleanly on `SIGTERM` (`cmd/swarm-relay/main.go`: `signal.NotifyContext`
on `SIGTERM`/`SIGINT`, then `srv.Close()`), which is what `systemctl stop`/`restart` send, so this
is a clean shutdown path, not a kill.

## 6. Install Caddy

On Debian/Ubuntu, via Caddy's official apt repository:

```bash
sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | \
  sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | \
  sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt update
sudo apt install caddy
```

On another distribution, follow Caddy's own install instructions
(<https://caddyserver.com/docs/install>) — this project has no CI coverage over the Caddy install
step itself, only over `swarm-relay` and the config it consumes.

## 7. Install the Caddyfile and point DNS

`deploy/relay/Caddyfile` ships with `relay-swarm.dsfactory.org` — **keep any replacement
short** (§11 explains why, precisely), then copy it to `/etc/caddy/Caddyfile`:

```bash
sudo systemctl reload caddy
```

Create an `A` (or `AAAA`) record at your DNS provider pointing that domain at the VPS's public IP,
and wait for it to resolve:

```bash
dig +short relay-swarm.dsfactory.org
```

Caddy will not obtain a certificate for a name that does not yet resolve to this host.

## 8. Verify reachability

```bash
curl -i --http1.1 --max-time 5 https://relay-swarm.dsfactory.org/
```

Expect:

```
HTTP/1.1 426 Upgrade Required
Connection: Upgrade
Upgrade: websocket
...
WebSocket protocol violation: Connection header "" does not contain Upgrade
```

**This 426 is success, not a bug.** `swarm-relay`'s only HTTP route hands every request to
`websocket.Accept` (`github.com/coder/websocket`), which refuses any request that is not a
properly-formed WebSocket upgrade — verified directly by reading `accept.go` and by running the
relay locally against the exact config template shipped here, which returned exactly this status
and body for a plain `curl`. Seeing it over `https://your-domain/` proves DNS, Caddy's ACME
certificate, the reverse proxy, and the relay's own HTTP handler are all wired correctly, in one
request — a `502 Bad Gateway` instead means Caddy can't reach `127.0.0.1:9440` (check
`systemctl status swarm-relay` and that the Caddyfile's backend port matches `relay.config`'s
`listen`); a connection failure or TLS error means Caddy itself, or its certificate, isn't up yet
(see §12).

## 9. Compute the SPKI pin — from the live endpoint

> **AMENDED BY ADR-016 (2026-08-15), landed with wave R2 (`ADR-016:5`):** §9, §9a and §11's SPKI
> computation below feeds the **expert `pinned_spki` policy** (`--relay-pin`) **or** the ADR-016
> compatibility window on the **default** `webpki` policy (`--relay-pin-compat`, §11) — two
> different flags with two different meanings, never the same command. Once you close the
> compatibility window (§11) on a `webpki` machine, none of this section is needed any longer: no
> pin to compute, no pin to recover, no `--relay-pin` or `--relay-pin-compat` flag at `swarm remote
> init`. **Until then, this section is not optional — read on.**

Unlike the LAN runbook's self-signed certificate, there is no local `relay.crt` file in this
topology — Caddy manages the certificate and its private key itself. Compute the pin the same way
`docs/operations/relay-runbook.md` section 6 verifies one: against the **live** endpoint.

```bash
openssl s_client -connect relay-swarm.dsfactory.org:443 -servername relay-swarm.dsfactory.org </dev/null 2>/dev/null |
  openssl x509 -pubkey -noout |
  openssl pkey -pubin -outform der |
  openssl dgst -sha256 -binary | openssl base64
```

This is SHA-256 over the certificate's SubjectPublicKeyInfo — the value `relaycfg.Config.Pin()`
(`internal/remote/relaycfg/relaycfg.go`) expects: base64 of exactly 32 raw bytes. It is **not** the
certificate fingerprint.

**This pin survives renewal only because the Caddyfile asks it to.** Caddy's documented default is
to rotate: *"a new key is created for every new certificate to mitigate pinning and reduce the
scope of key compromise"* (<https://caddyserver.com/docs/caddyfile/directives/tls>). Under that
default the pin computed here would go stale at the first ACME renewal — unattended, roughly 60
days after deployment — and break the phone and the machine in the same minute. `deploy/relay/Caddyfile`
therefore sets `tls { reuse_private_keys }`, and that directive is the only reason the value below
is durable. **If you edited the Caddyfile by hand, confirm the block is still there before you
pin anything.**

Caddy documents that option as *"against industry best practices"* and *"subject to removal in a
future version"*, so treat pin stability as a property to re-check, not a guarantee: re-run the
command above after your first renewal to confirm the value is unchanged, and read §9a now so the
failure is recognisable if it ever arrives. This project has not executed a renewal against a live
Caddy instance and does not claim to have verified the directive's effect end to end.

## 9a. Recovery: the pin changed, and the phone says it does not trust the relay

Do not skip this because §9 is configured correctly. `reuse_private_keys` is documented as
removable, a Caddy upgrade can reset it, and restoring the VPS from a backup that predates the
certificate can strand the pin too. This section is what to do when it happens.

**What you will see.** The phone stops connecting and shows a banner with no spinner
(`android/app/src/main/kotlin/dev/swarm/phone/ui/ConnectionUi.kt`, `RELAY_UNTRUSTED`):

> This phone will not connect to that relay: it is not presenting the identity your machine
> published when you paired. Pair this phone again.

The machine fails at the same moment, on every dial path, with `relay: server certificate does not
match the pin` (`relay.ErrPinMismatch`). Both endpoints break together because both check the same
pin — the phone against the copy it stored at pairing, the machine against `relay.json`. **Two
endpoints failing simultaneously is the signature of a rotated key, not of an attack on one of
them.**

**Step 1 — decide whether it is a renewal or a real interception.** These are distinguishable, and
guessing is not necessary:

```bash
openssl s_client -connect relay-swarm.dsfactory.org:443 -servername relay-swarm.dsfactory.org </dev/null 2>/dev/null |
  openssl x509 -noout -issuer -subject -dates -fingerprint -sha256
```

A routine renewal shows your own hostname in the subject, your ACME CA in the issuer (Let's Encrypt
by default), and a `notBefore` within the last few days. Cross-check it against Caddy's own record
of the event:

```bash
sudo journalctl -u caddy --no-pager | grep -i "certificate obtained\|renew"
```

A renewal logged by your Caddy, at a time matching `notBefore`, for your hostname, from your CA, is
a renewal. Treat it as an interception if any of those disagree — an unexpected issuer, a hostname
that is not yours, a `notBefore` with no corresponding Caddy log line — and in that case do **not**
re-pin: you would be pinning the attacker. Investigate the VPS and the network path first.

**Step 2 — re-pin the machine.** Recompute and rewrite the pin with the §11 command; `swarm remote
init` overwrites `<stateDir>/remote/relay.json` in place.

**Step 3 — clear the old registration, then re-pair the phone.** This step is not optional and
cannot be skipped by editing anything on the phone. **A pin reaches a handset through the pairing
exchange (msg2) and through no other channel** — the pairing QR has no field for one, so there is
no way to hand a phone a corrected pin short of pairing again.

`swarm remote pair` on its own is **refused** here, because the phone you are recovering is still
registered: it answers *"a device is already paired (single-device v1); run `swarm remote devices`
to see its id, then `swarm remote revoke <device-id>` to unregister it, and pair again"*
(`internal/skeleton/pairing.go`). So the sequence is three commands, not one:

```bash
swarm remote devices            # find the stale device id
swarm remote revoke <device-id> # unregister it — needs the relay, hence step 2 first
swarm remote pair               # then scan the new QR on the phone
```

**Step 2 is a prerequisite of this step, not merely earlier in the list.** `swarm remote revoke`
reaches the relay over the CLI's own short-lived owner connection, which is pinned exactly like
every other machine dial path (`relay-runbook.md` §8b), so a machine still holding the stale pin
cannot revoke anything.

**And do not re-pair before step 2 for a second reason: it accuses the relay of an attack.** A
machine still holding the old pin puts that old pin in msg2, while the phone's pairing dial — which
is the dial that *fetches* the pin and therefore cannot itself be pinned — observes the new
certificate. The phone compares the two and refuses:

> the relay presented a certificate the machine did not pin; the pairing connection is being
> intercepted

(`mobile/relay.go`, reported as a failed pairing). That message is accurate about what the phone
observed and wrong about the cause, and read alongside step 1 it would send you hunting an
interception that is not happening. Re-pin the machine first and it does not appear.

**Step 4 — prevent the next one.** Confirm `tls { reuse_private_keys }` is present in
`/etc/caddy/Caddyfile`, `sudo systemctl reload caddy`, and re-run §9 to record the new baseline
pin. If a Caddy upgrade removed support for the directive, Caddy will fail to load the config and
say so — check `sudo systemctl status caddy` and `journalctl -u caddy` rather than assuming the
reload succeeded.

## 10. Verify the WebSocket upgrade end to end

A TLS handshake and a 426 both prove the HTTP layer works; drive one real upgrade to be sure a
proxy in front of Caddy (see §12) isn't quietly mangling it:

```bash
python3 - <<'PY'
import base64, hashlib, os, socket, ssl
HOST, PORT = "relay-swarm.dsfactory.org", 443
ctx = ssl.create_default_context()  # a real ACME cert: verify it properly, unlike the LAN runbook's pin-only check
tls = ctx.wrap_socket(socket.create_connection((HOST, PORT)), server_hostname=HOST)
key = base64.b64encode(os.urandom(16)).decode()
tls.sendall((f"GET / HTTP/1.1\r\nHost: {HOST}\r\n"
             "Upgrade: websocket\r\nConnection: Upgrade\r\n"
             f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n").encode())
resp = tls.recv(4096).decode(errors="replace")
accept = base64.b64encode(hashlib.sha1(
    (key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
print(resp.split("\r\n")[0], "| accept ok:", accept.lower() in resp.lower())
PY
```

Expect `HTTP/1.1 101 Switching Protocols | accept ok: True`.

## 11. Provision the machine

```bash
swarm remote init --relay-url wss://relay-swarm.dsfactory.org --relay-pin "$(
  openssl s_client -connect relay-swarm.dsfactory.org:443 -servername relay-swarm.dsfactory.org </dev/null 2>/dev/null |
    openssl x509 -pubkey -noout |
    openssl pkey -pubin -outform der |
    openssl dgst -sha256 -binary | openssl base64
)"
```

> **AMENDED BY ADR-016 (2026-08-15), landed with wave R2 (`ADR-016:5`).** `--relay-pin` with no
> `--relay-tls-policy` — exactly the command above — keeps its exact meaning under ADR-016 W1's
> legacy inference: it selects the **expert `pinned_spki` policy**, spelled explicitly as
> `--relay-tls-policy pinned_spki --relay-pin`. Nothing above requires you to change it. The
> **default** flow, needing nothing to compute and nothing to pin, is now:
>
> ```bash
> swarm remote init --relay-url wss://relay-swarm.dsfactory.org --relay-tls-policy webpki
> ```
>
> (also the default with the flag omitted entirely). And a machine inside the ADR-016
> **compatibility window** (W9) is **not** on `pinned_spki` — it is on `webpki` and publishes the
> same SPKI value as a **compatibility pin**, so an un-migrated build keeps working while a
> migrated build stops consulting the pin (W3):
>
> ```bash
> swarm remote init --relay-url wss://relay-swarm.dsfactory.org \
>   --relay-tls-policy webpki --relay-pin-compat "$(
>     openssl s_client -connect relay-swarm.dsfactory.org:443 -servername relay-swarm.dsfactory.org </dev/null 2>/dev/null |
>       openssl x509 -pubkey -noout |
>       openssl pkey -pubin -outform der |
>       openssl dgst -sha256 -binary | openssl base64
>   )"
> ```
>
> `--relay-pin` is mandatory under `pinned_spki` and refused under `webpki`; `--relay-pin-compat`
> is legal only under `webpki` (ADR-016 W1). `--relay-pin-next` (W5, expert-policy key rotation)
> is not built yet (tracked: bd agents-tracker-hggx.3.5.1). **The gate that would refuse a pinless
> `webpki` profile until every paired device has acknowledged the migration is not built yet
> either** (tracked: bd agents-tracker-hggx.3.5.3) — publish `--relay-pin-compat` explicitly
> during the compatibility window rather than relying on a refusal that does not exist yet.

Every one of these commands writes `<stateDir>/remote/relay.json` (`internal/remote/relaycfg`) at
0600, read by all three machine-side dial paths. Constraints enforced by `cmd/swarm/remote.go`
before anything is written to disk, today:

- **`--relay-url` is capped at 39 characters** (`pairing.MaxRelayURLLen`) because it rides verbatim
  into the pairing QR, which is why the domain in this doc's examples is kept short.
  `wss://relay-swarm.dsfactory.org` is 31 characters; omit an explicit `:443` (the default for `wss://`) to
  keep every character you can.
- **`--relay-pin` is refused outright on a `ws://` URL** (`validateRelayPin`) — cleartext presents
  no certificate, so a pin on it could never be checked. Once R2 ships, the same refusal extends to
  the `webpki` policy, where a compatibility pin is `--relay-pin-compat`, a different flag with a
  different meaning (ADR-016 W9).

## 12. Troubleshooting: the WebSocket upgrade doesn't pass through the proxy

This is the failure you are most likely to actually hit — §8's 426 test and §9's TLS handshake can
both succeed while a real client's upgrade still fails, because something *between* the client and
Caddy is stripping or mishandling the handshake. In rough order of likelihood:

- **A CDN or "orange-cloud" proxy sits in front of Caddy** (Cloudflare and similar). Many of these
  proxy plain HTTP/HTTPS fine but need WebSocket support explicitly enabled, or need the zone set to
  DNS-only ("grey-cloud") for this hostname. If §8 works over `curl` but §10's script fails or hangs,
  suspect this first — Caddy's `reverse_proxy` needs no configuration for WebSocket, so a failure
  here that Caddy alone would not produce points upstream of Caddy.
- **Caddy hasn't reloaded the Caddyfile you just edited.** `caddy reload`/`systemctl reload caddy`
  is required after every edit; a stale config keeps proxying to whatever the old file said, which
  looks identical to a DNS problem from the client side.
- **The Caddyfile's `reverse_proxy` target doesn't match `relay.config`'s `listen`.** A mismatch here
  fails as a `502 Bad Gateway`, not a WebSocket-specific error — check
  `systemctl status swarm-relay` and confirm the two ports agree.
- **ACME issuance itself failed** (port 80 blocked, so the default `HTTP-01` challenge can't
  complete; or DNS from §7 hasn't propagated to the resolvers Let's Encrypt's validation servers
  use, even though it resolves for you). Check `journalctl -u caddy --no-pager -n 100` for ACME
  errors; Caddy serves a locally-trusted (not publicly-trusted) certificate while it cannot get a
  real one, which §9's pin computation would silently pick up as a wrong-looking pin rather than an
  obvious error — always compute the pin fresh after confirming §8's 426 test succeeds over a
  connection with no certificate warnings.
- **A corporate/local network the *phone or CLI machine* sits behind strips WebSocket upgrades or
  the `Sec-WebSocket-*` headers.** This is outside the VPS entirely; rule out everything above first
  by running §10's script from a different network.

---

## 13. Backup and restore

**Added by wave R2 (playbook §6.5), 2026-08-15.** The relay binary itself supports `backup` and
`restore` subcommands (`docs/operations/relay-runbook.md` §11 has the full mechanism and
guarantees — read that first if you have not). On this systemd-managed deployment, the operator
step is stopping the service around the backup/restore call, since a running relay holds its store
file's OS lock for as long as the unit is up:

```bash
# Backup: brief stop, snapshot, restart. mailbox delivery pauses for the stop's duration; nothing
# is lost -- a paused gateway/phone resumes from its own cursor once the relay is back.
sudo systemctl stop swarm-relay
sudo -u swarm-relay /opt/swarm-relay/bin/swarm-relay backup \
  --config /etc/swarm-relay/relay.config /var/backups/swarm-relay/relay-$(date +%F).db
sudo systemctl start swarm-relay

# Restore: the service must already be stopped -- `restore` refuses otherwise (relay-runbook.md §11).
sudo systemctl stop swarm-relay
sudo -u swarm-relay /opt/swarm-relay/bin/swarm-relay restore \
  --config /etc/swarm-relay/relay.config /var/backups/swarm-relay/relay-2026-08-15.db
sudo systemctl start swarm-relay
```

**Upgrade order before a planned restore:** deploy the incarnation-aware phone and machine gateway
consumers first, upgrade the relay server second, and restore only after all affected components are
on that build. The added wire fields are backward-compatible during this rollout, but an older
consumer has no durable mailbox-incarnation binding and cannot perform the safe automatic rewind.
After the restored relay starts, an upgraded consumer sees the new incarnation, discards queued
acks from the retired mailbox generation, rewinds only its relay cursor (not its authenticated
replay high-waters), and drains again from zero. Already-applied frames are authenticated and
compacted without repeating their effects.

One limit is deliberate: a queued frame encrypted under a content-key epoch the consumer has
already retired cannot be authenticated as either new content or a safe duplicate. A full page or
mailbox tail of those frames may block the re-drain until the default seven-day retention cap
removes it or an operator explicitly purges/reinitialises the affected relay state and re-pairs.
`swarm remote revoke <device-id>` is the supported destructive purge for that handset's
machine-to-phone route; there is no narrow CLI purge for retired phone-to-machine frames in the
machine mailbox. Do not edit the bbolt database directly. See the relay runbook §11, “Restore
compatibility and upgrade order,” for the exact flow and scope before choosing the destructive
recovery.

Run as `swarm-relay` (the `sudo -u`), not root: the store file and its directory are owned by that
user (`WorkingDirectory=/var/lib/swarm-relay` in the unit, §5), and a root-owned backup or a
root-written restore leaves permissions the service can no longer open on its next start. The
destination directory (`/var/backups/swarm-relay` above) needs to exist and be writable by that
user before the first backup; it is not created by anything in §3.

**Restore undoes revocations performed after the backup.** `restore` replaces the whole store,
revocation state included — relay-runbook.md §11 has the full explanation and a pinned test. If you
restore a backup, re-run `swarm remote revoke` for anything that was revoked after that backup was
taken, especially after recovering from a lost or stolen phone.

---

## 14. Docker Compose alternative

**Added by wave R2 (playbook §6.5), 2026-08-15.** Steps 1-12 above stand the relay up directly on
a systemd-managed VPS. `deploy/relay/Dockerfile` and `deploy/relay/docker-compose.yml` package the
same two processes — swarm-relay and Caddy — as containers instead, with persistent named volumes
for the bbolt store and Caddy's ACME state, a Docker `HEALTHCHECK` wired to the relay's own
`/healthz`/`/readyz` (§14a), `restart: unless-stopped`, memory/pids resource limits, and
`json-file` log rotation. Nothing about the relay's own protocol or TLS story changes: Caddy is
still the entire TLS story (the intro above), and the relay still authenticates nothing at the
transport layer.

```bash
cd deploy/relay
cp relay.config.example relay.config   # the shipped defaults already match the compose
                                        # topology's listen/admin_listen addresses
# production hostname is relay-swarm.dsfactory.org (same requirement as §7)
export RELAY_VERSION=v0.13.27          # replace with the reviewed immutable release tag
docker compose build
docker compose up -d
docker compose ps                      # swarm-relay should read "healthy"
```

Read `deploy/relay/docker-compose.yml`'s own header comment before changing the topology: Caddy
joins swarm-relay's network namespace on purpose (`network_mode: "service:swarm-relay"`), which is
what lets the unmodified `Caddyfile` and `relay.config.example` — written for the bare-VPS
`127.0.0.1` topology above — work here with no address changed.

§§1-2, 6-12's steps for cross-compiling (the image build replaces this), DNS, the SPKI pin (if you
have not migrated to `webpki`, ADR-016), and machine provisioning are identical either way — only
how the two processes are packaged and supervised differs.

### 14a. Health and readiness endpoints

**Added by wave R2 (playbook §6.5), 2026-08-15.** The relay serves `/healthz` (process up) and
`/readyz` (bbolt store writable, public listener accepting, free disk above
`quotas.disk_free_min_bytes`, durable objects below `max_durable_objects`, and bbolt below
`max_db_bytes`) plus aggregate `/metrics` on `admin_listen` — a SEPARATE, loopback-only port from the public
`listen` address, refused outright by `Start` if pointed anywhere else
(`internal/remote/relay/health.go`). This is not new attack surface on the public protocol: the
doctor rule (playbook §6.5, `swarm relay doctor`) — no privileged unauthenticated endpoint on the
public listener — applies to health too, and `admin_listen` is exactly as unreachable from outside
as the systemd deployment's own `listen` address is. Under Docker Compose, the `HEALTHCHECK`
directive execs `swarm-relay healthcheck --config ...` (distroless has no shell/curl/wget for an
external probe to use) which reads `admin_listen` straight out of the same config file and GETs its
`/readyz`; the same subcommand doubles as a manual check on the systemd deployment:

```bash
sudo -u swarm-relay /opt/swarm-relay/bin/swarm-relay healthcheck --config /etc/swarm-relay/relay.config
```

Falling below `quotas.disk_free_min_bytes` (1 GiB shipped default) fails `/readyz` and logs one
bounded warning per transition into the low state — never once per poll, since an orchestrator
healthcheck hits this every few seconds for the container's whole life. Reaching the global durable
object or database ceiling also fails readiness. `/metrics` exposes aggregate occupancy, growth,
and refusal reasons only; it contains no relay identity, mailbox, token, source address, or payload.

### 14b. The generated operator secret

**Added by wave R2 (playbook §6.5), 2026-08-15.** If `operator_secret_file` is set (the shipped
example points it at the same state directory/named volume as `db_path`) and the file does not yet
exist, the relay generates a high-entropy secret at first boot and persists it there at `0600`. It
is diagnostic/admin authority for the `swarm relay doctor` capability — **not** a substitute for
Web-PKI server authentication (playbook §6.5) — and is never logged. Leave `operator_secret_file`
out of your config entirely to keep diagnostics disabled. `docs/operations/relay-runbook.md` §12
has the doctor command itself — run it once you've deployed to prove DNS/TCP+TLS/WebSocket/protocol/
mailbox/storage all work end to end, rather than trusting §§8-10 above in isolation. The storage
step (`diag_status`) reports the SAME store-writable/free-disk verdict `/readyz` reports above, over
the public `wss://` connection, for an operator who has no `admin_listen` access to this host.

---

## What this deliberately does not cover

Per `docs/specifications/remote-phaseB-requirements.md:664`: **backup/restore, log rotation,
health checks and monitoring, TLS renewal automation beyond Caddy's own default behavior, resource
limits, and cross-version compatibility are all Phase C.**

> **AMENDED (wave R2, playbook §6.5):** backup/restore shipped — see §13. The relay itself now
> serves `/healthz`/`/readyz` and a disk-space alarm on any deployment that sets `admin_listen`
> (`relay.config.example` does) — see §14a. Resource limits, `json-file` log rotation, and an
> orchestrator that actually RESTARTS the container on a failed check ship through the Docker
> Compose bundle specifically — see §14. The bare-VPS systemd unit (§5) restarts on process crash
> (`Restart=on-failure`) but is not wired to this readiness signal. TLS renewal automation beyond
> Caddy's own default behavior and cross-version compatibility are still Phase C / a separate R2
> slice and still absent here.

This runbook gets a relay reachable over
real `wss://` and documents what `swarm remote init` needs — nothing here should be read as a claim
of production operability beyond that.

**The handset is provisioned by pairing, not by this document, and that is sufficient.** This
runbook configures the **machine** — exactly as the LAN runbook's §4a does.

> **AMENDED BY ADR-016 (2026-08-15), landed with wave R2 (`ADR-016:5`):** The paragraph this
> replaces described the pin as something every phone gets from pairing, which remains exactly
> true under the expert `pinned_spki` policy (or the compatibility window): the machine's
> published payload still carries the pin, the phone still persists it (`relay_spki_pin`) and
> dials with it thereafter, and the QR still has no pin field of its own — by design, unchanged
> from before (ADR-007 B33/B34, ADR-016 W7). A machine that adopts the **default** `webpki` policy
> instead publishes `relay_tls_policy` and `relay_host` in `MachinePayload`, with no
> `RelaySPKIPin` round trip because there is no pin to carry.

The one thing that has **no** channel is changing a pin a handset already holds under the expert
policy — see §9a, whose third step is re-pairing for exactly this reason.
