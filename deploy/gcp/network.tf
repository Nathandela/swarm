resource "google_compute_network" "public" {
  project                 = var.project_id
  name                    = "swarm-public"
  auto_create_subnetworks = false
  routing_mode            = "REGIONAL"

  lifecycle {
    prevent_destroy = true
  }

  depends_on = [google_project_service.required["compute.googleapis.com"]]
}

resource "google_compute_subnetwork" "public_euw6" {
  project                  = var.project_id
  region                   = var.region
  name                     = "swarm-public-euw6"
  network                  = google_compute_network.public.id
  ip_cidr_range            = "10.80.0.0/24"
  private_ip_google_access = true

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_compute_firewall" "public_web" {
  project   = var.project_id
  name      = "swarm-public-web"
  network   = google_compute_network.public.name
  direction = "INGRESS"
  priority  = 1000

  source_ranges = ["0.0.0.0/0"]
  target_tags   = ["swarm-public-web"]

  allow {
    protocol = "tcp"
    ports    = ["80", "443"]
  }

  log_config {
    metadata = "INCLUDE_ALL_METADATA"
  }
}

resource "google_compute_firewall" "iap_ssh" {
  project   = var.project_id
  name      = "swarm-iap-ssh"
  network   = google_compute_network.public.name
  direction = "INGRESS"
  priority  = 1000

  source_ranges = ["35.235.240.0/20"]
  target_tags   = ["swarm-iap-ssh"]

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }

  log_config {
    metadata = "INCLUDE_ALL_METADATA"
  }
}

resource "google_compute_address" "relay" {
  project      = var.project_id
  region       = var.region
  name         = "swarm-relay-ip"
  address      = "34.65.198.161"
  address_type = "EXTERNAL"
  network_tier = "PREMIUM"

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_compute_address" "pushgw" {
  project      = var.project_id
  region       = var.region
  name         = "swarm-pushgw-ip"
  address      = "34.65.34.57"
  address_type = "EXTERNAL"
  network_tier = "PREMIUM"

  lifecycle {
    prevent_destroy = true
  }
}
