package gate

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ADR-008 -- the Go toolchain floor is 1.25, as a consequence of PB-BIND/§2's
// mandatory `golang.org/x/mobile` tool directive rather than as a free choice.
//
// This slice owns the fallout, because PB-TOOL-1 is "toolchain pinned in-repo",
// PB-TOOL-5 is "no Go regression" and PB-TOOL-7 is "CI covers the new artifacts".
//
// The defect these tests exist for is specific and it is NOT "the pin is wrong".
// `.github/workflows/ci.yml` pins go-version '1.24' while go.mod declares
// 1.25.0, and with the default GOTOOLCHAIN=auto those jobs do not fail -- Go
// downloads 1.25 and carries on. The pin has not broken; it has stopped being
// true, silently, which is worse, because the job still reports success and the
// version in the YAML is what a reader believes was tested.
//
// EVERY expectation below is DERIVED from go.mod's own `go` directive. None of
// them contains the string "1.25". At the next bump the tests keep working and
// keep failing on a stale pin; a test that enumerated versions would have to be
// remembered, and would rot exactly when it mattered.

// goVersion is a parsed Go release. patch is -1 when the version names only a
// release line ("1.25"), which is how setup-go's go-version is usually written
// and which resolves to the newest patch in that line.
type goVersion struct {
	major, minor, patch int
}

var goVersionRe = regexp.MustCompile(`^(\d+)\.(\d+)(?:\.(\d+))?$`)

func parseGoVersion(s string) (goVersion, bool) {
	m := goVersionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return goVersion{}, false
	}
	v := goVersion{patch: -1}
	v.major, _ = strconv.Atoi(m[1])
	v.minor, _ = strconv.Atoi(m[2])
	if m[3] != "" {
		v.patch, _ = strconv.Atoi(m[3])
	}
	return v, true
}

func (v goVersion) String() string {
	if v.patch < 0 {
		return strconv.Itoa(v.major) + "." + strconv.Itoa(v.minor)
	}
	return strconv.Itoa(v.major) + "." + strconv.Itoa(v.minor) + "." + strconv.Itoa(v.patch)
}

// satisfies reports whether a toolchain pinned as v can build a module that
// requires floor. A pin naming only a release line satisfies any floor in that
// same line, because setup-go resolves it to the newest patch.
func (v goVersion) satisfies(floor goVersion) bool {
	switch {
	case v.major != floor.major:
		return v.major > floor.major
	case v.minor != floor.minor:
		return v.minor > floor.minor
	case v.patch < 0:
		return true
	default:
		return v.patch >= floor.patch
	}
}

// moduleGoFloor is the single source of truth for every assertion in this file.
func moduleGoFloor(t *testing.T) goVersion {
	t.Helper()
	gomod := readFileOrFail(t, filepath.Join(repoRoot(t), "go.mod"), "ADR-008")
	m := regexp.MustCompile(`(?m)^go\s+(\S+)\s*$`).FindStringSubmatch(gomod)
	if m == nil {
		t.Fatalf("ADR-008: go.mod has no `go` directive to derive the floor from")
	}
	v, ok := parseGoVersion(m[1])
	if !ok {
		t.Fatalf("ADR-008: go.mod's `go %s` is not a version this test can parse", m[1])
	}
	return v
}

