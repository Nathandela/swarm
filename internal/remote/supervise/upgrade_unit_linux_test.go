package supervise

// Guards for auto-upgrade-plan.md L1's LINUX half: the systemd templates in
// packaging/systemd/ (the launchd twin's guards are upgrade_unit_test.go, same posture:
// the tracked template is the artifact under test, and nothing here writes to it).
//
// What must hold, and why it is load-bearing:
//
//   - The script converges UNCONDITIONALLY (`fetch || true` before the exec): the plist
//     joins its steps with ";" for the same reason -- a restart deferred one night must
//     be retried the next, and a binary upgraded by hand must be converged too.
//   - The script's own exit status is `daemon restart --unattended`'s (it ends in exec),
//     and the service names ADR-020's non-action codes (2 deferred, 3 refused) as
//     success, so a deferral is not a red unit in `systemctl --user status`.
//   - The tarball is verified against the release's checksums.txt before anything is
//     installed, and the install is stage-then-rename, never a truncate of the running
//     daemon's inode.
//   - Persistent=true is systemd's spelling of launchd's run-missed-jobs-at-wake.

import (
	"os"
	"regexp"
	"testing"
)

func readTemplateFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("tracked template missing: %v", err)
	}
	return string(b)
}

func TestLinuxUpgradeScriptConvergesWhetherOrNotTheFetchSucceeded(t *testing.T) {
	s := readTemplateFile(t, "../../../packaging/systemd/swarm-upgrade.sh")
	if !regexp.MustCompile(`(?m)^fetch \|\| true$`).MatchString(s) {
		t.Fatal("the fetch must not gate the converge (the plist's \";\" rule): want a `fetch || true` line")
	}
	if !regexp.MustCompile(`(?m)^exec "\$SWARM_BIN" daemon restart --unattended$`).MatchString(s) {
		t.Fatal("the script's exit status must be --unattended's: want a final `exec \"$SWARM_BIN\" daemon restart --unattended`")
	}
	if !regexp.MustCompile(`sha256sum --check`).MatchString(s) {
		t.Fatal("nothing installs unverified: want a `sha256sum --check` against the release's checksums.txt")
	}
	if !regexp.MustCompile(`mv -f "\$dst\.new" "\$dst"`).MatchString(s) {
		t.Fatal("the install must be stage-then-rename, never a truncate of the running binary's inode")
	}
}

func TestLinuxUpgradeServiceTreatsConvergeNonActionsAsSuccess(t *testing.T) {
	s := readTemplateFile(t, "../../../packaging/systemd/swarm-upgrade.service")
	if !regexp.MustCompile(`(?m)^SuccessExitStatus=2 3$`).MatchString(s) {
		t.Fatal("ADR-020's deferred (2) and refused (3) are non-actions, not failures: want SuccessExitStatus=2 3")
	}
	if !regexp.MustCompile(`(?m)^Type=oneshot$`).MatchString(s) {
		t.Fatal("the nightly run is a job, not a daemon: want Type=oneshot")
	}
	if !regexp.MustCompile(`(?m)^ExecStart=%h/\.local/bin/swarm-upgrade$`).MatchString(s) {
		t.Fatal("the unit runs the installed script by %h, systemd's own home expansion: want ExecStart=%h/.local/bin/swarm-upgrade")
	}
}

func TestLinuxUpgradeTimerRunsMissedNightsLikeLaunchdDoes(t *testing.T) {
	s := readTemplateFile(t, "../../../packaging/systemd/swarm-upgrade.timer")
	if !regexp.MustCompile(`(?m)^OnCalendar=\*-\*-\* 04:00:00$`).MatchString(s) {
		t.Fatal("both platforms prefer the quiet hour: want OnCalendar=*-*-* 04:00:00")
	}
	if !regexp.MustCompile(`(?m)^Persistent=true$`).MatchString(s) {
		t.Fatal("launchd runs a missed StartCalendarInterval at wake; want its systemd spelling, Persistent=true")
	}
	if !regexp.MustCompile(`(?m)^WantedBy=timers\.target$`).MatchString(s) {
		t.Fatal("the timer must survive enable: want WantedBy=timers.target")
	}
}
