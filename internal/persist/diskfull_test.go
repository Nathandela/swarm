package persist

// Disk-full (ENOSPC) injection for the meta write path (E14.3 T7b, invariant S8).
// TestCrashDuringSaveNeverTears already proves S8 against a real SIGKILL mid-write;
// this proves the complementary ERROR-RETURN path: an ENOSPC while committing the
// temp file must fail the Save cleanly — never a torn or partial meta.json, never a
// leftover temp file, never a panic or a hang — leaving the previously committed
// meta.json byte-intact, and the store must recover once space is available.
//
// The fault is injected at the persist layer's sole byte-commit point via the
// writeTemp seam (a package var defaulting to the real *os.File.Write); production
// always uses the default, so behavior is unchanged.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSaveDiskFullNeverTears(t *testing.T) {
	s, root := newTestStore(t)
	metaPath := filepath.Join(root, "sess-abc123", metaFile)

	// 1. Commit a good baseline meta and snapshot its exact on-disk bytes.
	base := fullMeta()
	base.ConversationID = "conv-before-diskfull"
	if err := s.Save(base); err != nil {
		t.Fatalf("baseline Save: %v", err)
	}
	before, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read baseline meta.json: %v", err)
	}

	// 2. Arm a disk-full fault on the meta write path, restored on cleanup.
	orig := writeTemp
	t.Cleanup(func() { writeTemp = orig })
	writeTemp = func(*os.File, []byte) (int, error) { return 0, syscall.ENOSPC }

	// 3. A Save under ENOSPC must return the error promptly — no panic, no hang.
	next := base
	next.ConversationID = "conv-after-diskfull"
	errCh := make(chan error, 1)
	go func() { errCh <- s.Save(next) }()
	select {
	case saveErr := <-errCh:
		if saveErr == nil {
			t.Fatal("Save under ENOSPC returned nil; want a disk-full error")
		}
		if !errors.Is(saveErr, syscall.ENOSPC) {
			t.Fatalf("Save under ENOSPC error = %v; want it to wrap ENOSPC", saveErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Save under ENOSPC blocked (>5s); it must fail fast, never hang")
	}

	// 4. The prior commit is byte-intact: the failed write never touched meta.json.
	after, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("meta.json unreadable after a failed Save (torn or removed?): %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("a failed Save mutated meta.json (torn): before=%dB after=%dB", len(before), len(after))
	}
	got, err := s.Load("sess-abc123")
	if err != nil {
		t.Fatalf("Load after a failed Save (torn read or lost commit): %v", err)
	}
	if got.ConversationID != base.ConversationID {
		t.Fatalf("Load returned the half-written meta (conv=%q); want the prior committed %q",
			got.ConversationID, base.ConversationID)
	}

	// 5. No leftover temp file: the atomic path removed its meta.json.tmp*.
	leftovers, err := filepath.Glob(filepath.Join(root, "sess-abc123", metaFile+".tmp*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("a failed Save left temp files behind: %v", leftovers)
	}

	// 6. With space back, the store recovers: the next Save commits and Loads.
	writeTemp = orig
	if err := s.Save(next); err != nil {
		t.Fatalf("Save after ENOSPC cleared: %v", err)
	}
	got2, err := s.Load("sess-abc123")
	if err != nil {
		t.Fatalf("Load after recovery: %v", err)
	}
	if got2.ConversationID != next.ConversationID {
		t.Fatalf("recovered Load conv=%q; want %q", got2.ConversationID, next.ConversationID)
	}
}
