package skeleton

// FAILING-FIRST suite for ADR-010 Amendment 3, slice 2: the passive supervisor that lives
// in the daemon assembly (C2), delivers one submitted message into the SOURCE session when
// a handoff child needs attention and the source is safe to interrupt (C3), and survives a
// daemon restart without ever delivering an event twice (C4).
//
// Unit level, injected fakes, no daemon: the supervisor's three seams (get / controlled /
// send) are closures over one mutex-guarded fake, so a test can flip the source's raw
// state, its controller lease and the send outcome and read back every send verbatim.
//
// FROZEN API (package skeleton, internal/skeleton/supervision.go):
//
//	type supervisionRecord struct {
//	    Child       string            `json:"child"`
//	    Source      string            `json:"source"`
//	    LastGroup   string            `json:"last_group"`
//	    SeenWorking bool              `json:"seen_working"`
//	    Seq         uint64            `json:"seq"`
//	    Pending     *supervisionEvent `json:"pending,omitempty"`
//	}
//	type supervisionEvent struct {
//	    Seq         uint64             `json:"seq"`
//	    Group       status.Group       `json:"group"`
//	    Interaction status.Interaction `json:"interaction"`
//	}
//	type supervisor struct { dir string; get ...; controlled ...; send ...; ... }
//	func newSupervisor(endpointID, dir string, retry time.Duration,
//	    get func(local string) (persist.Meta, bool),
//	    controlled func(local string) bool,
//	    send func(local string, req protocol.SendInputReq) error) (*supervisor, error)
//	func (s *supervisor) arm(m persist.Meta)
//	func (s *supervisor) signal(local string)
//	func (s *supervisor) pending(local string) bool
//	func (s *supervisor) close()   // idempotent; no send after it returns
//	func supervisionNotification(child string /*namespaced*/, ev supervisionEvent) string
//
// endpointID is the daemon's own endpoint id (serve.go endpointID(stateDir)): the
// notification names the child by its NAMESPACED id, the form `swarm peek` / `swarm send`
// take. retry is the cadence at which pending events re-check the source (production 2s;
// tests use a short one). Records live at <dir>/<child>.json, dir 0700, files 0600.
//
// RED today: none of the identifiers above exist, so this file does not compile.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

const (
	supEndpoint = "ep-test"
	supRetry    = 20 * time.Millisecond
	// supSettle spans several retry ticks: a duplicate or a forbidden delivery surfaces here.
	supSettle = 150 * time.Millisecond
)

var (
	stWorking = status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone}
	stReady   = status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
	stPrompt  = status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPrompt}
	stPerm    = status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPermission}
	stExited  = status.Status{Process: status.ProcessExited, Turn: status.TurnIdle, Interaction: status.InteractionNone}
)

// supSend is one recorded call of the send seam, successful or not.
type supSend struct {
	local string
	req   protocol.SendInputReq
	err   error
}

// supFakes backs the supervisor's three seams. Every mutation is under mu so a test can
// flip state from the test goroutine while the supervisor's retry goroutine reads it.
type supFakes struct {
	mu         sync.Mutex
	metas      map[string]persist.Meta
	controlled map[string]bool
	sendErr    error
	sendDelay  time.Duration
	sends      []supSend
}

func newSupFakes(metas ...persist.Meta) *supFakes {
	f := &supFakes{metas: map[string]persist.Meta{}, controlled: map[string]bool{}}
	for _, m := range metas {
		f.metas[m.ID] = m
	}
	return f
}

func (f *supFakes) get(local string) (persist.Meta, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.metas[local]
	return m, ok
}

func (f *supFakes) isControlled(local string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.controlled[local]
}

func (f *supFakes) send(local string, req protocol.SendInputReq) error {
	f.mu.Lock()
	err, delay := f.sendErr, f.sendDelay
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay) // an in-flight delivery, so overlapping signals really overlap
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, supSend{local: local, req: req, err: err})
	return err
}

func (f *supFakes) put(m persist.Meta) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metas[m.ID] = m
}

func (f *supFakes) setStatus(local string, st status.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.metas[local]
	m.Status = st
	f.metas[local] = m
}

func (f *supFakes) remove(local string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.metas, local)
}

func (f *supFakes) setControlled(local string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.controlled[local] = on
}

func (f *supFakes) setSendErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendErr = err
}

