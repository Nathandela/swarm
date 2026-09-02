# `swarm-8404f` production infrastructure

`deploy/gcp` is an import-first Terraform description of the two public services that already
exist in project `swarm-8404f`. It is not a generic example and must not be pointed at another
project. The configuration deliberately contains no service secret, credential, bootstrap script,
or mutable container tag.

| Service | VM | Zone | Fixed public IPv4 | Runtime identity | Data disk |
|---|---|---|---|---|---|
| Relay | `swarm-relay-prod` | `europe-west6-a` | `34.65.198.161` | `swarm-relay-runtime@swarm-8404f.iam.gserviceaccount.com` | `swarm-relay-data` |
| Push gateway | `swarm-pushgw-prod` | `europe-west6-a` | `34.65.34.57` | `swarm-push-runtime@swarm-8404f.iam.gserviceaccount.com` | `swarm-pushgw-data` |

Both VMs are `e2-small`, use Shielded VM secure boot/vTPM/integrity monitoring, block project SSH
keys, and require OS Login over IAP. Only TCP 80/443 is public. TCP 22 is limited to Google's
`35.235.240.0/20` IAP forwarding range; application and admin ports remain loopback-only. Each
20GB `pd-balanced` data disk has a daily 03:00 UTC snapshot, retained for 14 days in the `eu`
multi-region and kept if the source disk is removed. VM deletion protection and Terraform
`prevent_destroy` are independent safeguards.

For every principal in `operator_members`, the stack manages exactly four least-privilege IAM grants
per operator: IAP tunnel access, OS Login, and `roles/iam.serviceAccountUser` on each of the two
runtime service accounts attached to the VMs. The last two grants are required by OS Login when a
VM has an attached service account. They do not grant the operator runtime API permissions and
avoid relying on a project Owner role for SSH access.

## 1. Preconditions and remote state

Use Terraform 1.7 or newer, the Google provider version selected by the committed lock file, a
reviewed operations group, and a separate versioned GCS state bucket. The state bucket is
intentionally outside this stack: a destroy or a bad plan must not delete the state needed for
recovery. Grant its reader/writer role only to the Terraform operators; use ADC or Workload
Identity Federation, never a downloaded service-account key.

```bash
cd deploy/gcp
cp backend.hcl.example backend.hcl
# Set the separately administered state bucket in backend.hcl.
cp terraform.tfvars.example terraform.tfvars
# Replace the operations group. Do not add an owner/basic-role grant here.
terraform init -backend-config=backend.hcl
terraform validate
```

Confirm both the project ID and number before touching state:

```bash
gcloud projects describe swarm-8404f --project=swarm-8404f \
  --format='value(projectId,projectNumber)'
# Exact result: swarm-8404f 733314021126
```

## 2. Adopt the existing resources before the first plan

This module is **import-first**. Do not run `terraform apply` before every existing resource is in
the selected remote state. An import changes Terraform state only; it does not mutate the live
resource. Run the imports from `deploy/gcp` after `terraform init`.

