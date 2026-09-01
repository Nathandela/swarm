resource "google_service_account" "relay" {
  project      = var.project_id
  account_id   = "swarm-relay-runtime"
  display_name = "Swarm Relay Runtime"

  depends_on = [google_project_service.required["iam.googleapis.com"]]
}

resource "google_service_account" "pushgw" {
  project      = var.project_id
  account_id   = "swarm-push-runtime"
  display_name = "Swarm Push Gateway Runtime"

  depends_on = [google_project_service.required["iam.googleapis.com"]]
}

# Keep the public push service's Firebase authority narrower than Firebase Admin.
# Play Integrity decode uses the linked project's OAuth authority; the only
# project IAM permission the runtime needs in addition to telemetry is FCM send.
resource "google_project_iam_custom_role" "pushgw_runtime" {
  project     = var.project_id
  role_id     = "swarmPushGatewayRuntime"
  title       = "Swarm Push Gateway Runtime"
  description = "Send Swarm FCM wake messages; Play Integrity decode uses linked-project OAuth authority."
  permissions = ["cloudmessaging.messages.create"]
  stage       = "GA"

  depends_on = [google_project_service.required["iam.googleapis.com"]]
}

locals {
  runtime_telemetry_members = {
    relay  = google_service_account.relay.member
    pushgw = google_service_account.pushgw.member
  }
}

resource "google_project_iam_member" "runtime_log_writer" {
  for_each = local.runtime_telemetry_members

  project = var.project_id
  role    = "roles/logging.logWriter"
  member  = each.value
}

resource "google_project_iam_member" "runtime_metric_writer" {
  for_each = local.runtime_telemetry_members

  project = var.project_id
  role    = "roles/monitoring.metricWriter"
  member  = each.value
}

resource "google_project_iam_member" "pushgw_runtime" {
  project = var.project_id
  role    = google_project_iam_custom_role.pushgw_runtime.name
  member  = google_service_account.pushgw.member
}

# Project owners are not used as the normal VM access path. An operator must be
# named here, traverse IAP's TCP proxy, and authenticate through OS Login. There
# is no firewall rule for public SSH and project SSH keys are blocked on each VM.
resource "google_project_iam_member" "operator_iap_tunnel" {
  for_each = var.operator_members

  project = var.project_id
  role    = "roles/iap.tunnelResourceAccessor"
  member  = each.value
}

resource "google_project_iam_member" "operator_os_login" {
  for_each = var.operator_members

  project = var.project_id
  role    = "roles/compute.osLogin"
  member  = each.value
}