// delivered returns the sends the seam ACCEPTED (nil error), in order.
func (f *supFakes) delivered() []supSend {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []supSend
	for _, s := range f.sends {
		if s.err == nil {
			out = append(out, s)
		}
	}
	return out
}

func (f *supFakes) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

// plainMeta is an ordinary session: no lineage, no supervision.
func plainMeta(id string, st status.Status) persist.Meta {
	return persist.Meta{ID: id, AgentType: "fake", Status: st}
}

// passiveChild is a handoff child under passive supervision, spawned from source.
func passiveChild(id, source string, st status.Status) persist.Meta {
	m := plainMeta(id, st)
	m.SpawnedFrom = source
	m.SpawnIntent = protocol.SpawnIntentHandoff
	m.Supervision = protocol.SupervisionPassive
	return m
}

// newTestSupervisor constructs a supervisor over a fresh state dir with the fast retry
// cadence, closed at cleanup.
func newTestSupervisor(t *testing.T, f *supFakes) (*supervisor, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "supervision")
	s, err := newSupervisor(supEndpoint, dir, supRetry, f.get, f.isControlled, f.send)
	if err != nil {
		t.Fatalf("newSupervisor: %v", err)
	}
	t.Cleanup(s.close)
	return s, dir
}

// armedPair is the common fixture: a working passive child of an idle, safe source, armed.
func armedPair(t *testing.T) (*supFakes, *supervisor, string) {
	t.Helper()
	f := newSupFakes(plainMeta("src", stReady), passiveChild("kid", "src", stWorking))
	s, dir := newTestSupervisor(t, f)
	m, _ := f.get("kid")
	s.arm(m)
	return f, s, dir
}

// awaitDelivered waits until exactly n sends have been accepted, then holds for a settle
// window and re-checks: a delivery is exactly-once per event, whether the supervisor
// delivers inside signal or from its retry goroutine.
func awaitDelivered(t *testing.T, f *supFakes, n int) []supSend {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for len(f.delivered()) < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(supSettle)
	got := f.delivered()
	if len(got) != n {
		t.Fatalf("accepted sends = %d, want exactly %d: %+v", len(got), n, got)
	}
	return got
}

// settle lets the supervisor's retry goroutine run several ticks, then asserts the accepted
// send count is exactly n (a negative: nothing was delivered that should not have been).
func settle(t *testing.T, f *supFakes, n int) {
	t.Helper()
	time.Sleep(supSettle)
	if got := f.delivered(); len(got) != n {
		t.Fatalf("accepted sends after settling = %d, want %d: %+v", len(got), n, got)
	}
}

func recordPath(dir, child string) string { return filepath.Join(dir, child+".json") }

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
	return err == nil
}

func readRecord(t *testing.T, dir, child string) supervisionRecord {
	t.Helper()
	data, err := os.ReadFile(recordPath(dir, child))
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	var r supervisionRecord
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("decode record %s: %v", data, err)
	}
	return r
}

func nsChild(local string) string { return protocol.NamespacedID(supEndpoint, local) }

// ---------------------------------------------------------------------------
// arm
// ---------------------------------------------------------------------------

