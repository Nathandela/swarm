# Retained GCP inventory

`deploy/gcp` is the source inventory for the retained Pushgw VM and the GCP resources it uses in
`swarm-8404f`. It is not the relay-v2 deployment path: relay-v2 is owned by Cloudflare Worker and
Durable Object infrastructure.

The relay-v1 VM, data disk, external address, runtime identity, telemetry grants, operator binding,
DNS output, and image-deployment instructions are intentionally absent from this Terraform source.
That source change does **not** change any live cloud resource. Existing live resources remain until
a separately authorized, reviewed Terraform plan is applied; do not infer their live state from this
repository.

The retained resources are:

| Purpose | Terraform resources |
|---|---|
| Push gateway host | `swarm-pushgw-prod`, `swarm-pushgw-data`, `swarm-pushgw-ip`, `swarm-push-runtime` |
| Shared connectivity | `swarm-public`, `swarm-public-euw6`, public HTTPS firewall, IAP SSH firewall |
| Shared recovery | `swarm-daily-snapshots`, retained Pushgw disk attachment |
| Minimal operations access | IAP tunnel, OS Login, and Service Account User on the Pushgw runtime identity |

The Pushgw VM remains shielded, has deletion protection and Terraform `prevent_destroy`, and uses
the dedicated runtime identity. Its FCM permission is the narrow custom role; telemetry uses logging
and monitoring writer roles. Operators receive exactly three least-privilege IAM grants per
principal: IAP tunnel access, OS Login, and `roles/iam.serviceAccountUser` on that attached
identity. No basic project role, default Compute identity, secret material, startup script, or
public SSH rule belongs in this stack.

The external DNS provider—not Cloud DNS—owns `dsfactory.org`; `terraform output -json dns_a_records`
reports only the retained Pushgw record. The snapshot schedule is daily at 03:00 UTC, retains 14
days in `eu`, and keeps automatic snapshots if the source disk is removed.

This repository does not provide a VM deployment procedure. The v2 Pushgw contract is
[push-gateway-deploy.md](push-gateway-deploy.md), and its runbook is
[push-gateway-runbook.md](push-gateway-runbook.md). Review a saved Terraform plan, state ownership,
and the live-resource retirement scope separately before any cloud operation.
