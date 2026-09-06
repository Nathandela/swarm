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
//	type Store interface{ Load() State; Save(State) error; ...custody verbs... }
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
	"strconv"
	"strings"
	"testing"
	"time"

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
	st := State{
		Machine:                    "m1",
		MachineName:                "nathans-mbp",
		MachineStatic:              bytes.Repeat([]byte{0xA1}, 32),
		MachineSignPub:             bytes.Repeat([]byte{0xB2}, ed25519.PublicKeySize),
		MachineRelayAuthPub:        bytes.Repeat([]byte{0xC3}, ed25519.PublicKeySize),
		OperatorNamespace:          "owner",
		RelaySPKIPin:               bytes.Repeat([]byte{0xD4}, sha256.Size),
		RelayTLSPolicy:             "pinned_spki",
		RoutingID:                  "rid-m1",
		EpochID:                    7,
		PushToken:                  "fcm-token-m1",
		PushPreference:             PushPreference{Alerts: true, Mentions: true},
		ReconciledEpoch:            7,
		Keys:                       crypto.EpochKeys{WakeKey: wake, ContentKey: testContentKey()},
		SendSeq:                    map[uint32]uint64{7: 512, 6: 1024},
		Receive:                    map[Bucket]uint64{journalBucket(7): 42, replyBucket(7): 5},
		GrantEpoch:                 7,
		GrantSeq:                   2,
		WakeReplay:                 91,
		RelayCursor:                17,
		RelayIncarnation:           "AAAAAAAAAAAAAAAAAAAAAA",
		DiscardRecoveryGeneration:  3,
		DiscardRecoveryCompleted:   2,
		DiscardRecoveryToken:       "fedcba9876543210fedcba9876543210",
		DiscardRecoveryIncarnation: "AAAAAAAAAAAAAAAAAAAAAA",
		DiscardRecoveryCursor:      18,
		RosterRevision:             23,
		Sessions:                   []CachedSession{{SessionID: "m1/s1", Group: status.Group("running"), Present: true}},
		Snapshots:                  []Snapshot{{Session: "m1/s1", Lines: []string{"$ ls"}, Cols: 80, Rows: 24}},
		PendingOps:                 []QueuedOp{{Op: "kill", SessionID: "m1/s1", Cmd: protocol.DeviceCommandAuth{OperationID: "op-pending"}}},
		PendingPublications: []PendingPublication{
			{
				LogicalID: "logical-pending", OperationID: "op-publication", Kind: PublicationComposer,
				SessionID: "m1/s1", SessionInstance: "instance-1", ExpectedTurn: "turn-1", Text: "ship it",
				Machine: "m1", EpochID: 7, Target: "rid-m1", AuthorityPub: bytes.Repeat([]byte{0xC3}, ed25519.PublicKeySize), Phase: PublicationSealed,
				Sequence: 41, Envelope: []byte("exact-envelope"), CreatedAt: time.Unix(1_700_000_000, 0),
				Command: protocol.DeviceCommandAuth{
					Action: protocol.ActionComposerSend, Machine: "m1", Session: "m1/s1",
					OperationID: "op-publication", ExpiresAt: time.Unix(1_700_000_060, 0),
				},
				Composer: &protocol.ComposerSendReq{
					Session: "m1/s1", SessionInstance: "instance-1", ExpectedTurn: "turn-1", Text: "ship it",
				},
			},
			{
				LogicalID: "logical-result", OperationID: "op-result", Kind: PublicationComposer,
				SessionID: "m1/s1", SessionInstance: "instance-1", ExpectedTurn: "turn-1", Text: "result",
				Machine: "m1", EpochID: 7, Target: "rid-m1", AuthorityPub: bytes.Repeat([]byte{0xC3}, ed25519.PublicKeySize),
				Phase: PublicationTerminal, TerminalCode: "policy", ResultOrder: 29, CreatedAt: time.Unix(1_700_000_001, 0),
				Command: protocol.DeviceCommandAuth{
					Action: protocol.ActionComposerSend, Machine: "m1", Session: "m1/s1",
					OperationID: "op-result", ExpiresAt: time.Unix(1_700_000_061, 0),
				},
				Composer: &protocol.ComposerSendReq{
					Session: "m1/s1", SessionInstance: "instance-1", ExpectedTurn: "turn-1", Text: "result",
				},
			},
		},
		OpOutcomes: map[string]protocol.Control{
			"op-done":   {Op: protocol.OpOK, OperationID: "op-done"},
			"op-result": {Op: protocol.OpError, OperationID: "op-result", ErrorCode: protocol.CodePolicy},
		},
		Stale:        map[Bucket]bool{replyBucket(7): true},
		StaleStreams: map[string]bool{StreamJournal: true},
		LastHeardAt:  1753900000000,
		Disowned:     true,
		Items: []Item{{
			SessionID: "m1/s1", ItemID: "itm-1", Cursor: 9, LastCursor: 11, Kind: KindAgentMessage,
			Status: StatusCompleted, TurnID: "turn-1", TSUnixMs: 1753900000000, Text: "on it",
			Body: json.RawMessage(`{"v":1,"item_id":"itm-1","kind":"agent_message"}`),
		}},
		HistoryFloor:  map[string]bool{"m1/history-floor": true},
		HistoryCapped: map[string]bool{"m1/history-capped": true},
	}
	st.pairingPushOwned = EncodePushAddress(PushAddress{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
	})
	st.phoneBinding = PhoneBinding{
		Home:       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		PhoneRID:   "0123456789abcdef0123456789abcdef",
		Generation: 7,
		Active:     true,
	}
	return st
}

