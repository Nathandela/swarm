package conformance_test

// Slice S16 -- PB-APP-7: the settings screen's two coarse push toggles, "honored by
// PB-PUSH-0's trigger" and "demonstrably suppressing delivery".
//
// VERIFIED AT THE SENDER, NEVER AT THE RECEIVER. PB-PUSH-8 states the reason and it is not a
// stylistic preference: a push that is sent and then filtered on the handset still lets the
// provider observe the token, the timing and the size, which contradicts PB-PUSH-3's
// disclosure claim. Only ZERO calls to PushTrigger satisfy "disabled". So the machine side
// here is the real one -- a real CommandBridge opening the phone's real sealed command, the
// real durable filePushPrefs custody, and the real PushNotifier reading it before it wakes
// anybody -- and the assertion counts calls at the relay seam.
//
// WHAT THE FACADE DOES TODAY, in full: SetPushPreference persists the pair locally and then
// returns a.refuse("push_preference", ...) -- "has no wire verb yet ... owed by another
// slice (PB-PUSH-8)". That comment is STALE. S12 shipped ActionPushPrefs, protocol.
// OpPushPrefs, the daemon's requireRemoteAuthz arm, remotegw.applyPushPrefs and the
// notifier's categoryEnabled gate. The verb exists; nothing on the phone seals it. So the
// user's toggle is a local boolean the machine has never heard of, and every push the user
// turned off is still sent.
//
// The daemon's authorization of push_prefs is S12's fence (internal/skeleton/
// pushprefs_authz_test.go) and is deliberately not re-litigated here: the forwarder below
// answers OK, because the subject is whether the PHONE carries the preference to the
// machine and whether the machine's SENDER then honours it.

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remotegw"
	"github.com/Nathandela/swarm/internal/status"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// s16Machine is the machine-side half of the push path: the command bridge that applies a
// push_prefs, the durable custody it writes, and the notifier that reads the same custody
// before it wakes the phone. One custody object shared by both, exactly as production wires
// it (CommandBridgeConfig.Prefs and PushConfig.Prefs are the same *filePushPrefs).
type s16Machine struct {
	bridge   *remotegw.CommandBridge
	notifier *remotegw.PushNotifier
	prefs    remotegw.PushPrefsSource
	pusher   *s16CountingPusher
}

// s16CountingPusher counts wakes at the relay seam -- the LAST point before the provider
// sees a token. Counting anywhere closer to the phone would measure local filtering, which
// PB-PUSH-8 says explicitly is not enough.
type s16CountingPusher struct {
	mu    sync.Mutex
	calls int
}

func (p *s16CountingPusher) PushTrigger(_ context.Context, _ string, _ []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return nil
}

func (p *s16CountingPusher) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// s16OKForwarder is the daemon, answering OK. Its authorization is S12's requirement and its
// own test; folding it in here would make a PB-APP-7 failure indistinguishable from a
// PB-PUSH-8 one.
type s16OKForwarder struct{}

func (s16OKForwarder) ForwardCommand(op, session string, cmd protocol.DeviceCommandAuth,
	_ *protocol.LaunchReq) (protocol.Control, error) {
	return protocol.Control{Op: protocol.OpOK, SessionID: session, OperationID: cmd.OperationID}, nil
}

func s16NewMachine(t *testing.T, h *harness) *s16Machine {
	t.Helper()
	prefs, err := remotegw.OpenPushPrefs(filepath.Join(t.TempDir(), "push-prefs.json"))
	if err != nil {
		t.Fatalf("OpenPushPrefs: %v", err)
	}
	pusher := &s16CountingPusher{}
	return &s16Machine{
		bridge: remotegw.NewCommandBridge(remotegw.CommandBridgeConfig{
			Mailbox:     h.machineRelay,
			Forwarder:   s16OKForwarder{},
			Key:         h.Keys.ContentKey,
			EpochID:     h.EpochID,
			ReplyTarget: h.phoneTarget,
			Prefs:       prefs,
		}),
		notifier: remotegw.NewPushNotifier(h.sink, remotegw.PushConfig{
			Pusher:  pusher,
			Target:  h.phoneTarget,
			WakeKey: h.Keys.WakeKey,
			EpochID: h.EpochID,
			Prefs:   prefs,
		}),
		prefs:  prefs,
		pusher: pusher,
	}
}

