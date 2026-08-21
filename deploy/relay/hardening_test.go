// Package relay_test statically asserts the relay container hardening that
// remote-control-product-playbook.md 11.1 requires ("Relay container scan,
// non-root execution, read-only filesystem except declared volumes"). No
// docker runs here: these fences prove the *declarations* -- the Dockerfile's
// non-root final stage, docker-compose.yml's read-only/cap-dropped services,
// and the relay-container.yml workflow that is the executable CI proof
// (image build, trivy scan failing on HIGH/CRITICAL, Config.User assertion).
package relay_test

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

// composeService returns the indented body of one service block from
// docker-compose.yml (everything until the next 2-space-indented key or a
// top-level key). Comment lines never terminate a block.
func composeService(t *testing.T, compose, name string) string {
	t.Helper()
	lines := strings.Split(compose, "\n")
	start := -1
	for i, l := range lines {
		if l == "  "+name+":" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("service %q not found in docker-compose.yml", name)
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(lines[i], "    ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

func TestDockerfileFinalStageRunsNonRoot(t *testing.T) {
	df := read(t, "Dockerfile")
	froms := regexp.MustCompile(`(?m)^FROM\s+(\S+)`).FindAllStringSubmatch(df, -1)
	if len(froms) == 0 {
		t.Fatal("no FROM lines in Dockerfile")
	}
	final := froms[len(froms)-1][1]
	if !strings.Contains(final, "distroless") || !strings.Contains(final, "nonroot") {
		t.Errorf("final stage base %q is not a distroless nonroot image", final)
	}
	if !regexp.MustCompile(`(?m)^USER\s+nonroot`).MatchString(df) {
		t.Error("Dockerfile final stage has no explicit non-root USER directive")
	}
	if regexp.MustCompile(`(?m)^USER\s+(root|0)\b`).MatchString(df) {
		t.Error("Dockerfile sets USER root")
	}
}

func TestComposeServicesAreHardened(t *testing.T) {
	compose := read(t, "docker-compose.yml")
	required := map[string][]string{
		// busybox chown one-shot: needs CHOWN (change to 65532) and
		// DAC_OVERRIDE (re-run traversal once the tree is 65532-owned).
		"relay-init": {"read_only: true", "- ALL", "no-new-privileges:true", "- CHOWN", "- DAC_OVERRIDE"},
		// the relay binds loopback high ports only: no capability at all.
		"swarm-relay": {"read_only: true", "- ALL", "no-new-privileges:true"},
		// caddy binds :80/:443 in the shared namespace.
		"caddy": {"read_only: true", "- ALL", "no-new-privileges:true", "- NET_BIND_SERVICE"},
	}
	for name, wants := range required {
		block := composeService(t, compose, name)
		for _, want := range wants {
			if !strings.Contains(block, want) {
				t.Errorf("service %s: missing %q", name, want)
			}
		}
	}
	if strings.Contains(composeService(t, compose, "swarm-relay"), "cap_add") {
		t.Error("swarm-relay must not re-add any capability")
	}
}

func TestRelayContainerWorkflowWiresTheGates(t *testing.T) {
	wf := read(t, "../../.github/workflows/relay-container.yml")
	for _, want := range []string{
		"deploy/relay/Dockerfile",   // builds the real relay image
		"aquasecurity/trivy-action", // vulnerability scan
		"HIGH,CRITICAL",             // fail threshold
		".trivyignore",              // documented ignore mechanism
		"Config.User",               // non-root execution assertion
		"read_only",                 // compose hardening asserted in CI
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("relay-container.yml: missing %q", want)
		}
	}
	if _, err := os.Stat(".trivyignore"); err != nil {
		t.Errorf("deploy/relay/.trivyignore: %v", err)
	}
}