// durableStateFieldEqual keeps the restart/schema guards byte-strict for every durable
// coordinate except time.Time's process-local representation. encoding/json preserves a
// time's instant and numeric offset, but it cannot preserve its *time.Location identity or
// monotonic clock reading. Those are not state-file coordinates: publication expiry and
// command signatures both consume the instant (Before/Unix), so compare the two persisted
// publication timestamps in the canonical location while leaving every other bit exact.
func durableStateFieldEqual(name string, want, got any) bool {
	if name != "PendingPublications" {
		return reflect.DeepEqual(want, got)
	}
	wantPublications, wantOK := want.([]PendingPublication)
	gotPublications, gotOK := got.([]PendingPublication)
	if !wantOK || !gotOK {
		return false
	}
	canonicalTimes := func(in []PendingPublication) []PendingPublication {
		if in == nil {
			return nil
		}
		out := clonePendingPublications(in)
		for i := range out {
			out[i].CreatedAt = out[i].CreatedAt.UTC()
			out[i].Command.ExpiresAt = out[i].Command.ExpiresAt.UTC()
		}
		return out
	}
	return reflect.DeepEqual(canonicalTimes(wantPublications), canonicalTimes(gotPublications))
}

func TestDurableStateFieldEqual_PendingPublicationTimesAreInstantsAcrossZones(t *testing.T) {
	want := clonePendingPublications(fullState().PendingPublications)
	for i := range want {
		want[i].CreatedAt = want[i].CreatedAt.UTC()
		want[i].Command.ExpiresAt = want[i].Command.ExpiresAt.UTC()
	}
	got := clonePendingPublications(want)
	zones := []*time.Location{
		time.FixedZone("UTC-07", -7*60*60),
		time.FixedZone("UTC+05:45", 5*60*60+45*60),
	}
	for i := range got {
		got[i].CreatedAt = got[i].CreatedAt.In(zones[i%len(zones)])
		got[i].Command.ExpiresAt = got[i].Command.ExpiresAt.In(zones[(i+1)%len(zones)])
	}

	if !durableStateFieldEqual("PendingPublications", want, got) {
		t.Fatal("equal publication instants in UTC and fixed non-UTC locations compare unequal")
	}
	got[0].CreatedAt = got[0].CreatedAt.Add(time.Nanosecond)
	if durableStateFieldEqual("PendingPublications", want, got) {
		t.Fatal("different publication instants compare equal")
	}
	got = clonePendingPublications(want)
	got[0].Text = "different"
	if durableStateFieldEqual("PendingPublications", want, got) {
		t.Fatal("a non-time publication mutation compares equal")
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
	// UNEXPORTED fields are custody coordinates or trust-bound authorities callers must not
	// manufacture through Save/Mutate. They are skipped by this generic fixture and covered
	// by their dedicated transition tests; the authenticated profile's populated restart path
	// is driven through Reconcile in TestLastProfileAndComposerSurviveProcessDeath.
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
		if !durableStateFieldEqual(name, wv.Field(i).Interface(), gv.Field(i).Interface()) {
			t.Errorf("State.%s after restart = %#v; want %#v (resume-critical, PB-STATE-1)",
				name, gv.Field(i).Interface(), wv.Field(i).Interface())
		}
	}

	// purgeGen must NOT come back from disk. It counts the lock purges THIS process has taken,
	// and a restored one would
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

// stateCurrentFixtureKEK is the pinned tier KEK for the synthetic current-schema fixture.
var stateCurrentFixtureKEK = func() []byte {
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(0x5A + i)
	}
	return kek
}()

