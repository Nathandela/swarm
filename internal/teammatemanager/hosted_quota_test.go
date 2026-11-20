package teammatemanager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/discord"
	"github.com/Nathandela/swarm/internal/protocol"
)

// The separate testdata overlay modifies one genuine completion notification.
func armHostedManagerQuota(t *testing.T, state, project string, old discord.TeammateNativeStatus, owner *protocol.Client, send func(string, string, string), wait func(string, time.Duration, func() bool)) string {
	t.Helper()
	wait("old dispatcher finished its previous native turn", time.Minute, func() bool {
		v, err := owner.ManagerRuntime(old.ManagerSession, old.ManagerInstance)
		return err == nil && v.ReadyForInput
	})
	send("302", "401", "While the worker continues, run a shell command that waits until "+filepath.Join(project, "quota-release")+" exists (poll every second for up to 60 seconds), then end this turn. Do not start or contact a worker. Leave the ongoing worker job intact.")
	var target struct{ Session, Instance, Thread, Turn string }
	wait("quota probe has a real running dispatcher turn", time.Minute, func() bool {
		v, err := owner.ManagerRuntime(old.ManagerSession, old.ManagerInstance)
		if err != nil || v.Turn.State != "running" || v.Turn.NativeTurnID == "" {
			return false
		}
		_, target.Session, _ = strings.Cut(old.ManagerSession, "/")
		target.Instance, target.Thread, target.Turn = old.ManagerInstance, v.Binding.NativeSessionID, v.Turn.NativeTurnID
		return true
	})
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(state, ".hosted-quota-target"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("armed fixture for manager %s instance %s thread %s turn %s", old.ManagerSession, target.Instance, target.Thread, target.Turn)
	return target.Turn
}

func finishHostedManagerQuota(t *testing.T, state, project string, old discord.TeammateNativeStatus, owner *protocol.Client, turn string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(project, "quota-release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(75 * time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		v, err := owner.ManagerRuntime(old.ManagerSession, old.ManagerInstance)
		if err == nil && v.ProviderExhausted && v.Turn.NativeTurnID == turn && v.Turn.State == "completed" && v.Turn.Reason == "error" {
			if _, err = os.Stat(filepath.Join(state, ".hosted-quota-target.consumed")); err != nil {
				t.Fatal(err)
			}
			t.Logf("original dispatcher exposed verified quota for terminal turn %s while connector offline", turn)
			return
		}
	}
	t.Fatal("isolated Codex callback did not report correlated quota; build the fixture with the testdata quota_overlay.py Go overlay")
}
