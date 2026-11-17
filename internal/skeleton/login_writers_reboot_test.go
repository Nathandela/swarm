package skeleton

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

// A host reboot kills the shim before it can publish native-writers-stopped.json.
// The boot ID in the native record proves the recorded writers ended with the old
// boot, so the session stays resumable without that proof.
func TestNativeRecordFromPreviousBootProvesWritersStopped(t *testing.T) {
	current, err := procstart.BootID()
	if err != nil || current == "" {
		t.Fatalf("current boot id unavailable: %q, %v", current, err)
	}
	self, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	const deadShim = 1 << 30 // above every pid_max: never a live process
	for _, tc := range []struct {
		name, boot string
		want       error
	}{
		{"previous boot", "00000000-0000-4000-8000-000000000000", nil},
		{"this boot", current, logins.ErrInUse},
		{"boot not recorded", "", logins.ErrInUse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			meta := persist.Meta{ID: "rebootproofaaaaa", AgentType: logins.ProviderClaude, Status: status.Status{Process: status.ProcessLost}, ShimPID: deadShim, ShimStartTime: 1}
			// The recorded writer pid and group name this live test process: after a
			// reboot those numbers belong to unrelated processes and must not block.
			native := shim.NativeProcessInfo{SchemaVersion: shim.NativeWriterSchemaVersion, Generation: strings.Repeat("ab", 16), PID: os.Getpid(), PGID: syscall.Getpgrp(), StartTime: self, ShimPID: deadShim, ShimStartTime: 1, Provider: logins.ProviderClaude, BootID: tc.boot}
			raw, err := json.Marshal(native)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(state, meta.ID)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, shim.NativeProcessFile), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := verifyLoginWritersStopped(state, meta); !errors.Is(err, tc.want) {
				t.Fatalf("verifyLoginWritersStopped = %v, want %v", err, tc.want)
			}
		})
	}
}
