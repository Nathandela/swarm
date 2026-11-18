package managerproof

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func replayFixture(t *testing.T) (*Replay, string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	id, err := InitializeReplay(dir, "place/session", "conversation")
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenReplay(dir, "place/session", "conversation", id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Claim("actual-observation"); err != nil {
		t.Fatal(err)
	}
	return store, dir, id
}
func TestReplayGuardsStayNegativeAcrossReopenAndScopeChange(t *testing.T) {
	s, dir, id := replayFixture(t)
	b := proofBinding("claude")
	key := ReplayKey{Kind: ReplayCall, ID: "call", Owner: ReplayFingerprint("turn", "", "publish")}
	if err := s.Reserve(b, key); err != nil {
		t.Fatal(err)
	}
	if err := s.Reserve(b, key); err != nil {
		t.Fatal("exact live duplicate rejected", err)
	}
	changed := key
	changed.Owner = ReplayFingerprint("other", "", "publish")
	if err := s.Reserve(b, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("ID rebound", err)
	}
	if err := s.CloseKeys(b, key); err != nil {
		t.Fatal(err)
	}
	if err := s.Reserve(b, key); !errors.Is(err, ErrStale) {
		t.Fatal("closed ID reopened", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenReplay(dir, "place/session", "conversation", id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if err := reopened.Claim("replacement-observation"); !errors.Is(err, ErrStale) {
		t.Fatal("unclean source ownership silently renewed", err)
	}
	if seen, err := reopened.Seen(ReplayCall, "call"); err != nil || !seen {
		t.Fatal("negative guard lost", err)
	}
}
func TestReplayMissingReplacedCorruptAuthorityFailsClosed(t *testing.T) {
	for _, mode := range []string{"missing", "wrong-id", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			s, dir, id := replayFixture(t)
			_ = s.Close()
			switch mode {
			case "missing":
				_ = os.Remove(filepath.Join(dir, ReplayFile))
			case "wrong-id":
				id = strings.Repeat("a", 32)
			case "corrupt":
				if err := os.WriteFile(filepath.Join(dir, ReplayFile), []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if r, err := OpenReplay(dir, "place/session", "conversation", id); err == nil {
				_ = r.Close()
				t.Fatal("lost authority reopened")
			}
			if _, err := InitializeReplay(dir, "place/session", "conversation"); err == nil {
				t.Fatal("existing authority silently recreated")
			}
		})
	}
}
func TestReplayTerminalReceiptIsHistoryWithoutPositivePermission(t *testing.T) {
	s, _, _ := replayFixture(t)
	b := proofBinding("claude")
	nonce := strings.Repeat("a", 64)
	keys := []ReplayKey{{Kind: ReplayNonce, ID: nonce, Owner: ReplayFingerprint("operation")}, {Kind: ReplayOperation, ID: "operation", Owner: ReplayFingerprint(nonce)}, {Kind: ReplayTurn, ID: "turn", Owner: ReplayFingerprint("")}}
	if err := s.Reserve(b, keys...); err != nil {
		t.Fatal(err)
	}
	receipt := TerminalReceipt{Binding: b, Nonce: nonce, OperationID: "operation", TextHash: strings.Repeat("b", 64), NativeTurnID: "turn", Reason: "answer"}
	if err := s.RecordTerminal(receipt, keys...); err != nil {
		t.Fatal(err)
	}
	got, err := s.Terminal(b, nonce)
	if err != nil || got.NativeTurnID != "turn" || got.Reason != "answer" {
		t.Fatal("compact terminal history lost", err)
	}
	if err := s.Reserve(b, keys...); !errors.Is(err, ErrStale) {
		t.Fatal("terminal receipt restored permission", err)
	}
	replacement := b
	replacement.ObservationGeneration = "replacement"
	if _, err := s.Terminal(replacement, nonce); !errors.Is(err, ErrStale) {
		t.Fatal("history silently rebound", err)
	}
}
func TestReplayAdmissionCapStillAllowsExistingClosure(t *testing.T) {
	s, _, _ := replayFixture(t)
	s.maxGuards = 2
	b := proofBinding("claude")
	key := ReplayKey{Kind: ReplayCall, ID: "one", Owner: ReplayFingerprint("turn")}
	other := key
	other.ID = "two"
	if err := s.Reserve(b, key, other); err != nil {
		t.Fatal(err)
	}
	extra := key
	extra.ID = "three"
	if err := s.Reserve(b, extra); !errors.Is(err, ErrCapacity) {
		t.Fatal("admission ignored capacity", err)
	}
	if err := s.CloseKeys(b, key); err != nil {
		t.Fatal("existing closure blocked at capacity", err)
	}
	if err := s.CloseKeys(b, extra); !errors.Is(err, ErrCapacity) {
		t.Fatal("unknown closure forgotten at capacity", err)
	}
}

func TestReplayRefusesUnusedIDsAfterBindingScopeChange(t *testing.T) {
	s, _, _ := replayFixture(t)
	b := proofBinding("claude")
	key := ReplayKey{Kind: ReplayCall, ID: "first", Owner: ReplayFingerprint("turn")}
	if err := s.Reserve(b, key); err != nil {
		t.Fatal(err)
	}
	b.ObservationGeneration = "new-observer"
	key.ID = "new-id"
	if err := s.Reserve(b, key); !errors.Is(err, ErrQuarantined) {
		t.Fatal("changed source renewed unused-ID authority", err)
	}
}
func TestReplayTerminalCannotInventUnreservedCompletion(t *testing.T) {
	s, _, _ := replayFixture(t)
	r := TerminalReceipt{Binding: proofBinding("claude"), Nonce: strings.Repeat("a", 64), OperationID: "unseen", TextHash: strings.Repeat("b", 64), NativeTurnID: "unseen-turn", Reason: "answer"}
	if err := s.RecordTerminal(r); !errors.Is(err, ErrUnbound) {
		t.Fatal("unreserved native completion was invented", err)
	}
}
func TestReplayRejectsMarkerSymlinkReplacementWhileOpen(t *testing.T) {
	s, dir, _ := replayFixture(t)
	path := filepath.Join(dir, replayMarker)
	backup := path + ".old"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backup, path); err != nil {
		t.Fatal(err)
	}
	if err := s.Reserve(proofBinding("claude"), ReplayKey{Kind: ReplayCall, ID: "new", Owner: ReplayFingerprint("turn")}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("replaced marker retained authority", err)
	}
}
func TestReplayDirtyOwnerQuarantinesEvenUncommittedObservedID(t *testing.T) {
	s, dir, id := replayFixture(t)
	// The source has observed/enqueued an ID but dies before its Reserve commit.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := OpenReplay(dir, "place/session", "conversation", id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Close() }()
	if err := next.Claim("replacement"); !errors.Is(err, ErrQuarantined) {
		t.Fatal("lost uncommitted observation silently renewed", err)
	}
	if err := next.Reserve(proofBinding("claude"), ReplayKey{Kind: ReplayCall, ID: "lost-before-commit", Owner: ReplayFingerprint("new-turn")}); !errors.Is(err, ErrQuarantined) {
		t.Fatal("lost ID could be borrowed after reopen", err)
	}
}
func TestReplayRecordSetIntegrityDetectsDeletedOrSubstitutedGuard(t *testing.T) {
	for _, mode := range []string{"delete", "substitute"} {
		t.Run(mode, func(t *testing.T) {
			s, dir, id := replayFixture(t)
			key := ReplayKey{Kind: ReplayCall, ID: "protected", Owner: ReplayFingerprint("turn")}
			if err := s.Reserve(proofBinding("claude"), key); err != nil {
				t.Fatal(err)
			}
			_ = s.Close()
			db, err := openReplayDB(filepath.Join(dir, ReplayFile), false)
			if err != nil {
				t.Fatal(err)
			}
			if err = db.Update(func(tx *bolt.Tx) error {
				b := tx.Bucket([]byte("records"))
				k, v := b.Cursor().First()
				if mode == "delete" {
					return b.Delete(k)
				}
				changed := append([]byte(nil), v...)
				changed[34] ^= 1
				return b.Put(k, changed)
			}); err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
			if got, err := OpenReplay(dir, "place/session", "conversation", id); err == nil {
				_ = got.Close()
				t.Fatal("changed keyed record set reopened")
			}
		})
	}
}

func TestReplayWriteFailureCannotRenewSameOpenOwner(t *testing.T) {
	s, dir, id := replayFixture(t)
	if s.descriptor == nil {
		t.Fatal("fixture has no owned descriptor")
	}
	if err := s.descriptor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Reserve(proofBinding("claude"), ReplayKey{Kind: ReplayCall, ID: "not-durable", Owner: ReplayFingerprint("turn")}); !errors.Is(err, ErrQuarantined) {
		t.Fatal("failed write retained usable source", err)
	}
	if err := s.Claim("actual-observation"); !errors.Is(err, ErrQuarantined) {
		t.Fatal("failed owner renewed in memory", err)
	}
	_ = s.Close()
	next, err := OpenReplay(dir, "place/session", "conversation", id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Close() }()
	if err := next.Claim("new-observer"); !errors.Is(err, ErrQuarantined) {
		t.Fatal("failed dirty write was forgotten across reopen", err)
	}
}
