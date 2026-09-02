# Public push-gateway deployment

This playbook prepares `push-swarm.dsfactory.org` as a service separate from
`relay-swarm.dsfactory.org`. It does not authorize a deployment by itself. The cellular/background
acceptance gate at the end must pass before changing the mobile production endpoint.

## Production identity and secrets

The only accepted production identity is:

- Google Cloud project `swarm-8404f`, project number `733314021126`;
- Android package `dev.swarm.phone`;
- Play App Signing certificate SHA-256 (base64url)
  `hz8YTGhTTgpYccjMiQDrhx5HcddqRsTu1HRcmhhknmU`;
- hostname `push-swarm.dsfactory.org`.

Do not reuse any Play-publishing/upload credential as runtime authority. Use a dedicated VM service
account attached to the push-gateway VM and Application Default Credentials. Grant only the FCM
send and Play Integrity decode permissions required by this service. If the runtime cannot use an
attached identity, store one narrow `swarm-8404f` runtime key in Secret Manager and pass its
read-only mount with `-google-credentials`; never bake it into an image, archive, Compose file, or
source checkout.

Every cloud command must name the project explicitly. Before provisioning, fail closed on project
identity rather than trusting the operator's ambient `gcloud` default:

```bash
gcloud projects describe swarm-8404f --project=swarm-8404f \
  --format='value(projectId,projectNumber)'
# Must print: swarm-8404f 733314021126
```

Use `--project=swarm-8404f` on repository, IAM, Secret Manager, Compute Engine, logging, and
monitoring commands. Abort if either value differs. The current project may have no VM or Artifact
Registry repository yet; create those explicitly through reviewed infrastructure changes, not an
interactive command copied from an ambient project.

## Host and network

Use a dedicated, patched Linux VM with a persistent encrypted disk. Permit inbound TCP 80/443 only;
do not expose 8450 or 8451. Point the DNS `A`/`AAAA` records for `push-swarm.dsfactory.org` at it.
Caddy obtains and renews the public certificate and proxies to `127.0.0.1:8450`. The health,
readiness, and metrics listener stays on `127.0.0.1:8451` and must never be routed by Caddy.

The supplied Compose topology shares one network namespace so these loopback bindings remain real.
Copy `deploy/pushgw/pushgw.env.example` to `.env` and pin `PUSHGW_VERSION` to an immutable release
tag. The base bundle uses the VM's attached `swarm-8404f` identity through ADC, so it mounts no
long-lived JSON key. Keep `.env` mode 0600. Then validate before starting:

```bash
cd deploy/pushgw
docker compose config
docker compose build --pull
```

The image is also a separately archived `swarm-pushgw` release artifact. Do not use `latest`.
Verify the release checksum signature through the normal Swarm release procedure.

## Fail-closed readiness

`/healthz` proves only that the process is alive. `/readyz` additionally requires the database and
its exact adjacent AEAD key to be writable/readable, the retention worker to be running, and real
production FCM sender, Play Integrity verifier, package/project/certificate configuration to have
constructed successfully. Missing or malformed production authority must remain 503. A transient
FCM or Play API failure affects the request that observed it but does not flap readiness: readiness
does not probe either upstream.

The command constructs one OAuth token source with both required scopes, then constructs FCM and
Play Integrity clients from it. It intentionally stays unready while any production
verifier/configuration seam is absent.

```bash
docker compose up -d
docker compose ps
docker exec swarm-pushgw /swarm-pushgw healthcheck \
  -url http://127.0.0.1:8451/readyz
```

## Acceptance before endpoint cutover

Use a Play-installed production-signed build, not a sideload. With Wi-Fi disabled, verify fresh
pairing/registration, an FCM wake from a backgrounded and force-stopped app, relay reconnect,
outgoing FIFO messages, idempotent retry, and revoke. Repeat after phone reboot, gateway restart,
and VM restart. Confirm the phone never needs LAN reachability, readiness stays 200 through a
short upstream outage, and no token, push address, attestation token, request path, or payload is
present in application/Caddy logs or metrics.

Do not update the app's production URL until this entire gate and the incident/restore drill pass.
