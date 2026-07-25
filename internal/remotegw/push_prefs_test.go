package remotegw

// Failing-first tests for PB-PUSH-8 (the device -> machine push-preference verb) and the
// custody half of PB-PUSH-10 (a durable, machine-authoritative record with acknowledged,
// versioned updates).
//
// The SENDER-side suppression these preferences drive is pinned in push_trigger_test.go;
// this file pins how the preference gets there and how it survives.
//
// RED is undefined-only. Nothing here has an implementation yet.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// --- helpers ----------------------------------------------------------------

// sealedPrefsCmd seals a push-preference RemoteCommand the way the phone core does: the
// signed tuple plus the preference body.
//
// The body is deliberately NOT bound by ContentHash the way a launch spec is, and the
// difference is load-bearing rather than an omission. A launch spec is forwarded through
// the gateway IN CLEARTEXT (protocol.Control.Launch), so the hash is what stops the
// gateway altering it. A preference body never leaves the gateway: it arrives sealed
// under the epoch content key -- which the relay, the declared adversary, cannot forge --
// and the gateway is itself the custodian that decides delivery, so a hash it recomputes
// against its own file would protect nothing it could not simply overwrite. Adding one
// would also require a new protocol.Control field for the daemon to recompute from.
func sealedPrefsCmd(t *testing.T, key crypto.ContentKey, seq uint64, opID string, spec *protocol.PushPrefs) []byte {
	t.Helper()
	rc := protocol.RemoteCommand{
		DeviceCommandAuth: protocol.DeviceCommandAuth{
			DeviceID:    "d1",
			Action:      protocol.ActionPushPrefs,
			Machine:     "m",
			Session:     protocol.LaunchSessionSentinel,
			OperationID: opID,
			ExpiresAt:   time.Now().Add(time.Minute),
			Sig:         "device-signature",
		},
		PushPrefs: spec,
	}
	plain, err := json.Marshal(rc)
	if err != nil {
		t.Fatalf("marshal push_prefs command: %v", err)
	}
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version: crypto.VersionV1, EpochID: 1, Seq: seq, IssuedAt: time.Now().UnixMilli(),
	}, plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return env.Marshal()
}

// refusingForwarder is the daemon refusing a command -- an unknown device, a forged or
// expired signature, an insufficient capability, or the kill switch. The gateway holds
// no device key and cannot tell these apart; all it may do is NOT apply the preference.
type refusingForwarder struct {
	ops []string
}

func (f *refusingForwarder) ForwardCommand(op, _ string, _ protocol.DeviceCommandAuth, _ *protocol.LaunchReq) (protocol.Control, error) {
	f.ops = append(f.ops, op)
	return protocol.Control{Op: protocol.OpError, Error: "device signature invalid", ErrorCode: protocol.CodeNotAuthorized}, nil
}

// prefsBridge assembles a CommandBridge over an inbox holding exactly the given
// envelopes, with a durable preference record at path.
func prefsBridge(t *testing.T, key crypto.ContentKey, prefs PushPrefsSource, fwd CommandForwarder, envs ...[]byte) (*CommandBridge, *fakeMailbox) {
	t.Helper()
	mb := &fakeMailbox{}
	for i, e := range envs {
		mb.inbox = append(mb.inbox, relay.Item{Cursor: uint64(i + 1), Envelope: e})
	}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
		Prefs:       prefs,
	})
	return b, mb
}

// --- PB-PUSH-8: the verb ----------------------------------------------------

