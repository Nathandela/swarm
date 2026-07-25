package phonecore

// FAILING-FIRST (TDD RED, GG-5) tests for slice S7, PB-STATE-1 and PB-STATE-5: the phone
// core persists and restores EVERY resume-critical coordinate in ONE versioned schema.
//
// Today internal/phonecore performs NO persistence at all (§4.3). Sequencer is a bare
// atomic.Uint64 returning 1 on first call (input.go:33-36) and MailboxReceiver.highest is
// in-memory (crypto/envelope.go:211-216), so one Android process death restarts the phone
// at seq 1 under the same epoch -- every keystroke, take_control, launch and kill refused
// as stale -- and simultaneously resets the phone's replay high-water to zero so a
// retaining relay can redeliver.
//
// THE SEAM THESE TESTS PIN (undefined symbols -> compile-fail RED):
//
//	type Bucket struct{ Sender [8]byte; Epoch uint32 }   // per-(sender,epoch) receive bucket
//	type State struct{ ... }                             // the ONE enumerated schema
//	const StateSchemaVersion = 1
//	type Store interface{ Load() State; Save(State) error }
//	func OpenStore(path, machine string) (Store, error)  // mirrors remotegw.OpenInboundState
//	var ErrCorruptState, ErrFutureSchema error
//	type Config struct{ Dir, Machine string; State Store; Ack Acker }
//	func Resume(cfg Config) (*Core, error)               // the process-start entry point
//	func (*Core) KeyStore() crypto.KeyStore
//	func (*Core) Seq() *Sequencer
//	func (*Core) Router() *MailboxRouter
//	func (*Core) Grants() *crypto.GrantReceiver
//	func (*Core) Ops() *OpQueue
//	func (*Core) State() State
//	func (*Core) Save(State) error
//	func (*Core) RecordOutcome(protocol.Control) error
//	func (*Core) UnresolvedOps() []QueuedOp
//
// internal/remote/crypto is FROZEN. Persistence goes AROUND it through the seams it
// already exposes: KeyStore custody is crypto.NewFileKeyStore / OpenFileKeyStore, the
// receive high-water is replayed in through MailboxReceiver.SeedHighWater, and the grant
// watermark through crypto.NewGrantReceiverAt.
//
// NOTE ON Resume's CONTRACT, deliberately pinned by omission: there is no Close(). An
// Android process is SIGKILLed, never shut down cleanly (PB-STATE-2), so durability may
// not depend on a graceful exit. Every Save must be durable when it returns.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/status"
)

// journalBucket is the machine-sender bucket carrying journal, terminal and reconcile
// frames. replyBucket is the SEPARATE sender-zero bucket carrying command replies
// (command_in.go leaves SenderKeyID zero). Two buckets under one epoch is the
// multi-instance case PB-STATE-1 is keyed for: a SCALAR receive high-water would let the
// reply stream's seq 4 stale-drop the journal stream's seq 4, silently deleting one of
// the two channels -- the exact defect class the reviewer caught in S2 (per-(sender,epoch)
// keying) and S2b (per-session stashing).
func journalBucket(epoch uint32) Bucket { return Bucket{Sender: machineSender, Epoch: epoch} }
func replyBucket(epoch uint32) Bucket   { return Bucket{Sender: [8]byte{}, Epoch: epoch} }

// fullState is a State with EVERY field distinctively non-zero, so a restore that drops
// any one of them is detectable. The round-trip test proves the fixture really is
// exhaustive before comparing, which is what makes it cover fields added later.
func fullState() State {
	var wake crypto.WakeKey
	for i := range wake {
		wake[i] = byte(i + 100)
	}
	return State{
		Machine:             "m1",
		MachineStatic:       bytes.Repeat([]byte{0xA1}, 32),
		MachineSignPub:      bytes.Repeat([]byte{0xB2}, ed25519.PublicKeySize),
		MachineRelayAuthPub: bytes.Repeat([]byte{0xC3}, ed25519.PublicKeySize),
		RoutingID:           "rid-m1",
		EpochID:             7,
		PushToken:           "fcm-token-m1",
		PushPreference:      PushPreference{Alerts: true, Mentions: true},
		ReconciledEpoch:     7,
		Keys:                crypto.EpochKeys{WakeKey: wake, ContentKey: testContentKey()},
		SendSeq:             map[uint32]uint64{7: 512, 6: 1024},
		Receive:             map[Bucket]uint64{journalBucket(7): 42, replyBucket(7): 5},
		GrantEpoch:          7,
		GrantSeq:            2,
		WakeReplay:          91,
		RelayCursor:         17,
		Sessions:            []CachedSession{{SessionID: "m1/s1", Group: status.Group("running"), Present: true}},
		Snapshots:           []Snapshot{{Session: "m1/s1", Lines: []string{"$ ls"}, Cols: 80, Rows: 24}},
		PendingOps:          []QueuedOp{{Op: "kill", SessionID: "m1/s1", Cmd: protocol.DeviceCommandAuth{OperationID: "op-pending"}}},
		OpOutcomes:          map[string]protocol.Control{"op-done": {Op: protocol.OpOK, OperationID: "op-done"}},
		Stale:               map[Bucket]bool{replyBucket(7): true},
	}
}

