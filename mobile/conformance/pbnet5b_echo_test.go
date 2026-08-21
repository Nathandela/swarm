package conformance_test

// PB-NET-5(b) ECHO LATENCY -- the machine->phone leg, over the shipped phone
// (ADR-007 B98 step 3, fence 3 of 3; budget set by B104).
//
// WHY THIS LEG HAD NO FENCE FOR SIX ROUNDS. PB-NET-5's numeric criterion stops at the PTY
// write: it measures phone `Type` -> keystroke lands, and says nothing about when the user
// SEES the result. The row's other half -- "low-latency input across BOTH hops" -- was fenced
// only by internal/remote/transport/s6b_input_test.go, which drove Session.Follow, a
// MailboxWait-based concurrent-dispatch tail with zero production constructions (B94/B100).
// The shipped phone has no equivalent: it POLLS, at mobile/app.go's pollInterval of 500 ms,
// continuing immediately whenever the durable cursor advanced. So the mechanism the
// requirement names does not ship, and the mechanism that does ship was unmeasured.
//
// THE BUDGET IS NEW AND NARROW, and the fence must not overstate it. §6.0's PB-NET-5(b) row:
// poll wait <= 500 ms plus one non-wait request, p95 <= 750 ms, p99 <= 1000 ms, n >= 200,
// **CLOSED-TEST SCOPE ONLY** (owner ruling, ADR-007 B104). Production is explicitly NOT
// covered. Nothing here should be read as a production latency claim, and the numbers are
// DERIVED from the poll wait rather than measured from a prior run -- this test is the first
// end-to-end measurement of the leg that exists.
//
// IT SAMPLES THE WORST MOMENT ON PURPOSE. An event published at a random instant relative to
// the poll reports the MEAN wait (~half an interval) and would pass a budget the product can
// still miss. Each sample therefore publishes IMMEDIATELY AFTER a mailbox_read goes past the
// proxy -- the instant that guarantees a full poll interval before the phone looks again --
// which is the bound the budget is actually about.
//
// GATING follows the sibling harness in internal/skeleton: §6.0 describes a benchmark, not a
// unit test, and 200 worst-case samples cost roughly two minutes of wall clock. The file
// compiles, vets and lints on every run and the measurement costs one t.Skip; set
// SWARM_S6B_LATENCY=1 to run it, the same gesture that runs the outbound leg.
//
// WHAT IT DOES NOT DECIDE. One host, one in-process relay, loopback. It says nothing about a
// handset on a real network, which is PB-E2E-5's, and nothing about production, which the
// budget's own scope excludes.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// §6.0's PB-NET-5(b) budgets, closed-test scope.
const (
	pbnet5bBudgetP95 = 750 * time.Millisecond
	pbnet5bBudgetP99 = 1000 * time.Millisecond
)

// pollObserver proxies the phone's connection and signals every time a mailbox_read passes
// toward the relay, so a sample can be published at the WORST moment rather than a random one.
type pollObserver struct {
	srv *httptest.Server

	reads chan struct{}
}

func newPollObserver(t *testing.T, upstream string) *pollObserver {
	t.Helper()
	p := &pollObserver{reads: make(chan struct{}, 64)}
	p.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		down, err := websocket.Accept(rw, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = down.CloseNow() }()
		down.SetReadLimit(relay.MaxFrame + 64)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		up, _, err := websocket.Dial(ctx, upstream, nil)
		if err != nil {
			return
		}
		defer func() { _ = up.CloseNow() }()
		up.SetReadLimit(relay.MaxFrame + 64)

		done := make(chan struct{}, 2)
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := up.Read(ctx)
				if err != nil || down.Write(ctx, mt, data) != nil {
					return
				}
			}
		}()
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := down.Read(ctx)
				if err != nil {
					return
				}
				// The relay's control ops are a JSON body inside a binary frame; matching the
				// op on raw bytes keeps the observer from having to decode the wire format.
				// Since Wave R9 the phone's drain is a parked mailbox_wait (mobile/relay.go
				// drainWait) and mailbox_read survives only as the old-relay fallback, so the
				// observer treats either op as "the phone just looked": publishing immediately
				// after a wait goes out lands the item on a freshly parked wait, which is that
				// mechanism's own worst-aligned moment.
				if bytes.Contains(data, []byte("mailbox_read")) || bytes.Contains(data, []byte("mailbox_wait")) {
					select {
					case p.reads <- struct{}{}:
					default: // a full channel means the sampler is busy; never block the proxy
					}
				}
				if up.Write(ctx, mt, data) != nil {
					return
				}
			}
		}()
		<-done
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *pollObserver) URL() string { return strings.Replace(p.srv.URL, "http://", "ws://", 1) }

