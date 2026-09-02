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

func TestTerraformPinsTheExistingProductionTopology(t *testing.T) {
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

	for name, address := range map[string]string{
		"relay":  "34.65.198.161",
		"pushgw": "34.65.34.57",
	} {
		block := resourceBlock(t, tf, "google_compute_address", name)
		requireContains(t, block, `address      = "`+address+`"`, `network_tier = "PREMIUM"`)
	}

	for name, instance := range map[string]string{
		"relay":  "swarm-relay-prod",
		"pushgw": "swarm-pushgw-prod",
	} {
		block := resourceBlock(t, tf, "google_compute_instance", name)
		requireContains(t, block,
			`name                      = "`+instance+`"`,
			`machine_type              = "e2-small"`,
			`deletion_protection       = true`,
			`enable_secure_boot          = true`,
			`enable_vtpm                 = true`,
			`enable_integrity_monitoring = true`,
			`"enable-oslogin"       = "TRUE"`,
			`"block-project-ssh-keys" = "TRUE"`,
			`prevent_destroy = true`,
		)
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

func TestRuntimeIdentitiesAreDedicatedAndLeastPrivilege(t *testing.T) {
	tf := terraform(t)
	requireContains(t, tf,
		`account_id   = "swarm-relay-runtime"`,
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

func TestEveryOperatorCanUseBothAttachedRuntimeIdentities(t *testing.T) {
	tf := terraform(t)
	requireContains(t, tf,
		`operator_attached_service_accounts = {`,
		`relay  = google_service_account.relay.name`,
		`pushgw = google_service_account.pushgw.name`,
		`for pair in setproduct(var.operator_members, keys(local.operator_attached_service_accounts))`,
	)
	binding := resourceBlock(t, tf, "google_service_account_iam_member", "operator_service_account_user")
	requireContains(t, binding,
		`for_each = local.operator_service_account_users`,
		`service_account_id = each.value.service_account_id`,
		`role               = "roles/iam.serviceAccountUser"`,
		`member             = each.value.member`,
	)
}

func TestDataDisksAndSnapshotsAreDurableAndEUResident(t *testing.T) {
	tf := terraform(t)
	for _, name := range []string{"relay_data", "pushgw_data"} {
		block := resourceBlock(t, tf, "google_compute_disk", name)
		requireContains(t, block, `type = "pd-balanced"`, `size = 20`, `prevent_destroy = true`)
	}
	policy := resourceBlock(t, tf, "google_compute_resource_policy", "daily_snapshots")
	requireContains(t, policy,
		`start_time      = "03:00"`,
		`max_retention_days    = 14`,
		`on_source_disk_delete = "KEEP_AUTO_SNAPSHOTS"`,
		`storage_locations = ["eu"]`,
	)
	requireContains(t, tf,
		`resource "google_compute_disk_resource_policy_attachment" "relay"`,
		`resource "google_compute_disk_resource_policy_attachment" "pushgw"`,
	)
}

func TestDNSIsAnExplicitExternalOutputAndDocsAreOperational(t *testing.T) {
	tf := terraform(t)
	requireContains(t, tf,
		`output "dns_a_records"`,
		`"relay-swarm.dsfactory.org."`,
		`"push-swarm.dsfactory.org."`,
	)
	if strings.Contains(tf, "google_dns_") {
		t.Fatal("swarm-8404f does not host dsfactory.org in Cloud DNS; Terraform must output, not fork, DNS authority")
	}

	doc := read(t, "../../docs/operations/gcp-production-iac.md")
	requireContains(t, doc,
		"terraform init -backend-config=backend.hcl",
		"terraform plan",
		"terraform import",
		"Do not run `terraform apply` before",
		"container-images.json",
		"@sha256:",
		`export RELAY_VERSION="${release}"`,
		`export PUSHGW_VERSION="${release}"`,
		"gh attestation verify",
		"--tunnel-through-iap",
		"exactly four least-privilege IAM grants per operator",
		"roles/iam.serviceAccountUser",
		`operator_service_account_user["relay:user:operator@example.com"]`,
		`operator_service_account_user["pushgw:user:operator@example.com"]`,
		"terraform output -json dns_a_records",
		"deletion protection",
		"restore drill",
		"Required ADC authorization probe",
		"https://fcm.googleapis.com/v1/projects/swarm-8404f/messages:send",
		"https://playintegrity.googleapis.com/v1/dev.swarm.phone:decodeIntegrityToken",
		`test "${fcm_status}" != 401`,
		`test "${play_status}" != 403`,
		"gcloud compute instances stop swarm-pushgw-prod",
		"gcloud compute instances set-service-account swarm-pushgw-prod",
		"gcloud compute instances start swarm-pushgw-prod",
	)
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
