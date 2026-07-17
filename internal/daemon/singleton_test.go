package daemon

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

// TestSingleton_SecondOpenLoses asserts E5.1/S12: two Opens over the same lock
// path — exactly one wins; the loser gets ErrAlreadyRunning. flock is per
// open-file-description, so a second Open contends even within one process.
func TestSingleton_SecondOpenLoses(t *testing.T) {
	cfg := daemonConfig(t)

	d1, err := Open(cfg)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	t.Cleanup(func() { _ = d1.Close() })

	d2, err := Open(cfg)
	if err == nil {
		_ = d2.Close()
		t.Fatalf("second Open unexpectedly succeeded; want ErrAlreadyRunning")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Open error = %v; want ErrAlreadyRunning", err)
	}
}

// TestSingleton_WinnerReachableAfterLoss asserts the surviving daemon is the one
// bound to the socket: the loser's client can reach the winner (S12/D-7). After
// the loser fails, dialing the socket succeeds against the live winner.
func TestSingleton_WinnerReachableAfterLoss(t *testing.T) {
	cfg := daemonConfig(t)
	d1 := openDaemon(t, cfg)

	if _, err := Open(cfg); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Open error = %v; want ErrAlreadyRunning", err)
	}

	// The winner (d1) owns the socket. A version-checked dial reaches it.
	conn, err := Dial(cfg.SocketPath, ProtocolVersion)
	if err != nil {
		t.Fatalf("Dial winner socket: %v", err)
	}
	_ = conn.Close()
	_ = d1
}

// TestSingleton_StaleSocketReclaimedUnderLock asserts E5.1: a leftover socket
// file from a crashed prior daemon (no listener behind it) is unlinked and
// rebound on Open — and only after the lock is held. Observable outcome: Open
// succeeds despite the pre-existing path, and the socket is a live listener.
func TestSingleton_StaleSocketReclaimedUnderLock(t *testing.T) {
	cfg := daemonConfig(t)

	// Plant a stale, non-listening file exactly where the socket will bind.
	if err := os.WriteFile(cfg.SocketPath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("plant stale socket: %v", err)
	}

	d := openDaemon(t, cfg)
	_ = d

	// The path is now a real listening UDS: a dial connects.
	deadline := time.Now().Add(pollTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", cfg.SocketPath)
		if err == nil {
			_ = conn.Close()
			return
		}
		lastErr = err
		time.Sleep(pollStep)
	}
	t.Fatalf("stale socket not reclaimed into a live listener: %v", lastErr)
}

// TestSingleton_ReopenAfterCloseSucceeds asserts the lock is released on Close so
// a fresh daemon can take over — the in-process analogue of a clean restart.
func TestSingleton_ReopenAfterCloseSucceeds(t *testing.T) {
	cfg := daemonConfig(t)

	d1, err := Open(cfg)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := d1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	d2, err := Open(cfg)
	if err != nil {
		t.Fatalf("reopen after Close: %v; want success (lock released)", err)
	}
	_ = d2.Close()
}
