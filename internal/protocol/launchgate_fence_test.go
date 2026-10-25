package protocol

import (
	"errors"
	"testing"
	"time"
)

func tryGateFence(t *testing.T, s *Server, fn func() error) error {
	t.Helper()
	f, ok := any(s).(interface {
		TryWithInputFence(string, func() error) error
	})
	if !ok {
		t.Fatal("server has no nonblocking launch-gate input fence")
	}
	return f.TryWithInputFence("session", fn)
}

func TestLaunchGateFenceBusyNeverCallsBackAndReleasesEarlierLocks(t *testing.T) {
	for _, name := range []string{"attach", "input"} {
		t.Run(name, func(t *testing.T) {
			ls := &sessionLease{}
			s := &Server{leases: map[string]*sessionLease{"session": ls}}
			if name == "attach" {
				ls.attachMu.Lock()
				defer ls.attachMu.Unlock()
			} else {
				ls.inMu.Lock()
				defer ls.inMu.Unlock()
			}
			called := false
			start := time.Now()
			err := tryGateFence(t, s, func() error { called = true; return nil })
			if err == nil || called || time.Since(start) > 250*time.Millisecond {
				t.Fatalf("busy fence: err=%v called=%v elapsed=%v", err, called, time.Since(start))
			}
			if name == "input" {
				if !ls.attachMu.TryLock() {
					t.Fatal("busy input lock leaked attach lock")
				}
				ls.attachMu.Unlock()
			}
		})
	}
}

func TestLaunchGateFenceOwnsBothLocksWithoutForeignInputGate(t *testing.T) {
	s := &Server{leases: map[string]*sessionLease{}}
	s.SetInputGateFunc(func(string) error { t.Error("own gate invoked foreign-input hook"); return nil })
	want := errors.New("observer refused")
	err := tryGateFence(t, s, func() error {
		ls := s.leases["session"]
		if ls == nil {
			t.Fatal("fence did not register current lease")
		}
		if ls.attachMu.TryLock() {
			ls.attachMu.Unlock()
			t.Error("callback did not own attach lock")
		}
		if ls.inMu.TryLock() {
			ls.inMu.Unlock()
			t.Error("callback did not own input lock")
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("callback refusal = %v", err)
	}
	if err := tryGateFence(t, s, func() error { return nil }); err != nil {
		t.Fatalf("callback refusal leaked fence: %v", err)
	}
}

func TestLaunchGateFenceRemoteRefusalReleasesOwner(t *testing.T) {
	owner := &Server{leases: map[string]*sessionLease{"session": {}}}
	remote := &Server{leases: map[string]*sessionLease{"session": {}}}
	remote.leases["session"].inMu.Lock()
	defer remote.leases["session"].inMu.Unlock()
	called := false
	err := tryGateFence(t, owner, func() error {
		return tryGateFence(t, remote, func() error { called = true; return nil })
	})
	if err == nil || called {
		t.Fatalf("remote refusal: err=%v called=%v", err, called)
	}
	if err := tryGateFence(t, owner, func() error { return nil }); err != nil {
		t.Fatalf("remote refusal retained owner locks: %v", err)
	}
}

func TestLaunchGateFenceClosedAndNilCallbackRefuse(t *testing.T) {
	called := false
	s := &Server{closed: true}
	if err := tryGateFence(t, s, func() error { called = true; return nil }); err == nil || called {
		t.Fatalf("closed fence: err=%v called=%v", err, called)
	}
	s = &Server{leases: map[string]*sessionLease{}}
	if err := tryGateFence(t, s, nil); err == nil {
		t.Fatal("nil callback was accepted")
	}
}
