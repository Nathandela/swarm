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
certificate and documents the two things `PB-OPS-1`'s acceptance criteria actually asks for: a
`wss://` URL and an SPKI pin, both fed to `swarm remote init`.

**Why there are two processes.** `swarm-relay` serves **plain `ws://` only** — confirmed directly
in `internal/remote/relay/server.go`: `Start` does `s.url = "ws://" + ln.Addr().String()`
unconditionally, and `Config.TLSMode` (`internal/remote/relay/config.go`) is parsed but read by
nothing that serves. `docs/operations/relay-runbook.md` section 0 says the same. So a reverse
proxy terminating real TLS in front of a loopback-only relay is not one option among several — it
is the only way to get `wss://` at all.

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

Read the `_comment_quotas` note in that file before you tighten any quota: this deployment topology
(Caddy proxying to a loopback relay) collapses every client's per-source rate window into one
shared bucket, because the relay keys rate limits off the raw TCP peer address it accepts
(`defaultSourceKey`, `internal/remote/relay/server.go`) and `cmd/swarm-relay` never installs an
`X-Forwarded-For`-aware override. Harmless at v1's single-machine-plus-single-phone scope; a
reason to revisit before fronting genuinely independent clients through this same proxy.

```bash
sudo chmod 0644 /etc/swarm-relay/relay.config
```

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

Edit `deploy/relay/Caddyfile`, replacing `relay.example.com` with your real domain — **keep it
short** (§11 explains why, precisely), then copy it to `/etc/caddy/Caddyfile`:

```bash
sudo systemctl reload caddy
```

Create an `A` (or `AAAA`) record at your DNS provider pointing that domain at the VPS's public IP,
and wait for it to resolve:

```bash
dig +short relay.example.com
```

Caddy will not obtain a certificate for a name that does not yet resolve to this host.

## 8. Verify reachability

```bash
curl -i --http1.1 --max-time 5 https://relay.example.com/
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

Unlike the LAN runbook's self-signed certificate, there is no local `relay.crt` file in this
topology — Caddy manages the certificate and its private key itself. Compute the pin the same way
`docs/operations/relay-runbook.md` section 6 verifies one: against the **live** endpoint.

```bash
openssl s_client -connect relay.example.com:443 -servername relay.example.com </dev/null 2>/dev/null |
  openssl x509 -pubkey -noout |
  openssl pkey -pubin -outform der |
  openssl dgst -sha256 -binary | openssl base64
```

This is SHA-256 over the certificate's SubjectPublicKeyInfo — the value `relaycfg.Config.Pin()`
(`internal/remote/relaycfg/relaycfg.go`) expects: base64 of exactly 32 raw bytes. It is **not** the
certificate fingerprint.

**Caddy's automatic HTTPS renews the certificate on its own schedule and, per Caddy's default
behavior, reuses the same private key across a renewal** unless the key is deliberately rotated —
so the SPKI pin computed here is expected to survive routine renewal the same way
`docs/operations/relay-runbook.md` section 8 requires. This project has not run that renewal
against a live Caddy instance and is not claiming to have verified it; treat it as the same
necessary-but-unverified claim section 8 already makes about `certbot --reuse-key`, and re-run
this command to confirm the pin is unchanged after your first renewal.

## 10. Verify the WebSocket upgrade end to end

A TLS handshake and a 426 both prove the HTTP layer works; drive one real upgrade to be sure a
proxy in front of Caddy (see §12) isn't quietly mangling it:

```bash
python3 - <<'PY'
import base64, hashlib, os, socket, ssl
HOST, PORT = "relay.example.com", 443
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
swarm remote init --relay-url wss://relay.example.com --relay-pin "$(
  openssl s_client -connect relay.example.com:443 -servername relay.example.com </dev/null 2>/dev/null |
    openssl x509 -pubkey -noout |
    openssl pkey -pubin -outform der |
    openssl dgst -sha256 -binary | openssl base64
)"
```

This writes `<stateDir>/remote/relay.json` (`internal/remote/relaycfg`) at 0600, read by all three
machine-side dial paths. Two hard constraints, both enforced by `cmd/swarm/remote.go` before
anything is written to disk:

- **`--relay-url` is capped at 39 characters** (`pairing.MaxRelayURLLen`) because it rides verbatim
  into the pairing QR, which is why the domain in this doc's examples is kept short.
  `wss://relay.example.com` is 23 characters; omit an explicit `:443` (the default for `wss://`) to
  keep every character you can.
- **A pin is refused outright on a `ws://` URL** (`validateRelayPin`) — cleartext presents no
  certificate, so a pin on it could never be checked.

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

## What this deliberately does not cover

Per `docs/specifications/remote-phaseB-requirements.md:664`: **backup/restore, log rotation,
health checks and monitoring, TLS renewal automation beyond Caddy's own default behavior, resource
limits, and cross-version compatibility are all Phase C.** This runbook gets a relay reachable over
real `wss://` and documents the URL/pin pair `swarm remote init` needs — nothing here should be
read as a claim of production operability beyond that.

The Android handset side of certificate pinning is a separate, already-tracked gap: per
`docs/operations/relay-runbook.md`, the pairing QR has no field for a pin today, so a release
handset refuses every `wss://` dial with `relay.ErrPinRequired` regardless of what this document
provisions on the machine side (ADR-007 residual 1.9). This document provisions the **machine**
only, exactly as the LAN runbook's §4a does.