// stateV26Fixture is the byte-literal current relay-v2 checkpoint. It keeps the schema
// stamp tied to every top-level field and sealed-container coordinate this build writes.
const stateV26Fixture = `{"schema_version":26,"machine":"m1","machine_name":"nathans-mbp","machine_static":"oaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaE=","machine_sign_pub":"srKysrKysrKysrKysrKysrKysrKysrKysrKysrKysrI=","machine_relay_auth_pub":"w8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8PDw8M=","operator_namespace":"owner","relay_spki_pin":"1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NTU1NQ=","relay_tls_policy":"pinned_spki","disowned":true,"routing_id":"rid-m1","epoch_id":7,"push_preference":{"alerts":true,"mentions":true},"reconciled_epoch":7,"last_profile":null,"pairing_push_owned":"AQIDBAUGBwgJCgsMDQ4PEA","wake_key":"wxIn8aKOxBJMwj19x7gRj24/V2u9zReMnuX7rnMjma/ErTIK7/FhYC1GbXhanGjSYOdoz5xhO5x5JWgP","content_key":"2OqBct584ZZJTQs75xBA6/3R1jGyPO7vMA/Tq9ihIZofvhQg7DUTNFuYPqsMS0ZLs+47HpJ8UCZCQnUp","wake_state":"6tzAvVKLUGaFYZyF91njodGhY9UfsJXZh/9DtCnSCkoZ2SiYXFcDSJz2L7sGPYjS/cK7HFTeiwBj809In1DrVTabgi04VfTkGag=","content_kept":"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","content_purgeable":"yjUxZbiMHHTc8KQSkhuOiV6iz4uxBpna49PQD2GvELNcJQR4n7gzuNUevRoxG1NjTZqnWyG/T0yzD9yyt7alxk82pGbT4CFVzJcPLGfgBdlM/t0kR/9XK7Tt3RGCb7KGPtGRt6rFQmgGojpWvHaZFUhNz8aVG2zvHWVmudFuD475+OhuGyNk+UffXELl3OGfDfkWi1uWlYhrjfWMJIOA/W+TjmlPDDQnjM6iybwX+9Xn15IRZDDK83KGfqUlvIgsgnh526nSVJ0TVLsXbOTiKXZ8Jpmhmo8F1K+GiE+7X5gCzUJ6nNUHBNG6FP2p9Dv08Q+/0UuqpJ5EERXqsJh0Az0y/L46LnUXPrJ4bty+Lhh3+8zOukHGykwAVfEgau0qEgQ/67IvJkCtvYCRu7ebXnH4j2ACnLIno+aLzf/FS1kq8da9yuTIzEeAJP8Q6Y9um3Jin3jgpwqrZu2S1y+3tI3TniTZIrmapgaJ+KLdZcuFTy1sD9kuaYqQTtbCGWs6GHKgRPIW09JvZUVmJYZYxkUfEU9BmMpQssUMpgQQKiqMO6aJqWh2RKR181Ihtxg5kV5AXRxkFMUh6gUH3qWB0Uurjlum2g32ErgKe9FA2MGrC/753dXFFmcxeaaJP7y8DcqOz4SsANz2IytwFpRUwot1SS1Sc4oj8vl/h7TKj3zWV8ZOtBwr+2OofFxKuGRasNolF2sPEa+fjzVuje9FJ0zjLVXHqmOYBuXG41ZTGJAuagMeR/5myrtj6LRfpDqbuPFlmuG+Cxn+/IszC0nGnVcCZ6Wob09umA4wZ0EB+iSfV9zSJ+55mnbOmqb+nMemWiAuAXoZDp5xSXxF/USjgGZIEpsuexgVsHDCSwDJE44+cXbTIPF7k9dE/BY9RRiMDxhvfMSAsubwB/ke+RAdmXbwStgF6SbESWn3pt21WC9GnNSQatBTkTyoZUVxrcoYQpwCoIzQYE0IoMLvM5pJgLs01ij3jtgVokAlykvrCW0HyYqkXb//SFbhb1X3w46Dc5YBO7RMDCIV4FCKB/QVDCo6JyyNMjfkCAZ3paz+a5SXx+N0HRRtsIqiUtDWsXyux34obuBcd3x7w7NEdLiUS0bxfzO58aRwydarcvD+rd7G/c0WOfuPfz/hG+RatO5/MCgQ9WUyvn2olXDS+z8eJd49Ms7PWJM4M4ngTMHEstWHjr9XbnXATE2MMQuzBGCR9iXl+UQbsJLOp9VSOMdFNObTPz0DckY8ldAg2H5H","grant_epoch":7,"grant_seq":2,"relay_cursor":17,"relay_incarnation":"AAAAAAAAAAAAAAAAAAAAAA","relay_generation":5,"phone_binding":{"home":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","phone_rid":"0123456789abcdef0123456789abcdef","generation":"7","active":true},"discard_recovery_generation":3,"discard_recovery_completed":2,"discard_recovery_token":"fedcba9876543210fedcba9876543210","discard_recovery_incarnation":"AAAAAAAAAAAAAAAAAAAAAA","discard_recovery_cursor":18,"roster_revision":23,"stale":[{"sender":"0000000000000000","epoch":7}],"stale_streams":["journal"],"last_heard_at":1753900000000}`

