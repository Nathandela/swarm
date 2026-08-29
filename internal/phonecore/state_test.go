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
// already exposes: the receive high-water is replayed in through
// MailboxReceiver.SeedHighWater and the grant watermark through crypto.NewGrantReceiverAt.
// KeyStore custody was crypto.NewFileKeyStore / OpenFileKeyStore when this was written; S14a
// replaced both with a sealed container this package owns (keycustody.go), and the raw
// pre-seam layout those two produce is now REFUSED rather than adopted -- a layout with no
// public half cannot be authenticated (see TestS14A_R3_ARawDeviceKeyBlobIsRefusedNotAdopted).
//
// NOTE ON Resume's CONTRACT, deliberately pinned by omission: there is no Close(). An
// Android process is SIGKILLed, never shut down cleanly (PB-STATE-2), so durability may
// not depend on a graceful exit. Every Save must be durable when it returns.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
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
		MachineName:         "nathans-mbp",
		MachineStatic:       bytes.Repeat([]byte{0xA1}, 32),
		MachineSignPub:      bytes.Repeat([]byte{0xB2}, ed25519.PublicKeySize),
		MachineRelayAuthPub: bytes.Repeat([]byte{0xC3}, ed25519.PublicKeySize),
		RelaySPKIPin:        bytes.Repeat([]byte{0xD4}, sha256.Size),
		RelayTLSPolicy:      "pinned_spki",
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
		RelayIncarnation:    "0123456789abcdef0123456789abcdef",
		RosterRevision:      23,
		Sessions:            []CachedSession{{SessionID: "m1/s1", Group: status.Group("running"), Present: true}},
		Snapshots:           []Snapshot{{Session: "m1/s1", Lines: []string{"$ ls"}, Cols: 80, Rows: 24}},
		PendingOps:          []QueuedOp{{Op: "kill", SessionID: "m1/s1", Cmd: protocol.DeviceCommandAuth{OperationID: "op-pending"}}},
		OpOutcomes:          map[string]protocol.Control{"op-done": {Op: protocol.OpOK, OperationID: "op-done"}},
		Stale:               map[Bucket]bool{replyBucket(7): true},
		StaleStreams:        map[string]bool{StreamJournal: true},
		LastHeardAt:         1753900000000,
		Disowned:            true,
		Items: []Item{{
			SessionID: "m1/s1", ItemID: "itm-1", Cursor: 9, LastCursor: 11, Kind: KindAgentMessage,
			Status: StatusCompleted, TurnID: "turn-1", TSUnixMs: 1753900000000, Text: "on it",
			Body: json.RawMessage(`{"v":1,"item_id":"itm-1","kind":"agent_message"}`),
		}},
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
	//
	// UNEXPORTED fields are custody's own bookkeeping, not coordinates: nothing outside this
	// package can set or read one, so no caller can lose one across a restart and a durable
	// coordinate can never be one. They are skipped here and asserted NOT to survive at the
	// bottom of this test, so the exemption is a stated property rather than a blind spot.
	fv := reflect.ValueOf(want)
	for i := 0; i < fv.NumField(); i++ {
		if !fv.Type().Field(i).IsExported() {
			continue
		}
		if fv.Field(i).IsZero() {
			t.Fatalf("fullState() leaves %s at its zero value; PB-STATE-1 enumerates every resume-critical field, so the fixture must set it",
				fv.Type().Field(i).Name)
		}
	}

	// PB-KEY-9: Resume fails closed with no sealer, and a restart must present the SAME
	// KEKs -- a different KEK is a different device.
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	c1, err := Resume(Config{Dir: dir, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume (first launch): %v", err)
	}
	if err := c1.Save(want); err != nil {
		t.Fatalf("Save state: %v", err)
	}

	// RESTART: a fresh Core over the same directory, nothing carried in memory.
	c2, err := Resume(Config{Dir: dir, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume (second launch): %v", err)
	}
	got := c2.State()

	// Field by field, so a failure names the coordinate that was lost.
	wv, gv := reflect.ValueOf(want), reflect.ValueOf(got)
	for i := 0; i < wv.NumField(); i++ {
		if !wv.Type().Field(i).IsExported() {
			continue
		}
		name := wv.Type().Field(i).Name
		if !reflect.DeepEqual(wv.Field(i).Interface(), gv.Field(i).Interface()) {
			t.Errorf("State.%s after restart = %#v; want %#v (resume-critical, PB-STATE-1)",
				name, gv.Field(i).Interface(), wv.Field(i).Interface())
		}
	}

	// The other half of the exemption above: custody's bookkeeping must NOT come back from
	// disk. purgeGen counts the lock purges THIS process has taken, and a restored one would
	// make a fresh process refuse the first Save of every caller holding a legitimate
	// snapshot.
	//
	// THIS FIXTURE NEVER PURGES, so the stamp is zero on both sides here whatever custody
	// does: the check below states the property, it does not measure it. The measurement is
	// TestS14A_R4_ThePurgeStampDoesNotSurviveARestart, which purges first and then asserts
	// both halves -- the counter, and the Save that must still land once it is gone.
	if got.purgeGen != 0 {
		t.Errorf("State.purgeGen after restart = %d; unexported custody bookkeeping must not be "+
			"persisted -- if it ever is, it belongs in the durable schema and in fullState()", got.purgeGen)
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
	c3, err := Resume(Config{Dir: dir, WakeSealer: wake, ContentSealer: content})
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

	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	c1, err := Resume(Config{Dir: dir, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume (first launch): %v", err)
	}
	msg := []byte("canonical-command-tuple")
	sig, err := c1.KeyStore().SignCommand(msg)
	if err != nil {
		t.Fatalf("SignCommand: %v", err)
	}
	cmdPub := append([]byte(nil), c1.KeyStore().CommandSigningPublic()...)
	recipPub := append([]byte(nil), c1.KeyStore().RecipientPublic()...)

	c2, err := Resume(Config{Dir: dir, WakeSealer: wake, ContentSealer: content})
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

	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	c1, err := Resume(Config{Dir: dir, WakeSealer: wake, ContentSealer: content})
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
	c2, err := Resume(Config{Dir: dir, WakeSealer: wake, ContentSealer: content})
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

// stateV4FixtureKEK is the fixture's PINNED tier KEK. Every other test in this package mints
// a random KEK per run, which is right for them and impossible here: a byte literal cannot
// carry a ciphertext whose key is generated at run time, and the two sealed key fields have
// to be IN the literal or the field-set tie below has a hole exactly where PB-KEY-9 lives.
// So the fixture pins the KEK and the ciphertext together. It seals nothing but this
// fixture's two throwaway epoch keys.
var stateV4FixtureKEK = func() []byte {
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(0x5A + i)
	}
	return kek
}()

// stateV4Fixture is the PINNED v4 on-disk blob: byte-for-byte what this build writes for
// fullState(), with the two epoch keys sealed under stateV4FixtureKEK.
//
// It does a SECOND job the v1 literal cannot. StateSchemaVersion was reverted from 4 to 3 in
// a mutation with the whole repository still green: nothing tied the constant to the field
// set it stamps, so the next durable field added without a bump would ship silently and a
// build one version back would drop it -- which for a send-seq ceiling or a receive
// high-water means a replay guard reset to zero, the exact hole the version exists to close.
// This literal is the tie: it must keep LOADING (which a downgrade of the constant refuses,
// ErrFutureSchema) and it must keep carrying EVERY durable field (which a new field without
// a bump breaks). Raising the version therefore forces a new literal beside this one.
//
// v2 and v3 have no literal, and neither can be produced honestly here. A v2 blob carrying
// either epoch key is REFUSED outright by load() -- its cleartext keys read as sealed blobs
// are the silent reinterpretation the v3 bump exists to prevent -- so the only v2 literal
// that could load is one with the coordinates the bump was about removed, which pins
// nothing. A v3 blob is this literal minus stale_streams alone.
const stateV4Fixture = `{
  "schema_version": 4,
  "machine": "m1",
  "machine_static": "oaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaE=",
  "machine_sign_pub": "srKysrKysrKysrKysrKysrKysrKysrKysrKysrKysrI=",
  "machine_relay_auth_pub": "w8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8M=",
  "routing_id": "rid-m1",
  "epoch_id": 7,
  "push_token": "fcm-token-m1",
  "push_preference": {"alerts": true, "mentions": true},
  "reconciled_epoch": 7,
  "wake_key": "AQIDBAUGBwgJCgsMUjnRQtz97KtVbLtHTf4/N++9li4rV9S30xkVq4qvx6jpwus/7bgP0NJzPAB5ADWz",
  "content_key": "AQIDBAUGBwgJCgsMMVS+L7+Yi842EcQ6LptYUozQ+UNIMrPSsERK9ikKYA2Hx40/KrnqG7mZ4mEWFuJ9",
  "send_seq": [{"epoch": 6, "ceiling": 1024}, {"epoch": 7, "ceiling": 512}],
  "receive": [
    {"sender": "0000000000000000", "epoch": 7, "seq": 5},
    {"sender": "090a0b0c0d0e0f10", "epoch": 7, "seq": 42}
  ],
  "grant_epoch": 7,
  "grant_seq": 2,
  "wake_replay": 91,
  "relay_cursor": 17,
  "sessions": [{"SessionID": "m1/s1", "Group": "running", "Present": true}],
  "snapshots": [{"Session": "m1/s1", "Lines": ["$ ls"], "Cols": 80, "Rows": 24}],
  "pending_ops": [
    {
      "op": "kill",
      "session_id": "m1/s1",
      "cmd": {
        "Action": "", "ContentHash": null, "DeviceID": "",
        "ExpiresAt": "0001-01-01T00:00:00Z", "Machine": "",
        "OperationID": "op-pending", "Session": "", "Sig": ""
      }
    }
  ],
  "op_outcomes": {"op-done": {"op": "ok", "operation_id": "op-done", "endpoint_id": ""}},
  "stale": [{"sender": "0000000000000000", "epoch": 7}],
  "stale_streams": ["journal"]
}`

// stateFixtures is the pinned literal for each version that HAS one, keyed by version. The
// map is what makes the version bump mechanical: TestStateSchemaVersion_IsPinnedToTheDurable
// FieldSet demands an entry for whatever StateSchemaVersion currently is.
// stateV5Fixture is the PINNED v5 blob: byte-for-byte what this build writes for fullState()
// under stateV4FixtureKEK. v5 is S15's tier split -- eight formerly-cleartext fields now live in
// three sealed containers (wake_state, content_kept, content_purgeable), which is exactly why the
// literal must move with the version: a reader that stops understanding one of those containers
// loses the coordinates inside it silently.
const stateV5Fixture = `{"schema_version":5,"machine":"m1","machine_static":"oaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaE=","machine_sign_pub":"srKysrKysrKysrKysrKysrKysrKysrKysrKysrKysrI=","machine_relay_auth_pub":"w8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8M=","routing_id":"rid-m1","epoch_id":7,"push_preference":{"alerts":true,"mentions":true},"reconciled_epoch":7,"wake_key":"RjJsKH8CxMYtEXUWrZFkAkpjXGQ7hIkUPW/rzAbFy0kLLGc2wmXcgEC987gxBteYR/mcVf1u8frgvuO5","content_key":"wgouA8IY8MNVJCdwyCkQjGtUFAc2pamA0Yl3ZIt//gPI2jBrZBOPo2btAzYsCQmQMvcI0IaObW1OzG6U","wake_state":"+jz9/kWzl25ZC56kWyDWjYI2Cb7iWCzpFDWQRjXojrCFrhOviRF5Iz0Iug8GLlbu5J7TqhH0Monc6yyPQA4EIfZOJgdJi5/h1Bw=","content_kept":"a3Bo9WGcGJ8fWEHQzTs+BmpWrOTeQHt9l13CGWZyVwz6Tgm5mOmRO2TX8LJz5iXESvYNj61WUypfDs1Ipxa+fWoeRlJBz4XelSETSfrkkE/YqlHbB6q6A2DlVu9xNaeHrtEp1camraZ2o48SFOXIouQ7Cu4vp75JhiXm4ddash3AyGjnFnUzz3iWsBOzUcir56wxT0wmiPKtepFC3V80BbMs2ToXx/oTISZm4H8RHu54pcnpE9BG4DjvhHWoaaNxlvSAzbIL2PlX/5AdU9vaLRlTl5mp6P1qVfsjHpuOLJ0PHcHcPgXN8Ujh4jS/bu/QaWLF2pYbk/rNJM6zmiNqn/ZGjiBeefcR5NJeK4Ywu1eVd19HBt8PBJg0cJsDYPahjUTQBNvUxEMhchAhd9vl1a/PcehqI7M5hVUXBeBDETYGYhei0X8RmTwrewhE89i6m/2jCknwImtN4TXEO1+B51WXkFJz6stRIA14Tj6N9wKzmdWZGIXrPa2kHPPtoilzyxIUCXuq9mEiDOUSngL0wgKWndGT","content_purgeable":"MmJl/G15AxXKAe9XJNm1g2GO1vdJpo3Re5+1qA91RaRFoVLvNE3dxcO6jI2Dtrhu6PsyktWS9XlCH3rq67zub9t/ILdXWUR2X8USXE7zKckfmJkiRMdGGDQNTH/8TLzx3n1/AEEkIyJkv0HMwQwmN6l40nmATM4kqSSxQhOQ61CVwMJFxwzQDKJmSmAeKkgKYz5Bv7CPb83SJNTSC+ZSYiMJBEf6QijTn9NjNfunzrlcemEgBD9jT8m76KqlwUYBPtewrKqb0KQiqd1Aec6td6gzHnCEvXtyYrYp0RiZzyzckCjXD0omWKQe/9ktQIDb4uD3iq4aDpU=","grant_epoch":7,"grant_seq":2,"relay_cursor":17,"stale":[{"sender":"0000000000000000","epoch":7}],"stale_streams":["journal"]}`

// sealedTags are the durable field names that S15 moved INSIDE a sealed container, so they
// cannot appear as top-level keys in the pinned literal -- that is the point of sealing them.
// They are still tied to the version: the container carrying them is itself a pinned key below,
// and TestStateStore_PinnedSealedFixturesStillLoad opens every pinned version through the fixture
// KEK and compares each restored coordinate against fullState(). So a field dropped from a sealed
// container fails THERE rather than here.
//
// Listing them explicitly, rather than skipping any absent tag, is what preserves the
// BOTH-DIRECTIONS property. If the check simply ignored a missing tag, absence would become
// SELF-JUSTIFYING: the next durable field added without a version bump would not appear in the
// literal, and the test would read that as "must be sealed" and pass -- the very defect this fence
// exists to catch, reintroduced one level up and harder to see. Every declared tag must be
// accounted for in exactly one place: present in the literal, or named here.
var sealedTags = map[string]bool{
	"push_token": true, "wake_replay": true,
	"send_seq": true, "receive": true, "pending_ops": true,
	"sessions": true, "snapshots": true, "op_outcomes": true,
}

// stateV6Fixture is the PINNED v6 blob. v6 adds PushPreference.Version -- the device-supplied
// monotonic counter the machine gates a push_prefs update on (PB-PUSH-10) -- INSIDE the
// existing push_preference object, so the top-level tag set is unchanged and the check below
// would not have noticed it. That is exactly why the version had to move: a build one version
// back drops the counter, it restarts at 1 on the next Save, and the machine then refuses every
// preference update as a replay while the settings screen shows the user's new value.
//
// It is the v5 literal with the stamp raised, which is what this build writes for fullState():
// the field is omitempty and fullState()'s counter is zero, so the two blobs' cleartext differs
// in the stamp alone. The counter's own round trip is pinned separately, by
// TestStateStore_PushPreferenceVersionSurvivesARestart, because no fixture can carry it while
// the v5 literal must go on restoring the same fullState().
const stateV6Fixture = `{"schema_version":6,"machine":"m1","machine_static":"oaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaE=","machine_sign_pub":"srKysrKysrKysrKysrKysrKysrKysrKysrKysrKysrI=","machine_relay_auth_pub":"w8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8M=","routing_id":"rid-m1","epoch_id":7,"push_preference":{"alerts":true,"mentions":true},"reconciled_epoch":7,"wake_key":"RjJsKH8CxMYtEXUWrZFkAkpjXGQ7hIkUPW/rzAbFy0kLLGc2wmXcgEC987gxBteYR/mcVf1u8frgvuO5","content_key":"wgouA8IY8MNVJCdwyCkQjGtUFAc2pamA0Yl3ZIt//gPI2jBrZBOPo2btAzYsCQmQMvcI0IaObW1OzG6U","wake_state":"+jz9/kWzl25ZC56kWyDWjYI2Cb7iWCzpFDWQRjXojrCFrhOviRF5Iz0Iug8GLlbu5J7TqhH0Monc6yyPQA4EIfZOJgdJi5/h1Bw=","content_kept":"a3Bo9WGcGJ8fWEHQzTs+BmpWrOTeQHt9l13CGWZyVwz6Tgm5mOmRO2TX8LJz5iXESvYNj61WUypfDs1Ipxa+fWoeRlJBz4XelSETSfrkkE/YqlHbB6q6A2DlVu9xNaeHrtEp1camraZ2o48SFOXIouQ7Cu4vp75JhiXm4ddash3AyGjnFnUzz3iWsBOzUcir56wxT0wmiPKtepFC3V80BbMs2ToXx/oTISZm4H8RHu54pcnpE9BG4DjvhHWoaaNxlvSAzbIL2PlX/5AdU9vaLRlTl5mp6P1qVfsjHpuOLJ0PHcHcPgXN8Ujh4jS/bu/QaWLF2pYbk/rNJM6zmiNqn/ZGjiBeefcR5NJeK4Ywu1eVd19HBt8PBJg0cJsDYPahjUTQBNvUxEMhchAhd9vl1a/PcehqI7M5hVUXBeBDETYGYhei0X8RmTwrewhE89i6m/2jCknwImtN4TXEO1+B51WXkFJz6stRIA14Tj6N9wKzmdWZGIXrPa2kHPPtoilzyxIUCXuq9mEiDOUSngL0wgKWndGT","content_purgeable":"MmJl/G15AxXKAe9XJNm1g2GO1vdJpo3Re5+1qA91RaRFoVLvNE3dxcO6jI2Dtrhu6PsyktWS9XlCH3rq67zub9t/ILdXWUR2X8USXE7zKckfmJkiRMdGGDQNTH/8TLzx3n1/AEEkIyJkv0HMwQwmN6l40nmATM4kqSSxQhOQ61CVwMJFxwzQDKJmSmAeKkgKYz5Bv7CPb83SJNTSC+ZSYiMJBEf6QijTn9NjNfunzrlcemEgBD9jT8m76KqlwUYBPtewrKqb0KQiqd1Aec6td6gzHnCEvXtyYrYp0RiZzyzckCjXD0omWKQe/9ktQIDb4uD3iq4aDpU=","grant_epoch":7,"grant_seq":2,"relay_cursor":17,"stale":[{"sender":"0000000000000000","epoch":7}],"stale_streams":["journal"]}`

// stateV7Fixture is the PINNED v7 blob: what this build writes for fullState() under
// stateV4FixtureKEK. v7 adds relay_spki_pin (ADR-007 B33/B34), the ONE coordinate a handset
// cannot re-learn without re-pairing -- msg2 is its only channel, because the QR has no room
// for it -- so a build that stops reading it leaves a pinning-only platform unable to dial
// and unable to say why.
const stateV7Fixture = `{"schema_version":7,"machine":"m1","machine_static":"oaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaE=","machine_sign_pub":"srKysrKysrKysrKysrKysrKysrKysrKysrKysrKysrI=","machine_relay_auth_pub":"w8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8M=","relay_spki_pin":"1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NQ=","routing_id":"rid-m1","epoch_id":7,"push_preference":{"alerts":true,"mentions":true},"reconciled_epoch":7,"wake_key":"EBaE3Bdu6rALf4KpmJgkUQ8vhYzj3JKs4Yuotx7gzLUv+Kwvcoi0tGTXcIKc9dZtz5f8xj6IPS45mCDn","content_key":"HAAHxTrbcrBi5XRWefLS34Fo3cP4wAYLtjS9yZgNK9l1tGcL6Dq9HVgF/KQmWx1xW8hyY0/9lmTeI1zz","wake_state":"KtkliB1bq/J5u7IxtFcKrN3dZISvExKteu91vzErxFE/TtU0fFdKBAvuEvWxm1oRdsZegekXuCd5tp2K/1fcjXmNbgMdlUS3f+E=","content_kept":"BcyyV6bCHEHoCl7SFwz9x9QIMmxWPIGWV0lpCgBcK415TC20pUECSPq2grb2II89LaUd2qU66o125A8vWd6QCOZs4A3IcxOuTbUgF13NfXt/cPWpZ0VdAAgrEYlZ1vIlLrQlqzLEcvqLvjLcKqNNQ2Bmyf33um0aGCB8U5cW7PPuHcKKoJ65QBMkml+BHXt8JA50mtS0Ts7uq1gMm6XjayvslgaxHY2WPH1QK911QY76IiEW5m9tpmWLqNGRFfv1Obhk9ztlo2ts5VyWOizaZNJuZ29dBS/bjw8ppHOGvXBRrh23JTAKxh1ZmmsxQF/20QLNouiV/V1qP0zMFuBqzlfuXn9kB8nHZdjb3iwlabWXSQN+rIOyPOjygpyMcNE3j38+PXBuVlx+G+9AwCHTLbVV2TuuY5xabcbbcCiYcVCAOaXbr8Fcl3ooRLjZ7cwl8zhYnJuQEGf5JhjX0suTSqcZd4O4XbHokHcsBbg6Wdn9oQwoLKOjRz7u00kblx1GO2DfiY5AHMyYw8jOpfkfhKAN1u1F","content_purgeable":"FtMqSl/uWSWlKM5N5GWhsvtgpRiiq9mnLBbFxUlcC+nCGkUUD1TQ1YzP7JaMOGDR0o0EcEOBjaD+X3k+jrnkj36Myicx+IID3GsSjEeHL0ANciRIEfPSZN1a6FHeRc8U+0d34P5RCJ+7zGhdb+6MAHM7myzKc6oegCZIvAXy5S+i0Py7umWaz81nnuZlyYUzwbAgOLBuC7HsfgB0CM0ISsqiO7qPqUd639EOGB4whjF9sSz3eN2nv7x162XEvGWc/4GX/Bu8IwtT+qtfB5pqWKwo4oRjc0XS434JxUhXMWGUzMhoJVuodgNUvNQ4kApFWIkbXBulavM=","grant_epoch":7,"grant_seq":2,"relay_cursor":17,"stale":[{"sender":"0000000000000000","epoch":7}],"stale_streams":["journal"]}`

// stateV8Fixture is the PINNED v8 blob: what this build writes for fullState() under
// stateV4FixtureKEK. v8 adds last_heard_at, PB-APP-11's freshness coordinate -- the newest
// authenticated machine timestamp the phone has accepted. A build one version back drops it,
// so the next launch reports a machine that has said nothing for hours as live and renders its
// restored sessions and grids as current, which is the one thing PB-APP-8 forbids and which
// nothing else on the handset can notice: a withholding relay leaves no gap, answers every
// poll, and is itself the source of the only other liveness signal (ADR-007 B121).
const stateV8Fixture = `{"schema_version":8,"machine":"m1","machine_static":"oaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaE=","machine_sign_pub":"srKysrKysrKysrKysrKysrKysrKysrKysrKysrKysrI=","machine_relay_auth_pub":"w8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8M=","relay_spki_pin":"1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NQ=","routing_id":"rid-m1","epoch_id":7,"push_preference":{"alerts":true,"mentions":true},"reconciled_epoch":7,"wake_key":"hPJbGpYBeXu34Ub3WP7aZ1tJB3D9iqeNhT7TTnXIrK8P6sGdWFuy9xwbpRW/FKCR/wpjAXP90vcitbQn","content_key":"AwfT5hR5qtbhxGZGYrz1UpBREKgZZVwBzdZmlhwnh2qOuTgH9Q3eyHgd44QB+lOOeLEmB81RHtQQpwKR","wake_state":"phivocSvyexkNimLclLozrWLhBwtNKBR62NlSbpfQcBdKBhO74q0qkZJcTUcOjWdY0KaiaoE4NxUGeCmL3S3ih0t2q9Gm5yLUlA=","content_kept":"HbPSffp/swWt4sNhdsxM9ee7HiedO/iID0toRsT86lpBWOqe9ML1U9ncv2oRm1n6dG5kuKmBwpsaUzCVfTEQMB+7bXqHFBshprbXQQlTfS5cme/FPEw9bkNyT/+0FYzwPlQ6F+UfWK+WWVG3ZZwRWYQ+zENzfS74A+i9Nzc5iGCEghYBo7lb2tuQKY/DHl+y1Aj6ruvdGoCrNKEi3vn56XKu25rC0Psep1D0zDqM2sbmKP7L8IAy3sC1BkLBvUgM4Vl3OvSHSWOMVXTe4jI/nP1UMeSrMGdhzjshlFVbKHm7wcVZTyuA98abyZboLTCI4tBJroNAYLaP/OfCm7y5r+uIJDtAKUtbEVpzc0txeUY4m2reVLLlhkZFJ4Xg7D8YEJ0+L8yC6qI03YcAp+/938pxOvLPfNvkjjPBv8xvE2tYOWfSSx2fA00nkR7rX+aqsHNE2TaEnWIeFd3hRR5V+4Sqctjw/E2KLxuE75e3DUuG8D8Zn08fap9ES9T8uk5IeOKIPXPt/J3r2tRC7Tr155XP1fBD","content_purgeable":"ERUT6HrGCfcR6YOQS1tqoSkPQeY6gqI2WmJmXriI9Sfe43f1NJvy2BRrb/ZE2HBb/HUd1R79v8BNvfae5lyrH2wfGx3snbMQmwRd84ZwY7wugMa4PQyMaaEVBiPgHYrCwyFejONtYOS9sGZZMd0tnOXwt1XZuSa7JTv6k27VSnM4cq1EJY8jj3zHPW49kICbZPCmSIjYg+7nx38leMjPSb6gcj43WxxRzly9B/ic/7mTRDhjsulfp1NlK4xa0XwiaJEtPIU3ljxSKTTFEsK9/bQflwAYdVVIOsMa3UAWIpBjSZzyxqieOBdYeLukrLTM62iqnjOEVF8=","grant_epoch":7,"grant_seq":2,"relay_cursor":17,"stale":[{"sender":"0000000000000000","epoch":7}],"stale_streams":["journal"],"last_heard_at":1753900000000}`

// stateV9Fixture is the PINNED v9 blob: what this build writes for fullState() under
// stateV4FixtureKEK. v9 adds disowned, PB-KEY-7's record that the OWNER ended this registration
// (ADR-007 B133). A build one version back drops it, and what comes up is agents-tracker-d0b8
// exactly: a phone that believes it is paired, holding no key of either tier, in a four-tab shell
// reading a roster from a machine that deregistered it, with the pairing entry point on the one
// screen the presentation gate will not show. Every other coordinate a pairing pinned survives a
// revoke by design -- one of them is what the blob is FILTERED on -- so this field is the only
// thing that can carry the fact.
const stateV9Fixture = `{"schema_version":9,"machine":"m1","machine_static":"oaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaE=","machine_sign_pub":"srKysrKysrKysrKysrKysrKysrKysrKysrKysrKysrI=","machine_relay_auth_pub":"w8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8M=","relay_spki_pin":"1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NQ=","disowned":true,"routing_id":"rid-m1","epoch_id":7,"push_preference":{"alerts":true,"mentions":true},"reconciled_epoch":7,"wake_key":"9A8+xA7qCVgjeQmNLMlmphMYLglEdfmvDlMC3Z0xyKFQHKICgvCTC6Y/qtXno2uPRkgFAcj9ZXIaKd6+","content_key":"z+o0S1vblLNcliSxEpKgiX7c2HbabSse8BoxlDiXvcUBsQVIo5yIAtZ7+bt6HKWKAQQ3RrqBCUADAwU5","wake_state":"VIaWolwwUV1eygdw8dkwzyTAurcQEJtZd0cqh0nyTblN5TlnH6LyZNgZhI4Mz1zg3YgfiplYONEZzoyBEkzjQXMXggGtV5/F3qE=","content_kept":"WQtT1MV2/CT6tSlSemtn/ksK7RnblCFENQi35DzNuYeGHAwxfn4344S3cXRFqyb4+rPy34Sgihgp8/sCpC4TMtPhen+Ha0l7F9VXfFW5hhNTtvYBUYehUJNue+3/KZLqsq60CeZ9yJDf2yYI9oh6VFpfFx/FMOxQRDNckAdVRW0MRJ1bPXTyFUr3A18kbPzVfiZo8u5xkpE/+iJ0q/skNXdFJ8eXJDDQc99XvzTITGl+uvQL72lE7BfT5gIF9Y3iWUWEPri8HTdoEtKLdaeaW2SZgSEILwa0rOCLVPVFqCAd+JRADLZ20jT7GVwD79w5or5cGXArfNXVw9K9XRL0NCxy55u2gpJZsc7pCre8PCGAMXA9Skwf+GJQq5qek8JKYqZXy302wOyiK8jGvZV1aHxOWOAalSK7dEjYUF07RSKGbg6CfXazHESNF+zbJatzrcQm+h/zfh+tE1VCwSefzHelOAIIlA0juS1Rzel99z18/rAyN7/Np27/nNGUjHpvEmTa6OSsFjA3V4uv/YXtDGK0MY1f","content_purgeable":"dSBXI9e9MQVcUcwpfuPkaRP4vT1ADKOIT+8I7umnrMV77yqworL08cH/jGP3ZGHgMZprckW5eGpZjmJ1cdF/iQ5F+wapazgFwEmCZkHVy/tp9r6lvSt1aviIopZfIaGUzLLRxAWhJIl+O+RMIVyFv1CsXySA8/zlh+llDJ1iq3U3vtIXbnx2wf6AtfV7bMp+pSQIQoW0B9mTF9cKSjGoy+XKOt8j0SzIYK2XVYJJCvOGl/MD0u7QOgSHV02zyQWAYYnumpdUDl2qvPzjERxbnBgHQZPT6/oRtZ2au5w8JImOwfcmxKDRt0KnGd9LSdcmtXZP8APK6/BXjeklrQmD9EGxAQ==","grant_epoch":7,"grant_seq":2,"relay_cursor":17,"stale":[{"sender":"0000000000000000","epoch":7}],"stale_streams":["journal"],"last_heard_at":1753900000000}`

// stateV10Fixture is the PINNED v10 blob: what this build writes for fullState() under
// stateV4FixtureKEK. v10 adds machine_name, the hostname the machine published in the pairing
// payload (agents-tracker-ksvb.1) -- the phone's only source for what a person calls the computer
// it is bound to, since the endpoint id is `ep-` plus four bytes of a hash of a directory.
//
// ITS BUMP IS THE MECHANICAL ONE AND SAYS SO. v7's pin, v8's clock and v9's revoke each named a
// specific brick a downgrade produced; this field's downgrade produces a machine rendered by its
// endpoint id, which is exactly what shipped before it and is not a lie. The version moves anyway
// because the tie between the constant and the durable field set is unconditional on purpose:
// see StateSchemaVersion's own paragraph.
const stateV10Fixture = `{"schema_version":10,"machine":"m1","machine_name":"nathans-mbp","machine_static":"oaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaE=","machine_sign_pub":"srKysrKysrKysrKysrKysrKysrKysrKysrKysrKysrI=","machine_relay_auth_pub":"w8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8M=","relay_spki_pin":"1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NQ=","disowned":true,"routing_id":"rid-m1","epoch_id":7,"push_preference":{"alerts":true,"mentions":true},"reconciled_epoch":7,"wake_key":"PmfgMwX6Uns8zJLuUA9mAtiU5jNx+YsH6Pr8oKj4Kb2wPrM9U3Wu9zHzj05lkCRlpyXqx/YQB4DkbRYn","content_key":"NlluJ6V31SU/CpV62DvokkATJsxs6eForsyF4/V1VA+oYpH/QJXIWIdOXSvYdUQe7huMVPjJvlyja9IC","wake_state":"oFDW8MQcwJa4DD29//q9dhMq8+LApZFF7N+purtiCUBb3SPMEfmPzLj4OZ/mE15bhn886KHvOtpsQIKQ8kVyiu9K2vIn35X/Pug=","content_kept":"oxITQCQRAB5hSLdI+3/9i8IdXjRZ45C3HVuNCsKIL05lg1HDz6rcK3qPu50tiDJccn58TCkpm8Uu758qcF2NBEqOnU0DjeBefiFgyH0Huts8AaECGOmfzjWPpxO6FCPApGaxVVQMLWtLSvUVkqDJfmtzqua7Z3oyQleLKAec17JgDvq1X+WfEh7o8Ck3Bd4AoZ+XcDy681SvkjllmNlZkDFYuYGTmsP8FSxNUciyGL9iSRiADxrkwajmYgh9Eb8tDyzLmislEwUDJeXKbGWOCBT6uS/GuPNIcmQiv54hY84+N/2e07RTNYTOJQmUzqNfY8UL0dyHKmI9ceChmO3grVdCTqMQTFFZtOPa9iQQLcDIO3k5oWWZ5xcsJP3ThE1QViP/QEqoSw1eHgdETiDa5BFAcmouBrXjxzWAaJBEJJAaO5DLRdjis0sYCzlCEW/G7zHFcUK1hO/+k3uO6zNRWXRtrcLnaFLy7lBe2FjVEMuE85jre3CrRqc2a5TXmgoLm1QcKMBqOwj9RWB5jz1sX/yhp5Ha","content_purgeable":"HCJqYRqSKBXvaEQ0MR8py30GK33SvI9Ut5On8ZHctwzlnpMoNDYdS1+cpKcpprGQtST2QeaFMhqJYU2difDE50YE8ShreL8U8JpvB6vqoPKBHi+/dcUacMo8vQcRJ7ZD8w2JJ2y73lgu93ZFGNONvKVUX3rrj6ygl90p6OT/xXxdxLk6RBetRY2QRx9bmAQ4aaZL/E2ywXa1wMx2VHQUrcxu0Y0QxyrrzsBgTJXQ+WImeJmqPg2D2pOkmiAdg2ivC52wKEOoUfLh4j5KI0Da9b4y/nho+zI8LaJd1J72bwIjAns9Y8qeJhzLYEqdSC7tJJDBAVxAiDRVZ/EZxlraAtxnXt0/++Cz7xtfuHM=","grant_epoch":7,"grant_seq":2,"relay_cursor":17,"stale":[{"sender":"0000000000000000","epoch":7}],"stale_streams":["journal"],"last_heard_at":1753900000000}`

// stateV12Fixture is the PINNED v12 blob: what this build writes for fullState() under
// stateV4FixtureKEK. v12 IS THE MERGE OF TWO INDEPENDENT v10s and the literal is where that
// becomes checkable: main minted v10 for machine_name (agents-tracker-ksvb.1) while the
// interaction-program branch minted v10 for items -- the phone's TRANSCRIPT -- and then v11 for
// Item.LastCursor, its per-item fold high water. No shipped build ever wrote the union, so no
// older literal can stand in for it.
//
// It is CAPTURED, not regenerated, and it has to be: persistState seals with a fresh AEAD nonce
// per write (state.go), so the file is not byte-stable from v5 on -- a literal can only ever be a
// recording of one write. The two branch v10 literals are deliberately NOT both kept: they pin
// different field sets under one number, and a map cannot carry that.
//
// What a downgrade costs is the union of both bumps. Dropping machine_name renders the machine by
// its endpoint id, which is what shipped before and is not a lie. Dropping items empties the
// transcript permanently -- the receive high-water is durable, so the relay refuses to redeliver
// the frames that built it (crypto.ErrStaleSeq) -- and takes any pending approval card with it
// (IS-LIFE-3). Dropping LastCursor is worse than either because it is silent: the first repair
// after that launch re-folds records already folded, concatenating each increment twice
// (IS-DELTA-1), and what the user reads is prose, and wrong.
const stateV12Fixture = `{"schema_version":12,"machine":"m1","machine_name":"nathans-mbp","machine_static":"oaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaE=","machine_sign_pub":"srKysrKysrKysrKysrKysrKysrKysrKysrKysrKysrI=","machine_relay_auth_pub":"w8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8M=","relay_spki_pin":"1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NQ=","disowned":true,"routing_id":"rid-m1","epoch_id":7,"push_preference":{"alerts":true,"mentions":true},"reconciled_epoch":7,"wake_key":"foz43PX1gRLgN2TdFCccQ4jELrDM4zKOCrdYzswqIzyo0nueNhfJq362MUODyDfQiGyYlhEMRjK0iOQu","content_key":"/TfZWg3cGcevtGTUXouSdSjnO49NfLdWHply6YPkVbkZ5VN0VyGx2J4w8pmRrlQam2TSZcvGZn1S7vL0","wake_state":"tgH4O3Z2cDqiKVD+ywzQMhNEvGJ1QtZzwqbwBqARsZdYW3Oil/P2efseTrLQig8QpXTg3C19qJ2jJy7cPKDNJCGh44D9xtsPvvM=","content_kept":"yK2Arrg/RiCNM9Qe/kAl0xA0yI2/l5UvLN7hNinbeuBw9DYrqem2Cu9dC25L1bRBGj0Myjjw3HE7NVMosPAsGIJoOfpoOTBOA24UmOkz0aQksCV3Lta1Tk/BF0prIg2h3dnV5dEV8Y8qOaA1SnDPa2ljyZXr7DhcSMEV/YExJlq+aEyQu0iP/hndOVlY2DoLPAq5ZVpxWwTSxSW+Ljspenz5PaGZ7G4VcI1L+kbYPY2I/n14nF6Cb0YjB66ZdcVnHdeCdSfFhr1a2vCSxWdPOyfLc5LsSwnYDS6RZKDXoit1E9nR6vONDjjd0D4E0lIkI9yw0uuTZSXsZKbwroGstkWIxbJvgyLVbhBZECN5vXv8cF8bqJCFTsknCESVZNDH9kzQoQAswgbqqdhoX8pTGW4aoB5PuvadGJGAqeUeKY8kbIMRru7YOj44+OH1BmswEboFDrjw/F4T3JGlbAy4FyyehuLPusUb4U8hU9FuM1da3OLigmEzkZmX4agQaSkl5KtAi3UY4mi/sFsruXk65DL+MzDz","content_purgeable":"MHJeU/Egku4tPsnRHLmBSRcCw9mxRQI82BUJuxY1d5Lm/mLx5DV8/TusrsZTDzyAybStYY3Lg6nkLI99bbd45NGdGDAR7gQJG7uKjwBKSC7w6y73y7fIHRL+C2urqBLZkf3c0mQGg+hOEKhQJde/rl+5EY24B5ORWncZv56h2Nir2X09zfgfsKa0WSgES1nZtBY3TUgQgr4/HRz63OJr0hMLTTKcZshjv7oh82njhYkOBUdZAr4+iYGuiPjzchs8+2/4gFFkE5mW0ipNIZAPbNg/yrByuj+01zTvPZ94tE9J9q+wqVbKe8PVKoMFpV3FYEK2k48AJp06qM350JpdCpCgm1x0NlfHdD/mx9opMGnn/uJk9A2VyP/AIAXDOE3qJ2DYtoEjd6R8e/JUdy0w5n1AxWa2VDXuehsVYUxiIzWKxaHeHKcH4MqkHaH2kYKwcNawonMfCGugsNo7fVYJDDNBgT/Kcj2Gl3+sP9CRVQCiHa0ZbqRI2PLlE97b1Rqwioomwhny7y1kBk9VR49X+cezssvyjl3SeIVA0K7S3wiPrJ9bV2U/GfrFFoqtt0kDkpCTSdzpdXZoOySGUQCD7b72oZVEHg181171615jluZC9I8efDfFv+BPYvNgnASg0rPduI9ynGP71bRN5Y5j4VQnWFlTpZLQgb82/ErlW8+ofXPpF0M6ZxrOBzSbhABeyipmL+23C/l1tuEetinkBlTEQNbmpdDlmmyxSmGAemaoNQyfLs3pHYr6nTh1BaA24+KJFvd7bA==","grant_epoch":7,"grant_seq":2,"relay_cursor":17,"stale":[{"sender":"0000000000000000","epoch":7}],"stale_streams":["journal"],"last_heard_at":1753900000000}`

// stateV13Fixture is the PINNED v13 blob: what this build writes for fullState() under
// stateV4FixtureKEK. v13 adds relay_tls_policy (ADR-016 W1), independent of relay_spki_pin
// -- a build one version back drops the policy and reads the retained pin as the whole of
// verification again, exactly the defect W3's scoping exists to retire.
const stateV13Fixture = `{"schema_version":13,"machine":"m1","machine_name":"nathans-mbp","machine_static":"oaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaE=","machine_sign_pub":"srKysrKysrKysrKysrKysrKysrKysrKysrKysrKysrI=","machine_relay_auth_pub":"w8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8M=","relay_spki_pin":"1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NQ=","relay_tls_policy":"pinned_spki","disowned":true,"routing_id":"rid-m1","epoch_id":7,"push_preference":{"alerts":true,"mentions":true},"reconciled_epoch":7,"wake_key":"wxIn8aKOxBJMwj19x7gRj24/V2u9zReMnuX7rnMjma/ErTIK7/FhYC1GbXhanGjSYOdoz5xhO5x5JWgP","content_key":"2OqBct584ZZJTQs75xBA6/3R1jGyPO7vMA/Tq9ihIZofvhQg7DUTNFuYPqsMS0ZLs+47HpJ8UCZCQnUp","wake_state":"6tzAvVKLUGaFYZyF91njodGhY9UfsJXZh/9DtCnSCkoZ2SiYXFcDSJz2L7sGPYjS/cK7HFTeiwBj809In1DrVTabgi04VfTkGag=","content_kept":"ob6D9eQNRVmkgHh4jphA4tsgH3BpOD1BkzIjbmf1zZI/MPqimovt71IX+S3BjBjiuOBvlVTm54YuFQSki6KRfrNt1mR5DI9Dd/fEcSbZkqTrAMNYO16rfQxMz+7n3Ij1SjoGN/kUH9b8VR09EFZasBs3X/mZzSHk0KIypYl9BkDBMWfJglPnyuWZ5acsAuNaPGEb+lHS5puEC6gUzEmm2O1dynDa+TvbZ0+QK3EohpL+sDvnEUVyxC4Jr/31MdMuNzBOnXJI9GObWep7XyB4wRTY8Yxu3L4TK3rw6qfUKIXCj4KcD4xRl3nO1Kf4pdA2j5DrBfM+mbLqVatXaK7gVrrrNGwzMmWR6Ah/F1sJtOXv2UJUJWR8zPfQirYYjwjmH9NDf9beALB8XyQWP/uoCPJvWFPRZw78+2g2Xr9Eo27xDTBrcMsG4NHDm5fwDt2OrL0r6fggV9K2qraNbuX4Spo7XLk9tkdgNymv1lwJ5fq0OSpy6w0YFxrr03y+k2ZCZZt9yQYfcKzjI8eaOkat/kIs/l5w","content_purgeable":"RpMmNMQRtQ3ELy5wWO1josrrF6G0rD1D+HFHOZL5IDIzfjiCdC7R2AZvtJgBbyK0yV/n23Z65rKHXog35poDU20moMXZpWp3woEnmSfOV8+K8Egmpuef3ESZzxvyrE8Wr8bMr1T0XiyaRn4Iiz6rMvLxsNzb+uri3PzXqBFY+3ixq4EkhJkvsO+XA+Go1/FK9efZ8pdqsM92kZ2i/cB5xU9Xzfq2UyPy1N6VpTSkvY71KNTEuaXCvdp8t9eLX7bBrJyZmR4Y6xFDUprN1okGlCJ0baYocLP3uUVvA84WYTJftie4dbgv4IEspoKVLj8fM2CzYemiixr2L9cX0hBfIMCGN4PcOi5wBP+djJ2nE8VVsOTdNwrF8vpI1Zjvt3KvpKfSVaRMCdM8l0GoTPoZ6IYjId81ltyHGbVtCDpiVbxkdkEXlQiS1iA1jw3T0jLrYBLdMLk0RNt0JMIOnZz6Zgskahru6vMrB7TjZl+DWRZR4N47UFFbMofkY45JTFfjPc4fhXrVsc3heRZOpXhEMASrttIor6/tMf5SX5f5SHHAlfQTxKJvGVJtybb2PPJ2PqQ09fsE6bW+ihoLVjwh58aShWN2/Oh0e+QXmMOS7tCg+HaVChnRAKyyi4YqQIzW5AXPnbanTAWoANDqzWOeea9tqwVTCDy2Rten2s1lQy1ihpS/+9MSVmQu/y9ZapkP5q6hQL9fzoov0XvNPyrI9hXlVru2+fEING/kzSwMvOc+NRiMJOXd0Uhw2BSlse35A/vBg8d/Jw==","grant_epoch":7,"grant_seq":2,"relay_cursor":17,"stale":[{"sender":"0000000000000000","epoch":7}],"stale_streams":["journal"],"last_heard_at":1753900000000}`

// stateV14Fixture adds the durable proof that an authoritative roster, including an empty one,
// has committed. It is cleartext because it is a synchronization coordinate, not roster content.
const stateV14Fixture = `{"schema_version":14,"machine":"m1","machine_name":"nathans-mbp","machine_static":"oaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaE=","machine_sign_pub":"srKysrKysrKysrKysrKysrKysrKysrKysrKysrKysrI=","machine_relay_auth_pub":"w8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8M=","relay_spki_pin":"1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NQ=","relay_tls_policy":"pinned_spki","disowned":true,"routing_id":"rid-m1","epoch_id":7,"push_preference":{"alerts":true,"mentions":true},"reconciled_epoch":7,"wake_key":"wxIn8aKOxBJMwj19x7gRj24/V2u9zReMnuX7rnMjma/ErTIK7/FhYC1GbXhanGjSYOdoz5xhO5x5JWgP","content_key":"2OqBct584ZZJTQs75xBA6/3R1jGyPO7vMA/Tq9ihIZofvhQg7DUTNFuYPqsMS0ZLs+47HpJ8UCZCQnUp","wake_state":"6tzAvVKLUGaFYZyF91njodGhY9UfsJXZh/9DtCnSCkoZ2SiYXFcDSJz2L7sGPYjS/cK7HFTeiwBj809In1DrVTabgi04VfTkGag=","content_kept":"ob6D9eQNRVmkgHh4jphA4tsgH3BpOD1BkzIjbmf1zZI/MPqimovt71IX+S3BjBjiuOBvlVTm54YuFQSki6KRfrNt1mR5DI9Dd/fEcSbZkqTrAMNYO16rfQxMz+7n3Ij1SjoGN/kUH9b8VR09EFZasBs3X/mZzSHk0KIypYl9BkDBMWfJglPnyuWZ5acsAuNaPGEb+lHS5puEC6gUzEmm2O1dynDa+TvbZ0+QK3EohpL+sDvnEUVyxC4Jr/31MdMuNzBOnXJI9GObWep7XyB4wRTY8Yxu3L4TK3rw6qfUKIXCj4KcD4xRl3nO1Kf4pdA2j5DrBfM+mbLqVatXaK7gVrrrNGwzMmWR6Ah/F1sJtOXv2UJUJWR8zPfQirYYjwjmH9NDf9beALB8XyQWP/uoCPJvWFPRZw78+2g2Xr9Eo27xDTBrcMsG4NHDm5fwDt2OrL0r6fggV9K2qraNbuX4Spo7XLk9tkdgNymv1lwJ5fq0OSpy6w0YFxrr03y+k2ZCZZt9yQYfcKzjI8eaOkat/kIs/l5w","content_purgeable":"RpMmNMQRtQ3ELy5wWO1josrrF6G0rD1D+HFHOZL5IDIzfjiCdC7R2AZvtJgBbyK0yV/n23Z65rKHXog35poDU20moMXZpWp3woEnmSfOV8+K8Egmpuef3ESZzxvyrE8Wr8bMr1T0XiyaRn4Iiz6rMvLxsNzb+uri3PzXqBFY+3ixq4EkhJkvsO+XA+Go1/FK9efZ8pdqsM92kZ2i/cB5xU9Xzfq2UyPy1N6VpTSkvY71KNTEuaXCvdp8t9eLX7bBrJyZmR4Y6xFDUprN1okGlCJ0baYocLP3uUVvA84WYTJftie4dbgv4IEspoKVLj8fM2CzYemiixr2L9cX0hBfIMCGN4PcOi5wBP+djJ2nE8VVsOTdNwrF8vpI1Zjvt3KvpKfSVaRMCdM8l0GoTPoZ6IYjId81ltyHGbVtCDpiVbxkdkEXlQiS1iA1jw3T0jLrYBLdMLk0RNt0JMIOnZz6Zgskahru6vMrB7TjZl+DWRZR4N47UFFbMofkY45JTFfjPc4fhXrVsc3heRZOpXhEMASrttIor6/tMf5SX5f5SHHAlfQTxKJvGVJtybb2PPJ2PqQ09fsE6bW+ihoLVjwh58aShWN2/Oh0e+QXmMOS7tCg+HaVChnRAKyyi4YqQIzW5AXPnbanTAWoANDqzWOeea9tqwVTCDy2Rten2s1lQy1ihpS/+9MSVmQu/y9ZapkP5q6hQL9fzoov0XvNPyrI9hXlVru2+fEING/kzSwMvOc+NRiMJOXd0Uhw2BSlse35A/vBg8d/Jw==","grant_epoch":7,"grant_seq":2,"relay_cursor":17,"roster_revision":23,"stale":[{"sender":"0000000000000000","epoch":7}],"stale_streams":["journal"],"last_heard_at":1753900000000}`

// stateV15Fixture adds the relay mailbox incarnation beside its durable cursor. The new
// coordinate is cleartext synchronization metadata, so v15 deliberately retains v14's
// sealed tier bytes and adds only the top-level field.
var stateV15Fixture = func() string {
	fixture := strings.Replace(stateV14Fixture, `"schema_version":14`, `"schema_version":15`, 1)
	return strings.Replace(fixture, `"relay_cursor":17`, `"relay_cursor":17,"relay_incarnation":"0123456789abcdef0123456789abcdef"`, 1)
}()

var stateFixtures = map[int]string{
	1:  stateV1Fixture,
	4:  stateV4Fixture,
	5:  stateV5Fixture,
	6:  stateV6Fixture,
	7:  stateV7Fixture,
	8:  stateV8Fixture,
	9:  stateV9Fixture,
	10: stateV10Fixture,
	12: stateV12Fixture,
	13: stateV13Fixture,
	14: stateV14Fixture,
	15: stateV15Fixture,
}

// TestStateStore_PinnedV4FixtureStillLoads is the current version's migration guard, and the
// half that catches a DOWNGRADE of the constant: a build stamping 3 refuses this blob with
// ErrFutureSchema before a single coordinate is read.
//
// It restores through the fixture's PINNED KEK -- a real AEAD, the same s14aSealer every
// other test here uses, over a key that is a literal rather than fresh entropy. That is what
// lets the two sealed fields live in the byte literal at all.
func TestStateStore_PinnedSealedFixturesStillLoad(t *testing.T) {
	// EVERY pinned version from v4 on, not just the newest -- this is the forward-migration path:
	// a v4 blob must still yield every coordinate it carries after those fields moved inside
	// sealed containers. Iterating the map is what makes the sealed-tag exemption above honest --
	// a field dropped from a container has no top-level tag to miss, and fails HERE instead.
	//
	// The comparison is version-aware, and it MUST be. An earlier version of this test compared
	// every fixture against the CURRENT fullState(), which is right only while every pinned version
	// is current: the moment a durable field is added, an old blob cannot restore a coordinate that
	// did not exist when it was written, and the only ways to go green are to splice the key into a
	// literal that never carried it -- falsifying the very artifact that proves migration works --
	// or to weaken this guard. An implementer hit exactly that wall and correctly refused both.
	//
	// So: the CURRENT version must equal fullState() exactly, and an OLDER version must load and
	// restore every coordinate ITS OWN literal carries. A field added later is legitimately absent
	// from an older blob; a field the old blob carries and this build drops is the defect.
	for _, version := range sortedFixtureVersions() {
		if version < 4 {
			continue // v1 predates the KEK and has its own test below
		}
		version := version
		t.Run("v"+strconv.Itoa(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "phone-state.json")
			if err := os.WriteFile(path, []byte(stateFixtures[version]), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			kek := &s14aSealer{kek: stateV4FixtureKEK}
			st, err := OpenStore(path, "m1", kek, kek)
			if err != nil {
				t.Fatalf("OpenStore on the pinned v%d fixture: %v (a shipped schema version must keep "+
					"loading; if StateSchemaVersion was lowered, this blob is now from the future)", version, err)
			}

			// Which coordinates should this literal restore? Exactly the ones it carries: a
			// top-level json key, or a field sealed into a container the literal has.
			var blob map[string]any
			if err := json.Unmarshal([]byte(stateFixtures[version]), &blob); err != nil {
				t.Fatalf("decode the pinned v%d fixture: %v", version, err)
			}
			carries := func(tag string) bool {
				if _, ok := blob[tag]; ok {
					return true
				}
				if !sealedTags[tag] {
					return false
				}
				for _, container := range []string{"wake_state", "content_kept", "content_purgeable"} {
					if _, ok := blob[container]; ok {
						return true
					}
				}
				return false
			}

			want, got := fullState(), st.Load()
			wv, gv := reflect.ValueOf(want), reflect.ValueOf(got)
			rt := reflect.TypeOf(stateFile{})
			tagOf := map[string]string{}
			for i := 0; i < rt.NumField(); i++ {
				tag, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
				tagOf[rt.Field(i).Name] = tag
			}
			for i := 0; i < wv.NumField(); i++ {
				name := wv.Type().Field(i).Name
				if !wv.Type().Field(i).IsExported() {
					continue
				}
				if version != StateSchemaVersion && !carries(tagOf[name]) {
					continue // added after this version was pinned; legitimately absent
				}
				if !reflect.DeepEqual(wv.Field(i).Interface(), gv.Field(i).Interface()) {
					t.Errorf("the pinned v%d fixture restored State.%s = %#v; want %#v. A coordinate the "+
						"literal carries and this build no longer reads is a durable field dropped without "+
						"a schema bump", version, name, gv.Field(i).Interface(), wv.Field(i).Interface())
				}
			}
		})
	}
}

// TestStateSchemaVersion_IsPinnedToTheDurableFieldSet is F2 itself: the constant and the
// field set it stamps must move together. Nothing connected them, so both mutations were
// silent -- lowering the constant, and adding a durable field without raising it.
//
// The tie is mechanical in BOTH directions. A field added to stateFile is missing from the
// pinned literal for the current version, so it fails here until the version is raised and a
// literal for the new one is pinned; a field REMOVED leaves a key in the pinned literal this
// build can no longer decode, which is a coordinate silently dropped on every load of an
// existing blob.
func TestStateSchemaVersion_IsPinnedToTheDurableFieldSet(t *testing.T) {
	fixture, ok := stateFixtures[StateSchemaVersion]
	if !ok {
		t.Fatalf("StateSchemaVersion is %d and stateFixtures pins no literal for it (it pins %v). "+
			"PB-STATE-5's forward-migration path is only mechanical if every shipped version keeps "+
			"a byte-literal that must go on loading, so raising the version means pinning the blob "+
			"the new version writes", StateSchemaVersion, sortedFixtureVersions())
	}
	var blob map[string]any
	if err := json.Unmarshal([]byte(fixture), &blob); err != nil {
		t.Fatalf("decode the pinned v%d fixture: %v", StateSchemaVersion, err)
	}

	tags := map[string]bool{}
	rt := reflect.TypeOf(stateFile{})
	for i := 0; i < rt.NumField(); i++ {
		tag, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if tag == "" || tag == "-" {
			t.Fatalf("stateFile.%s has no json tag; a durable field's on-disk name must be explicit",
				rt.Field(i).Name)
		}
		tags[tag] = true
		if _, present := blob[tag]; !present && !sealedTags[tag] {
			t.Errorf("the durable field %q is absent from the pinned v%d fixture. Either it is NEW "+
				"-- in which case StateSchemaVersion must be raised and a literal for the new version "+
				"pinned, or a build one version back drops it silently and a replay guard comes back "+
				"as zero -- or the literal has drifted from what this build writes",
				tag, StateSchemaVersion)
		}
	}
	for name := range blob {
		if !tags[name] {
			t.Errorf("the pinned v%d fixture carries %q and stateFile no longer has a field for it. "+
				"Removing a durable coordinate is a schema change too: every existing blob still "+
				"carries it, and this build now drops it on load", StateSchemaVersion, name)
		}
	}
}

func sortedFixtureVersions() []int {
	out := make([]int, 0, len(stateFixtures))
	for v := range stateFixtures {
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

// TestStateStore_PinnedV1FixtureStillLoads is the forward-migration guard (PB-STATE-5).
func TestStateStore_PinnedV1FixtureStillLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "phone-state.json")
	if err := os.WriteFile(path, []byte(stateV1Fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	st, err := OpenStore(path, "m1", s14aNewSealer(t), s14aNewSealer(t))
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
	if _, err := OpenStore(path, "m1", s14aNewSealer(t), s14aNewSealer(t)); !errors.Is(err, ErrFutureSchema) {
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
	st, err := OpenStore(foreign, "some-other-machine", s14aNewSealer(t), s14aNewSealer(t))
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
		if _, err := OpenStore(p, "m1", s14aNewSealer(t), s14aNewSealer(t)); !errors.Is(err, ErrCorruptState) {
			t.Errorf("OpenStore on a %s blob = %v; want ErrCorruptState (never a silent reset to zero)", name, err)
		}
	}

	// (d) A missing file is first run, not corruption.
	fresh, err := OpenStore(filepath.Join(dir, "absent.json"), "m1", s14aNewSealer(t), s14aNewSealer(t))
	if err != nil {
		t.Fatalf("OpenStore on a missing file = %v; want a fresh empty state (first launch)", err)
	}
	if got := fresh.Load(); got.EpochID != 0 {
		t.Fatalf("missing file loaded as %+v; want the zero State", got)
	}
}

func TestStateStore_MalformedRelayIncarnationFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "malformed-incarnation.json")
	body := `{"schema_version":15,"machine":"m1","relay_cursor":3,"relay_incarnation":"NOT-CANONICAL"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path, "m1", s14aNewSealer(t), s14aNewSealer(t)); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("OpenStore(malformed relay incarnation) = %v, want ErrCorruptState", err)
	}
}
