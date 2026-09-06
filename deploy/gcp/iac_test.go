package gcp_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func terraform(t *testing.T) string {
	t.Helper()
	paths, err := filepath.Glob("*.tf")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("deploy/gcp has no Terraform configuration")
	}
	var all strings.Builder
	for _, path := range paths {
		all.WriteString("\n# ")
		all.WriteString(path)
		all.WriteByte('\n')
		all.WriteString(read(t, path))
	}
	return all.String()
}

func resourceBlock(t *testing.T, src, kind, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?ms)resource\s+"` + regexp.QuoteMeta(kind) + `"\s+"` + regexp.QuoteMeta(name) + `"\s*\{(.*?)\n\}`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("Terraform resource %s.%s missing", kind, name)
	}
	return m[1]
}

func requireContains(t *testing.T, src string, wants ...string) {
	t.Helper()
	normalized := strings.Join(strings.Fields(src), " ")
	for _, want := range wants {
		if !strings.Contains(normalized, strings.Join(strings.Fields(want), " ")) {
			t.Errorf("configuration missing %q", want)
		}
	}
}

func TestTerraformKeepsOnlyThePushGatewayProductionTopology(t *testing.T) {
	tf := terraform(t)
	requireContains(t, tf,
		`backend "gcs"`,
		`default     = "swarm-8404f"`,
		`condition     = var.project_id == "swarm-8404f"`,
		`default     = "europe-west6"`,
		`default     = "europe-west6-a"`,
		`name                    = "swarm-public"`,
		`name                     = "swarm-public-euw6"`,
		`ip_cidr_range            = "10.80.0.0/24"`,
		`private_ip_google_access = true`,
	)

	address := resourceBlock(t, tf, "google_compute_address", "pushgw")
	requireContains(t, address, `address      = "34.65.34.57"`, `network_tier = "PREMIUM"`)

	pushgw := resourceBlock(t, tf, "google_compute_instance", "pushgw")
	requireContains(t, pushgw,
		`name                      = "swarm-pushgw-prod"`,
		`machine_type              = "e2-small"`,
		`deletion_protection       = true`,
		`enable_secure_boot          = true`,
		`enable_vtpm                 = true`,
		`enable_integrity_monitoring = true`,
		`"enable-oslogin"       = "TRUE"`,
		`"block-project-ssh-keys" = "TRUE"`,
		`prevent_destroy = true`,
	)

	for _, retired := range []string{
		`google_compute_instance" "relay`,
		`google_compute_address" "relay`,
		`google_service_account" "relay`,
		`output "relay_instance"`,
		`swarm-relay-prod`,
		`swarm-relay-ip`,
		`34.65.198.161`,
	} {
		if strings.Contains(tf, retired) {
			t.Errorf("Terraform still contains retired relay-v1 infrastructure %q", retired)
		}
	}
}

func TestFirewallAllowsOnlyPublicWebAndIAPSSH(t *testing.T) {
	tf := terraform(t)
	web := resourceBlock(t, tf, "google_compute_firewall", "public_web")
	requireContains(t, web, `source_ranges = ["0.0.0.0/0"]`, `ports = ["80", "443"]`, `log_config {`)
	if strings.Contains(web, `"22"`) {
		t.Fatal("public web firewall also exposes SSH")
	}

	iap := resourceBlock(t, tf, "google_compute_firewall", "iap_ssh")
	requireContains(t, iap, `source_ranges = ["35.235.240.0/20"]`, `ports = ["22"]`, `log_config {`)
	if regexp.MustCompile(`(?s)source_ranges\s*=\s*\[[^]]*0\.0\.0\.0/0`).MatchString(iap) {
		t.Fatal("SSH is reachable outside Google's IAP TCP-forwarding range")
	}

	if strings.Contains(tf, `ports    = ["8450"`) || strings.Contains(tf, `ports    = ["8451"`) || strings.Contains(tf, `ports    = ["9440"`) {
		t.Fatal("an application or admin loopback port is exposed by a VPC firewall")
	}
}

func TestRuntimeIdentityIsDedicatedAndLeastPrivilege(t *testing.T) {
	tf := terraform(t)
	requireContains(t, tf,
		`account_id   = "swarm-push-runtime"`,
		`role_id     = "swarmPushGatewayRuntime"`,
		`permissions = ["cloudmessaging.messages.create"]`,
		`role   = "roles/logging.logWriter"`,
		`role   = "roles/monitoring.metricWriter"`,
		`role   = "roles/iap.tunnelResourceAccessor"`,
		`role   = "roles/compute.osLogin"`,
	)
	pushgw := resourceBlock(t, tf, "google_compute_instance", "pushgw")
	requireContains(t, pushgw,
		`scopes = [ "cloud-platform", "https://www.googleapis.com/auth/playintegrity", ]`,
	)
	for _, forbidden := range []string{
		`swarm-relay-runtime`,
		`roles/owner`,
		`roles/editor`,
		`roles/secretmanager.secretAccessor`,
		`733314021126-compute@developer.gserviceaccount.com`,
	} {
		if strings.Contains(tf, forbidden) {
			t.Errorf("Terraform grants or uses forbidden broad/default authority %q", forbidden)
		}
	}
}

func TestEveryOperatorCanUseThePushGatewayRuntimeIdentity(t *testing.T) {
	tf := terraform(t)
	requireContains(t, tf,
		`for_each = var.operator_members`,
		`service_account_id = google_service_account.pushgw.name`,
	)
	binding := resourceBlock(t, tf, "google_service_account_iam_member", "operator_service_account_user")
	requireContains(t, binding,
		`for_each = { for member in var.operator_members : "pushgw:${member}" => member }`,
		`service_account_id = google_service_account.pushgw.name`,
		`role               = "roles/iam.serviceAccountUser"`,
		`member             = each.value`,
	)
}

func TestDataDisksAndSnapshotsAreDurableAndEUResident(t *testing.T) {
	tf := terraform(t)
	block := resourceBlock(t, tf, "google_compute_disk", "pushgw_data")
	requireContains(t, block, `type = "pd-balanced"`, `size = 20`, `prevent_destroy = true`)
	policy := resourceBlock(t, tf, "google_compute_resource_policy", "daily_snapshots")
	requireContains(t, policy,
		`start_time      = "03:00"`,
		`max_retention_days    = 14`,
		`on_source_disk_delete = "KEEP_AUTO_SNAPSHOTS"`,
		`storage_locations = ["eu"]`,
	)
	requireContains(t, tf,
		`resource "google_compute_disk_resource_policy_attachment" "pushgw"`,
	)
	for _, retired := range []string{
		`google_compute_disk" "relay_data`,
		`google_compute_disk_resource_policy_attachment" "relay`,
		`swarm-relay-data`,
		`operator_attached_service_accounts`,
		`operator_service_account_users`,
	} {
		if strings.Contains(tf, retired) {
			t.Errorf("Terraform still contains retired relay-v1 storage %q", retired)
		}
	}
}

func TestDNSIsAnExplicitExternalOutputAndDocsAreOperational(t *testing.T) {
	tf := terraform(t)
	requireContains(t, tf,
		`output "dns_a_records"`,
		`"push-swarm.dsfactory.org."`,
	)
	if strings.Contains(tf, "google_dns_") {
		t.Fatal("swarm-8404f does not host dsfactory.org in Cloud DNS; Terraform must output, not fork, DNS authority")
	}

	doc := read(t, "../../docs/operations/gcp-production-iac.md")
	requireContains(t, doc,
		"source change does **not** change any live cloud resource",
		"Cloudflare Worker and Durable Object infrastructure",
		"swarm-pushgw-prod",
		"swarm-pushgw-data",
		"swarm-pushgw-ip",
		"swarm-push-runtime",
		"swarm-public",
		"swarm-daily-snapshots",
		"exactly three least-privilege IAM grants per principal",
		"roles/iam.serviceAccountUser",
		"terraform output -json dns_a_records",
		"deletion protection",
		"daily at 03:00 UTC",
		"push-gateway-deploy.md",
		"push-gateway-runbook.md",
	)
	for _, retired := range []string{
		"swarm-relay",
		"relay-swarm.dsfactory.org",
		"container-images.json",
		"RELAY_VERSION",
		"deploy/relay",
	} {
		if strings.Contains(doc, retired) {
			t.Errorf("GCP operator documentation still contains retired relay-v1 deployment detail %q", retired)
		}
	}
	if strings.Contains(strings.ToLower(doc), ":latest") {
		t.Fatal("operator documentation suggests a mutable latest deployment")
	}
}

func TestTerraformContainsNoSecretMaterial(t *testing.T) {
	tf := terraform(t)
	for _, forbidden := range []string{
		"private_key",
		"google_credentials",
		"fcm_token",
		"play_signing_cert_sha256",
		"secret_data",
		"startup-script",
	} {
		if strings.Contains(strings.ToLower(tf), forbidden) {
			t.Errorf("Terraform contains secret-bearing or metadata-injection field %q", forbidden)
		}
	}
}
