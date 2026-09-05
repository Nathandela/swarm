package pushgw_test

import (
	"os"
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

func TestPushGatewayImageIsHardened(t *testing.T) {
	df := read(t, "Dockerfile")
	if !strings.Contains(df, "./cmd/swarm-pushgw") || !regexp.MustCompile(`(?m)^USER\s+nonroot`).MatchString(df) {
		t.Fatal("Dockerfile must build swarm-pushgw and run as nonroot")
	}
	if strings.Contains(df, ":latest") {
		t.Fatal("Dockerfile references latest tag")
	}
	if !strings.Contains(df, `ENTRYPOINT ["/swarm-pushgw"]`) {
		t.Fatal("image must start the sole v2 command")
	}
	if !strings.Contains(df, "ENV PORT=8080") || !strings.Contains(df, "EXPOSE 8080") {
		t.Fatal("image default listener and exposed Cloud Run port must agree")
	}
}

func TestPushGatewayContainerWorkflowIsAReleaseGate(t *testing.T) {
	wf := read(t, "../../.github/workflows/pushgw-container.yml")
	for _, want := range []string{
		"deploy/pushgw/Dockerfile", "aquasecurity/trivy-action",
		"exit-code: '1'", "severity: HIGH,CRITICAL",
		"deploy/pushgw/.trivyignore", "docker inspect",
		"go test ./deploy/pushgw",
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("pushgw-container workflow missing %q", want)
		}
	}
	if strings.Contains(wf, "docker compose") {
		t.Error("workflow must not validate an obsolete VM backend")
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
