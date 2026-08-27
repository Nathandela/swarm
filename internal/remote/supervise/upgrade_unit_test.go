package supervise

// FAILING-FIRST tests for auto-upgrade-plan.md L1: the nightly timer's TEMPLATE,
// packaging/launchd/com.swarm.upgrade.plist. Read RAW here -- no @PREFIX@/@HOME@
// substitution -- because the property under test is the template itself; substitution is
// the owner's one install-time `sed`, documented in the template's own header comment.
//
// `internal/remote/supervise` is otherwise platform-symmetric (one Spec renders both a
// launchd plist and a systemd unit), and this template is the one place that is not: the
// timer is a launchd-only mechanism (brew, and this repo's only auto-upgrade path, are
// both macOS). It lives beside this package because launchdProgramArguments -- the same
// plist walk `launchdExec` uses -- is what parses it.
//
// The negative control follows the repo norm for a checked-in artifact (see
// internal/design/tokens_test.go, TestTheDriftCheckCanActuallyFail): perturb a COPY in
// memory and prove the check rejects it. Nothing here writes to the tracked file.

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// upgradeTemplatePath is the tracked template. packaging/ is dist/'s tracked sibling --
// dist/ is goreleaser's gitignored output (packaging/homebrew/swarm.rb is the precedent).
const upgradeTemplatePath = "../../../packaging/launchd/com.swarm.upgrade.plist"

func readUpgradeTemplate(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(upgradeTemplatePath)
	if err != nil {
		t.Fatalf("read %s: %v", upgradeTemplatePath, err)
	}
	return raw
}

// checkUpgradeShellCommand is ProgramArguments[2]'s contract (auto-upgrade-plan.md L1):
// brew's upgrade runs before the converge, joined by ";" and never "&&", so a brew upgrade
// that finds swarm already current -- the common case -- still runs the converge.
func checkUpgradeShellCommand(cmd string) error {
	brewAt := strings.Index(cmd, "@PREFIX@/bin/brew upgrade --cask swarm")
	restartAt := strings.Index(cmd, "@PREFIX@/bin/swarm daemon restart --unattended")
	switch {
	case brewAt == -1:
		return fmt.Errorf("missing the brew upgrade command: %q", cmd)
	case restartAt == -1:
		return fmt.Errorf("missing the daemon restart command: %q", cmd)
	case brewAt >= restartAt:
		return fmt.Errorf("daemon restart runs before brew upgrade: %q", cmd)
	case strings.Contains(cmd, "&&"):
		return fmt.Errorf("joined by \"&&\" -- a no-op brew upgrade would short-circuit the converge: %q", cmd)
	case !strings.Contains(cmd, ";"):
		return fmt.Errorf("not joined by \";\": %q", cmd)
	}
	return nil
}

// TestUpgradeTemplate_ProgramArguments walks the RAW template through
// launchdProgramArguments -- the same plist walk launchdExec uses, generalized to every
// element instead of just the first -- and checks the three-element shell-out shape a
// single ProgramArguments string would break (unit.go's own comment: launchd would exec a
// file literally named "/bin/sh -c ...").
func TestUpgradeTemplate_ProgramArguments(t *testing.T) {
	args, err := launchdProgramArguments(readUpgradeTemplate(t))
	if err != nil {
		t.Fatalf("launchdProgramArguments: %v", err)
	}
	if len(args) != 3 {
		t.Fatalf("ProgramArguments has %d elements, want 3: %v", len(args), args)
	}
	if args[0] != "/bin/sh" {
		t.Errorf("ProgramArguments[0] = %q, want /bin/sh", args[0])
	}
	if args[1] != "-c" {
		t.Errorf("ProgramArguments[1] = %q, want -c", args[1])
	}
	if err := checkUpgradeShellCommand(args[2]); err != nil {
		t.Errorf("ProgramArguments[2]: %v", err)
	}
}

// TestUpgradeTemplate_CheckCanActuallyFail is the negative control: checkUpgradeShellCommand
// is fed the template's OWN command with ";" swapped for "&&" -- the one hand-edit that
// would silently reintroduce revision 3's postflight bug (a deferred night never retried)
// -- and must reject it.
func TestUpgradeTemplate_CheckCanActuallyFail(t *testing.T) {
	args, err := launchdProgramArguments(readUpgradeTemplate(t))
	if err != nil {
		t.Fatalf("launchdProgramArguments: %v", err)
	}
	real := args[2]
	if err := checkUpgradeShellCommand(real); err != nil {
		t.Fatalf("the unperturbed command fails its own check: %v", err)
	}

	mutated := strings.Replace(real, ";", "&&", 1)
	if mutated == real {
		t.Fatal("the perturbation did not apply; this control proves nothing")
	}
	if err := checkUpgradeShellCommand(mutated); err == nil {
		t.Fatal("checkUpgradeShellCommand accepted \"&&\" in place of \";\"")
	}
}

var (
	upgradeLabelRe  = regexp.MustCompile(`<key>Label</key>\s*<string>([^<]*)</string>`)
	upgradeStdoutRe = regexp.MustCompile(`<key>StandardOutPath</key>\s*<string>([^<]*)</string>`)
	upgradeStderrRe = regexp.MustCompile(`<key>StandardErrorPath</key>\s*<string>([^<]*)</string>`)
)

func TestUpgradeTemplate_Label(t *testing.T) {
	m := upgradeLabelRe.FindStringSubmatch(string(readUpgradeTemplate(t)))
	if m == nil {
		t.Fatal("template declares no Label string value")
	}
	if m[1] != "com.swarm.upgrade" {
		t.Errorf("Label = %q, want com.swarm.upgrade", m[1])
	}
}

func TestUpgradeTemplate_LogPathsAreTemplatedOnHome(t *testing.T) {
	raw := string(readUpgradeTemplate(t))
	for _, re := range []*regexp.Regexp{upgradeStdoutRe, upgradeStderrRe} {
		m := re.FindStringSubmatch(raw)
		if m == nil {
			t.Fatalf("template declares no match for %s", re.String())
		}
		if !strings.HasPrefix(m[1], "@HOME@") {
			t.Errorf("log path %q does not start with @HOME@", m[1])
		}
	}
}

func TestUpgradeTemplate_EnvironmentVariables(t *testing.T) {
	raw := string(readUpgradeTemplate(t))
	if !strings.Contains(raw, "HOMEBREW_NO_INSTALL_CLEANUP") {
		t.Error("template's EnvironmentVariables is missing HOMEBREW_NO_INSTALL_CLEANUP " +
			"(cleanup exited 1 on an unrelated file once, masking a successful install)")
	}
}