// TestPBPUSH8_PushPrefsIsAuthorizedByTheDaemonBeforeItIsApplied pins that the new verb
// rides the SAME authorization plane as every other signed action instead of growing a
// second one inside the gateway. The gateway holds no device key: if it decided this
// verb locally, a compromised relay that could inject a plaintext-shaped frame would be
// deciding whether the owner's phone gets woken.
func TestPBPUSH8_PushPrefsIsAuthorizedByTheDaemonBeforeItIsApplied(t *testing.T) {
	key := testContentKey()
	prefs := &stubPrefs{prefs: PushPrefs{Version: 1, NeedsInput: true, Finished: true}}
	fwd := &fakeForwarder{}
	spec := &protocol.PushPrefs{Version: 2, NeedsInput: false, Finished: true}
	b, _ := prefsBridge(t, key, prefs, fwd, sealedPrefsCmd(t, key, 1, "op-prefs-1", spec))

	if _, err := b.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(fwd.ops) != 1 || fwd.ops[0] != protocol.OpPushPrefs {
		t.Fatalf("forwarded ops = %v, want exactly one %q: the preference must be authorized by the daemon", fwd.ops, protocol.OpPushPrefs)
	}
	// A blind conduit: the signature the phone produced reaches the daemon untouched.
	if len(fwd.seen) != 1 || fwd.seen[0].Sig != "device-signature" || fwd.seen[0].Action != protocol.ActionPushPrefs {
		t.Fatalf("forwarded auth tuple = %+v, want the phone's push_prefs tuple verbatim", fwd.seen)
	}
	got, err := prefs.LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs: %v", err)
	}
	if got.Version != 2 || got.NeedsInput != false || got.Finished != true {
		t.Fatalf("stored preference = %+v, want {Version:2 NeedsInput:false Finished:true}", got)
	}
}

// TestPBPUSH8_DaemonRefusalLeavesThePreferenceUnchanged is the forgery gate seen from
// the gateway. If the gateway applied on refusal, anyone who could get a plaintext-
// shaped frame into the mailbox could silence the owner's notifications -- and the
// silence is the kind of failure nobody reports, because nothing appears to break.
func TestPBPUSH8_DaemonRefusalLeavesThePreferenceUnchanged(t *testing.T) {
	key := testContentKey()
	before := PushPrefs{Version: 4, NeedsInput: true, Finished: true}
	prefs := &stubPrefs{prefs: before}
	fwd := &refusingForwarder{}
	spec := &protocol.PushPrefs{Version: 9, NeedsInput: false, Finished: false}
	b, mb := prefsBridge(t, key, prefs, fwd, sealedPrefsCmd(t, key, 1, "op-forged", spec))

	// The batch is processed; a refusal is a per-item outcome, not a wedged loop.
	_, _ = b.PollOnce(context.Background())

	got, err := prefs.LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs: %v", err)
	}
	if got != before {
		t.Fatalf("preference changed on a REFUSED push_prefs: got %+v, want %+v", got, before)
	}
	// The phone must be told, or it sits on a settings screen that disagrees with the
	// machine forever.
	if len(mb.replies) != 1 {
		t.Fatalf("sealed replies = %d, want 1 (the refusal must be acknowledged)", len(mb.replies))
	}
}

// TestPBPUSH8_AbsentPreferenceCustodyRefusesTheVerb is fail-closed-absent for the write
// side, matching the read side in push_trigger_test.go. A bridge with no durable custody
// must not answer OK to a preference change it did not store: the phone would show a
// setting the machine has never heard of.
func TestPBPUSH8_AbsentPreferenceCustodyRefusesTheVerb(t *testing.T) {
	key := testContentKey()
	spec := &protocol.PushPrefs{Version: 2, NeedsInput: false, Finished: false}

	// POSITIVE CONTROL. Without it this test passes on any build where push_prefs is
	// simply an unrecognised action -- refused for the wrong reason, and still refused
	// once custody is wired.
	okBridge, _ := prefsBridge(t, key, &stubPrefs{}, &fakeForwarder{}, sealedPrefsCmd(t, key, 1, "op-control", spec))
	processed, err := okBridge.PollOnce(context.Background())
	if err != nil || processed != 1 {
		t.Fatalf("control: an identical push_prefs WITH custody gave processed=%d err=%v, want 1/nil", processed, err)
	}

	b, _ := prefsBridge(t, key, nil, &fakeForwarder{}, sealedPrefsCmd(t, key, 1, "op-nocustody", spec))
	processed, err = b.PollOnce(context.Background())
	if err == nil {
		t.Fatal("PollOnce returned nil error with no preference custody: an unstorable preference must be loud")
	}
	if processed != 0 {
		t.Fatalf("processed = %d, want 0: nothing was stored, so nothing succeeded", processed)
	}
}

