package supervise

// FAILING-FIRST tests for slice S4 — PB-LIFE-6 and PB-OPS-4: swarm-remote and swarm-relay
// are buildable RELEASE artifacts.
//
// This lives beside the unit generator on purpose. A supervision unit whose ExecStart
// points at a binary the release pipeline never builds is a unit that cannot run on any
// machine but the one it was developed on: PB-LIFE-1 and PB-LIFE-6 are the same
// requirement seen from two ends. So the assertions below tie the generated unit's
// executable name to a real entry in the release matrix.
//
// VERIFIED STATE TODAY (RED): .goreleaser.yaml declares exactly one build, `./cmd/swarm`,
// and its header comment says "swarm is the ONLY release artifact ... swarm-char and
// swarm-fake-agent are dev/test tools and are never built by this pipeline" -- a comment
// that does not even mention swarm-relay, which is a real deployable. Both statements
// have to change together, or the next reader trusts the comment over the config.
//
// No YAML dependency is added for this: the module has none today, and a release manifest
// is not worth one. The scan below is deliberately shallow -- it reads the top-level
// blocks it needs and nothing else.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot resolves the module root from this package's directory
// (internal/remote/supervise -> ../../..).
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %q has no go.mod: %v", root, err)
	}
	return root
}

// releaseBuild is one entry of .goreleaser.yaml's `builds:` list.
type releaseBuild struct {
	id     string
	main   string
	binary string
}

// scanGoreleaser reads the `builds:` entries and the union of every `ids:` list under
// `archives:`. It tracks the top-level block by indentation, which is all this file's
// shape requires.
func scanGoreleaser(t *testing.T, path string) (builds []releaseBuild, archived map[string]bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	archived = map[string]bool{}
	block := ""
	inIDs := false
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimRight(raw, " \t")
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			block = strings.TrimSuffix(strings.TrimSpace(line), ":")
			inIDs = false
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch block {
		case "builds":
			if id, ok := strings.CutPrefix(trimmed, "- id:"); ok {
				builds = append(builds, releaseBuild{id: strings.TrimSpace(id)})
				continue
			}
			if len(builds) == 0 {
				continue
			}
			cur := &builds[len(builds)-1]
			if v, ok := strings.CutPrefix(trimmed, "main:"); ok {
				cur.main = strings.TrimSpace(v)
			}
			if v, ok := strings.CutPrefix(trimmed, "binary:"); ok {
				cur.binary = strings.TrimSpace(v)
			}
		case "archives", "homebrew_casks":
			if trimmed == "ids:" {
				inIDs = true
				continue
			}
			if inIDs && strings.HasPrefix(trimmed, "- ") {
				if block == "archives" {
					archived[strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))] = true
				}
				continue
			}
			inIDs = false
		}
	}
	return builds, archived
}

// TestGoreleaser_ShipsGatewayAndRelay is PB-LIFE-6 + PB-OPS-4: the release matrix builds
// swarm-remote and swarm-relay, each from its real main package, each landing in an
// archive. The gateway's binary name must match the name the supervision unit's ExecStart
// will carry, or a released machine installs a unit pointing at nothing.
func TestGoreleaser_ShipsGatewayAndRelay(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, ".goreleaser.yaml")
	builds, archived := scanGoreleaser(t, path)

	byID := map[string]releaseBuild{}
	for _, b := range builds {
		byID[b.id] = b
	}

	want := map[string]string{
		"swarm":        "./cmd/swarm",
		"swarm-remote": "./cmd/swarm-remote",
		"swarm-relay":  "./cmd/swarm-relay",
	}
	for id, main := range want {
		b, ok := byID[id]
		if !ok {
			t.Errorf("%s declares no build with id %q; got ids %v", filepath.Base(path), id, keysOf(byID))
			continue
		}
		if b.main != main {
			t.Errorf("build %q main = %q, want %q", id, b.main, main)
		}
		if b.binary != id {
			t.Errorf("build %q binary = %q, want %q", id, b.binary, id)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(main, "./")))); err != nil {
			t.Errorf("build %q main package %q does not exist: %v", id, main, err)
		}
		if !archived[id] {
			t.Errorf("build %q is built but reaches no archive; PB-OPS-4 wants a released ARTIFACT, not just a compile", id)
		}
	}

	// The unit generator and the release matrix must agree on the gateway's name.
	if b, ok := byID["swarm-remote"]; ok && b.binary != "" {
		spec := testSpec(t)
		spec.Exec = "/usr/local/bin/" + b.binary
		out, err := Render(PlatformSystemd, spec)
		if err != nil {
			t.Fatalf("Render(systemd) for released binary %q error = %v", b.binary, err)
		}
		if !strings.Contains(string(out), b.binary) {
			t.Errorf("systemd unit does not reference the released binary name %q", b.binary)
		}
	}

	// The header comment is part of the contract: it currently asserts the opposite of
	// what this test requires, and a stale comment on a release manifest is how the next
	// person ships the wrong set of binaries.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, stale := range []string{"ONLY release artifact", "never built by this pipeline"} {
		if strings.Contains(string(raw), stale) {
			t.Errorf("%s still says %q, which contradicts the build list it now carries", filepath.Base(path), stale)
		}
	}
}

// TestReleaseBinaries_BuildStatically is PB-OPS-4's "buildable" half, under the release
// pipeline's own conditions (CGO_ENABLED=0, the setting .goreleaser.yaml pins). A binary
// that only builds with cgo cannot ship in this matrix, and the failure would otherwise
// surface for the first time during a release.
func TestReleaseBinaries_BuildStatically(t *testing.T) {
	out := t.TempDir()
	for _, pkg := range []string{"swarm", "swarm-remote", "swarm-relay"} {
		t.Run(pkg, func(t *testing.T) {
			cmd := exec.Command("go", "build", "-o", filepath.Join(out, pkg), "github.com/Nathandela/swarm/cmd/"+pkg)
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("CGO_ENABLED=0 go build ./cmd/%s: %v\n%s", pkg, err, b)
			}
		})
	}
}

func keysOf(m map[string]releaseBuild) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
