package phonecore

// FAILING-FIRST (TDD RED, GG-5) for Wave R6's PHONE-CORE half of IS-LIFE-5: authoring one
// composer send, and folding the two additive item fields the M2 transcript renders from.
// Mirror M2.2 (tool_kind), M2.4 (composer op + honest source attribution).
// Bead: agents-tracker-hggx.7. RED is undefined-only: ComposerSendInput, SignComposerSend,
// SealComposerSendEnvelope, Item.Source and Item.ToolKind do not exist.
//
// THE BINDING RULE LIVES HERE, exactly as SignApprove's does: the content hash under the
// device signature is schema.ComposerSendContentHash over the SAME body the envelope
// carries -- (session, expected_turn, text) -- so a gateway that alters the text or
// re-points expected_turn breaks the signature rather than reaching the daemon as a
// well-formed send of something else. Re-deriving the hash at a call site is the same
// forbidden duplication mobile/commands.go records for the take_control token.

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestR6ComposerVerb_SignComposerSendBindsTheBodyIntoTheTuple: Action, coordinates, and
// ContentHash == schema.ComposerSendContentHash(body).
func TestR6ComposerVerb_SignComposerSendBindsTheBodyIntoTheTuple(t *testing.T) {
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	exp := time.Unix(1_700_000_100, 0)

	cmd, err := SignComposerSend(ks, ComposerSendInput{
		Machine:         "machine1",
		Session:         "machine1/sess1",
		SessionInstance: "instance-1",
		OperationID:     "op-comp-1",
		ExpiresAt:       exp,
		ExpectedTurn:    "01JTURN",
		Text:            "run the tests",
	})
	if err != nil {
		t.Fatalf("SignComposerSend: %v", err)
	}
	if cmd.Action != schema.ActionComposerSend {
		t.Errorf("Action = %q, want %q", cmd.Action, schema.ActionComposerSend)
	}
	if cmd.Session != "machine1/sess1" || cmd.Machine != "machine1" || cmd.OperationID != "op-comp-1" {
		t.Errorf("tuple coordinates = %+v, want the input's verbatim", cmd)
	}
	want := schema.ComposerSendContentHash(&schema.ComposerSendReq{
		Session: "machine1/sess1", SessionInstance: "instance-1", ExpectedTurn: "01JTURN", Text: "run the tests",
	})
	if !bytes.Equal(cmd.ContentHash, want) {
		t.Errorf("ContentHash = %x, want ComposerSendContentHash over the same body %x: the "+
			"signature must cover exactly what the envelope carries", cmd.ContentHash, want)
	}
	if cmd.Sig == "" {
		t.Error("Sig is empty; an unsigned composer send is a raw keystroke with extra steps")
	}
}

// TestR6ComposerVerb_SealComposerSendEnvelopeCarriesTheBodyBesideTheTuple: the sealed
// frame round-trips through the gateway's own opener with the body intact, so the daemon
// can recompute the binding from what actually arrived.
func TestR6ComposerVerb_SealComposerSendEnvelopeCarriesTheBodyBesideTheTuple(t *testing.T) {
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 1)
	}
	body := schema.ComposerSendReq{Session: "machine1/sess1", SessionInstance: "instance-1", ExpectedTurn: "01JTURN", Text: "run the tests"}
	cmd, err := SignComposerSend(ks, ComposerSendInput{
		Machine: "machine1", Session: body.Session, OperationID: "op-comp-2",
		SessionInstance: body.SessionInstance, ExpiresAt: time.Now().Add(time.Minute), ExpectedTurn: body.ExpectedTurn, Text: body.Text,
	})
	if err != nil {
		t.Fatalf("SignComposerSend: %v", err)
	}

	env, err := SealComposerSendEnvelope(key, 1, 7, cmd, &body)
	if err != nil {
		t.Fatalf("SealComposerSendEnvelope: %v", err)
	}
	rc, err := remotegw.OpenRemoteCommand(key, env)
	if err != nil {
		t.Fatalf("the gateway cannot open the sealed composer send: %v", err)
	}
	if rc.Action != schema.ActionComposerSend || rc.Sig != cmd.Sig {
		t.Errorf("opened tuple = %+v, want the signed tuple verbatim", rc.DeviceCommandAuth)
	}
	if rc.ComposerSend == nil || *rc.ComposerSend != body {
		t.Errorf("opened body = %+v, want %+v: a stripped body is refused at the gateway, so "+
			"the sealer must put it there", rc.ComposerSend, body)
	}
}

// TestR6ComposerVerb_TheFoldCarriesSourceAndToolKind: the two additive fields M2 renders
// from cross the fold onto Item -- `source` for honest phone-vs-terminal attribution on a
// user_message (M2.4), `tool_kind` for the card glyph on a tool_run (M2.2) -- and absent
// fields stay empty, never invented.
func TestR6ComposerVerb_TheFoldCarriesSourceAndToolKind(t *testing.T) {
	s := NewItemStore()
	apply := func(cursor uint64, item string) {
		t.Helper()
		if !s.Apply(schema.JournalRecord{Cursor: cursor, SessionID: "m/s1", Type: "interaction",
			Item: json.RawMessage(item)}) {
			t.Fatalf("record at cursor %d not applied", cursor)
		}
	}
	apply(1, `{"v":1,"item_id":"01JUSR","ts":"2026-08-16T10:00:00Z","kind":"user_message",`+
		`"text":"phone says hi","source":"phone","operation_id":"op-comp-9"}`)
	apply(2, `{"v":1,"item_id":"01JTOOL","ts":"2026-08-16T10:00:01Z","kind":"tool_run",`+
		`"status":"in_progress","tool":"Grep","tool_kind":"search"}`)
	apply(3, `{"v":1,"item_id":"01JOWN","ts":"2026-08-16T10:00:02Z","kind":"user_message",`+
		`"text":"typed at the terminal","source":"owner"}`)

	items := s.Session("m/s1")
	if len(items) != 3 {
		t.Fatalf("store holds %d items, want 3", len(items))
	}
	if items[0].Source != "phone" {
		t.Errorf("phone-authored user_message Source = %q, want \"phone\": without it the "+
			"transcript cannot attribute the message to the hand that wrote it", items[0].Source)
	}
	if items[1].ToolKind != "search" {
		t.Errorf("tool_run ToolKind = %q, want \"search\": the glyph reads one flat field and "+
			"parses nothing (IS-TOOL-1)", items[1].ToolKind)
	}
	if items[2].Source != "owner" {
		t.Errorf("owner-typed user_message Source = %q, want \"owner\"", items[2].Source)
	}
	if items[1].Source != "" {
		t.Errorf("a tool_run grew a Source %q; absent facts stay absent", items[1].Source)
	}
}
