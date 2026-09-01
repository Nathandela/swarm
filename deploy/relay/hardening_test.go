// Package relay_test statically asserts the relay container hardening that
// remote-control-product-playbook.md 11.1 requires ("Relay container scan,
// non-root execution, read-only filesystem except declared volumes"). No
// docker runs here: these fences prove the *declarations* -- the Dockerfile's
// non-root final stage, docker-compose.yml's read-only/cap-dropped services,
// and the relay-container.yml workflow that is the executable CI proof
// (image build, trivy scan failing on HIGH/CRITICAL, Config.User assertion).
package relay_test

import (
	"fmt"
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

func TestRelayImageVersionMustBeExplicitAndExamplesAreCurrent(t *testing.T) {
	compose := read(t, "docker-compose.yml")
	if !strings.Contains(compose, "${RELAY_VERSION:?") {
		t.Fatal("compose must require an explicit immutable RELAY_VERSION")
	}
	for path, content := range map[string]string{
		"docker-compose.yml": compose,
		"Dockerfile":         read(t, "Dockerfile"),
	} {
		if strings.Contains(content, "v0.10.3") {
			t.Errorf("%s still contains stale v0.10.3 release examples", path)
		}
	}
	caddy := read(t, "Caddyfile")
	if !strings.Contains(caddy, "relay-swarm.dsfactory.org") || strings.Contains(caddy, "relay.example.com {") {
		t.Error("relay Caddyfile does not pin the public production hostname relay-swarm.dsfactory.org")
	}
}

// workflowStep / workflowJob / workflowFile are the minimal parsed shape of
// relay-container.yml this test needs. Parsing (gopkg.in/yaml.v3) instead of substring
// matching is the point (committee finding Opus M5): a comment or a disabled job could
// satisfy a substring, but not a structural assertion on a live step's inputs.
type workflowStep struct {
	Name string         `yaml:"name"`
	Uses string         `yaml:"uses"`
	Run  string         `yaml:"run"`
	If   any            `yaml:"if"`
	With map[string]any `yaml:"with"`
}

type workflowJob struct {
	If    any            `yaml:"if"`
	Steps []workflowStep `yaml:"steps"`
}

type workflowFile struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}

// disabled reports whether an `if:` value statically switches a job/step off.
func disabled(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return !x
	case string:
		s := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(x), "${{"), "}}"))
		return strings.EqualFold(strings.TrimSpace(s), "false") || strings.TrimSpace(s) == "0"
	default:
		return false
	}
}

// withString reads one step input as a string ("" if absent).
func withString(s workflowStep, key string) string {
	v, ok := s.With[key]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func TestRelayContainerWorkflowWiresTheGates(t *testing.T) {
	var wf workflowFile
	if err := yaml.Unmarshal([]byte(read(t, "../../.github/workflows/relay-container.yml")), &wf); err != nil {
		t.Fatalf("relay-container.yml does not parse as YAML: %v", err)
	}

	// Locate the ONE enabled job that carries a trivy step, and its steps by role.
	var steps []workflowStep
	var jobName string
	for name, job := range wf.Jobs {
		if disabled(job.If) {
			continue
		}
		for _, s := range job.Steps {
			if strings.HasPrefix(s.Uses, "aquasecurity/trivy-action") {
				if steps != nil {
					t.Fatalf("two enabled jobs (%s, %s) carry a trivy step; this test pins exactly one", jobName, name)
				}
				jobName, steps = name, job.Steps
			}
		}
	}
	if steps == nil {
		t.Fatal("no ENABLED job in relay-container.yml runs aquasecurity/trivy-action")
	}

	build, trivy, nonroot, compose := -1, -1, -1, -1
	for i, s := range steps {
		if disabled(s.If) {
			continue
		}
		switch {
		case strings.Contains(s.Run, "docker build") && strings.Contains(s.Run, "deploy/relay/Dockerfile"):
			build = i
		case strings.HasPrefix(s.Uses, "aquasecurity/trivy-action"):
			trivy = i
		case strings.Contains(s.Run, "docker inspect") && strings.Contains(s.Run, ".Config.User"):
			nonroot = i
		case strings.Contains(s.Run, "docker compose config") && strings.Contains(s.Run, "read_only"):
			compose = i
		}
	}
	if build < 0 {
		t.Fatal("no enabled step builds the real relay image from deploy/relay/Dockerfile")
	}
	if trivy < 0 {
		t.Fatal("the trivy step is disabled (if: false)")
	}
	if trivy < build {
		t.Error("the trivy step runs before the image is built; it cannot be scanning the relay image")
	}

	// The scan gates are step INPUTS, not substrings anywhere in the file.
	ts := steps[trivy]
	if got := withString(ts, "exit-code"); got != "1" {
		t.Errorf("trivy exit-code input = %q, want \"1\" (a scan that cannot fail the job gates nothing)", got)
	}
	if got := withString(ts, "severity"); got != "HIGH,CRITICAL" {
		t.Errorf("trivy severity input = %q, want \"HIGH,CRITICAL\"", got)
	}
	if got := withString(ts, "image-ref"); got == "" {
		t.Error("trivy has no image-ref input; it is not scanning the built image")
	}
	if got := withString(ts, "trivyignores"); got != "deploy/relay/.trivyignore" {
		t.Errorf("trivy trivyignores input = %q, want the documented deploy/relay/.trivyignore waiver file", got)
	}
	if _, err := os.Stat(".trivyignore"); err != nil {
		t.Errorf("deploy/relay/.trivyignore: %v", err)
	}

	// Non-root execution is asserted by an enabled step running docker inspect on
	// Config.User, after the build, and its script must be able to fail.
	if nonroot < 0 {
		t.Fatal("no enabled step runs docker inspect on .Config.User (playbook 11.1 non-root assertion)")
	}
	if nonroot < build {
		t.Error("the non-root assertion runs before the image is built")
	}
	if !strings.Contains(steps[nonroot].Run, "exit 1") {
		t.Error("the non-root assertion step has no failing path (no `exit 1`); it cannot gate anything")
	}

	// Compose hardening is re-asserted in CI over the RESOLVED topology.
	if compose < 0 {
		t.Fatal("no enabled step re-asserts the compose read_only hardening via docker compose config")
	}
}
