package pushgw_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func serviceBlock(t *testing.T, compose, name string) map[string]any {
	t.Helper()
	var doc struct {
		Services map[string]map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(compose), &doc); err != nil {
		t.Fatalf("parse docker-compose.yml: %v", err)
	}
	svc := doc.Services[name]
	if svc == nil {
		t.Fatalf("compose service %q missing", name)
	}
	return svc
}

func TestPushGatewayImageAndComposeAreHardened(t *testing.T) {
	df := read(t, "Dockerfile")
	if !strings.Contains(df, "./cmd/swarm-pushgw") || !regexp.MustCompile(`(?m)^USER\s+nonroot`).MatchString(df) {
		t.Fatal("Dockerfile must build swarm-pushgw and run the distroless image as nonroot")
	}
	if strings.Contains(df, ":latest") {
		t.Fatal("Dockerfile references latest tag")
	}

	compose := read(t, "docker-compose.yml")
	if !strings.Contains(compose, "${PUSHGW_VERSION:?") {
		t.Fatal("compose must require an explicit immutable PUSHGW_VERSION")
	}
	for _, name := range []string{"pushgw-init", "swarm-pushgw", "caddy"} {
		svc := serviceBlock(t, compose, name)
		if svc["read_only"] != true {
			t.Errorf("service %s does not set read_only: true", name)
		}
		caps, _ := svc["cap_drop"].([]any)
		foundAll := false
		for _, cap := range caps {
			foundAll = foundAll || cap == "ALL"
		}
		if !foundAll {
			t.Errorf("service %s does not drop ALL capabilities", name)
		}
	}
	if _, ok := serviceBlock(t, compose, "swarm-pushgw")["cap_add"]; ok {
		t.Error("swarm-pushgw must not add capabilities")
	}
	if strings.Contains(compose, "8451:8451") || strings.Contains(compose, "8450:8450") {
		t.Error("compose publishes an app/admin loopback port directly")
	}
	for _, want := range []string{
		"-gcp-project-id", "swarm-8404f",
		"-gcp-project-number", "733314021126",
		"-android-package", "dev.swarm.phone",
		"-play-signing-cert-sha256", "hz8YTGhTTgpYccjMiQDrhx5HcddqRsTu1HRcmhhknmU",
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose production command missing %q", want)
		}
	}
	if strings.Contains(compose, "-fcm-credentials") {
		t.Error("compose still wires a separate FCM credential instead of one ADC runtime identity")
	}

	caddy := read(t, "Caddyfile")
	if !strings.Contains(caddy, "push-swarm.dsfactory.org") || !strings.Contains(caddy, "reverse_proxy 127.0.0.1:8450") {
		t.Fatal("Caddyfile does not terminate the production hostname onto the loopback app listener")
	}
	if strings.Contains(caddy, "127.0.0.1:8451") {
		t.Fatal("Caddyfile exposes the admin listener")
	}
}

func TestPushGatewayExecutableConfigCannotNameForeignProjects(t *testing.T) {
	for _, path := range []string{
		"docker-compose.yml",
		"swarm-pushgw.service",
		"pushgw.env.example",
		"../../.github/workflows/pushgw-container.yml",
	} {
		content := read(t, path)
		for _, forbidden := range []string{"quiet-training", "soml-ia-493903"} {
			if strings.Contains(content, forbidden) {
				t.Errorf("executable deployment config %s names foreign project %q", path, forbidden)
			}
		}
	}
}

func TestPushGatewaySystemdAndConfigPinProductionIdentity(t *testing.T) {
	unit := read(t, "swarm-pushgw.service")
	for _, want := range []string{"User=swarm-pushgw", "StateDirectory=swarm-pushgw", "NoNewPrivileges=true", "ProtectSystem=strict", "CapabilityBoundingSet="} {
		if !strings.Contains(unit, want) {
			t.Errorf("systemd unit missing %q", want)
		}
	}
	config := read(t, "pushgw.env.example")
	for _, want := range []string{
		"swarm-8404f",
		"733314021126",
		"dev.swarm.phone",
		"hz8YTGhTTgpYccjMiQDrhx5HcddqRsTu1HRcmhhknmU",
		"push-swarm.dsfactory.org",
	} {
		if !strings.Contains(config, want) {
			t.Errorf("production config missing %q", want)
		}
	}
}

func TestPushGatewayContainerWorkflowIsAReleaseGate(t *testing.T) {
	wf := read(t, "../../.github/workflows/pushgw-container.yml")
	for _, want := range []string{
		"deploy/pushgw/Dockerfile",
		"aquasecurity/trivy-action",
		"exit-code: '1'",
		"severity: HIGH,CRITICAL",
		"deploy/pushgw/.trivyignore",
		"docker inspect",
		"docker compose config",
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("pushgw-container workflow missing %q", want)
		}
	}
	release := read(t, "../../.github/workflows/release.yml")
	if !strings.Contains(release, "./.github/workflows/pushgw-container.yml") || !strings.Contains(release, "container_pushgw") {
		t.Fatal("release workflow does not require the push-gateway container gate")
	}
}

func TestPushGatewayOperationsDocsExist(t *testing.T) {
	for _, path := range []string{
		"../../docs/operations/push-gateway-deploy.md",
		"../../docs/operations/push-gateway-runbook.md",
		"../../docs/operations/push-gateway-incident-response.md",
	} {
		if info, err := os.Stat(path); err != nil || info.Size() == 0 {
			t.Errorf("required operations document %s missing/empty: %v", path, err)
		}
	}
}