// apply drains the phone's commands into the bridge until the preference the phone just set
// has been stored, so the assertion that follows is about a machine that HAS the setting.
func (m *s16Machine) apply(t *testing.T, h *harness, want swarmmobile.PushPreference) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := m.bridge.PollOnce(h.ctx); err != nil {
			// Per-item failures are aggregated by the bridge and are not fatal here: a
			// take_control or a keystroke this suite left in the mailbox has no forwarder arm.
			_ = err
		}
		if p, err := m.prefs.LoadPrefs(); err == nil && p.Version > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("PB-APP-7: the phone set %+v and no push_prefs command ever reached the machine "+
		"within 5s.\nApp.SetPushPreference persists the pair locally and records a LOCAL refusal "+
		"-- \"no wire verb yet\" -- which has been stale since S12 shipped ActionPushPrefs. The "+
		"user's toggle is a boolean the machine has never heard of, so every push they turned off "+
		"is still sent and the provider still sees the token, the timing and the size (PB-PUSH-3).",
		want)
}

// wake drives one journal transition into the notifier and reports how many pushes it caused.
// A DISTINCT session per call, because the notifier suppresses a re-entry into a group a
// session is already in -- a second transition on the same session would measure that
// dedupe rather than the preference.
func (m *s16Machine) wake(t *testing.T, session string, group status.Group) int {
	t.Helper()
	before := m.pusher.Calls()
	if err := m.notifier.Event(protocol.JournalRecord{
		SessionID: testMachineID + "/" + session, Type: "status", Group: group,
	}); err != nil {
		t.Fatalf("notifier.Event: %v", err)
	}
	return m.pusher.Calls() - before
}

func s16ReconciledHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})
	return h
}

// TestPBAPP7_ADisabledToggleSendsNoPushAtAll is the requirement's own criterion.
func TestPBAPP7_ADisabledToggleSendsNoPushAtAll(t *testing.T) {
	h := s16ReconciledHarness(t)
	m := s16NewMachine(t, h)

	off := swarmmobile.PushPreference{}
	if _, err := h.App.SetPushPreference(&off); err != nil {
		t.Fatalf("SetPushPreference(off): %v", err)
	}
	m.apply(t, h, off)

	if n := m.wake(t, "sess-off-needsinput", status.GroupNeedsInput); n != 0 {
		t.Errorf("PB-APP-7: %d push(es) were SENT for a needs_input transition with the toggle "+
			"off. Filtering on the handset is not suppression -- the provider has already seen "+
			"the token, the timing and the size (PB-PUSH-8)", n)
	}
	if n := m.wake(t, "sess-off-completed", status.GroupCompleted); n != 0 {
		t.Errorf("PB-APP-7: %d push(es) were sent for a completed transition with the toggle off", n)
	}

	// AND THE POSITIVE HALF. Without it every assertion above is satisfied by a machine that
	// never pushes at all, which is the vacuous form of "the toggle works".
	on := swarmmobile.PushPreference{Alerts: true, Mentions: true}
	if _, err := h.App.SetPushPreference(&on); err != nil {
		t.Fatalf("SetPushPreference(on): %v", err)
	}
	m.apply(t, h, on)
	if n := m.wake(t, "sess-on-needsinput", status.GroupNeedsInput); n != 1 {
		t.Errorf("PB-APP-7: a needs_input transition with the toggle ON produced %d pushes, want 1", n)
	}
	if n := m.wake(t, "sess-on-completed", status.GroupCompleted); n != 1 {
		t.Errorf("PB-APP-7: a completed transition with the toggle ON produced %d pushes, want 1", n)
	}
}