// TestPBPUSH8_PushPrefsCommandWithNoBodyIsRefused pins the counterpart of the launch
// path's "missing its launch spec in-envelope" refusal: a push_prefs whose body was
// stripped in transit must be refused loudly, never applied as some default.
func TestPBPUSH8_PushPrefsCommandWithNoBodyIsRefused(t *testing.T) {
	key := testContentKey()
	before := PushPrefs{Version: 4, NeedsInput: true, Finished: true}

	// POSITIVE CONTROL: the same command WITH a body is accepted, so the refusal below is
	// attributable to the missing body and not to push_prefs being unrecognised.
	ctl := &stubPrefs{prefs: before}
	okBridge, _ := prefsBridge(t, key, ctl, &fakeForwarder{},
		sealedPrefsCmd(t, key, 1, "op-control", &protocol.PushPrefs{Version: 5, NeedsInput: false, Finished: false}))
	if _, err := okBridge.PollOnce(context.Background()); err != nil {
		t.Fatalf("control: an identical push_prefs WITH a body was refused: %v", err)
	}

	prefs := &stubPrefs{prefs: before}
	b, _ := prefsBridge(t, key, prefs, &fakeForwarder{}, sealedPrefsCmd(t, key, 1, "op-nobody", nil))
	if _, err := b.PollOnce(context.Background()); err == nil {
		t.Fatal("PollOnce returned nil for a push_prefs with no body: it must be refused, not defaulted")
	}
	got, _ := prefs.LoadPrefs()
	if got != before {
		t.Fatalf("preference changed on a body-less push_prefs: got %+v, want %+v", got, before)
	}
}

// --- PB-PUSH-10: acknowledged, versioned updates ----------------------------

// TestPBPUSH10_AcknowledgementIsSealedOnlyAfterThePreferenceIsDurable pins the ordering
// PB-PUSH-10's "acknowledged" depends on. An ack the phone receives is what makes its
// settings screen authoritative; if the ack can precede (or survive) a failed persist,
// the screen says "off" while the next gateway start pushes again -- the exact defect
// the requirement names.
func TestPBPUSH10_AcknowledgementIsSealedOnlyAfterThePreferenceIsDurable(t *testing.T) {
	key := testContentKey()
	prefs := &failingSavePrefs{stubPrefs: stubPrefs{prefs: PushPrefs{Version: 1, NeedsInput: true, Finished: true}}}
	fwd := &fakeForwarder{}
	spec := &protocol.PushPrefs{Version: 2, NeedsInput: false, Finished: false}
	b, mb := prefsBridge(t, key, prefs, fwd, sealedPrefsCmd(t, key, 1, "op-persistfail", spec))

	if _, err := b.PollOnce(context.Background()); err == nil {
		t.Fatal("PollOnce returned nil although the preference could not be persisted")
	}
	// The phone must receive exactly one reply and it must NOT be an OK. Merely checking
	// "no OK among the replies" is satisfied by sending no reply at all -- which leaves
	// the phone's settings screen waiting forever instead of wrongly confident.
	if len(mb.replies) != 1 {
		t.Fatalf("sealed replies = %d, want exactly 1: a preference the machine could not store must still be answered", len(mb.replies))
	}
	ctrl := prefsReplyControl(t, key, mb.replies[0])
	if ctrl.Op == protocol.OpOK {
		t.Fatalf("the reply acknowledged success for a preference that was never stored: %+v", ctrl)
	}
}

