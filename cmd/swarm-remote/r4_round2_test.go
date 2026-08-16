package main

// FAILING-FIRST (TDD RED, GG-5) for Wave R4 round 2's BLOCKING finding (bead
// agents-tracker-u37c): the durable revoke retry across machine death was UNREACHABLE
// in production. main ran requireSomethingToServe BEFORE the pending-obligation
// redrive, and a completed revoke leaves ZERO paired devices -- exactly the state the
// quiescence gate exits on, and the ONLY state in which a pending revoke obligation
// can exist. If the gateway was unreachable at the revoke moment, the pairing's push
// address stayed live at the gateway forever: precisely the u37c gap this wave claims
// to close end to end.
//
// THE CONTRACT UNDER TEST (undefined today -- compile-level RED, then a runtime RED on
// main's ordering):
//
//   - redrivePendingMachineRevoke(stateDir): drives whatever revoke obligation an
//     earlier process left pending, needing ONLY StateDir and push-gateway.json --
//     never a paired device, never resolved gateway params.
//   - revokeHTTPClient: the producer's HTTP-client seam, so a test can point the
//     drive at its TLS double.
//   - main() calls the redrive BEFORE the quiescence gate.

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/remote/supervise"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestMain lets the ordering test below re-exec this test binary AS swarm-remote: the
// helper-process pattern, because main() ends in os.Exit and must run in a child.
func TestMain(m *testing.M) {
	if os.Getenv("SWARM_R4R2_RUN_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// r4r2Capture records revoke requests a test gateway double sees.
type r4r2Capture struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (c *r4r2Capture) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.requests = append(c.requests, r.Clone(r.Context()))
		c.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}
}

func (c *r4r2Capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

// r4r2RecordObligation leaves a durable pending revoke obligation under stateDir --
// the residue of an earlier process that recorded the revoke and died before the
// delete resolved.
func r4r2RecordObligation(t *testing.T, stateDir string) {
	t.Helper()
	remoteDir := filepath.Join(stateDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	store, err := remotegw.OpenRevokeObligationStore(filepath.Join(remoteDir, "revoke-obligation.json"))
	if err != nil {
		t.Fatalf("OpenRevokeObligationStore: %v", err)
	}
	var addr remotegw.PushAddress
	for i := range addr {
		addr[i] = 0x4E
	}
	machine := remotegw.NewRevokeObligationMachine(remotegw.RevokeObligationConfig{Store: store, Address: addr})
	if err := machine.Record(); err != nil {
		t.Fatalf("Record: %v", err)
	}
}

// TestR4R2_RedrivePendingMachineRevoke_NeedsNoPairedDevice: the redrive works in the
// EXACT state the quiescence gate refuses -- zero paired devices -- because that is
// the only state a pending obligation can exist in. It needs StateDir and
// push-gateway.json, nothing else.
func TestR4R2_RedrivePendingMachineRevoke_NeedsNoPairedDevice(t *testing.T) {
	stateDir := t.TempDir()

	// Pin WHY the ordering matters: this state dir is quiescent by the gate's own
	// judgement. If this ever stops holding, the redrive's placement stops mattering.
	if err := requireSomethingToServe(stateDir); err == nil || !errors.Is(err, supervise.ErrQuiescent) {
		t.Fatalf("requireSomethingToServe over a 0-device dir answered %v; want the quiescent refusal", err)
	}

	capture := &r4r2Capture{}
	hs := httptest.NewTLSServer(capture.handler())
	defer hs.Close()

	writePushGatewayFile(t, stateDir, pushGatewayFile{
		GatewayURL:              hs.URL,
		SubmitCapability:        "cap-submit-000000000000000000000",
		MachineRevokeCapability: "cap-machine-revoke-00000000000000",
		PushAddress:             "4e4e4e4e4e4e4e4e4e4e4e4e4e4e4e4e",
	})
	r4r2RecordObligation(t, stateDir)

	prev := revokeHTTPClient
	revokeHTTPClient = func() *http.Client { return hs.Client() }
	t.Cleanup(func() { revokeHTTPClient = prev })

	redrivePendingMachineRevoke(stateDir)

	if got := capture.count(); got != 1 {
		t.Fatalf("the gateway saw %d DELETE(s), want exactly 1: the pending obligation was "+
			"not driven from a 0-device state dir", got)
	}
	capture.mu.Lock()
	req := capture.requests[0]
	capture.mu.Unlock()
	if req.Method != http.MethodDelete {
		t.Errorf("method %s, want DELETE", req.Method)
	}
	if got := req.Header.Get("Authorization"); got != "Swarm-Revoke cap-machine-revoke-00000000000000" {
		t.Errorf("Authorization %q, want the machine-revoke arm verbatim", got)
	}

	reopened, err := remotegw.OpenRevokeObligationStore(filepath.Join(stateDir, "remote", "revoke-obligation.json"))
	if err != nil {
		t.Fatalf("reopening the obligation store: %v", err)
	}
	if _, ok := reopened.Pending(); ok {
		t.Errorf("the obligation is still pending after the gateway's 204")
	}
}

// TestR4R2_Main_RedrivesThePendingRevokeBeforeTheQuiescenceGate: the process-level
// ordering itself, driven through the REAL main() in a child process. A state dir with
// zero devices and a pending obligation must ATTEMPT the drive (observable as the
// drive report on stderr, here against an unreachable gateway) BEFORE the gate reports
// quiescence -- and still exit with the quiescent SUCCESS status.
func TestR4R2_Main_RedrivesThePendingRevokeBeforeTheQuiescenceGate(t *testing.T) {
	stateDir := t.TempDir()
	writePushGatewayFile(t, stateDir, pushGatewayFile{
		GatewayURL:              "https://127.0.0.1:1",
		SubmitCapability:        "cap-submit-000000000000000000000",
		MachineRevokeCapability: "cap-machine-revoke-00000000000000",
		PushAddress:             "4e4e4e4e4e4e4e4e4e4e4e4e4e4e4e4e",
	})
	r4r2RecordObligation(t, stateDir)

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		"SWARM_R4R2_RUN_MAIN=1",
		daemon.EnvStateDir+"="+stateDir,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("swarm-remote over a 0-device state dir exited %v; quiescence is a SUCCESS "+
			"status (supervise.ExitQuiescent). stderr:\n%s", err, stderr.String())
	}

	out := stderr.String()
	gate := strings.Index(out, "no paired device to serve")
	drive := strings.Index(out, "machine revoke: drive obligation")
	if gate < 0 {
		t.Fatalf("the quiescence gate never reported; this test's premise broke. stderr:\n%s", out)
	}
	if drive < 0 {
		t.Fatalf("the pending revoke obligation was never DRIVEN: the process start in the "+
			"only state that can hold one (post-revoke, zero paired devices) exited at the "+
			"quiescence gate first, so an address the gateway never deleted stays live "+
			"forever. stderr:\n%s", out)
	}
	if drive > gate {
		t.Errorf("the drive ran AFTER the quiescence gate reported; the retry must precede "+
			"the gate, whose refusal ends the process. stderr:\n%s", out)
	}
}