// TestState_EveryResumeCriticalFieldSurvivesARestart is PB-STATE-1's acceptance criterion
// verbatim: "a test asserts each field survives a restart". The restart is a real one --
// the first Core is dropped and a SECOND Core is built from the directory alone, so
// nothing in memory can supply an answer.
func TestState_EveryResumeCriticalFieldSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	want := fullState()

	// The fixture must exercise EVERY field, or the comparison below silently stops
	// covering fields added after this test was written. Checked here rather than in its
	// own test so there is no assertion in this slice that a state-less implementation
	// could satisfy.
	fv := reflect.ValueOf(want)
	for i := 0; i < fv.NumField(); i++ {
		if fv.Field(i).IsZero() {
			t.Fatalf("fullState() leaves %s at its zero value; PB-STATE-1 enumerates every resume-critical field, so the fixture must set it",
				fv.Type().Field(i).Name)
		}
	}

	c1, err := Resume(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Resume (first launch): %v", err)
	}
	if err := c1.Save(want); err != nil {
		t.Fatalf("Save state: %v", err)
	}

	// RESTART: a fresh Core over the same directory, nothing carried in memory.
	c2, err := Resume(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Resume (second launch): %v", err)
	}
	got := c2.State()

	// Field by field, so a failure names the coordinate that was lost.
	wv, gv := reflect.ValueOf(want), reflect.ValueOf(got)
	for i := 0; i < wv.NumField(); i++ {
		name := wv.Type().Field(i).Name
		if !reflect.DeepEqual(wv.Field(i).Interface(), gv.Field(i).Interface()) {
			t.Errorf("State.%s after restart = %#v; want %#v (resume-critical, PB-STATE-1)",
				name, gv.Field(i).Interface(), wv.Field(i).Interface())
		}
	}

	// The restored coordinates must be WIRED, not merely readable: a Store nothing
	// assembles is the S1b brick re-created at the seam.
	if got := c2.Router().Stale(replyBucket(7)); !got {
		t.Errorf("Router().Stale(reply bucket) = false after restart; want the persisted stale flag to be in force")
	}
	if ops := c2.Ops().Peek(); len(ops) != 1 || ops[0].Cmd.OperationID != "op-pending" {
		t.Errorf("Ops().Peek() after restart = %+v; want the persisted pending op", ops)
	}
	if unresolved := c2.UnresolvedOps(); len(unresolved) != 1 || unresolved[0].Cmd.OperationID != "op-pending" {
		t.Errorf("UnresolvedOps() after restart = %+v; want the pending op whose outcome was never recorded", unresolved)
	}
	if err := c2.RecordOutcome(protocol.Control{Op: protocol.OpOK, OperationID: "op-pending"}); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	if unresolved := c2.UnresolvedOps(); len(unresolved) != 0 {
		t.Errorf("UnresolvedOps() after recording the outcome = %+v; want empty", unresolved)
	}
	c3, err := Resume(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Resume (third launch): %v", err)
	}
	if _, ok := c3.State().OpOutcomes["op-pending"]; !ok {
		t.Errorf("the recorded outcome for op-pending did not survive a restart; PB-STATE-1 persists ops AND their outcomes")
	}
}

// TestState_DeviceKeysSurviveARestart covers the first item of PB-STATE-1's enumeration.
// The keys must be the SAME keys, not merely present: a Resume that quietly regenerates
// material on every launch would pass a "keystore is non-nil" check while every command
// the phone signs is rejected by the daemon registry (which pins the device id to the
// command-signing public key, R-DEV.1) and every grant fails to open.
func TestState_DeviceKeysSurviveARestart(t *testing.T) {
	dir := t.TempDir()

	c1, err := Resume(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Resume (first launch): %v", err)
	}
	msg := []byte("canonical-command-tuple")
	sig := c1.KeyStore().SignCommand(msg)
	cmdPub := append([]byte(nil), c1.KeyStore().CommandSigningPublic()...)
	recipPub := append([]byte(nil), c1.KeyStore().RecipientPublic()...)

	c2, err := Resume(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Resume (second launch): %v", err)
	}
	if !bytes.Equal(cmdPub, c2.KeyStore().CommandSigningPublic()) {
		t.Fatalf("command-signing public key changed across a restart; the device identity the daemon pinned is gone")
	}
	if !bytes.Equal(recipPub, c2.KeyStore().RecipientPublic()) {
		t.Fatalf("sealed-box recipient public key changed across a restart; no epoch grant can be opened")
	}
	if err := crypto.VerifyCommandSig(c2.KeyStore().CommandSigningPublic(), msg, sig); err != nil {
		t.Fatalf("a signature made before the restart no longer verifies after it: %v", err)
	}
}