// TestPBTOOL5_EveryCIGoPinSatisfiesTheModuleFloor is the assertion the silent
// download defeats. It reads every actions/setup-go step in the workflow and
// fails on any that names a version the module cannot be built with.
func TestPBTOOL5_EveryCIGoPinSatisfiesTheModuleFloor(t *testing.T) {
	floor := moduleGoFloor(t)
	jobs := parseCIJobs(readFileOrFail(t, ciWorkflowPath(t), "PB-TOOL-5"))
	if len(jobs) == 0 {
		t.Fatalf("PB-TOOL-5: no jobs parsed from ci.yml; this assertion would pass vacuously")
	}

	checked := 0
	for _, j := range jobs {
		for _, pinned := range j.setupGoVersions() {
			if pinned == "" {
				t.Errorf("PB-TOOL-5: job %q sets Go up without a go-version, so it uses "+
					"whatever the runner image ships", j.id)
				continue
			}
			checked++
			v, ok := parseGoVersion(pinned)
			if !ok {
				t.Errorf("PB-TOOL-5: job %q pins go-version %q, which is not a plain version",
					j.id, pinned)
				continue
			}
			if !v.satisfies(floor) {
				t.Errorf("PB-TOOL-5/ADR-008: job %q pins Go %s but go.mod requires %s. This "+
					"does not fail the job -- with GOTOOLCHAIN=auto Go downloads %s and "+
					"builds anyway -- so the pin reads as verified while naming a toolchain "+
					"nothing was built with", j.id, v, floor, floor)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("PB-TOOL-5: no actions/setup-go step found in any job; the workflow reader "+
			"is broken or CI no longer pins Go at all. floor=%s", floor)
	}
}

// TestPBTOOL5_EveryLaneThatRunsGoPinsIt closes the other half. A job that shells
// out to `go` without a setup-go step runs the runner image's preinstalled
// toolchain, whose version this repository does not choose and GitHub changes
// without notice.
func TestPBTOOL5_EveryLaneThatRunsGoPinsIt(t *testing.T) {
	jobs := parseCIJobs(readFileOrFail(t, ciWorkflowPath(t), "PB-TOOL-5"))
	saw := false
	for _, j := range jobs {
		if !j.runsGoCommand() {
			continue
		}
		saw = true
		if len(j.setupGoVersions()) == 0 {
			t.Errorf("PB-TOOL-5: job %q runs the go tool but never sets Go up; it compiles "+
				"with the runner image's preinstalled toolchain", j.id)
		}
	}
	if !saw {
		t.Fatalf("PB-TOOL-5: no job appears to run the go tool; the reader is broken and " +
			"this assertion is vacuous")
	}
}

// TestPBTOOL1_GoToolchainIsPinnedSoAMismatchFailsLoudly.
//
// Raising the version strings alone fixes today and not tomorrow. GOTOOLCHAIN
// defaults to `auto`, so the moment go.mod's floor moves past the pinned
// toolchain again -- which ADR-008 records as having happened by accident, as a
// side effect of a tool directive -- CI silently downloads the newer one and the
// pin quietly stops describing the build a second time.
//
// GOTOOLCHAIN=local turns that into a hard error ("go.mod requires go >= X;
// GOTOOLCHAIN=local"), which is a one-line diff to fix and impossible to miss.
// A pin that auto-satisfies itself is not a pin. This is also what the
// release-dryrun job's existing comment already asks for in prose -- "this is
// the release pipeline, so its toolchain should be reproducible" -- for one job
// rather than all of them.
func TestPBTOOL1_GoToolchainIsPinnedSoAMismatchFailsLoudly(t *testing.T) {
	src := readFileOrFail(t, ciWorkflowPath(t), "PB-TOOL-1")
	workflowEnv := parseWorkflowEnv(src)
	jobs := parseCIJobs(src)

	checked := 0
	for _, j := range jobs {
		if len(j.setupGoVersions()) == 0 && !j.runsGoCommand() {
			continue
		}
		checked++
		got, ok := j.effectiveEnv(workflowEnv, "GOTOOLCHAIN")
		if !ok {
			t.Errorf("PB-TOOL-1/ADR-008: job %q builds Go code with no GOTOOLCHAIN set, so "+
				"it defaults to `auto` and will silently download a toolchain other than "+
				"the one it pins the moment go.mod's floor moves again", j.id)
			continue
		}
		if got != "local" {
			t.Errorf("PB-TOOL-1/ADR-008: job %q sets GOTOOLCHAIN=%q. Only `local` makes a "+
				"floor/pin mismatch fail rather than self-heal", j.id, got)
		}
	}
	if checked == 0 {
		t.Fatalf("PB-TOOL-1: no Go-building job found; this assertion is vacuous")
	}
}

// TestPBTOOL1_PinnedGoVersionSatisfiesTheModuleFloor ties the Android toolchain
// pin to the same source. The AAR is a Go cross-compile, so a fresh shell that
// sources the pin and gets a 1.24 toolchain cannot build it either.
func TestPBTOOL1_PinnedGoVersionSatisfiesTheModuleFloor(t *testing.T) {
	floor := moduleGoFloor(t)
	env := sourcePin(t, repoRoot(t))
	raw := strings.TrimSpace(env["SWARM_GO_VERSION"])
	v, ok := parseGoVersion(raw)
	if !ok {
		t.Fatalf("PB-TOOL-1: SWARM_GO_VERSION=%q is not a plain Go version", raw)
	}
	if !v.satisfies(floor) {
		t.Fatalf("PB-TOOL-1/ADR-008: the toolchain pin names Go %s but go.mod requires %s",
			v, floor)
	}
}

// TestPBTOOL1_DocumentedGoFloorMatchesTheModuleDirective.
//
// ADR-008's own consequences list names these two files. Both currently claim
// ">= 1.24", which was true when ADR-005 raised it for the VT emulator and is
// now the number a new contributor installs before discovering it cannot build.
// The floor is read out of each document and compared to go.mod, so this stays
// correct at the next bump instead of pinning today's answer.
func TestPBTOOL1_DocumentedGoFloorMatchesTheModuleDirective(t *testing.T) {
	floor := moduleGoFloor(t)
	root := repoRoot(t)

	// "Go toolchain >= 1.25", however the sentence around it is phrased.
	stated := regexp.MustCompile(`Go toolchain\s*>=\s*(\d+\.\d+(?:\.\d+)?)`)

	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		body := readFileOrFail(t, filepath.Join(root, name), "PB-TOOL-1")
		m := stated.FindStringSubmatch(body)
		if m == nil {
			t.Errorf("PB-TOOL-1/ADR-008: %s states no `Go toolchain >= X` floor", name)
			continue
		}
		v, ok := parseGoVersion(m[1])
		if !ok {
			t.Errorf("PB-TOOL-1: %s states an unparseable Go floor %q", name, m[1])
			continue
		}
		if !v.satisfies(floor) {
			t.Errorf("PB-TOOL-1/ADR-008: %s tells the reader Go %s is enough, but go.mod "+
				"requires %s. Someone following it installs a toolchain that cannot build "+
				"the repository", name, v, floor)
		}
		if !strings.Contains(body, "ADR-008") {
			t.Errorf("PB-TOOL-1/ADR-008: %s states a Go floor without citing ADR-008. The "+
				"floor moved as a side effect of a tool directive, which is exactly the "+
				"kind of change that reads as an accident without the decision next to it",
				name)
		}
	}
}

// TestPBTOOL1_ADR008Exists guards the citation above from pointing at nothing.
func TestPBTOOL1_ADR008Exists(t *testing.T) {
	path := filepath.Join(repoRoot(t), "docs", "adr", "ADR-008-go-toolchain-floor-1-25.md")
	if !exists(path) {
		t.Fatalf("ADR-008 is cited by CLAUDE.md and AGENTS.md but %s does not exist",
			mustRel(t, path))
	}
}
