resource "google_compute_instance" "relay" {
  project                   = var.project_id
  zone                      = var.zone
  name                      = "swarm-relay-prod"
  machine_type              = "e2-small"
  can_ip_forward            = false
  deletion_protection       = true
  allow_stopping_for_update = true

  labels = {
    env     = "production"
    service = "relay"
  }

  tags = ["swarm-iap-ssh", "swarm-public-web"]

  boot_disk {
    auto_delete = true
    device_name = "persistent-disk-0"

    initialize_params {
      image = var.boot_image
      size  = 15
      type  = "pd-balanced"
    }
  }

  attached_disk {
    source      = google_compute_disk.relay_data.id
    device_name = "swarm-relay-data"
    mode        = "READ_WRITE"
  }

  network_interface {
    subnetwork = google_compute_subnetwork.public_euw6.id
    network_ip = "10.80.0.3"
    stack_type = "IPV4_ONLY"

    access_config {
      nat_ip       = google_compute_address.relay.address
      network_tier = "PREMIUM"
    }
  }

  metadata = {
    "enable-oslogin"         = "TRUE"
    "block-project-ssh-keys" = "TRUE"
  }

  service_account {
    email  = google_service_account.relay.email
    scopes = ["cloud-platform"]
  }

  scheduling {
    automatic_restart   = true
    on_host_maintenance = "MIGRATE"
    preemptible         = false
    provisioning_model  = "STANDARD"
  }

  shielded_instance_config {
    enable_secure_boot          = true
    enable_vtpm                 = true
    enable_integrity_monitoring = true
  }

  lifecycle {
    prevent_destroy = true
  }

  depends_on = [
    google_compute_disk_resource_policy_attachment.relay,
    google_project_iam_member.runtime_log_writer,
    google_project_iam_member.runtime_metric_writer,
  ]
}

resource "google_compute_instance" "pushgw" {
  project                   = var.project_id
  zone                      = var.zone
  name                      = "swarm-pushgw-prod"
  machine_type              = "e2-small"
  can_ip_forward            = false
  deletion_protection       = true
  allow_stopping_for_update = true

  labels = {
    env     = "production"
    service = "pushgw"
  }

  tags = ["swarm-iap-ssh", "swarm-public-web"]

  boot_disk {
    auto_delete = true
    device_name = "persistent-disk-0"

    initialize_params {
      image = var.boot_image
      size  = 15
      type  = "pd-balanced"
    }
  }

  attached_disk {
    source      = google_compute_disk.pushgw_data.id
    device_name = "swarm-pushgw-data"
    mode        = "READ_WRITE"
  }

  network_interface {
    subnetwork = google_compute_subnetwork.public_euw6.id
    network_ip = "10.80.0.2"
    stack_type = "IPV4_ONLY"

    access_config {
      nat_ip       = google_compute_address.pushgw.address
      network_tier = "PREMIUM"
    }
  }

  metadata = {
    "enable-oslogin"         = "TRUE"
    "block-project-ssh-keys" = "TRUE"
  }

  service_account {
    email = google_service_account.pushgw.email
    scopes = [
      "cloud-platform",
      "https://www.googleapis.com/auth/playintegrity",
    ]
  }

  scheduling {
    automatic_restart   = true
    on_host_maintenance = "MIGRATE"
    preemptible         = false
    provisioning_model  = "STANDARD"
  }

  shielded_instance_config {
    enable_secure_boot          = true
    enable_vtpm                 = true
    enable_integrity_monitoring = true
  }

  lifecycle {
    prevent_destroy = true
  }

  depends_on = [
    google_compute_disk_resource_policy_attachment.pushgw,
    google_project_iam_member.pushgw_runtime,
    google_project_iam_member.runtime_log_writer,
    google_project_iam_member.runtime_metric_writer,
  ]
}
