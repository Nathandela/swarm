package swarmmobile

// Wave R6 review round 3, finding F5(a): PB-NET-4's band was asserted on a quantity the
// code does not control.
//
// mobile/conformance/pbnet4_flappingrelay_test.go times dial ARRIVALS at an HTTP tap and
// held each gap to section 6.0's +/-20% band. An arrival gap is the SCHEDULED delay plus
// however long this host took to wake the dialling goroutine and carry the connection, and
// the top of the band is exactly the largest legal schedule -- so the band had zero
// headroom for latency and a correct run loop failed it: `gap #0 ... was 605.604042ms, want
// within [400ms, 600ms]` on the reviewer's machine, and `gap #1 ... 1.200393917s, want
// within [800ms, 1.2s]` reproduced here at GOMAXPROCS=1 under 6x hw.ncpu of load.
//
// The house rule is to measure what the code controls. That quantity -- the delay App.run
// SCHEDULES -- exists only inside this package, so this is where it is asserted, over the
// REAL run loop (NewApp + Start, the facade PhoneRuntime drives) rather than over a
// *reconnectBackoff built by hand. It is strictly tighter than what it replaces: no
// tolerance, no headroom, and it fails for a wiring revert
// (`case <-time.After(250 * time.Millisecond)`) exactly as the conformance fence does,
// because a 250 ms constant is outside attempt 1's band.
//
// The conformance fence keeps its arrival-timing check, restated as what a wall clock over
// a network hop can honestly bound (see its own comment).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
)

// TestPBNET4_TheRunLoopSchedulesSection60sBackoff drives App.run against a relay endpoint
// that refuses every dial, and asserts the delays the loop SCHEDULES.
func TestPBNET4_TheRunLoopSchedulesSection60sBackoff(t *testing.T) {
	var mu sync.Mutex
	got := make([]time.Duration, 0, 4)
	at := make([]time.Time, 0, 4)
	ready := make(chan struct{})
	var once sync.Once
	obs := func(attempt int, d time.Duration) {
		mu.Lock()
		got = append(got, d)
		at = append(at, time.Now())
		n := len(got)
		mu.Unlock()
		if n >= 3 {
			once.Do(func() { close(ready) })
		}
	}
	reconnectDelayObserver.Store(&obs)
	t.Cleanup(func() { reconnectDelayObserver.Store(nil) })

	// A relay endpoint that answers every dial with 503 before any websocket upgrade: the
	// dial fails fast and recoverably, which is the condition the backoff exists for.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "refused", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	stateDir := t.TempDir()
	custody := r4r3Custody{}
	reg, err := phonecore.NewMachineRegistry(stateDir)
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	machineDir, err := reg.AddMachine(phonecore.MachineDescriptor{ID: "m-a"})
	if err != nil {
		t.Fatalf("AddMachine: %v", err)
	}
	if _, err := phonecore.Resume(phonecore.Config{
		Dir: machineDir, Machine: "m-a",
		WakeSealer:    custodySealer{tier: "wake", fetch: custody.WakeKEK},
		ContentSealer: custodySealer{tier: "content", fetch: custody.ContentKEK},
	}); err != nil {
		t.Fatalf("provision v2 namespace: %v", err)
	}
	app, err := NewApp(&Config{
		StateDir:  stateDir,
		MachineID: "m-a",
		RelayURL:  "ws" + strings.TrimPrefix(srv.URL, "http"),
	}, custody)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if err := app.Start(); err != nil {
		t.Fatalf("App.Start: %v", err)
	}

	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		mu.Lock()
		n := len(got)
		mu.Unlock()
		t.Fatalf("App.run scheduled %d reconnect delay(s) in 30s; want at least 3. A run loop "+
			"that never schedules one is a phone that reconnects hard against a refusing relay", n)
	}

	mu.Lock()
	delays := append([]time.Duration(nil), got[:3]...)
	stamps := append([]time.Time(nil), at[:3]...)
	mu.Unlock()

	// Section 6.0, transcribed (not read from the constants under test): initial 500ms,
	// factor 2, jitter +/-20%.
	bands := []struct{ lo, hi time.Duration }{
		{400 * time.Millisecond, 600 * time.Millisecond},
		{800 * time.Millisecond, 1200 * time.Millisecond},
		{1600 * time.Millisecond, 2400 * time.Millisecond},
	}
	for i, b := range bands {
		if delays[i] < b.lo || delays[i] > b.hi {
			t.Errorf("PB-NET-4: App.run scheduled %v before dial attempt %d, want within [%v, %v] "+
				"(section 6.0: initial 500ms, factor 2, jitter +/-20%%). This is the delay the loop "+
				"itself chose, so no host load can move it: a failure here is the schedule, not the "+
				"scheduler", delays[i], i+2, b.lo, b.hi)
		}
	}
	// The bands do not overlap, so three correct delays already prove growth; asserting it
	// separately is what catches a change that widened the bands rather than the schedule.
	if delays[0] >= delays[1] || delays[1] >= delays[2] {
		t.Errorf("PB-NET-4: the scheduled delays %v do not grow. A fixed delay (the pre-fix 250ms, "+
			"or any other constant) is exactly what this forbids", delays)
	}
	// THE LOOP MUST ALSO WAIT WHAT IT SCHEDULED. Observing rb.next()'s answer proves the
	// schedule and NOT the wiring: `time.After(250 * time.Millisecond)` beside a correctly
	// computed delay would satisfy every assertion above. The elapsed time between two
	// consecutive observations is the wait plus one dial attempt, so it is bounded BELOW by
	// the wait and by nothing else -- a timer never fires early and load can only add. That
	// one-sided comparison is the whole of what a wall clock can honestly say here, and it
	// is enough: a 250 ms wait under a 500 ms schedule fails it on any host.
	for i := 0; i+1 < len(delays); i++ {
		if elapsed := stamps[i+1].Sub(stamps[i]); elapsed < delays[i] {
			t.Errorf("PB-NET-4: the loop scheduled %v before dial attempt %d but only %v passed "+
				"before it scheduled the next one. The delay is computed and then not waited on: "+
				"the phone is redialling a refusing relay faster than the budget allows",
				delays[i], i+2, elapsed)
		}
	}
}