// TestPBAPP7_TheTwoTogglesAreIndependentAndNotInverted.
//
// The facade's field names are Alerts and Mentions; the wire's are NeedsInput and Finished,
// and PB-APP-7's categories are "a transition into needs_input" and "a transition into
// ready_for_review or completed". There are no mentions anywhere in this product, so the
// mapping between the two pairs is a decision nothing currently records -- and two of its
// three plausible readings are silent defects: wiring BOTH toggles to NeedsInput leaves the
// second switch dead, and swapping them silences the notifications the user asked for while
// delivering the ones they refused.
//
// This does not prescribe WHICH facade field carries which category. It asserts only that
// the mapping is a bijection, which is the property a settings screen with two switches on
// it needs to be true.
func TestPBAPP7_TheTwoTogglesAreIndependentAndNotInverted(t *testing.T) {
	h := s16ReconciledHarness(t)
	m := s16NewMachine(t, h)

	type probe struct {
		pref     swarmmobile.PushPreference
		needsIn  int
		finished int
	}
	first := probe{pref: swarmmobile.PushPreference{Alerts: true}}
	second := probe{pref: swarmmobile.PushPreference{Mentions: true}}

	for i, p := range []*probe{&first, &second} {
		if _, err := h.App.SetPushPreference(&p.pref); err != nil {
			t.Fatalf("SetPushPreference(%+v): %v", p.pref, err)
		}
		m.apply(t, h, p.pref)
		p.needsIn = m.wake(t, "sess-bij-n-"+string(rune('a'+i)), status.GroupNeedsInput)
		p.finished = m.wake(t, "sess-bij-f-"+string(rune('a'+i)), status.GroupReadyForReview)
	}

	// Exactly one category per toggle, and a different one for each.
	if first.needsIn+first.finished != 1 || second.needsIn+second.finished != 1 {
		t.Errorf("PB-APP-7: one toggle on delivered %d+%d and the other %d+%d of the two "+
			"categories, want exactly 1 each. A switch that gates both categories leaves the "+
			"other switch dead; one that gates neither is a control with no effect",
			first.needsIn, first.finished, second.needsIn, second.finished)
	}
	if first.needsIn == second.needsIn && first.finished == second.finished {
		t.Errorf("PB-APP-7: both toggles gate the SAME category (needs_input %d/%d, finished "+
			"%d/%d). The settings screen shows two switches and the user has one",
			first.needsIn, second.needsIn, first.finished, second.finished)
	}
}

// TestPBAPP7_ThePreferenceSurvivesAProcessDeathAndStillTakesEffect.
//
// THE BRICK THIS IS WRITTEN AGAINST, and it is invisible from every screen. The machine
// refuses any push_prefs whose Version does not STRICTLY exceed the stored one
// (filePushPrefs.SavePrefs), because the relay may replay a frame from before the user
// turned pushes off. phonecore.PushPreference is {Alerts, Mentions} and carries NO version,
// so whatever the phone counts must be durable or it restarts at 1 on the next process
// death -- and on Android a process death is routine. Every toggle after the first restart
// is then refused by the machine while the settings screen shows the new value happily.
//
// The user turns notifications off, the app is killed, they turn something else on, and
// nothing they do from that moment forward ever reaches the machine again.
func TestPBAPP7_ThePreferenceSurvivesAProcessDeathAndStillTakesEffect(t *testing.T) {
	h := s16ReconciledHarness(t)
	m := s16NewMachine(t, h)

	on := swarmmobile.PushPreference{Alerts: true, Mentions: true}
	if _, err := h.App.SetPushPreference(&on); err != nil {
		t.Fatalf("SetPushPreference(on): %v", err)
	}
	m.apply(t, h, on)
	firstVersion := s16PrefsVersion(t, m)

	// PROCESS DEATH. Android SIGKILLs the app; Close is the closest this test can come, and
	// the state directory outlives it.
	if err := h.App.Close(); err != nil {
		t.Fatalf("App.Close: %v", err)
	}
	h.App = h.openApp()

	// The screen must come back showing what the user chose, not a default.
	got, err := h.App.PushPreference()
	if err != nil {
		t.Fatalf("PushPreference after restart: %v", err)
	}
	if got.Alerts != on.Alerts || got.Mentions != on.Mentions {
		t.Errorf("PB-APP-7: after a process death the settings screen reads %+v, want %+v", got, on)
	}

	// And the next change must still REACH the machine.
	off := swarmmobile.PushPreference{}
	if _, err := h.App.SetPushPreference(&off); err != nil {
		t.Fatalf("SetPushPreference(off) after restart: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := m.bridge.PollOnce(h.ctx); err != nil {
			_ = err
		}
		if s16PrefsVersion(t, m) > firstVersion {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v := s16PrefsVersion(t, m); v <= firstVersion {
		t.Fatalf("PB-APP-7/PB-PUSH-10: the preference version is still %d after a post-restart "+
			"change (was %d before the restart). The phone's version counter is not durable, so "+
			"the machine refuses every update from here on as a replay -- and the settings screen "+
			"keeps showing the user's choice while the machine keeps the old one, forever",
			v, firstVersion)
	}
	if n := m.wake(t, "sess-after-restart", status.GroupNeedsInput); n != 0 {
		t.Errorf("PB-APP-7: %d push(es) sent after the user turned notifications off following a "+
			"restart", n)
	}
}

func s16PrefsVersion(t *testing.T, m *s16Machine) uint64 {
	t.Helper()
	p, err := m.prefs.LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs: %v", err)
	}
	return p.Version
}
