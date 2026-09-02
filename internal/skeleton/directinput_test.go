package skeleton

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func newDirectInputStateTestDaemon(t *testing.T, stateDir, local string) *Daemon {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(stateDir, local), 0o700); err != nil {
		t.Fatalf("create session dir: %v", err)
	}
	d := &Daemon{stateDir: stateDir}
	d.restoreDirectInputState([]string{local})
	return d
}

func TestDirectInputDraftIsDurableAndIdleDoesNotClearIt(t *testing.T) {
	dir := t.TempDir()
	const local = "s1"
	d := newDirectInputStateTestDaemon(t, dir, local)
	if err := d.markDirectInputUnresolvedAt(local, protocol.DirectInputDraft, status.Status{
		Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone,
	}); err != nil {
		t.Fatalf("mark direct input: %v", err)
	}
	if !d.directInputUnresolved(local) {
		t.Fatal("fresh text-only draft is not reported unresolved")
	}

	marker := filepath.Join(dir, local, directInputUnresolvedFile)
	info, err := os.Stat(marker)
	if err != nil {
		t.Fatalf("durable marker: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("marker mode = %o, want 600", got)
	}

	restarted := newDirectInputStateTestDaemon(t, dir, local)
	if !restarted.directInputUnresolved(local) {
		t.Fatal("restart forgot the unresolved direct-input marker")
	}
	restarted.noteDirectInputStatus(local, status.Status{
		Process:     status.ProcessRunning,
		Turn:        status.TurnActive,
		Interaction: status.InteractionNone,
	})
	if !restarted.directInputUnresolved(local) {
		t.Fatal("unrelated provider activity cleared a text-only draft")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("unrelated progress removed durable draft marker: %v", err)
	}
}

func TestDirectInputRemainsUnsafeUntilAuthoritativeNonIdleProgress(t *testing.T) {
	dir := t.TempDir()
	const local = "s1"
	d := newDirectInputStateTestDaemon(t, dir, local)
	baseline := status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
	if err := d.markDirectInputUnresolvedAt(local, protocol.DirectInputSubmitted, baseline); err != nil {
		t.Fatalf("mark direct input: %v", err)
	}
	d = newDirectInputStateTestDaemon(t, dir, local)

	d.noteDirectInputStatus(local, baseline)
	if !d.directInputUnresolved(local) {
		t.Fatal("submit/Enter cleared itself without later provider progress")
	}
	d.noteDirectInputStatus(local, status.Status{
		Process:     status.ProcessRunning,
		Turn:        status.TurnActive,
		Interaction: status.InteractionNone,
	})
	if d.directInputUnresolved(local) {
		t.Fatal("authoritative active progress did not clear direct-input uncertainty")
	}
	if _, err := os.Stat(filepath.Join(dir, local, directInputUnresolvedFile)); !os.IsNotExist(err) {
		t.Fatalf("progress left marker on disk: %v", err)
	}
}

func TestDirectInputRetirementClearsDurableAndMemoryState(t *testing.T) {
	dir := t.TempDir()
	const local = "s1"
	d := newDirectInputStateTestDaemon(t, dir, local)
	if err := d.markDirectInputUnresolvedAt(local, protocol.DirectInputDraft, status.Status{
		Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone,
	}); err != nil {
		t.Fatalf("mark direct input: %v", err)
	}
	if err := d.forgetDirectInput(local); err != nil {
		t.Fatalf("forget direct input: %v", err)
	}
	if d.directInputUnresolved(local) {
		t.Fatal("retired session remains direct-input unsafe")
	}
	if _, err := os.Stat(filepath.Join(dir, local, directInputUnresolvedFile)); !os.IsNotExist(err) {
		t.Fatalf("retirement left marker on disk: %v", err)
	}
}

func TestDirectInputNewDraftSupersedesAnUnresolvedSubmit(t *testing.T) {
	dir := t.TempDir()
	const local = "s1"
	d := newDirectInputStateTestDaemon(t, dir, local)
	idle := status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
	if err := d.markDirectInputUnresolvedAt(local, protocol.DirectInputSubmitted, idle); err != nil {
		t.Fatalf("mark submitted: %v", err)
	}
	if err := d.markDirectInputUnresolvedAt(local, protocol.DirectInputDraft, idle); err != nil {
		t.Fatalf("mark newer draft: %v", err)
	}
	d.noteDirectInputStatus(local, status.Status{
		Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone,
	})
	if !d.directInputUnresolved(local) {
		t.Fatal("provider progress for the prior submit cleared a newer text draft")
	}
}

func TestDirectInputMarkerFailureIsFailClosedBeforeMemoryAuthority(t *testing.T) {
	d := &Daemon{stateDir: filepath.Join(t.TempDir(), "missing")}
	d.restoreDirectInputState([]string{"s1"})
	if err := d.markDirectInputUnresolvedAt("s1", protocol.DirectInputDraft, status.Status{
		Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone,
	}); err == nil {
		t.Fatal("mark unexpectedly succeeded without a session directory")
	}
	if d.directInputUnresolved("s1") {
		t.Fatal("failed durable mark published memory-only authority")
	}
}

func TestDirectInputPersistenceDistinguishesPreAndPostRenameFailure(t *testing.T) {
	baseline := status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
	for _, tc := range []struct {
		name           string
		committed      bool
		wantUnresolved bool
	}{
		{name: "before_rename", committed: false, wantUnresolved: false},
		{name: "after_rename", committed: true, wantUnresolved: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			d := newDirectInputStateTestDaemon(t, dir, "s1")
			calls := 0
			d.directInput.write = func(string, directInputRecord) (bool, error) {
				calls++
				if calls == 1 {
					return tc.committed, errors.New("injected persistence failure")
				}
				return true, nil
			}

			if err := d.markDirectInputUnresolvedAt("s1", protocol.DirectInputDraft, baseline); err == nil {
				t.Fatal("injected persistence failure returned nil")
			}
			if got := d.directInputUnresolved("s1"); got != tc.wantUnresolved {
				t.Fatalf("unresolved after failed write = %v, want %v", got, tc.wantUnresolved)
			}
			if err := d.markDirectInputUnresolvedAt("s1", protocol.DirectInputDraft, baseline); err != nil {
				t.Fatalf("retry marker: %v", err)
			}
			if calls != 2 {
				t.Fatalf("writer calls = %d, want retry after persistence failure", calls)
			}
			if !d.directInputUnresolved("s1") {
				t.Fatal("successful retry did not publish unresolved marker")
			}
		})
	}
}