// TestPBPUSH10_AcknowledgementIsAttributableToTheCommand pins that the ack the phone
// receives names the operation it answers. ReplyCache is an unkeyed FIFO, so without the
// operation id on the reply the phone cannot tell an ack for "disable" from an ack for
// any other in-flight op -- it converges on whichever arrived, which for a preference
// means showing a setting the machine does not hold.
func TestPBPUSH10_AcknowledgementIsAttributableToTheCommand(t *testing.T) {
	key := testContentKey()
	prefs := &stubPrefs{prefs: PushPrefs{Version: 1, NeedsInput: true, Finished: true}}
	fwd := &fakeForwarder{}
	spec := &protocol.PushPrefs{Version: 2, NeedsInput: false, Finished: true}
	b, mb := prefsBridge(t, key, prefs, fwd, sealedPrefsCmd(t, key, 1, "op-ack-me", spec))

	if _, err := b.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(mb.replies) != 1 {
		t.Fatalf("sealed replies = %d, want 1", len(mb.replies))
	}
	ctrl := prefsReplyControl(t, key, mb.replies[0])
	if ctrl.Op != protocol.OpOK {
		t.Fatalf("reply op = %q, want %q", ctrl.Op, protocol.OpOK)
	}
	if ctrl.OperationID != "op-ack-me" {
		t.Fatalf("reply operation_id = %q, want %q: an unattributable ack cannot make the phone's screen authoritative", ctrl.OperationID, "op-ack-me")
	}
}

// TestPBPUSH10_StaleVersionNeverOverwritesANewerPreference pins the monotonic half of
// "versioned updates". The relay is the declared adversary and may reorder or replay
// what it retains, so a preference frame from before the user turned pushes off must not
// turn them back on.
func TestPBPUSH10_StaleVersionNeverOverwritesANewerPreference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-prefs.json")
	prefs, err := OpenPushPrefs(path)
	if err != nil {
		t.Fatalf("OpenPushPrefs: %v", err)
	}
	if err := prefs.SavePrefs(PushPrefs{Version: 7, NeedsInput: false, Finished: false}); err != nil {
		t.Fatalf("SavePrefs(7): %v", err)
	}
	for _, stale := range []uint64{0, 1, 6, 7} {
		if err := prefs.SavePrefs(PushPrefs{Version: stale, NeedsInput: true, Finished: true}); err == nil {
			t.Fatalf("SavePrefs(version %d) succeeded over a stored version 7: a replayed preference re-enables push", stale)
		}
		got, err := prefs.LoadPrefs()
		if err != nil {
			t.Fatalf("LoadPrefs: %v", err)
		}
		if got.Version != 7 || got.NeedsInput || got.Finished {
			t.Fatalf("stored preference after a stale version %d = %+v, want the version-7 disabled record", stale, got)
		}
	}
	if err := prefs.SavePrefs(PushPrefs{Version: 8, NeedsInput: true, Finished: false}); err != nil {
		t.Fatalf("SavePrefs(8): %v", err)
	}
	got, _ := prefs.LoadPrefs()
	if got.Version != 8 || !got.NeedsInput || got.Finished {
		t.Fatalf("stored preference after a fresh version 8 = %+v, want {8 true false}", got)
	}
}

// --- PB-PUSH-10: durable custody -------------------------------------------

// TestPBPUSH10_PreferenceSurvivesAProcessRestart is the plain durability check: what one
// process stored, an entirely new handle over the same path reads back.
func TestPBPUSH10_PreferenceSurvivesAProcessRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-prefs.json")
	first, err := OpenPushPrefs(path)
	if err != nil {
		t.Fatalf("OpenPushPrefs: %v", err)
	}
	want := PushPrefs{Version: 3, NeedsInput: false, Finished: true}
	if err := first.SavePrefs(want); err != nil {
		t.Fatalf("SavePrefs: %v", err)
	}

	second, err := OpenPushPrefs(path)
	if err != nil {
		t.Fatalf("OpenPushPrefs(restart): %v", err)
	}
	got, err := second.LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs(restart): %v", err)
	}
	if got != want {
		t.Fatalf("preference after restart = %+v, want %+v", got, want)
	}
}

