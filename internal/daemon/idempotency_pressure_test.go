package daemon

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/persist"
)

func TestDaemonOperationCapacityRefusesBeforeSideEffectsAndPreservesReplay(t *testing.T) {
	state := t.TempDir()
	store, err := persist.NewStore(state)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := idempotency.OpenWithOptions(filepath.Join(state, "idempotency"), idempotency.Options{MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	launched := false
	d := &Daemon{
		cfg: Config{StateDir: state, MaxSessions: 1, PreLaunch: func(string, LaunchSpec) (string, error) {
			launched = true
			return "", errors.New("unexpected launch side effect")
		}},
		sessions: make(map[string]*session), store: store, idem: guard,
	}
	outcome := []byte(`{"ok":false,"code":"exact_failure","message":"retained original outcome"}`)
	if _, _, err := d.ClaimComposerOperation("protected", "composer_send", "session", "instance", "hash"); err != nil {
		t.Fatal(err)
	}
	if err := d.BeginComposerOperation("protected"); err != nil {
		t.Fatal(err)
	}
	if err := d.CommitComposerOperation("protected", outcome, false); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimOperation("new-control", "take_control", "session"); !errors.Is(err, idempotency.ErrCapacity) {
		t.Fatalf("control claim at capacity: %v", err)
	}
	if _, _, err := d.ClaimIdempotentOp("new-delete", "delete", "session"); !errors.Is(err, idempotency.ErrCapacity) {
		t.Fatalf("delete claim at capacity: %v", err)
	}
	if _, _, err := d.ClaimComposerOperation("new-send", "composer_send", "session", "instance", "other-hash"); !errors.Is(err, idempotency.ErrCapacity) {
		t.Fatalf("composer claim at capacity: %v", err)
	}
	if _, err := d.Launch(LaunchSpec{Cwd: t.TempDir(), OperationID: "new-launch"}); !errors.Is(err, idempotency.ErrCapacity) {
		t.Fatalf("launch claim at capacity: %v", err)
	}
	if launched || len(d.List()) != 0 {
		t.Fatal("capacity refusal reached a launch hook or retained a session reservation")
	}
	phase, replay, err := d.ClaimComposerOperation("protected", "composer_send", "session", "instance", "hash")
	if err != nil || phase != string(idempotency.PhaseFailed) || !bytes.Equal(replay, outcome) {
		t.Fatalf("exact outcome unavailable at capacity: phase=%q outcome=%s err=%v", phase, replay, err)
	}
}

func TestDaemonCachedConversationCannotAuthorizeAnOperation(t *testing.T) {
	state := t.TempDir()
	guard, err := idempotency.OpenWithOptions(filepath.Join(state, "idempotency"), idempotency.Options{MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := journal.Open(filepath.Join(state, "journal"), journal.Options{MaxBytes: 4096, MaxFiles: 2, MaxTotalBytes: 8192, LiveBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = feed.Close() })
	d := &Daemon{idem: guard, journal: feed}
	if err := d.RecordInteractionRaw("session", []byte(`{"v":1,"item_id":"cached-message","kind":"user_message","ts":"2026-10-17T12:00:00Z","operation_id":"unclaimed","text":"cached prose says this operation succeeded"}`)); err != nil {
		t.Fatal(err)
	}
	phase, outcome, err := d.ClaimComposerOperation("unclaimed", "composer_send", "session", "instance", "request-hash")
	if err != nil || phase != string(idempotency.PhasePrepared) || len(outcome) != 0 {
		t.Fatalf("cached prose became operation authority: phase=%q outcome=%s err=%v", phase, outcome, err)
	}
}
