package protocol

// FAILING-FIRST (TDD RED, GG-5) for Wave R6's Mirror M3.1 / M3.3 read ops -- paged
// interaction history (the ADR-014 obligation M3.1 books; this file freezes the op shape
// that ADR must ratify or amend WITH quoted supersession) and detail-on-demand
// (IS-CAP-2/-3, interaction-schema.md §5). Bead: agents-tracker-hggx.7.
//
// THE CONTRACT these tests freeze:
//
//   - OpInteractionHistory = "interaction_history": the per-session read
//     `(session, before_item, limit)` on Control.History (wire `interaction_history`,
//     body fields `session` / `before_item` / `limit`). The reply rides the EXISTING
//     Control.Journal carrier -- records ascending by cursor, every one for the named
//     session, every one strictly older than before_item -- plus HistoryFloor (wire
//     `history_floor`), the honest "nothing older is retained" signal the phone renders
//     as a retention floor instead of an infinite spinner.
//   - OpInteractionDetail = "interaction_detail": IS-CAP-2's UNSIGNED read
//     `(session, item_id)` on Control.Detail (wire `interaction_detail`, body fields
//     `session` / `item_id`). Reply: one Journal record whose Item is the FULL
//     pre-truncation body. Outside retention: IS-CAP-3's `unavailable`, never a partial
//     body presented as whole. Freeze: CodeUnavailable = "unavailable".
//   - BOTH are READS on the ActionTerminalWatch precedent (IS-CAP-2: "gateway-routed,
//     not forwarded to the device authenticator"): no device fields required, the
//     device authenticator is NEVER consulted, and no new device-signed action is
//     introduced -- PB-SYNC-5's actionClass switch stays closed.
//
//     CORRECTED (Wave R6 review fix-pack, finding B2). This bullet ended "Gating
//     (capability, kill switch) is daemon-side, behind the seams", and the second half
//     of that sentence was false: the handlers called their seams directly and honored
//     NEITHER gate, so with the kill switch off interaction_history served a
//     user_message's text verbatim while journal_read -- the cited precedent -- refused.
//     The gate is now IN THE HANDLER (requireJournalPlaneRead): the negotiated `journal`
//     capability, then the remote-tier kill switch, the same two journal_read applies.
//     The consequence for THIS file is that every connection below negotiates CapJournal
//     alongside CapRemoteGateway; no assertion about a reply's CONTENT is changed, and
//     the two gates get their own fences in r6fix_chatgates_test.go.
//   - Seams: InteractionHistorian and InteractionDetailer, optional DaemonAPI
//     interfaces mirroring InteractionApprover's (ErrorCode, error) refusal shape.

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

var (
	errUnknownBeforeItem = errors.New("before_item 01JNEVER is not an item of this session")
	errDetailEvicted     = errors.New("detail for 01JGONE left the bounded retention window")
)

type r6HistoryQuery struct {
	session    string
	beforeItem string
	limit      int
}

type r6HistoryBackend struct {
	*stubDaemon
	mu       sync.Mutex
	history  []schema.JournalRecord
	floor    bool
	histCode ErrorCode
	histErr  error
	detail   json.RawMessage
	detCode  ErrorCode
	detErr   error
	histQ    []r6HistoryQuery
	detQ     [][2]string // (session, item_id) queries seen
}

func newR6HistoryBackend() *r6HistoryBackend {
	return &r6HistoryBackend{stubDaemon: newStubDaemon()}
}

func (b *r6HistoryBackend) InteractionHistory(session, beforeItem string, limit int) ([]schema.JournalRecord, bool, ErrorCode, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.histQ = append(b.histQ, r6HistoryQuery{session: session, beforeItem: beforeItem, limit: limit})
	return b.history, b.floor, b.histCode, b.histErr
}

func (b *r6HistoryBackend) InteractionDetail(session, itemID string) (json.RawMessage, ErrorCode, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.detQ = append(b.detQ, [2]string{session, itemID})
	return b.detail, b.detCode, b.detErr
}

var (
	_ InteractionHistorian = (*r6HistoryBackend)(nil)
	_ InteractionDetailer  = (*r6HistoryBackend)(nil)
)

// TestR6HistoryDetail_CodeUnavailableIsItsOwnSealedCode pins IS-CAP-3's wire value.
func TestR6HistoryDetail_CodeUnavailableIsItsOwnSealedCode(t *testing.T) {
	if got := string(CodeUnavailable); got != "unavailable" {
		t.Fatalf("CodeUnavailable = %q, want IS-CAP-3's sealed value \"unavailable\"", got)
	}
}