```bash
# Enabled APIs. Add the two newly declared access-path APIs after import with a reviewed plan.
for api in compute.googleapis.com fcm.googleapis.com iam.googleapis.com \
  iamcredentials.googleapis.com logging.googleapis.com monitoring.googleapis.com \
  playintegrity.googleapis.com; do
  terraform import "google_project_service.required[\"${api}\"]" "swarm-8404f/${api}"
done

# Dedicated runtime identities and the narrow FCM role.
terraform import google_service_account.relay \
  projects/swarm-8404f/serviceAccounts/swarm-relay-runtime@swarm-8404f.iam.gserviceaccount.com
terraform import google_service_account.pushgw \
  projects/swarm-8404f/serviceAccounts/swarm-push-runtime@swarm-8404f.iam.gserviceaccount.com
terraform import google_project_iam_custom_role.pushgw_runtime \
  projects/swarm-8404f/roles/swarmPushGatewayRuntime

terraform import 'google_project_iam_member.runtime_log_writer["relay"]' \
  'swarm-8404f roles/logging.logWriter serviceAccount:swarm-relay-runtime@swarm-8404f.iam.gserviceaccount.com'
terraform import 'google_project_iam_member.runtime_log_writer["pushgw"]' \
  'swarm-8404f roles/logging.logWriter serviceAccount:swarm-push-runtime@swarm-8404f.iam.gserviceaccount.com'
terraform import 'google_project_iam_member.runtime_metric_writer["relay"]' \
  'swarm-8404f roles/monitoring.metricWriter serviceAccount:swarm-relay-runtime@swarm-8404f.iam.gserviceaccount.com'
terraform import 'google_project_iam_member.runtime_metric_writer["pushgw"]' \
  'swarm-8404f roles/monitoring.metricWriter serviceAccount:swarm-push-runtime@swarm-8404f.iam.gserviceaccount.com'
terraform import google_project_iam_member.pushgw_runtime \
  'swarm-8404f projects/swarm-8404f/roles/swarmPushGatewayRuntime serviceAccount:swarm-push-runtime@swarm-8404f.iam.gserviceaccount.com'

# Network, the only two ingress rules, and reserved public addresses.
terraform import google_compute_network.public \
  projects/swarm-8404f/global/networks/swarm-public
terraform import google_compute_subnetwork.public_euw6 \
  projects/swarm-8404f/regions/europe-west6/subnetworks/swarm-public-euw6
terraform import google_compute_firewall.public_web \
  projects/swarm-8404f/global/firewalls/swarm-public-web
terraform import google_compute_firewall.iap_ssh \
  projects/swarm-8404f/global/firewalls/swarm-iap-ssh
terraform import google_compute_address.relay \
  projects/swarm-8404f/regions/europe-west6/addresses/swarm-relay-ip
terraform import google_compute_address.pushgw \
  projects/swarm-8404f/regions/europe-west6/addresses/swarm-pushgw-ip

# Durable disks and their one shared regional snapshot schedule.
terraform import google_compute_resource_policy.daily_snapshots \
  projects/swarm-8404f/regions/europe-west6/resourcePolicies/swarm-daily-snapshots
terraform import google_compute_disk.relay_data \
  projects/swarm-8404f/zones/europe-west6-a/disks/swarm-relay-data
terraform import google_compute_disk.pushgw_data \
  projects/swarm-8404f/zones/europe-west6-a/disks/swarm-pushgw-data
terraform import google_compute_disk_resource_policy_attachment.relay \
  projects/swarm-8404f/zones/europe-west6-a/disks/swarm-relay-data/swarm-daily-snapshots
terraform import google_compute_disk_resource_policy_attachment.pushgw \
  projects/swarm-8404f/zones/europe-west6-a/disks/swarm-pushgw-data/swarm-daily-snapshots

# Import the VMs last, after their dependencies are represented in state.
terraform import google_compute_instance.relay \
  projects/swarm-8404f/zones/europe-west6-a/instances/swarm-relay-prod
terraform import google_compute_instance.pushgw \
  projects/swarm-8404f/zones/europe-west6-a/instances/swarm-pushgw-prod
```

`iap.googleapis.com` and `oslogin.googleapis.com` were not explicit services in the hand-built
inventory. Leave them unimported: the first reviewed apply enables them without disabling anything
on destroy. IAM resources for `operator_members` are new and therefore are not imported unless the
same explicit member/role pairs already exist. A plan must show four grants for each operator: one
IAP tunnel grant, one project OS Login grant, and one Service Account User grant on each attached
runtime identity. It must not add `roles/owner`, `roles/editor`, or a project-wide Service Account
User grant.

If any of those exact bindings already exists, import it rather than allowing Terraform to attempt
to recreate it. For example, after replacing the sample principal, the per-service-account imports
for one operator are:

```bash
terraform import \
  'google_service_account_iam_member.operator_service_account_user["relay:user:operator@example.com"]' \
  'projects/swarm-8404f/serviceAccounts/swarm-relay-runtime@swarm-8404f.iam.gserviceaccount.com roles/iam.serviceAccountUser user:operator@example.com'
terraform import \
  'google_service_account_iam_member.operator_service_account_user["pushgw:user:operator@example.com"]' \
  'projects/swarm-8404f/serviceAccounts/swarm-push-runtime@swarm-8404f.iam.gserviceaccount.com roles/iam.serviceAccountUser user:operator@example.com'
```

