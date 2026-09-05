# Push-gateway v2 incident response

Record timestamps, aggregate status/operation counts, image and secret version identifiers,
runtime identity and readiness reasons. Do not copy secret values, raw paths, push
addresses, FCM tokens, capabilities, request bodies or Play tokens into tickets or chat.

## Credential or token exposure

Fence the affected hosted service through an explicitly authorized operator action.
Audit and replace the dedicated runtime identity or compromised secret material in
`swarm-8404f`; never substitute a Play publishing credential. Determine which encrypted
token records and key versions were exposed. Possession of both ciphertext and its key
exposes those tokens; require fresh enrollment where authority cannot be recovered safely.

## State or key loss

Fence writers and preserve available Firestore recovery copies and referenced key versions.
Follow the [v2 recovery contract](push-gateway-runbook.md) in isolated state. Do not create
a replacement token key to conceal a missing version, and do not resume from a snapshot
before revocation reconciliation. A successful readiness read does not prove recovery.

## Abuse or denial of service

Use aggregate quota/refusal counts to identify registration, allocation and wake pressure.
Review owner admission and tighten bounded limits deliberately. Keep the admin listener
private. Do not trust arbitrary forwarded headers, disable attestation/signatures/revocation,
or discard required tombstones to restore availability. Preserve bounded physical cleanup;
budget alerts and maximum instances are not substitutes for application limits.

## Provider or TLS failure

Check the dedicated project's identity/IAM, package and signing-certificate binding without
printing credentials. FCM/Play failures remain request-scoped. Inspect the actual direct
HTTPS service endpoint and provider TLS; do not expose plaintext ingress or introduce an
old-origin proxy as a workaround. After containment, repeat hosted authentication, revoke
and phone background/reconnect gates and record a secret-free corrective-action report.

These are incident procedures, not blanket permission to deploy, rotate live secrets,
reset a phone, delete cloud resources or change IAM during source implementation.