// TestStateStore_PinnedSealedFixturesStillLoad pins every current v2 coordinate through
// the byte-literal sealed fixture. A reader that drops a top-level or container field fails.
func TestStateStore_PinnedSealedFixturesStillLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFileName)
	if err := os.WriteFile(path, []byte(stateV26Fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	kek := &s14aSealer{kek: stateCurrentFixtureKEK}
	store, err := OpenStore(path, "m1", kek, kek)
	if err != nil {
		t.Fatalf("OpenStore on current v2 fixture: %v", err)
	}
	want, got := fullState(), store.Load()
	wv, gv := reflect.ValueOf(want), reflect.ValueOf(got)
	for i := 0; i < wv.NumField(); i++ {
		name := wv.Type().Field(i).Name
		if wv.Type().Field(i).IsExported() && !durableStateFieldEqual(name, wv.Field(i).Interface(), gv.Field(i).Interface()) {
			t.Errorf("current v2 fixture restored State.%s = %#v; want %#v", name, gv.Field(i).Interface(), wv.Field(i).Interface())
		}
	}
}

// Pairing push ownership is a current v2 crash boundary, not a legacy migration.
func TestStateStore_PairingOwnershipSurvivesCurrentUnrelatedSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFileName)
	if err := os.WriteFile(path, []byte(stateV26Fixture), 0o600); err != nil {
		t.Fatalf("write current fixture: %v", err)
	}
	kek := &s14aSealer{kek: stateCurrentFixtureKEK}
	store, err := OpenStore(path, "m1", kek, kek)
	if err != nil {
		t.Fatalf("open current fixture: %v", err)
	}
	const owned = "AQIDBAUGBwgJCgsMDQ4PEA"
	if got := store.(*fileStore).st.pairingPushOwned; got != owned {
		t.Fatalf("loaded ownership = %q, want %q", got, owned)
	}
	state := store.Load()
	state.PushPreference.Version++
	if err := store.Save(state); err != nil {
		t.Fatalf("unrelated current save: %v", err)
	}
	restarted, err := OpenStore(path, "m1", kek, kek)
	if err != nil {
		t.Fatalf("reopen current state: %v", err)
	}
	if got := restarted.(*fileStore).st.pairingPushOwned; got != owned {
		t.Fatalf("restarted ownership = %q, want %q", got, owned)
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
	fixture := stateV26Fixture
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
		if _, present := blob[tag]; !present {
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

// TestStateStore_UnknownFutureSchemaFailsClosed is PB-STATE-5's other half. A blob written
// by a NEWER app build (an upgrade, then a downgrade, or a restored backup) carries
// coordinates this build cannot interpret. Reading it with the current decoder would
// silently drop the fields it does not know -- which for a send-seq ceiling or a receive
// high-water means resetting a replay guard to zero. Refuse it instead.
func TestStateStore_UnknownFutureSchemaFailsClosed(t *testing.T) {
	var blob map[string]any
	if err := json.Unmarshal([]byte(stateV26Fixture), &blob); err != nil {
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

func TestStateStore_PreV2SchemasRequireResetWithoutOpeningOrRewritingState(t *testing.T) {
	for version := 1; version <= 25; version++ {
		t.Run("v"+strconv.Itoa(version), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, StateFileName)
			wake, content := s14aNewSealer(t), s14aNewSealer(t)
			store, err := OpenStore(path, "m1", wake, content)
			if err != nil {
				t.Fatalf("create current store: %v", err)
			}
			st := store.Load()
			st.MachineName = "data that reset must not erase"
			if err := store.Save(st); err != nil {
				t.Fatalf("seed current state: %v", err)
			}

			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var blob map[string]any
			if err := json.Unmarshal(raw, &blob); err != nil {
				t.Fatal(err)
			}
			blob["schema_version"] = version
			legacy, err := json.Marshal(blob)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, legacy, 0o600); err != nil {
				t.Fatal(err)
			}
			wake.opens, content.opens = 0, 0

			_, err = OpenStore(path, "m1", wake, content)
			if !errors.Is(err, ErrLegacyStateResetRequired) {
				t.Fatalf("OpenStore on pre-v2 schema %d = %v, want ErrLegacyStateResetRequired", version, err)
			}
			if wake.opens != 0 || content.opens != 0 {
				t.Fatalf("pre-v2 refusal opened sealed state: wake=%d content=%d", wake.opens, content.opens)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(after, legacy) {
				t.Fatal("pre-v2 refusal rewrote or erased the user's state")
			}
		})
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
	if err := os.WriteFile(foreign, []byte(stateV26Fixture), 0o600); err != nil {
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
	body := `{"schema_version":26,"machine":"m1","relay_cursor":3,"relay_incarnation":"NOT-CANONICAL"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path, "m1", s14aNewSealer(t), s14aNewSealer(t)); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("OpenStore(malformed relay incarnation) = %v, want ErrCorruptState", err)
	}
}