Use the corresponding `google_project_iam_member.operator_iap_tunnel` and
`google_project_iam_member.operator_os_login` addresses if the project-level pairs already exist.
Import only bindings confirmed by `gcloud projects get-iam-policy` and
`gcloud iam service-accounts get-iam-policy`; imports are not a way to create missing authority.

Now inspect, save, and review the complete plan:

```bash
terraform plan -out=production.tfplan -detailed-exitcode
terraform show -no-color production.tfplan
```

Exit 0 means no drift; exit 2 means a change exists; exit 1 is an error. Never apply a plan that
replaces a VM, address, network, or disk. If an imported boot disk produces a provider-normalization
difference, reconcile the configuration and state under review—do not accept a replacement because
deletion protection happened to stop it. Apply only the saved, reviewed plan:

```bash
terraform apply production.tfplan
```

## 3. Required ADC authorization probe and push VM scope reconciliation

The hand-built push VM originally exposed only the standard `cloud-platform` OAuth ceiling. A live
probe proved FCM authorization but Play Integrity returned 403
`ACCESS_TOKEN_SCOPE_INSUFFICIENT`. The Terraform resource therefore retains `cloud-platform` and
adds the explicit `https://www.googleapis.com/auth/playintegrity` scope. This is an in-place service
account update, but Compute Engine must stop the VM while changing its OAuth ceiling.

After the VM and service account have been imported, either apply the reviewed Terraform plan
(the resource has `allow_stopping_for_update = true`) or perform the exact one-time reconciliation
below during a maintenance window. The manual path is useful when bringing the live host into
agreement before the first zero-drift plan:

```bash
gcloud compute instances stop swarm-pushgw-prod \
  --project=swarm-8404f --zone=europe-west6-a
gcloud compute instances set-service-account swarm-pushgw-prod \
  --project=swarm-8404f --zone=europe-west6-a \
  --service-account=swarm-push-runtime@swarm-8404f.iam.gserviceaccount.com \
  --scopes=cloud-platform,https://www.googleapis.com/auth/playintegrity
gcloud compute instances start swarm-pushgw-prod \
  --project=swarm-8404f --zone=europe-west6-a
```

Do not detach or substitute the service account. Refresh state and require the next Terraform plan
to be zero-drift after the manual path. Before starting or restarting the public service after any
Terraform/IAM change, prove that the attached `swarm-push-runtime` identity can reach **both** APIs
from the VM. This is mandatory; metadata identity plus a healthy process is not proof of usable
upstream authority.

Connect only through IAP:

```bash
gcloud compute ssh swarm-pushgw-prod --project=swarm-8404f \
  --zone=europe-west6-a --tunnel-through-iap
```

On the VM, obtain the attached-identity token and make non-delivering validation calls:

```bash
TOKEN="$(curl --fail --silent --show-error \
  -H 'Metadata-Flavor: Google' \
  'http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token' \
  | jq -er .access_token)"

# validate_only plus an invalid target never sends a wake. 400/404 proves the request
# passed OAuth/IAM and reached payload validation; 401/403 is a deployment blocker.
fcm_status="$(curl --silent --output /tmp/fcm-probe.json --write-out '%{http_code}' \
  -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -X POST 'https://fcm.googleapis.com/v1/projects/swarm-8404f/messages:send' \
  --data '{"validate_only":true,"message":{"token":"invalid-probe-token"}}')"

# An invalid integrity token likewise exercises OAuth/IAM without accepting a verdict.
play_status="$(curl --silent --output /tmp/play-probe.json --write-out '%{http_code}' \
  -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -X POST 'https://playintegrity.googleapis.com/v1/dev.swarm.phone:decodeIntegrityToken' \
  --data '{"integrity_token":"invalid-probe-token"}')"

printf 'FCM=%s PlayIntegrity=%s\n' "${fcm_status}" "${play_status}"
test "${fcm_status}" != 401 && test "${fcm_status}" != 403
test "${play_status}" != 401 && test "${play_status}" != 403
rm -f /tmp/fcm-probe.json /tmp/play-probe.json
```

