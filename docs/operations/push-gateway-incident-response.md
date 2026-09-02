# Push-gateway incident response

Preserve evidence without copying secrets into tickets or chat. Record timestamps, aggregate status
codes/operation labels, release digest, VM identity, and readiness reasons. Do not record raw paths,
push addresses, FCM tokens, capability values, request bodies, or Play tokens.

## Credential or token exposure

Remove the host from service, revoke the dedicated `swarm-8404f` runtime identity/key, audit its IAM
use in project `swarm-8404f`, and issue a replacement with the same narrow roles. Never substitute
a Play publishing/upload credential. If the encrypted database or AEAD key escaped,
rotate the runtime identity and treat all installations as requiring re-registration; possession of
both DB and key exposes stored FCM tokens.

## Database/key loss or corruption

Stop writers. Preserve the DB and `.key` together, plus the last known-good encrypted backup. Follow
the restore runbook into absent target paths and validate readiness before traffic returns. Never
generate a fresh key beside an old database: that makes its tokens permanently undecryptable.

## Abuse or denial of service

Keep 8450/8451 firewalled from the internet; only 80/443 reach Caddy and only 8450 is proxied. Use
aggregate quota/refusal metrics to distinguish source, registration, allocation, and per-address
pressure. Tighten limits deliberately, block abusive sources at the edge, and preserve the bounded
retention worker. Do not disable attestation or accept an unlicensed/unrecognized signing
certificate to restore traffic.

## Provider or TLS failure

FCM/Play failures are request-scoped and should not flap local readiness. Validate Google service
health, IAM, project number, package, and the Play App Signing certificate allowlist. For TLS/DNS,
verify `push-swarm.dsfactory.org`, Caddy renewal state, clock, and firewall; never expose plaintext
8450 as a workaround. After containment, repeat the off-Wi-Fi/background acceptance gate and write
a secret-free timeline and corrective-action report.
