package protocol

import (
	"runtime"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

func TestSendInputRejectsRetiredLeaseAfterWaitingForInputLock(t *testing.T) {
	for _, change := range []string{"server-close", "lease-replaced"} {
		t.Run(change, func(t *testing.T) {
			d := &pastingDaemon{stubDaemon: newStubDaemon()}
			d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
			srv := NewServer(d, "endpoint")
			lease := &sessionLease{}
			srv.mu.Lock()
			srv.leases["sess1"] = lease
			srv.mu.Unlock()
			lease.inMu.Lock()
			inputLocked, joined := true, false
			done := make(chan error, 1)
			go func() {
				done <- srv.SendInput("sess1", SendInputReq{Text: "message", Submit: true})
			}()
			t.Cleanup(func() {
				if inputLocked {
					lease.inMu.Unlock()
				}
				if !joined {
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						t.Error("sender did not finish after releasing input lock")
					}
				}
				_ = srv.Close()
			})

			// Only this sender can own attachMu; inMu remains held by the test.
			deadline := time.Now().Add(2 * time.Second)
			for lease.attachMu.TryLock() {
				lease.attachMu.Unlock()
				select {
				case err := <-done:
					joined = true
					t.Fatalf("sender did not wait on captured lease: %v", err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("sender never acquired captured lease's attach lock")
				}
				runtime.Gosched()
			}

			if change == "server-close" {
				if err := srv.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				srv.dropLease("sess1")
				srv.mu.Lock()
				srv.leases["sess1"] = &sessionLease{}
				srv.mu.Unlock()
			}
			lease.inMu.Unlock()
			inputLocked = false
			select {
			case err := <-done:
				joined = true
				if err == nil {
					t.Error("retired lease was allowed to send")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("sender did not refuse retired lease")
			}
			d.mu.Lock()
			opened := append([]*pastingStream(nil), d.opened...)
			d.mu.Unlock()
			if len(opened) != 0 {
				t.Errorf("retired lease reached Attach: %d calls", len(opened))
			}
			for _, stream := range opened {
				if writes := stream.written(); len(writes) != 0 {
					t.Errorf("retired lease delivered paste/raw effects: %d writes", len(writes))
				}
			}
		})
	}
}
