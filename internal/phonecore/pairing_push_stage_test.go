package phonecore

import (
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

func stageCore(t *testing.T, dir string, wake, content Sealer) *Core {
	t.Helper()
	c, err := Resume(Config{Dir: dir, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPairingPushStage_SurvivesCrashAndCommitsIdempotently(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	c := stageCore(t, dir, wake, content)
	var addr PushAddress
	addr[0] = 0x51
	var key crypto.WakeKey
	key[0] = 0x52

	if err := c.StagePushBinding(addr, key); err != nil {
		t.Fatal(err)
	}
	restarted := stageCore(t, dir, wake, content)
	if got := restarted.PendingPushBindingRevocations(); len(got) != 1 || got[0] != addr {
		t.Fatalf("pending after restart=%x, want staged address", got)
	}
	if err := restarted.CommitStagedPushBinding(addr); err != nil {
		t.Fatal(err)
	}
	if err := restarted.CommitStagedPushBinding(addr); err != nil {
		t.Fatalf("idempotent commit: %v", err)
	}
	if got := restarted.PendingPushBindingRevocations(); len(got) != 0 {
		t.Fatalf("pending after commit=%x, want none", got)
	}
	if err := restarted.AcceptWakeV1(r3aSeal(t, key, addr, 1, time.Now())); err != nil {
		t.Fatalf("committed binding refused its first wake: %v", err)
	}
}

func TestPairingPushStage_AbandonDropsKeyButRetainsDurableRevokeUntilConfirmed(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	c := stageCore(t, dir, wake, content)
	var addr PushAddress
	addr[0] = 0x61
	var key crypto.WakeKey
	key[0] = 0x62
	if err := c.StagePushBinding(addr, key); err != nil {
		t.Fatal(err)
	}
	if err := c.AbandonStagedPushBinding(addr); err != nil {
		t.Fatal(err)
	}

	restarted := stageCore(t, dir, wake, content)
	if err := restarted.AcceptWakeV1(r3aSeal(t, key, addr, 1, time.Now())); !errors.Is(err, ErrNoWakeKey) {
		t.Fatalf("wake after abandonment=%v, want ErrNoWakeKey", err)
	}
	if got := restarted.PendingPushBindingRevocations(); len(got) != 1 || got[0] != addr {
		t.Fatalf("pending revoke after abandonment=%x, want address", got)
	}
	if err := restarted.CompleteStagedPushRevoke(addr); err != nil {
		t.Fatal(err)
	}
	if err := restarted.CompleteStagedPushRevoke(addr); err != nil {
		t.Fatalf("idempotent revoke completion: %v", err)
	}
	if got := restarted.PendingPushBindingRevocations(); len(got) != 0 {
		t.Fatalf("pending after confirmed revoke=%x, want none", got)
	}
}