// TestSupervisor_ArmOnlyPassiveHandoffAndIsIdempotent: only a handoff child under passive
// supervision gets a record (and its 0600 file under the 0700 dir); manual, none, delegate
// and plain sessions get nothing, and arming twice leaves one record. A fresh record has
// nothing pending.
func TestSupervisor_ArmOnlyPassiveHandoffAndIsIdempotent(t *testing.T) {
	manual := passiveChild("kid", "src", stWorking)
	manual.Supervision = protocol.SupervisionManual
	none := passiveChild("kid", "src", stWorking)
	none.Supervision = protocol.SupervisionNone
	delegate := passiveChild("kid", "src", stWorking)
	delegate.SpawnIntent = protocol.SpawnIntentDelegate
	unset := passiveChild("kid", "src", stWorking)
	unset.Supervision = ""

	cases := []struct {
		name  string
		meta  persist.Meta
		armed bool
	}{
		{"passive handoff", passiveChild("kid", "src", stWorking), true},
		{"manual handoff", manual, false},
		{"none handoff", none, false},
		{"passive under delegate", delegate, false},
		{"handoff without a mode", unset, false},
		{"plain session", plainMeta("kid", stWorking), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSupFakes(plainMeta("src", stReady), tc.meta)
			s, dir := newTestSupervisor(t, f)
			s.arm(tc.meta)
			s.arm(tc.meta)

			if s.pending("kid") {
				t.Fatal("pending(kid) = true right after arm; a fresh record has no event")
			}
			if got := fileExists(t, recordPath(dir, "kid")); got != tc.armed {
				t.Fatalf("record file exists = %v, want %v", got, tc.armed)
			}
			entries, err := os.ReadDir(dir)
			if err != nil && !os.IsNotExist(err) {
				t.Fatalf("read dir: %v", err)
			}
			if want := map[bool]int{true: 1, false: 0}[tc.armed]; len(entries) != want {
				t.Fatalf("supervision dir holds %d entries, want %d (arm is idempotent, and only passive handoff arms)", len(entries), want)
			}
			if tc.armed {
				fi, err := os.Stat(recordPath(dir, "kid"))
				if err != nil {
					t.Fatalf("stat record: %v", err)
				}
				if fi.Mode().Perm() != 0o600 {
					t.Errorf("record file mode = %o, want 0600", fi.Mode().Perm())
				}
				di, err := os.Stat(dir)
				if err != nil {
					t.Fatalf("stat dir: %v", err)
				}
				if di.Mode().Perm() != 0o700 {
					t.Errorf("supervision dir mode = %o, want 0700", di.Mode().Perm())
				}
				r := readRecord(t, dir, "kid")
				if r.Child != "kid" || r.Source != "src" || r.Pending != nil {
					t.Errorf("fresh record = %+v; want child kid, source src, nothing pending", r)
				}
			}

			// The proof a non-armed session has NO record: an attention state moves nothing.
			f.setStatus("kid", stPrompt)
			s.signal("kid")
			if tc.armed {
				awaitDelivered(t, f, 1)
			} else {
				settle(t, f, 0)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// evaluate + deliver
// ---------------------------------------------------------------------------

// TestSupervisor_NeedsInputNotifiesSourceOncePerTransition: entering needs_input (prompt)
// from working types EXACTLY one submitted notification into the SOURCE, seq 1; a repeated
// signal in the same group is silent; working -> needs_input again is seq 2 with a distinct
// text. The text is the pure notification of (namespaced child, event) and carries none of
// the child's own metadata.
func TestSupervisor_NeedsInputNotifiesSourceOncePerTransition(t *testing.T) {
	f, s, _ := armedPair(t)
	kid, _ := f.get("kid")
	kid.Name = "secret-child-name"
	kid.Cwd = "/tmp/secret-child-cwd"
	f.put(kid)

	s.signal("kid") // working: not an attention state
	settle(t, f, 0)

	f.setStatus("kid", stPrompt)
	s.signal("kid")
	got := awaitDelivered(t, f, 1)
	if got[0].local != "src" {
		t.Fatalf("notification went to %q, want the source src", got[0].local)
	}
	if !got[0].req.Submit || got[0].req.Key != "" {
		t.Fatalf("send req = %+v; want Text submitted (Submit true, no Key)", got[0].req)
	}
	want1 := supervisionNotification(nsChild("kid"), supervisionEvent{Seq: 1, Group: status.GroupNeedsInput, Interaction: status.InteractionPrompt})
	if got[0].req.Text != want1 {
		t.Fatalf("seq 1 text = %q\nwant %q", got[0].req.Text, want1)
	}
	for _, leak := range []string{"secret-child-name", "secret-child-cwd"} {
		if strings.Contains(got[0].req.Text, leak) {
			t.Errorf("notification carries the child's %q; ids, seq, group and interaction only", leak)
		}
	}
	if s.pending("kid") {
		t.Fatal("pending(kid) = true after a delivered event")
	}

	s.signal("kid") // same group, nothing new
	s.signal("src")
	settle(t, f, 1)

	f.setStatus("kid", stWorking)
	s.signal("kid")
	settle(t, f, 1)
	f.setStatus("kid", stPrompt)
	s.signal("kid")
	got = awaitDelivered(t, f, 2)
	want2 := supervisionNotification(nsChild("kid"), supervisionEvent{Seq: 2, Group: status.GroupNeedsInput, Interaction: status.InteractionPrompt})
	if got[1].req.Text != want2 {
		t.Fatalf("seq 2 text = %q\nwant %q", got[1].req.Text, want2)
	}
	if want1 == want2 {
		t.Fatal("seq 1 and seq 2 notifications are identical; the seq must make them distinct")
	}
}

// TestSupervisor_FirstReadyForReviewWaitsForWorking: the idle moment right after launch
// never wakes the source -- ready_for_review is an attention state only once the child has
// been observed working.
func TestSupervisor_FirstReadyForReviewWaitsForWorking(t *testing.T) {
	f := newSupFakes(plainMeta("src", stReady), passiveChild("kid", "src", stReady))
	s, _ := newTestSupervisor(t, f)
	m, _ := f.get("kid")
	s.arm(m)

	s.signal("kid") // ready_for_review straight after launch
	settle(t, f, 0)
	if s.pending("kid") {
		t.Fatal("pending(kid) = true for the launch-time ready_for_review; it must not be an event")
	}

	f.setStatus("kid", stWorking)
	s.signal("kid")
	settle(t, f, 0)

	f.setStatus("kid", stReady)
	s.signal("kid")
	got := awaitDelivered(t, f, 1)
	want := supervisionNotification(nsChild("kid"), supervisionEvent{Seq: 1, Group: status.GroupReadyForReview, Interaction: status.InteractionNone})
	if got[0].req.Text != want {
		t.Fatalf("ready_for_review text = %q\nwant %q", got[0].req.Text, want)
	}
}

// TestSupervisionNotification_TextContract pins the message the source agent reads: the
// `[swarm supervision <child-local>#<seq>]` prefix, the namespaced id in the peek and send
// commands, the state named per group/interaction (a permission request is told apart from a
// prompt and says to ask the human, never approve; completed asks for a final review), ONE
// line (the CR that submits it is the only line boundary), and under the send bound.
func TestSupervisionNotification_TextContract(t *testing.T) {
	const child = "abc123"
	ns := nsChild(child)
	cases := []struct {
		name string
		ev   supervisionEvent
		want []string
		deny []string
	}{
		{"needs_input prompt", supervisionEvent{Seq: 1, Group: status.GroupNeedsInput, Interaction: status.InteractionPrompt},
			[]string{"needs_input (prompt)"}, []string{"permission request"}},
		{"needs_input permission", supervisionEvent{Seq: 2, Group: status.GroupNeedsInput, Interaction: status.InteractionPermission},
			[]string{"needs_input (permission request", "ask the human", "never approve"}, []string{"(prompt)"}},
		{"ready_for_review", supervisionEvent{Seq: 3, Group: status.GroupReadyForReview, Interaction: status.InteractionNone},
			[]string{"ready_for_review"}, []string{"needs_input", "completed"}},
		{"completed", supervisionEvent{Seq: 4, Group: status.GroupCompleted, Interaction: status.InteractionNone},
			[]string{"completed", "final review"}, []string{"needs_input"}},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := supervisionNotification(ns, tc.ev)
			prefix := "[swarm supervision " + child + "#" + strconv.FormatUint(tc.ev.Seq, 10) + "]"
			if !strings.HasPrefix(text, prefix) {
				t.Errorf("text %q does not start with %q", text, prefix)
			}
			for _, cmd := range []string{"swarm peek " + ns, "swarm send " + ns} {
				if !strings.Contains(text, cmd) {
					t.Errorf("text %q lacks the command %q", text, cmd)
				}
			}
			for _, w := range tc.want {
				if !strings.Contains(text, w) {
					t.Errorf("text %q lacks %q", text, w)
				}
			}
			for _, d := range tc.deny {
				if strings.Contains(text, d) {
					t.Errorf("text %q must not say %q for this event", text, d)
				}
			}
			if strings.ContainsAny(text, "\r\n") {
				t.Errorf("text %q spans lines; one message, one line", text)
			}
			if len(text) >= protocol.MaxSendInputText {
				t.Errorf("text is %d bytes, must be under protocol.MaxSendInputText (%d)", len(text), protocol.MaxSendInputText)
			}
			if seen[text] {
				t.Errorf("text %q repeats an earlier event's text; seq/state must make each distinct", text)
			}
			seen[text] = true
			if again := supervisionNotification(ns, tc.ev); again != text {
				t.Errorf("notification is not a pure function of (child, event): %q then %q", text, again)
			}
		})
	}
}

// TestSupervisor_UnsafeSourceKeepsEventPending: the event stays pending (nothing typed)
// while the source is mid-turn, at a permission dialog, not running, under a controller
// lease, or absent; once the source is safe again, one signal on it delivers exactly once.
func TestSupervisor_UnsafeSourceKeepsEventPending(t *testing.T) {
	cases := []struct {
		name   string
		unsafe func(f *supFakes)
		safe   func(f *supFakes)
	}{
		{"source turn active", func(f *supFakes) { f.setStatus("src", stWorking) }, func(f *supFakes) { f.setStatus("src", stReady) }},
		{"source at a permission dialog", func(f *supFakes) { f.setStatus("src", stPerm) }, func(f *supFakes) { f.setStatus("src", stReady) }},
		// A source waiting on its own question (interaction prompt) would read the typed
		// notification as the human's answer: the human answers first, then it is safe.
		{"source waiting on a question", func(f *supFakes) { f.setStatus("src", stPrompt) }, func(f *supFakes) { f.setStatus("src", stReady) }},
		{"source not running", func(f *supFakes) { f.setStatus("src", stExited) }, func(f *supFakes) { f.setStatus("src", stReady) }},
		{"source under a controller lease", func(f *supFakes) { f.setControlled("src", true) }, func(f *supFakes) { f.setControlled("src", false) }},
		{"source missing from the roster", func(f *supFakes) { f.remove("src") }, func(f *supFakes) { f.put(plainMeta("src", stReady)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, s, _ := armedPair(t)
			s.signal("kid")
			tc.unsafe(f)

			f.setStatus("kid", stPrompt)
			s.signal("kid")
			s.signal("src")
			settle(t, f, 0)
			if !s.pending("kid") {
				t.Fatal("pending(kid) = false while the source is unsafe; the event must wait")
			}

			tc.safe(f)
			s.signal("src")
			awaitDelivered(t, f, 1)
			if s.pending("kid") {
				t.Fatal("pending(kid) = true after the delivery")
			}
		})
	}
}

// TestSupervisor_RetryDeliversAfterControllerReleaseWithoutSignal: a human detach emits no
// status signal, so the retry cadence alone must notice the lease is gone and deliver.
func TestSupervisor_RetryDeliversAfterControllerReleaseWithoutSignal(t *testing.T) {
	f, s, _ := armedPair(t)
	s.signal("kid")
	f.setControlled("src", true)
	f.setStatus("kid", stPrompt)
	s.signal("kid")
	settle(t, f, 0)
	if !s.pending("kid") {
		t.Fatal("pending(kid) = false under a controller lease")
	}

	f.setControlled("src", false) // no signal follows
	awaitDelivered(t, f, 1)
	if s.pending("kid") {
		t.Fatal("pending(kid) = true after the retry delivered")
	}
}

// TestSupervisor_SendErrorKeepsEventPending: a refused write (the Server closing, a stream
// hiccup) leaves the event pending; the next safe check delivers the SAME event once.
func TestSupervisor_SendErrorKeepsEventPending(t *testing.T) {
	f, s, _ := armedPair(t)
	s.signal("kid")
	f.setSendErr(os.ErrClosed)
	f.setStatus("kid", stPrompt)
	s.signal("kid")
	deadline := time.Now().Add(3 * time.Second)
	for f.calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.calls() == 0 {
		t.Fatal("no send was attempted for a needs_input child of a safe source")
	}
	if len(f.delivered()) != 0 || !s.pending("kid") {
		t.Fatalf("after a failed send: delivered=%d pending=%v; want 0 and true", len(f.delivered()), s.pending("kid"))
	}

	f.setSendErr(nil)
	s.signal("src")
	got := awaitDelivered(t, f, 1)
	want := supervisionNotification(nsChild("kid"), supervisionEvent{Seq: 1, Group: status.GroupNeedsInput, Interaction: status.InteractionPrompt})
	if got[0].req.Text != want {
		t.Fatalf("redelivered text = %q\nwant the same seq 1 event %q", got[0].req.Text, want)
	}
	if s.pending("kid") {
		t.Fatal("pending(kid) = true after the redelivery")
	}
}

// TestSupervisor_NewerAttentionStateReplacesUndeliveredOne: at most ONE pending event per
// child (level-triggered): while needs_input is stuck behind a busy source and the child
// moves on to completed, the source gets ONE message, for completed, with the seq advanced.
func TestSupervisor_NewerAttentionStateReplacesUndeliveredOne(t *testing.T) {
	f, s, _ := armedPair(t)
	s.signal("kid")
	f.setStatus("src", stWorking)
	f.setStatus("kid", stPrompt)
	s.signal("kid")
	f.setStatus("kid", stExited)
	s.signal("kid")
	settle(t, f, 0)

	f.setStatus("src", stReady)
	s.signal("src")
	got := awaitDelivered(t, f, 1)
	want := supervisionNotification(nsChild("kid"), supervisionEvent{Seq: 2, Group: status.GroupCompleted, Interaction: status.InteractionNone})
	if got[0].req.Text != want {
		t.Fatalf("delivered %q\nwant the latest state only, %q", got[0].req.Text, want)
	}
}

// ---------------------------------------------------------------------------
// lifecycle of the record
// ---------------------------------------------------------------------------

// TestSupervisor_CompletedDeliveryRetiresRecord: once completed is delivered the record and
// its file are gone, and later signals move nothing.
func TestSupervisor_CompletedDeliveryRetiresRecord(t *testing.T) {
	f, s, dir := armedPair(t)
	s.signal("kid")
	f.setStatus("kid", stExited)
	s.signal("kid")
	got := awaitDelivered(t, f, 1)
	if !strings.Contains(got[0].req.Text, "completed") {
		t.Fatalf("delivered %q; want the completed notification", got[0].req.Text)
	}
	if fileExists(t, recordPath(dir, "kid")) {
		t.Fatal("record file survives a delivered completed event")
	}
	if s.pending("kid") {
		t.Fatal("pending(kid) = true after the record was retired")
	}
	s.signal("kid")
	s.signal("src")
	settle(t, f, 1)
}

// TestSupervisor_ChildGoneRetiresRecord: a child deleted from the roster takes its record
// (and any pending event) with it on the next signal.
func TestSupervisor_ChildGoneRetiresRecord(t *testing.T) {
	f, s, dir := armedPair(t)
	s.signal("kid")
	f.setStatus("src", stWorking)
	f.setStatus("kid", stPrompt)
	s.signal("kid")
	if !s.pending("kid") {
		t.Fatal("pending(kid) = false behind a busy source")
	}

	f.remove("kid")
	s.signal("kid")
	if fileExists(t, recordPath(dir, "kid")) {
		t.Fatal("record file survives the child leaving the roster")
	}
	if s.pending("kid") {
		t.Fatal("pending(kid) = true for a child that is gone")
	}
	f.setStatus("src", stReady)
	s.signal("src")
	settle(t, f, 0)
}

// TestSupervisor_SourceGoneKeepsRecordPending: a source that ends first leaves the record in
// place and the event pending -- the orphaned-supervisor marker's truth -- and no one else is
// notified (no re-parenting).
func TestSupervisor_SourceGoneKeepsRecordPending(t *testing.T) {
	f, s, dir := armedPair(t)
	s.signal("kid")
	f.remove("src")
	f.setStatus("kid", stPrompt)
	s.signal("kid")
	s.signal("src")
	settle(t, f, 0)
	if !s.pending("kid") {
		t.Fatal("pending(kid) = false with the source gone; the orphan must stay visible")
	}
	if !fileExists(t, recordPath(dir, "kid")) {
		t.Fatal("record file removed although only the source is gone")
	}
}

// ---------------------------------------------------------------------------
// durability across a restart
// ---------------------------------------------------------------------------

// TestSupervisor_RestartReplaysUndeliveredEventExactlyOnce: an event left pending by one
// supervisor is delivered by the next one over the same dir -- the same seq and text --
// exactly once, and the file carries the documented keys.
func TestSupervisor_RestartReplaysUndeliveredEventExactlyOnce(t *testing.T) {
	f, s1, dir := armedPair(t)
	s1.signal("kid")
	f.setStatus("src", stWorking)
	f.setStatus("kid", stPrompt)
	s1.signal("kid")
	if !s1.pending("kid") {
		t.Fatal("pending(kid) = false behind a busy source")
	}
	s1.close()

	raw, err := os.ReadFile(recordPath(dir, "kid"))
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("decode record %s: %v", raw, err)
	}
	for _, k := range []string{"child", "source", "last_group", "seen_working", "seq", "pending"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("record %s lacks key %q", raw, k)
		}
	}
	var ev map[string]json.RawMessage
	_ = json.Unmarshal(keys["pending"], &ev)
	for _, k := range []string{"seq", "group", "interaction"} {
		if _, ok := ev[k]; !ok {
			t.Errorf("pending event %s lacks key %q", keys["pending"], k)
		}
	}

	f.setStatus("src", stReady) // safe now, and no signal will come
	s2, err := newSupervisor(supEndpoint, dir, supRetry, f.get, f.isControlled, f.send)
	if err != nil {
		t.Fatalf("second newSupervisor over the same dir: %v", err)
	}
	t.Cleanup(s2.close)
	got := awaitDelivered(t, f, 1)
	want := supervisionNotification(nsChild("kid"), supervisionEvent{Seq: 1, Group: status.GroupNeedsInput, Interaction: status.InteractionPrompt})
	if got[0].local != "src" || got[0].req.Text != want {
		t.Fatalf("replayed send = %+v\nwant seq 1 to src: %q", got[0], want)
	}
	if s2.pending("kid") {
		t.Fatal("pending(kid) = true after the replay delivered")
	}
	s2.signal("kid")
	s2.signal("src")
	settle(t, f, 1)
	if r := readRecord(t, dir, "kid"); r.Seq != 1 || r.Pending != nil || r.LastGroup != string(status.GroupNeedsInput) {
		t.Fatalf("record after replay = %+v; want seq 1, nothing pending, last_group needs_input", r)
	}
}

// TestSupervisor_RestartAfterDeliveryReplaysNothing: a delivered event is not delivered again
// by the next incarnation, even though the child still sits in the same attention state.
func TestSupervisor_RestartAfterDeliveryReplaysNothing(t *testing.T) {
	f, s1, dir := armedPair(t)
	s1.signal("kid")
	f.setStatus("kid", stPrompt)
	s1.signal("kid")
	awaitDelivered(t, f, 1)
	s1.close()

	s2, err := newSupervisor(supEndpoint, dir, supRetry, f.get, f.isControlled, f.send)
	if err != nil {
		t.Fatalf("second newSupervisor over the same dir: %v", err)
	}
	t.Cleanup(s2.close)
	s2.signal("kid")
	s2.signal("src")
	settle(t, f, 1)
	if s2.pending("kid") {
		t.Fatal("pending(kid) = true after a restart with nothing undelivered")
	}
}

// TestSupervisor_ReArmAfterRestartKeepsSeq: reconcile arms the same child again; the loaded
// record (and its seq) is kept, so the next event continues the sequence.
func TestSupervisor_ReArmAfterRestartKeepsSeq(t *testing.T) {
	f, s1, dir := armedPair(t)
	s1.signal("kid")
	f.setStatus("kid", stPrompt)
	s1.signal("kid")
	awaitDelivered(t, f, 1)
	s1.close()

	s2, err := newSupervisor(supEndpoint, dir, supRetry, f.get, f.isControlled, f.send)
	if err != nil {
		t.Fatalf("second newSupervisor: %v", err)
	}
	t.Cleanup(s2.close)
	m, _ := f.get("kid")
	s2.arm(m)
	f.setStatus("kid", stWorking)
	s2.signal("kid")
	f.setStatus("kid", stPrompt)
	s2.signal("kid")
	got := awaitDelivered(t, f, 2)
	want := supervisionNotification(nsChild("kid"), supervisionEvent{Seq: 2, Group: status.GroupNeedsInput, Interaction: status.InteractionPrompt})
	if got[1].req.Text != want {
		t.Fatalf("post-restart event text = %q\nwant seq 2 %q", got[1].req.Text, want)
	}
}

// ---------------------------------------------------------------------------
// concurrency
// ---------------------------------------------------------------------------

// TestSupervisor_ConcurrentSignalsDeliverOnce: signals racing from many goroutines (child
// and source alike, with a slow send in flight) still deliver one event exactly once.
func TestSupervisor_ConcurrentSignalsDeliverOnce(t *testing.T) {
	f, s, _ := armedPair(t)
	s.signal("kid")
	f.mu.Lock()
	f.sendDelay = 10 * time.Millisecond
	f.mu.Unlock()
	f.setStatus("kid", stPrompt)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); s.signal("kid") }()
		go func() { defer wg.Done(); s.signal("src") }()
	}
	wg.Wait()
	awaitDelivered(t, f, 1)
	if s.pending("kid") {
		t.Fatal("pending(kid) = true after the concurrent delivery")
	}
}