// TestState_GrantWatermarkRefusesAReplayedGrantAfterRestart is PB-STATE-1's explicitly
// named case: "including a grant-replay-after-restart test". crypto/epoch.go:167 states
// the requirement in its own words -- without a persisted watermark "a relay could replay
// an old correctly-signed grant after a phone/app restart and have it accepted as the
// first grant". NewGrantReceiverAt is the seam; what is missing is any durable source for
// its two arguments.
func TestState_GrantWatermarkRefusesAReplayedGrantAfterRestart(t *testing.T) {
	dir := t.TempDir()

	c1, err := Resume(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Resume (first launch): %v", err)
	}
	machinePub, machinePriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("machine key: %v", err)
	}
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	grant, err := crypto.SealEpochGrant(machinePriv, c1.KeyStore().RecipientPublic(), 7, 2, keys)
	if err != nil {
		t.Fatalf("seal grant: %v", err)
	}
	if _, _, _, err := c1.Grants().Accept(c1.KeyStore(), machinePub, grant); err != nil {
		t.Fatalf("accept the legitimate grant: %v", err)
	}
	st := c1.State()
	st.Machine, st.MachineSignPub, st.EpochID, st.Keys = "m1", machinePub, 7, keys
	st.GrantEpoch, st.GrantSeq = 7, 2
	if err := c1.Save(st); err != nil {
		t.Fatalf("Save state: %v", err)
	}

	// RESTART. The relay retained the grant and re-serves it.
	c2, err := Resume(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Resume (second launch): %v", err)
	}
	if _, _, _, err := c2.Grants().Accept(c2.KeyStore(), machinePub, grant); !errors.Is(err, crypto.ErrGrantReplay) {
		t.Fatalf("replayed grant after restart = %v; want crypto.ErrGrantReplay (the watermark must be persisted, epoch.go:167). A sealed-box open failure here means the DEVICE KEYS did not survive either, which is the same slice's first coordinate", err)
	}
	// The watermark is an anchor, not a wall: the NEXT legitimate grant still lands.
	next, err := crypto.SealEpochGrant(machinePriv, c2.KeyStore().RecipientPublic(), 7, 3, keys)
	if err != nil {
		t.Fatalf("seal next grant: %v", err)
	}
	if _, _, _, err := c2.Grants().Accept(c2.KeyStore(), machinePub, next); err != nil {
		t.Fatalf("grant above the restored watermark = %v; want accepted", err)
	}
}

// stateV1Fixture is the PINNED v1 on-disk blob (§9 rule 4). PB-STATE-5 requires a forward
// migration path, and the only mechanical way to have one is to keep a byte-literal of
// each shipped version loading. When StateSchemaVersion is raised this literal MUST keep
// loading with every v1 coordinate intact -- that is the migration test, and it cannot be
// satisfied by regenerating the fixture from the current code.
const stateV1Fixture = `{
  "schema_version": 1,
  "machine": "m1",
  "routing_id": "rid-m1",
  "epoch_id": 7,
  "send_seq": [{"epoch": 7, "ceiling": 512}],
  "receive": [{"sender": "090a0b0c0d0e0f10", "epoch": 7, "seq": 42}],
  "grant_epoch": 7,
  "grant_seq": 2,
  "wake_replay": 91,
  "relay_cursor": 17
}`

