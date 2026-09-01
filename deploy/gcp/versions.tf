terraform {
  required_version = ">= 1.7.0, < 2.0.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 7.45"
    }
  }

  # Supply bucket and prefix with backend.hcl. State storage is deliberately
  # bootstrapped outside this stack so destroying the stack cannot destroy the
  # state needed to recover it.
  backend "gcs" {}
}

provider "google" {
  project = var.project_id
  region  = var.region
  zone    = var.zone
}