// TestR6History_WireShapeRoundTripsUnderItsOwnKeys pins the body and reply keys.
func TestR6History_WireShapeRoundTripsUnderItsOwnKeys(t *testing.T) {
	c := Control{Op: OpInteractionHistory, History: &InteractionHistoryReq{
		Session: "ep/sess1", BeforeItem: "01JITEM", Limit: 50,
	}}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"interaction_history"`, `"session":"ep/sess1"`, `"before_item":"01JITEM"`, `"limit":50`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("serialized interaction_history Control %s lacks %s", raw, key)
		}
	}
	if raw, err := json.Marshal(Control{Op: OpKill}); err != nil ||
		strings.Contains(string(raw), "interaction_history") || strings.Contains(string(raw), "history_floor") {
		t.Errorf("a history-less Control leaks a new key: %s (err %v)", raw, err)
	}
}

// TestR6Detail_WireShapeRoundTripsUnderItsOwnKeys pins the detail body keys.
func TestR6Detail_WireShapeRoundTripsUnderItsOwnKeys(t *testing.T) {
	c := Control{Op: OpInteractionDetail, Detail: &InteractionDetailReq{Session: "ep/sess1", ItemID: "01JITEM"}}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"interaction_detail"`, `"session":"ep/sess1"`, `"item_id":"01JITEM"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("serialized interaction_detail Control %s lacks %s", raw, key)
		}
	}
}

// TestR6History_AnUnsignedReadReachesTheSeamAndNeverTheDeviceAuthenticator is the
// ActionTerminalWatch precedent applied: the op requires no device fields and the
// authenticator sees ZERO tuples -- capability is pinned at enrollment and PB-SYNC-5's
// switch stays closed.
func TestR6History_AnUnsignedReadReachesTheSeamAndNeverTheDeviceAuthenticator(t *testing.T) {
	b := newR6HistoryBackend()
	b.history = []schema.JournalRecord{
		{Cursor: 10, SessionID: "sess1", Type: "interaction", Item: json.RawMessage(`{"v":1,"item_id":"01JA","kind":"user_message"}`)},
		{Cursor: 11, SessionID: "sess1", Type: "interaction", Item: json.RawMessage(`{"v":1,"item_id":"01JB","kind":"agent_message"}`)},
	}
	b.floor = true
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway, CapJournal})
	sid := rep.EndpointID + "/sess1"

	rc.writeControl(Control{Op: OpInteractionHistory, EndpointID: rep.EndpointID, SessionID: sid,
		History: &InteractionHistoryReq{Session: sid, BeforeItem: "01JC", Limit: 2}})
	got := rc.readControl()
	if got.Op == OpError {
		t.Fatalf("interaction_history refused: code %q %q", got.ErrorCode, got.Error)
	}
	if len(got.Journal) != 2 {
		t.Fatalf("history reply carries %d records, want the seam's 2", len(got.Journal))
	}
	if got.Journal[0].Cursor >= got.Journal[1].Cursor {
		t.Errorf("history records out of ascending cursor order: %d then %d", got.Journal[0].Cursor, got.Journal[1].Cursor)
	}
	if !got.HistoryFloor {
		t.Error("history reply dropped the seam's floor signal; the phone would offer 'load earlier' forever")
	}
	if n := len(b.authorizedTuples()); n != 0 {
		t.Errorf("device authenticator saw %d tuples for an unsigned read, want 0 (IS-CAP-2)", n)
	}
	b.mu.Lock()
	q := append([]r6HistoryQuery(nil), b.histQ...)
	b.mu.Unlock()
	if len(q) != 1 || q[0].session != sid || q[0].beforeItem != "01JC" || q[0].limit != 2 {
		t.Errorf("seam query = %+v, want one query for (%s, 01JC, 2)", q, sid)
	}
}

// TestR6History_AnEmptyAnchorReachesTheHistorian pins the newest-page sentinel at the
// protocol choke point. The body is present; only before_item is empty, which deliberately
// means "start after the newest retained item" to the historian.
func TestR6History_AnEmptyAnchorReachesTheHistorian(t *testing.T) {
	b := newR6HistoryBackend()
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway, CapJournal})
	sid := rep.EndpointID + "/sess1"

	rc.writeControl(Control{Op: OpInteractionHistory, EndpointID: rep.EndpointID, SessionID: sid,
		History: &InteractionHistoryReq{Session: sid, BeforeItem: "", Limit: 50}})
	if got := rc.readControl(); got.Op == OpError {
		t.Fatalf("anchorless interaction_history refused: code %q %q", got.ErrorCode, got.Error)
	}
	b.mu.Lock()
	q := append([]r6HistoryQuery(nil), b.histQ...)
	b.mu.Unlock()
	if len(q) != 1 || q[0].session != sid || q[0].beforeItem != "" || q[0].limit != 50 {
		t.Fatalf("anchorless seam query = %+v, want one query for (%s, empty, 50)", q, sid)
	}
}

// TestR6History_ASeamRefusalSurfacesItsCodeVerbatim: an unknown before_item is the
// daemon's call and its code crosses back untouched.
func TestR6History_ASeamRefusalSurfacesItsCodeVerbatim(t *testing.T) {
	b := newR6HistoryBackend()
	b.histCode = CodeInvalidField
	b.histErr = errUnknownBeforeItem
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway, CapJournal})
	sid := rep.EndpointID + "/sess1"

	rc.writeControl(Control{Op: OpInteractionHistory, EndpointID: rep.EndpointID, SessionID: sid,
		History: &InteractionHistoryReq{Session: sid, BeforeItem: "01JNEVER", Limit: 5}})
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("unknown before_item = op %q code %q, want error/invalid_field", got.Op, got.ErrorCode)
	}
}

// TestR6History_ABodylessReadIsRefusedInvalidField: shape gate before the seam.
func TestR6History_ABodylessReadIsRefusedInvalidField(t *testing.T) {
	b := newR6HistoryBackend()
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway, CapJournal})

	rc.writeControl(Control{Op: OpInteractionHistory, EndpointID: rep.EndpointID, SessionID: rep.EndpointID + "/sess1"})
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("bodyless interaction_history = op %q code %q, want error/invalid_field", got.Op, got.ErrorCode)
	}
	b.mu.Lock()
	n := len(b.histQ)
	b.mu.Unlock()
	if n != 0 {
		t.Errorf("seam saw %d queries for a bodyless read, want 0", n)
	}
}

// TestR6Detail_TheFullBodyComesBackWhole: the reply's one record carries the full
// pre-truncation item verbatim.
func TestR6Detail_TheFullBodyComesBackWhole(t *testing.T) {
	full := json.RawMessage(`{"v":1,"item_id":"01JBIG","kind":"tool_run","output_excerpt":"` + strings.Repeat("x", 512) + `"}`)
	b := newR6HistoryBackend()
	b.detail = full
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway, CapJournal})
	sid := rep.EndpointID + "/sess1"

	rc.writeControl(Control{Op: OpInteractionDetail, EndpointID: rep.EndpointID, SessionID: sid,
		Detail: &InteractionDetailReq{Session: sid, ItemID: "01JBIG"}})
	got := rc.readControl()
	if got.Op == OpError {
		t.Fatalf("interaction_detail refused: code %q %q", got.ErrorCode, got.Error)
	}
	if len(got.Journal) != 1 {
		t.Fatalf("detail reply carries %d records, want exactly 1", len(got.Journal))
	}
	if string(got.Journal[0].Item) != string(full) {
		t.Errorf("detail body = %s, want the seam's full body verbatim", got.Journal[0].Item)
	}
	if n := len(b.authorizedTuples()); n != 0 {
		t.Errorf("device authenticator saw %d tuples for the unsigned detail read, want 0 (IS-CAP-2)", n)
	}
}

// TestR6Detail_OutsideRetentionAnswersUnavailableNeverAPartialBody is IS-CAP-3 verbatim.
func TestR6Detail_OutsideRetentionAnswersUnavailableNeverAPartialBody(t *testing.T) {
	b := newR6HistoryBackend()
	b.detCode = CodeUnavailable
	b.detErr = errDetailEvicted
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway, CapJournal})
	sid := rep.EndpointID + "/sess1"

	rc.writeControl(Control{Op: OpInteractionDetail, EndpointID: rep.EndpointID, SessionID: sid,
		Detail: &InteractionDetailReq{Session: sid, ItemID: "01JGONE"}})
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeUnavailable {
		t.Fatalf("evicted detail = op %q code %q, want error/unavailable", got.Op, got.ErrorCode)
	}
	if len(got.Journal) != 0 {
		t.Errorf("an unavailable refusal still carried %d records; a partial body presented "+
			"beside a refusal is the exact ambiguity IS-CAP-3 forbids", len(got.Journal))
	}
}