// TestStateStore_PinnedV1FixtureStillLoads is the forward-migration guard (PB-STATE-5).
func TestStateStore_PinnedV1FixtureStillLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "phone-state.json")
	if err := os.WriteFile(path, []byte(stateV1Fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	st, err := OpenStore(path, "m1")
	if err != nil {
		t.Fatalf("OpenStore on the pinned v1 fixture: %v (a shipped schema version must keep loading)", err)
	}
	got := st.Load()
	if got.EpochID != 7 || got.RelayCursor != 17 || got.WakeReplay != 91 {
		t.Errorf("v1 fixture loaded as epoch=%d cursor=%d wake_replay=%d; want 7/17/91", got.EpochID, got.RelayCursor, got.WakeReplay)
	}
	if got.SendSeq[7] != 512 {
		t.Errorf("v1 fixture send-seq ceiling for epoch 7 = %d; want 512", got.SendSeq[7])
	}
	if got.Receive[journalBucket(7)] != 42 {
		t.Errorf("v1 fixture receive high-water for the journal bucket = %d; want 42", got.Receive[journalBucket(7)])
	}
	if got.GrantEpoch != 7 || got.GrantSeq != 2 {
		t.Errorf("v1 fixture grant watermark = (%d,%d); want (7,2)", got.GrantEpoch, got.GrantSeq)
	}
}

// TestStateStore_UnknownFutureSchemaFailsClosed is PB-STATE-5's other half. A blob written
// by a NEWER app build (an upgrade, then a downgrade, or a restored backup) carries
// coordinates this build cannot interpret. Reading it with the current decoder would
// silently drop the fields it does not know -- which for a send-seq ceiling or a receive
// high-water means resetting a replay guard to zero. Refuse it instead.
func TestStateStore_UnknownFutureSchemaFailsClosed(t *testing.T) {
	var blob map[string]any
	if err := json.Unmarshal([]byte(stateV1Fixture), &blob); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	blob["schema_version"] = StateSchemaVersion + 1
	blob["a_field_this_build_has_never_heard_of"] = 1
	data, err := json.Marshal(blob)
	if err != nil {
		t.Fatalf("encode future blob: %v", err)
	}
	path := filepath.Join(t.TempDir(), "phone-state.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write future blob: %v", err)
	}
	if _, err := OpenStore(path, "m1"); !errors.Is(err, ErrFutureSchema) {
		t.Fatalf("OpenStore on schema version %d = %v; want ErrFutureSchema (never a silent reinterpretation)", StateSchemaVersion+1, err)
	}
}

// TestStateStore_CorruptFailsClosedButAForeignMachineIsMerelyEmpty is standing review
// question 2 applied to this slice: does making this durable turn a currently
// self-healing failure PERMANENT? S2 shipped exactly that regression -- its new durable
// checkpoint was bound to no identity, so a regenerated machine identity or a reset relay
// mailbox became a silent permanent brick where both had previously self-healed on
// restart.
//
// The phone has the same two conditions. `swarm remote init` regenerates the machine
// identity (epoch back to 1) and a re-paired phone must work; a state blob describing a
// DIFFERENT machine is not corrupt, it simply describes coordinates that do not exist
// here, so it loads EMPTY rather than erroring or -- far worse -- stale-dropping the
// freshly paired phone's first frames with a retained epoch-1 high-water.
//
// A truly unreadable blob is different: it is refused, because starting from an empty
// checkpoint would leave the replay guard blind (a fresh crypto.MailboxReceiver skips the
// staleness check entirely) and re-open every frame the relay still retains.
func TestStateStore_CorruptFailsClosedButAForeignMachineIsMerelyEmpty(t *testing.T) {
	dir := t.TempDir()

	// (a) Another machine's blob: empty, not an error, so a re-pair is possible.
	foreign := filepath.Join(dir, "foreign.json")
	if err := os.WriteFile(foreign, []byte(stateV1Fixture), 0o600); err != nil {
		t.Fatalf("write foreign blob: %v", err)
	}
	st, err := OpenStore(foreign, "some-other-machine")
	if err != nil {
		t.Fatalf("OpenStore for a different machine = %v; want an EMPTY state, not an error (a bricked re-pair is the S2 B1 regression)", err)
	}
	if got := st.Load(); got.EpochID != 0 || len(got.Receive) != 0 || len(got.SendSeq) != 0 {
		t.Fatalf("another machine's blob loaded as %+v; want empty (its epoch-1 high-water would stale-drop a freshly paired phone)", got)
	}

	// (b) Unversioned and (c) unparseable both fail closed.
	for name, body := range map[string]string{
		"unversioned": `{"machine":"m1","epoch_id":7}`,
		"garbage":     `{"machine":`,
	} {
		p := filepath.Join(dir, name+".json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if _, err := OpenStore(p, "m1"); !errors.Is(err, ErrCorruptState) {
			t.Errorf("OpenStore on a %s blob = %v; want ErrCorruptState (never a silent reset to zero)", name, err)
		}
	}

	// (d) A missing file is first run, not corruption.
	fresh, err := OpenStore(filepath.Join(dir, "absent.json"), "m1")
	if err != nil {
		t.Fatalf("OpenStore on a missing file = %v; want a fresh empty state (first launch)", err)
	}
	if got := fresh.Load(); got.EpochID != 0 {
		t.Fatalf("missing file loaded as %+v; want the zero State", got)
	}
}