If either API returns 401/403, do not serve traffic. Amend the VM OAuth scopes explicitly and
review the plan; do not compensate with Owner, Editor, Firebase Admin, or a JSON key.

## 4. Deploy the exact attested images

Each `vMAJOR.MINOR.PATCH` release attaches `container-images.json`. It names both images by
`@sha256:` digest; a moving alias is never deployment authority.

```bash
release=v0.13.22
# The base Compose files require these build/version inputs even though the
# digest-only override below remains the deployment authority.
export RELAY_VERSION="${release}"
export PUSHGW_VERSION="${release}"
mkdir -p /tmp/swarm-release
gh release download "${release}" --repo Nathandela/swarm \
  --pattern container-images.json --dir /tmp/swarm-release

relay_image="$(jq -er .images.relay /tmp/swarm-release/container-images.json)"
pushgw_image="$(jq -er .images.pushgw /tmp/swarm-release/container-images.json)"
case "${relay_image}" in ghcr.io/nathandela/swarm-relay@sha256:*) ;; *) exit 1 ;; esac
case "${pushgw_image}" in ghcr.io/nathandela/swarm-pushgw@sha256:*) ;; *) exit 1 ;; esac

# OCI verification requires registry authentication even when package visibility is public.
gh auth token | docker login ghcr.io -u "$(gh api user --jq .login)" --password-stdin
gh attestation verify "oci://${relay_image}" --repo Nathandela/swarm \
  --signer-workflow Nathandela/swarm/.github/workflows/release.yml
gh attestation verify "oci://${pushgw_image}" --repo Nathandela/swarm \
  --signer-workflow Nathandela/swarm/.github/workflows/release.yml
```

Use the digest references in a local Compose override and prohibit a build during deployment:

```bash
printf 'services:\n  swarm-relay:\n    image: %s\n' "${relay_image}" > /tmp/relay-image.yml
docker compose -f deploy/relay/docker-compose.yml -f /tmp/relay-image.yml pull
docker compose -f deploy/relay/docker-compose.yml -f /tmp/relay-image.yml up -d --no-build

printf 'services:\n  swarm-pushgw:\n    image: %s\n' "${pushgw_image}" > /tmp/pushgw-image.yml
docker compose -f deploy/pushgw/docker-compose.yml -f /tmp/pushgw-image.yml pull
docker compose -f deploy/pushgw/docker-compose.yml -f /tmp/pushgw-image.yml up -d --no-build
```

Run only the command for the service hosted by that VM. Keep service configuration and credentials
out of Terraform state and instance metadata. Follow
[`relay-vps-deploy.md`](relay-vps-deploy.md) or
[`push-gateway-deploy.md`](push-gateway-deploy.md) for configuration, readiness, and acceptance.
Mount the dedicated data disk at `/var/lib/docker` before the first Compose start so named service
volumes and their adjacent keys are inside the snapshotted disk; formatting an existing disk is a
destructive operation and requires an explicit empty-disk check and backup first.

## 5. DNS, recovery, and change discipline

`dsfactory.org` is not hosted in Cloud DNS in `swarm-8404f`. Terraform therefore outputs the two
records for the external authoritative provider instead of creating a competing zone:

```bash
terraform output -json dns_a_records
```

Apply those exact A records externally, then confirm public resolution matches the Terraform output
before changing traffic. Do not enable Cloud DNS merely to satisfy this stack.

Snapshots are crash-consistent (`guest_flush=false`), not application-level backups. Keep the
service backup procedures and perform a quarterly restore drill onto isolated replacement disks.
During a restore drill, never attach a recovered writable disk to the live VM at the same time as
the production disk. Verify database open, adjacent key custody, readiness, and one end-to-end
pairing/wake path before declaring the recovery point usable.

Every change follows `terraform plan -out`, peer review, saved-plan apply, ADC probe, service
readiness, and external acceptance. Never bypass deletion protection or `prevent_destroy` in the
same change that proposes replacement; split protection removal into an independently approved
break-glass operation with a verified backup and rollback owner.
