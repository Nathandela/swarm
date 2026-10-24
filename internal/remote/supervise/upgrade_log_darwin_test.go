//go:build darwin

package supervise

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Explicit qualification only: this creates one uniquely named fixture job in
// the caller's GUI domain and removes that exact job before removing its files.
func TestUpgradeLogNativeLaunchdJob(t *testing.T) {
	if os.Getenv("SWARM_TEST_LAUNCHD_UPGRADE_LOG") != "1" {
		t.Skip("set SWARM_TEST_LAUNCHD_UPGRADE_LOG=1 for isolated launchd qualification")
	}
	args, path := upgradeLogFixture(t)
	label := fmt.Sprintf("com.swarm.upgrade.storage-test.%d.%d", os.Getpid(), time.Now().UnixNano())
	raw := string(readUpgradeTemplate(t))
	raw = strings.Replace(raw, "<string>com.swarm.upgrade</string>", "<string>"+label+"</string>", 1)
	raw = strings.ReplaceAll(raw, "@HOME@", strings.TrimSuffix(path, "/.local/state/swarm/upgrade.log"))
	raw = strings.ReplaceAll(raw, "@PREFIX@", args[5])
	calendar := regexp.MustCompile(`(?s)<key>StartCalendarInterval</key>\s*<dict>.*?</dict>`)
	raw = calendar.ReplaceAllString(raw, "<key>RunAtLoad</key><true/>")
	job := filepath.Join(t.TempDir(), label+".plist")
	if err := os.WriteFile(job, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	service := domain + "/" + label
	command := exec.Command("/bin/launchctl", "bootstrap", domain, job)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bootstrap temporary launchd job: %v: %s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("/bin/launchctl", "bootout", service).CombinedOutput(); err != nil {
			t.Errorf("bootout temporary launchd job: %v: %s", err, out)
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		out, err := exec.Command("/bin/launchctl", "print", service).CombinedOutput()
		if err != nil {
			t.Fatalf("inspect temporary launchd job: %v: %s", err, out)
		}
		if strings.Contains(string(out), "last exit code = 17") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("temporary launchd job did not preserve final status: %s", out)
		}
		time.Sleep(100 * time.Millisecond)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "brew:update\nbrew:upgrade --cask swarm\nrestart:daemon restart --unattended\nstderr:current-run\n"
	if string(got) != want {
		t.Fatalf("native launchd log=%q want=%q", got, want)
	}
	t.Logf("qualified %s: final status 17; current invocation logged; exact job removed by cleanup", service)
}