// TestPBPUSH10_NeverConfiguredDefaultsToBothCategoriesEnabled pins the BOOTSTRAP
// default, which is deliberately the opposite direction from the corrupt case below. A
// machine on which the user has never expressed a preference has nothing to contradict,
// and push is the sole background wake path (ADR-007 B16) -- shipping it off by default
// would make the product inert out of the box.
func TestPBPUSH10_NeverConfiguredDefaultsToBothCategoriesEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-prefs.json")
	prefs, err := OpenPushPrefs(path)
	if err != nil {
		t.Fatalf("OpenPushPrefs on a fresh state dir: %v", err)
	}
	got, err := prefs.LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs on a fresh state dir: %v", err)
	}
	if !got.NeedsInput || !got.Finished {
		t.Fatalf("bootstrap preference = %+v, want both categories enabled", got)
	}
	if got.Version != 0 {
		t.Fatalf("bootstrap version = %d, want 0 so the phone's first real update always wins", got.Version)
	}
}

// TestPBPUSH10_CorruptPreferenceIsNeverSilentlyTheEnabledDefault is the standing
// class-(iv) guard for this requirement. A record that EXISTS but cannot be read may
// well say "off"; treating it as the enabled bootstrap default would resume pushing --
// leaking token, timing and size -- against a setting the user is looking at, with
// nothing failing anywhere.
func TestPBPUSH10_CorruptPreferenceIsNeverSilentlyTheEnabledDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-prefs.json")
	if err := os.WriteFile(path, []byte("{not json at all"), 0o600); err != nil {
		t.Fatalf("write corrupt prefs: %v", err)
	}
	prefs, err := OpenPushPrefs(path)
	if err != nil {
		return // fail-closed at open is an acceptable shape; nothing can then read a default
	}
	got, loadErr := prefs.LoadPrefs()
	if loadErr == nil {
		t.Fatalf("LoadPrefs on a corrupt record returned %+v with no error: an unreadable preference must never present as the enabled default", got)
	}
	if got.NeedsInput || got.Finished {
		t.Fatalf("LoadPrefs on a corrupt record returned an ENABLED preference alongside its error (%v): a caller that logs and continues resumes pushing", loadErr)
	}
}

// TestPBPUSH10_PreferenceIsStoredAsInspectableState pins that the record is real durable
// state and not, say, a value only ever held in memory behind a file that is created but
// never written -- the shape in which every in-process test passes and every restart
// forgets.
func TestPBPUSH10_PreferenceIsStoredAsInspectableState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push-prefs.json")
	prefs, err := OpenPushPrefs(path)
	if err != nil {
		t.Fatalf("OpenPushPrefs: %v", err)
	}
	if err := prefs.SavePrefs(PushPrefs{Version: 11, NeedsInput: false, Finished: true}); err != nil {
		t.Fatalf("SavePrefs: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read prefs file: %v", err)
	}
	var onDisk PushPrefs
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("prefs file is not the documented json record (%q): %v", raw, err)
	}
	if onDisk.Version != 11 || onDisk.NeedsInput || !onDisk.Finished {
		t.Fatalf("on-disk preference = %+v, want {11 false true}", onDisk)
	}
}

// --- shared fakes ------------------------------------------------------------

// failingSavePrefs reads fine but cannot persist -- a full or read-only state dir.
type failingSavePrefs struct {
	stubPrefs
}

func (f *failingSavePrefs) SavePrefs(PushPrefs) error {
	return errors.New("push-prefs.json: no space left on device")
}

// prefsReplyControl opens a sealed command_reply the bridge appended to the phone's
// mailbox and returns the daemon Control inside it (openReplyControl, in
// lease_confirm_test.go, additionally returns the header this file does not need).
func prefsReplyControl(t *testing.T, key crypto.ContentKey, raw []byte) protocol.Control {
	t.Helper()
	_, ctrl := openReplyControl(t, key, raw)
	return ctrl
}
