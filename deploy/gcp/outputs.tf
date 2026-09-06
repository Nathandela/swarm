output "pushgw_instance" {
  description = "Production push gateway instance name."
  value       = google_compute_instance.pushgw.name
}

output "dns_a_records" {
  description = "Apply these records at the external authoritative dsfactory.org DNS provider."
  value = {
    "push-swarm.dsfactory.org." = google_compute_address.pushgw.address
  }
}

output "iap_ssh_commands" {
  description = "OS Login commands; no production VM accepts SSH directly from the internet."
  value = {
    pushgw = "gcloud compute ssh swarm-pushgw-prod --project=${var.project_id} --zone=${var.zone} --tunnel-through-iap"
  }
}
