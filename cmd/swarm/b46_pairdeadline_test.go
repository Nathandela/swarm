package main

// ADR-007 B46 part 2, CLI half: `swarm remote pair` blocks on Pending() with NO DEADLINE
// OF ITS OWN. It prints an expiry and then waits past it forever, so the QR the owner is
// told is valid stays on screen indefinitely -- long after the relay slot behind it is
// gone. The announced window is now capped at the relay slot (internal/skeleton's
// pairWindow) and an expired id is burned rather than freed (relay burnRendezvous); this
// is the third leg: the command stops promising that waiting is enough.
//
// Fixture: the scripted fake owner daemon from remote_pair_test.go, with a host that
// announces a window and then never drives the SAS gate -- the shape of a phone that
// never scans.

import (
	"bytes"
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
)

// stalledPairingHost announces a pairing window and then does nothing: no confirm, no
// result. Every other DaemonAPI method comes from the scripted host it embeds.
type stalledPairingHost struct {
	*scriptedPairingHost
	window time.Duration
}

func (h *stalledPairingHost) BeginPairing(_ context.Context, _ protocol.PairStartReq,
	_ func(sas []string, deviceName string) (bool, error),
	_ func(protocol.PairResult)) (protocol.PairView, error) {

	exp := time.Now().Add(h.window)
	return protocol.PairView{QR: "otpauth://swarm-pair/STALLED", RendezvousID: "rvz-stalled", ExpiresAt: &exp}, nil
}

// startStalledPairingDaemon is startFakePairingDaemon over a host that never completes,
// which that helper's concrete parameter type cannot express.
func startStalledPairingDaemon(t *testing.T, stateDir string, host protocol.DaemonAPI) {
	t.Helper()
	sock := filepath.Join(stateDir, "daemon.sock")
	t.Setenv(daemon.EnvStateDir, stateDir)
	t.Setenv(daemon.EnvSocket, sock)
	t.Setenv(daemon.EnvLock, filepath.Join(stateDir, "daemon.lock"))
	t.Setenv(daemon.EnvLog, filepath.Join(stateDir, "daemon.log"))

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen stalled pairing daemon: %v", err)
	}
	srv := protocol.NewServer(host, "ep-stalled-pair")
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakePairingConn(srv, conn)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		_ = srv.Close()
	})
}

// TestB46_PairStopsWaitingWhenTheAnnouncedWindowCloses.
func TestB46_PairStopsWaitingWhenTheAnnouncedWindowCloses(t *testing.T) {
	dir := shortStateDir(t)
	host := &stalledPairingHost{scriptedPairingHost: newScriptedPairingHost(), window: 200 * time.Millisecond}
	startStalledPairingDaemon(t, dir, host)

	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- runRemotePair(nil, strings.NewReader(""), &stdout, &stderr) }()

	select {
	case exit := <-done:
		if exit == 0 {
			t.Fatalf("runRemotePair exit = 0 for a pairing no device ever joined; want a fail-closed nonzero.\n"+
				"stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
		if !strings.Contains(stderr.String(), "window") {
			t.Errorf("the refusal does not name the closed window, so the operator cannot tell it "+
				"from a crash: stderr=%q", stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("`swarm remote pair` was still waiting long past the expiry it printed itself. " +
			"A prompt that promises waiting is enough is exactly the PB-APP-10 failure this " +
			"project's own comments condemn; the relay slot behind that QR is already gone.")
	}
}