// awaitPoll blocks until a mailbox_read has just gone past, so the caller publishes into the
// gap immediately behind it.
func (p *pollObserver) awaitPoll(timeout time.Duration) bool {
	// Drain anything already queued so the sample aligns to the NEXT read, not a stale one.
	for {
		select {
		case <-p.reads:
			continue
		default:
		}
		break
	}
	select {
	case <-p.reads:
		return true
	case <-time.After(timeout):
		return false
	}
}

// TestPBNET5B_EchoLatencyMachineToPhoneVisible measures the leg §6.0 budgets at
// p95 <= 750 ms / p99 <= 1000 ms over n >= 200, sampling the worst moment each time.
func TestPBNET5B_EchoLatencyMachineToPhoneVisible(t *testing.T) {
	if os.Getenv("SWARM_S6B_LATENCY") != "1" {
		t.Skip("PB-NET-5(b) echo harness: set SWARM_S6B_LATENCY=1 to run (§6.0 describes a benchmark; 200 worst-case samples cost ~2 minutes)")
	}
	samples := pbnet5bEnvInt("SWARM_S6B_SAMPLES", 200)
	if samples < 200 {
		t.Fatalf("SWARM_S6B_SAMPLES=%d; §6.0 binds n >= 200 for this row", samples)
	}

	h := newHarness(t)
	obs := newPollObserver(t, h.RelayURL)
	if err := h.App.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.AppRelayURL = obs.URL()
	h.App = h.openApp()
	if err := h.App.InstallContentKey(h.Keys.ContentKey[:]); err != nil {
		t.Fatalf("InstallContentKey: %v", err)
	}
	eventually(t, "the phone never came online through the poll observer", func() bool {
		st, err := h.App.ConnectionState()
		return err == nil && st == "online"
	})
	if err := h.App.SubscribeJournal(); err != nil {
		t.Fatalf("SubscribeJournal: %v", err)
	}

	t.Logf("PB-NET-5(b) harness: n=%d, worst-case aligned (publish immediately after a poll), "+
		"budgets p95<=%v p99<=%v, CLOSED-TEST SCOPE", samples, pbnet5bBudgetP95, pbnet5bBudgetP99)

	visible := func(cursor uint64) bool {
		pg, err := h.App.ReadJournal(0, 400)
		if err != nil {
			return false
		}
		n, err := pg.Count()
		if err != nil {
			return false
		}
		for i := 0; i < n; i++ {
			if e, err := pg.At(i); err == nil && e != nil && e.Cursor == int64(cursor) {
				return true
			}
		}
		return false
	}

	ds := make([]time.Duration, 0, samples)
	for i := 1; i <= samples; i++ {
		cursor := uint64(i)
		// 30 s, not 10: an IDLE parked wait re-parks only after the relay's 25 s server
		// ceiling answers it empty, so the gap between two "the phone just looked" signals
		// can legitimately be a full ceiling.
		if !obs.awaitPoll(30 * time.Second) {
			t.Fatalf("sample %d: no mailbox_read or mailbox_wait passed the observer within 30s; the phone is "+
				"neither waiting nor polling, so this harness is measuring nothing", i)
		}
		start := time.Now()
		h.PushEvent(schema.JournalRecord{
			Cursor: cursor, SessionID: testSession, Type: "group_transition", Group: "working",
		})
		deadline := time.Now().Add(5 * time.Second)
		seen := false
		for time.Now().Before(deadline) {
			if visible(cursor) {
				seen = true
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		if !seen {
			t.Fatalf("sample %d (cursor %d) never became visible to the phone within 5s. That is not "+
				"a latency result -- it is a delivery failure, and it is worth more than the number "+
				"this test was written to produce", i, cursor)
		}
		ds = append(ds, time.Since(start))
	}

	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	p := func(q float64) time.Duration { return ds[min(int(float64(len(ds))*q), len(ds)-1)] }
	p50, p95, p99, worst := p(0.50), p(0.95), p(0.99), ds[len(ds)-1]
	t.Logf("PB-NET-5(b) echo latency, n=%d: p50=%v p95=%v p99=%v max=%v", len(ds), p50, p95, p99, worst)

	if p95 > pbnet5bBudgetP95 {
		t.Errorf("PB-NET-5(b): echo p95 = %v, budget %v (over by %v). The phone polls at 500 ms "+
			"(mobile/app.go pollInterval) and this is the leg that wait lands on: the user types, the "+
			"keystroke arrives fast, and the character comes back a poll later",
			p95, pbnet5bBudgetP95, p95-pbnet5bBudgetP95)
	}
	if p99 > pbnet5bBudgetP99 {
		t.Errorf("PB-NET-5(b): echo p99 = %v, budget %v (over by %v)", p99, pbnet5bBudgetP99, p99-pbnet5bBudgetP99)
	}
}

func pbnet5bEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
