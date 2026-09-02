package skeleton

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/status"
)

func TestWriteAuthWatchStateReportsPreRenameFailureAsUncommitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), authWatchStateFile)
	want := errors.New("rename failed")
	committed, err := writeAuthWatchStateWithOps(path, []byte(`{"killed":{"s1":true}}`), authWatchStateWriteOps{
		rename: func(string, string) error { return want },
	})
	if committed || !errors.Is(err, want) {
		t.Fatalf("write = committed %v err %v, want false/%v", committed, err, want)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("pre-rename failure published target: %v", statErr)
	}
}

func TestWriteAuthWatchStateReportsPostRenameSyncFailureAsCommitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), authWatchStateFile)
	want := errors.New("directory sync failed")
	committed, err := writeAuthWatchStateWithOps(path, []byte(`{"killed":{"s1":true}}`), authWatchStateWriteOps{
		syncDir: func(string) error { return want },
	})
	if !committed || !errors.Is(err, want) {
		t.Fatalf("write = committed %v err %v, want true/%v", committed, err, want)
	}
	if _, readErr := os.ReadFile(path); readErr != nil {
		t.Fatalf("post-rename failure did not publish target: %v", readErr)
	}
}

func TestAuthRecyclePreRenameClaimFailureDoesNotKillOrClaim(t *testing.T) {
	f := newAuthFake(identityB)
	m := runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000024")
	f.add(m)
	w := testWatcher(t, f)
	w.writeState = func(string, []byte) (bool, error) {
		return false, errors.New("disk unavailable")
	}
	if retry := w.recycle("codex", m); !retry {
		t.Fatal("pre-rename claim failure dropped the pending recycle")
	}
	if len(f.killed) != 0 || w.state.Killed["s1"] {
		t.Fatalf("pre-rename failure killed=%v claim=%v", f.killed, w.state.Killed)
	}
}

func TestAuthRecyclePostRenameSyncFailureRetainsEmbargoAndRepersistsBeforeKill(t *testing.T) {
	f := newAuthFake(identityB)
	m := runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000025")
	f.add(m)
	w := testWatcher(t, f)
	d := &Daemon{}
	w.withRecycleFence = d.withAuthRecycleFence
	w.withClaimedRecycleFence = d.withClaimedAuthRecycleFence

	writes := 0
	w.writeState = func(string, []byte) (bool, error) {
		writes++
		if writes == 1 {
			return true, errors.New("directory sync failed")
		}
		return true, nil
	}
	if retry := w.recycle("codex", m); !retry {
		t.Fatal("post-rename uncertainty dropped the pending recycle")
	}
	if len(f.killed) != 0 || !w.state.Killed["s1"] || !d.composerRecycleInFlight("s1") {
		t.Fatalf("uncertain claim killed=%v state=%v embargo=%v", f.killed, w.state.Killed, d.composerRecycleInFlight("s1"))
	}

	if retry := w.recycle("codex", m); retry {
		t.Fatal("confirmed redrive did not complete")
	}
	if writes < 2 || len(f.killed) != 1 || len(f.launched) != 1 {
		t.Fatalf("redrive writes=%d kills=%v launches=%d, want >=2/1/1", writes, f.killed, len(f.launched))
	}
}
