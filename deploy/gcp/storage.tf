resource "google_compute_resource_policy" "daily_snapshots" {
  project = var.project_id
  region  = var.region
  name    = "swarm-daily-snapshots"

  description = "Daily encrypted off-VM recovery points for Swarm relay and push gateway data disks"

  snapshot_schedule_policy {
    schedule {
      daily_schedule {
        days_in_cycle = 1
        start_time    = "03:00"
      }
    }

    retention_policy {
      max_retention_days    = 14
      on_source_disk_delete = "KEEP_AUTO_SNAPSHOTS"
    }

    snapshot_properties {
      guest_flush       = false
      storage_locations = ["eu"]
    }
  }

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_compute_disk" "relay_data" {
  project = var.project_id
  zone    = var.zone
  name    = "swarm-relay-data"
  type    = "pd-balanced"
  size    = 20

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_compute_disk" "pushgw_data" {
  project = var.project_id
  zone    = var.zone
  name    = "swarm-pushgw-data"
  type    = "pd-balanced"
  size    = 20

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_compute_disk_resource_policy_attachment" "relay" {
  project = var.project_id
  zone    = var.zone
  name    = google_compute_resource_policy.daily_snapshots.name
  disk    = google_compute_disk.relay_data.name
}

resource "google_compute_disk_resource_policy_attachment" "pushgw" {
  project = var.project_id
  zone    = var.zone
  name    = google_compute_resource_policy.daily_snapshots.name
  disk    = google_compute_disk.pushgw_data.name
}
