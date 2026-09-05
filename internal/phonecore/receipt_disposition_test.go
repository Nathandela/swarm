package phonecore

import (
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

type failOnceStore struct {
	inner Store
	fail  bool
}

func (s *failOnceStore) Load() State { return s.inner.Load() }
func (s *failOnceStore) Save(st State) error {
	if s.fail {
		s.fail = false
		return errStoreDied
	}
	return s.inner.Save(st)
}
func (s *failOnceStore) ActivatePhoneBinding(st State) error {
	if s.fail {
		s.fail = false
		return errStoreDied
	}
	return s.inner.ActivatePhoneBinding(st)
}
func (s *failOnceStore) CommitPhonePairing(st State) error {
	if s.fail {
		s.fail = false
		return errStoreDied
	}
	return s.inner.CommitPhonePairing(st)
}
func (s *failOnceStore) ReplacePhoneCheckpoint(st State) error {
	if s.fail {
		s.fail = false
		return errStoreDied
	}
	return s.inner.ReplacePhoneCheckpoint(st)
}
func (s *failOnceStore) PurgeKeys() error                   { return s.inner.PurgeKeys() }
func (s *failOnceStore) UnsealContent() error               { return s.inner.UnsealContent() }
func (s *failOnceStore) RewindRelayCursor() error           { return s.inner.RewindRelayCursor() }
func (s *failOnceStore) SetRelayIncarnation(v string) error { return s.inner.SetRelayIncarnation(v) }

func TestReceiptDisposition_DistinguishesDiscardableJunkFromRetainedEvidence(t *testing.T) {
	t.Run("malformed envelope is discardable", func(t *testing.T) {
		st := &memStore{}
		seedPaired(t, st)
		r := resumeRouter(t, st, &recordingAcker{})
		receipt, err := r.AcceptCommit([]byte("not an envelope"), 11)
		if err == nil {
			t.Fatal("AcceptCommit(malformed) = nil error")
		}
		if receipt.Disposition != ReceiptDiscardable {
			t.Fatalf("malformed receipt disposition = %v, want discardable", receipt.Disposition)
		}
	})

	t.Run("failed durable commit is retained", func(t *testing.T) {
		st := &memStore{}
		seedPaired(t, st)
		r := resumeRouter(t, &failAfterNStore{inner: st, n: 0}, &recordingAcker{})
		raw := sealFrame(t, testContentKey(), 1, marshalReply(t, takeControlReply()))
		receipt, err := r.AcceptCommit(raw, 11)
		if !errors.Is(err, errStoreDied) {
			t.Fatalf("AcceptCommit(failed commit) = %v, want %v", err, errStoreDied)
		}
		if receipt.Disposition != ReceiptRetained {
			t.Fatalf("failed-commit receipt disposition = %v, want retained", receipt.Disposition)
		}
	})

	t.Run("unique stale-age refusal is retained", func(t *testing.T) {
		st := &memStore{}
		seedPaired(t, st)
		r := resumeRouter(t, st, &recordingAcker{})
		now := time.UnixMilli(1_784_000_000_000)
		receipt, err := r.AcceptCommitAt(b42StaleFrame(t, 1, now), 11, now)
		if !errors.Is(err, crypto.ErrStaleAge) {
			t.Fatalf("AcceptCommitAt(stale) = %v, want ErrStaleAge", err)
		}
		if receipt.Disposition != ReceiptRetained {
			t.Fatalf("stale-age receipt disposition = %v, want retained", receipt.Disposition)
		}
	})

	t.Run("authenticated malformed plaintext is discardable", func(t *testing.T) {
		st := &memStore{}
		seedPaired(t, st)
		r := resumeRouter(t, st, &recordingAcker{})
		raw := sealFrame(t, testContentKey(), 1, []byte(`{"kind":`))
		receipt, err := r.AcceptCommit(raw, 11)
		if err == nil {
			t.Fatal("AcceptCommit(authenticated malformed plaintext) = nil error")
		}
		if receipt.Disposition != ReceiptDiscardable {
			t.Fatalf("authenticated-malformed receipt disposition = %v, want discardable", receipt.Disposition)
		}
	})
}

func TestAcceptCommit_RetriesSameProcessAfterOneShotSaveFailure(t *testing.T) {
	mem := &memStore{}
	seedPaired(t, mem)
	store := &failOnceStore{inner: mem, fail: true}
	ack := &recordingAcker{}
	r := resumeRouter(t, store, ack)
	raw := sealFrame(t, testContentKey(), 1, marshalReply(t, takeControlReply()))

	first, err := r.AcceptCommit(raw, 11)
	if !errors.Is(err, errStoreDied) || first.Acked || first.Disposition != ReceiptRetained {
		t.Fatalf("first AcceptCommit = (%+v, %v), want retained unacked Save failure", first, err)
	}
	second, err := r.AcceptCommit(raw, 11)
	if err != nil {
		t.Fatalf("same-process retry = %v, want commit", err)
	}
	if !second.Acked || len(ack.acked) != 1 || ack.acked[0] != 11 {
		t.Fatalf("retry receipt/acks = (%+v, %v), want one ack at 11", second, ack.acked)
	}
	if r.Replies().Len() != 1 || mem.Load().RelayCursor != 11 {
		t.Fatalf("retry did not commit content/cursor: replies=%d cursor=%d", r.Replies().Len(), mem.Load().RelayCursor)
	}
}

func TestBootstrap_RetriesSameProcessAfterOneShotSaveFailure(t *testing.T) {
	machine := newS10Machine(t)
	mem := &memStore{}
	if err := mem.Save(State{Machine: "m1", MachineSignPub: machine.pub, EpochID: 7}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	store := &failOnceStore{inner: mem, fail: true}
	ack := &recordingAcker{}
	c, err := Resume(Config{State: store, Ack: ack})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	frame, keys := machine.bootstrapFor(t, c.KeyStore(), 7, 1)

	first, err := c.Router().AcceptCommit(frame, 21)
	if !errors.Is(err, errStoreDied) || first.Acked || first.Disposition != ReceiptRetained {
		t.Fatalf("first bootstrap = (%+v, %v), want retained unacked Save failure", first, err)
	}
	second, err := c.Router().AcceptCommit(frame, 21)
	if err != nil {
		t.Fatalf("same-process bootstrap retry = %v, want commit", err)
	}
	if !second.Acked || len(ack.acked) != 1 || ack.acked[0] != 21 {
		t.Fatalf("retry receipt/acks = (%+v, %v), want one ack at 21", second, ack.acked)
	}
	got := mem.Load()
	if got.Keys != keys || got.GrantEpoch != 7 || got.GrantSeq != 1 || got.RelayCursor != 21 {
		t.Fatalf("bootstrap retry did not commit key/watermark/cursor: %+v", got)
	}
}
