variable "project_id" {
  description = "The fixed production project. This module is not a reusable sandbox topology."
  type        = string
  default     = "swarm-8404f"

  validation {
    condition     = var.project_id == "swarm-8404f"
    error_message = "Production infrastructure must remain in project swarm-8404f."
  }
}

variable "region" {
  description = "Region of the existing production topology."
  type        = string
  default     = "europe-west6"

  validation {
    condition     = var.region == "europe-west6"
    error_message = "The imported production resources are in europe-west6."
  }
}

variable "zone" {
  description = "Zone of the existing production VMs and disks."
  type        = string
  default     = "europe-west6-a"

  validation {
    condition     = var.zone == "europe-west6-a"
    error_message = "The imported production resources are in europe-west6-a."
  }
}

variable "operator_members" {
  description = "Explicit user: or group: principals allowed to reach production VMs through IAP and OS Login."
  type        = set(string)
  default     = []

  validation {
    condition = alltrue([
      for member in var.operator_members : can(regex("^(user|group):[^[:space:]]+@[^[:space:]]+$", member))
    ])
    error_message = "Each operator must be an explicit user:email or group:email principal."
  }
}

variable "boot_image" {
  description = "Exact image backing the imported boot disks; change only in a reviewed host-upgrade plan."
  type        = string
  default     = "debian-cloud/debian-12-bookworm-v20260826"
}
