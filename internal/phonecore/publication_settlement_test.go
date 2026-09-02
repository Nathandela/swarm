package phonecore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

func settlementState() State {
	return State{
		Machine: "m1", EpochID: 7, RoutingID: "phone-route",
		MachineRelayAuthPub: bytes.Repeat([]byte{0x44}, 32),
		Keys:                crypto.EpochKeys{ContentKey: testContentKey()},
	}
}

func historyPublication(operationID, session string) PendingPublication {
	body := &schema.InteractionHistoryReq{Session: session, BeforeItem: "newest", Limit: 20}
	return PendingPublication{
		LogicalID: operationID, OperationID: operationID, Kind: PublicationHistory,
		SessionID: session, Machine: "m1", EpochID: 7, Target: "machine-route",
		AuthorityPub: bytes.Repeat([]byte{0x44}, 32),
		Command: schema.DeviceCommandAuth{
			Action: schema.ActionInteractionHistory, Machine: "m1", Session: session,
			OperationID: operationID,
		},
		History: body, Phase: PublicationPrepared, CreatedAt: time.Unix(1_700_000_000, 0),
	}
}

func detailPublication(operationID, session, itemID string) PendingPublication {
	body := &schema.InteractionDetailReq{Session: session, ItemID: itemID}
	return PendingPublication{
		LogicalID: operationID, OperationID: operationID, Kind: PublicationDetail,
		SessionID: session, Machine: "m1", EpochID: 7, Target: "machine-route",
		AuthorityPub: bytes.Repeat([]byte{0x44}, 32),
		Command: schema.DeviceCommandAuth{
			Action: schema.ActionInteractionDetail, Machine: "m1", Session: session,
			OperationID: operationID,
		},
		Detail: body, Phase: PublicationPrepared, CreatedAt: time.Unix(1_700_000_000, 0),
	}
}

func prepareAdmittedPublication(t *testing.T, c *Core, p PendingPublication, sequence uint64) {
	t.Helper()
	if err := c.PreparePublication(p); err != nil {
		t.Fatalf("PreparePublication: %v", err)
	}
	if err := c.SealPublication(p.OperationID, sequence, []byte("exact-outbound-envelope-"+p.OperationID)); err != nil {
		t.Fatalf("SealPublication: %v", err)
	}
	if err := c.MarkPublicationAdmitted(p.OperationID); err != nil {
		t.Fatalf("MarkPublicationAdmitted: %v", err)
	}
}

func TestPublicationSettlement_HistoryReplyCommitsPageFloorVerdictAndSettlementAtomically(t *testing.T) {
	store := &memStore{}
	if err := store.Save(settlementState()); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	c, err := Resume(Config{State: store, Ack: &recordingAcker{}})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	const operationID, session = "op-history-atomic", "m1/s1"
	prepareAdmittedPublication(t, c, historyPublication(operationID, session), 31)
	page := []schema.JournalRecord{
		r2item(session, 2, "old-1", KindAgentMessage, "older", StatusCompleted, false),
	}
	reply := schema.Control{
		Op: schema.ActionInteractionHistory, OperationID: operationID, SessionID: session,
		Journal: page, HistoryFloor: true,
	}
	raw := sealFrame(t, testContentKey(), 1, marshalReply(t, reply))
	if _, err := c.Router().AcceptCommit(raw, 101); err != nil {
		t.Fatalf("AcceptCommit: %v", err)
	}

	st := store.Load()
	if len(st.Items) != 1 || st.Items[0].ItemID != "old-1" || !st.Items[0].Backfilled {
		t.Fatalf("durable page = %+v", st.Items)
	}
	if !st.HistoryFloor[session] || st.HistoryCapped[session] {
		t.Fatalf("durable history facts floor=%v capped=%v", st.HistoryFloor, st.HistoryCapped)
	}
	if got := st.OpOutcomes[operationID]; got.OperationID != operationID || len(got.Journal) != 0 {
		t.Fatalf("durable verdict = %+v; want attributed and payload-free", got)
	}
	if len(st.PendingPublications) != 0 {
		t.Fatalf("completed read still consumes publication capacity: %+v", st.PendingPublications)
	}
	if got := c.Router().Items().Session(session); len(got) != 1 || got[0].ItemID != "old-1" {
		t.Fatalf("live page diverged from committed state: %+v", got)
	}
}

func TestPublicationSettlement_FailedCommitMutatesNothingAndRetainedReplyRetries(t *testing.T) {
	store := &memStore{}
	if err := store.Save(settlementState()); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	c1, err := Resume(Config{State: store})
	if err != nil {
		t.Fatalf("Resume setup: %v", err)
	}
	const operationID, session = "op-history-retry", "m1/s1"
	prepareAdmittedPublication(t, c1, historyPublication(operationID, session), 32)
	reply := schema.Control{
		Op: schema.ActionInteractionHistory, OperationID: operationID, SessionID: session,
		Journal:      []schema.JournalRecord{r2item(session, 2, "old-retry", KindAgentMessage, "older", StatusCompleted, false)},
		HistoryFloor: true,
	}
	raw := sealFrame(t, testContentKey(), 1, marshalReply(t, reply))
	failing := &failAfterNStore{inner: store, n: 0}
	r1 := resumeRouter(t, failing, &recordingAcker{})
	receipt, err := r1.AcceptCommit(raw, 102)
	if !errors.Is(err, errStoreDied) || receipt.Acked {
		t.Fatalf("failed receipt = %+v, %v", receipt, err)
	}
	unchanged := store.Load()
	if len(unchanged.Items) != 0 || unchanged.HistoryFloor[session] ||
		unchanged.PendingPublications[0].Phase != PublicationAdmitted {
		t.Fatalf("failed commit leaked partial adoption: %+v", unchanged)
	}

	r2 := resumeRouter(t, store, &recordingAcker{})
	if _, err := r2.AcceptCommit(raw, 103); err != nil {
		t.Fatalf("same-process/restart redelivery: %v", err)
	}
	if got := store.Load(); len(got.Items) != 1 || !got.HistoryFloor[session] ||
		len(got.PendingPublications) != 0 {
		t.Fatalf("retry did not commit whole transaction: %+v", got)
	}
}

func TestPublicationSettlement_DetailReplyReplacesBodyDurablyAcrossRestart(t *testing.T) {
	store := &memStore{}
	st := settlementState()
	scratch := NewItemStore()
	scratch.Apply(r2item("m1/s1", 7, "clipped", KindAgentMessage, "HEAD...", StatusCompleted, true))
	st.Items = scratch.All()
	if err := store.Save(st); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	c, err := Resume(Config{State: store})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	const operationID, session = "op-detail-atomic", "m1/s1"
	prepareAdmittedPublication(t, c, detailPublication(operationID, session, "clipped"), 33)
	reply := schema.Control{
		Op: schema.ActionInteractionDetail, OperationID: operationID, SessionID: session,
		Journal: []schema.JournalRecord{r2detail(session, "clipped", KindAgentMessage, "HEAD AND ALL")},
	}
	if _, err := c.Router().AcceptCommit(sealFrame(t, testContentKey(), 1, marshalReply(t, reply)), 104); err != nil {
		t.Fatalf("AcceptCommit: %v", err)
	}
	restarted := resumeRouter(t, store, &recordingAcker{})
	item, ok := restarted.Items().Resolve(session, "clipped")
	if !ok || item.Text != "HEAD AND ALL" || item.Truncated {
		t.Fatalf("restored detail = %+v (ok=%v)", item, ok)
	}
	if len(store.Load().OpOutcomes[operationID].Journal) != 0 {
		t.Fatal("full detail body leaked into durable outcome")
	}
}

func TestPublicationSettlement_OverCapacityPageIsRefusedWholeAndFactsCommit(t *testing.T) {
	store := &memStore{}
	st := settlementState()
	scratch := NewItemStore()
	page := make([]schema.JournalRecord, 0, MaxBackfillPerSession)
	for i := 0; i < MaxBackfillPerSession; i++ {
		page = append(page, r2item("m1/s1", uint64(i+10), fmt.Sprintf("held-%03d", i),
			KindAgentMessage, "held", StatusCompleted, false))
	}
	if !scratch.ApplyPage(page) {
		t.Fatal("setup page did not fill the backfill region")
	}
	st.Items = scratch.All()
	if err := store.Save(st); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	c, err := Resume(Config{State: store})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	const operationID, session = "op-history-capacity", "m1/s1"
	prepareAdmittedPublication(t, c, historyPublication(operationID, session), 37)
	reply := schema.Control{
		Op: schema.ActionInteractionHistory, OperationID: operationID, SessionID: session,
		Journal: []schema.JournalRecord{
			r2item(session, 1, "must-not-land", KindAgentMessage, "too old", StatusCompleted, false),
		},
		HistoryFloor: true,
	}
	if _, err := c.Router().AcceptCommit(sealFrame(t, testContentKey(), 1, marshalReply(t, reply)), 108); err != nil {
		t.Fatalf("AcceptCommit: %v", err)
	}
	got := store.Load()
	if len(got.Items) != MaxBackfillPerSession {
		t.Fatalf("over-capacity page partly landed: %d items", len(got.Items))
	}
	if got.HistoryFloor[session] {
		t.Fatal("refused floor page claimed a machine floor the handset does not hold")
	}
	if !got.HistoryCapped[session] {
		t.Fatal("refused page did not persist the handset capacity fact")
	}
	if len(got.PendingPublications) != 0 {
		t.Fatalf("answered capacity-limited read still consumes queue: %+v", got.PendingPublications)
	}
	if _, ok := c.Router().Items().Resolve(session, "must-not-land"); ok {
		t.Fatal("live transcript diverged by partially holding the refused page")
	}
}

func TestPublicationSettlement_MismatchedReplyCannotSettleOrAdopt(t *testing.T) {
	for _, mismatch := range []struct {
		name    string
		op      string
		session string
	}{
		{"action", schema.ActionInteractionDetail, "m1/s1"},
		{"session", schema.ActionInteractionHistory, "m1/other"},
	} {
		t.Run(mismatch.name, func(t *testing.T) {
			store := &memStore{}
			if err := store.Save(settlementState()); err != nil {
				t.Fatalf("seed state: %v", err)
			}
			c, err := Resume(Config{State: store})
			if err != nil {
				t.Fatalf("Resume: %v", err)
			}
			const operationID, session = "op-mismatch", "m1/s1"
			prepareAdmittedPublication(t, c, historyPublication(operationID, session), 34)
			reply := schema.Control{
				Op: mismatch.op, OperationID: operationID, SessionID: mismatch.session,
				Journal: []schema.JournalRecord{r2item(mismatch.session, 2, "foreign", KindAgentMessage, "no", StatusCompleted, false)},
			}
			if _, err := c.Router().AcceptCommit(sealFrame(t, testContentKey(), 1, marshalReply(t, reply)), 105); err != nil {
				t.Fatalf("AcceptCommit: %v", err)
			}
			got := store.Load()
			if len(got.Items) != 0 || got.PendingPublications[0].Phase != PublicationAdmitted {
				t.Fatalf("mismatched reply changed publication/content: %+v", got)
			}
			if _, ok := got.OpOutcomes[operationID]; ok {
				t.Fatalf("mismatched reply was attributed as this operation's outcome: %+v", got.OpOutcomes[operationID])
			}
		})
	}
}

func TestPublicationSettlement_DetailReplyMustNameTheRequestedItem(t *testing.T) {
	store := &memStore{}
	st := settlementState()
	scratch := NewItemStore()
	scratch.Apply(r2item("m1/s1", 7, "requested", KindAgentMessage, "HEAD...", StatusCompleted, true))
	scratch.Apply(r2item("m1/s1", 8, "other", KindAgentMessage, "OTHER...", StatusCompleted, true))
	st.Items = scratch.All()
	if err := store.Save(st); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	c, err := Resume(Config{State: store})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	const operationID, session = "op-detail-binding", "m1/s1"
	prepareAdmittedPublication(t, c, detailPublication(operationID, session, "requested"), 38)
	reply := schema.Control{
		Op: schema.ActionInteractionDetail, OperationID: operationID, SessionID: session,
		Journal: []schema.JournalRecord{r2detail(session, "other", KindAgentMessage, "WRONG WHOLE BODY")},
	}
	if _, err := c.Router().AcceptCommit(sealFrame(t, testContentKey(), 1, marshalReply(t, reply)), 109); err != nil {
		t.Fatalf("AcceptCommit: %v", err)
	}
	got := store.Load()
	if got.PendingPublications[0].Phase != PublicationAdmitted {
		t.Fatalf("wrong-item reply settled publication: %+v", got.PendingPublications)
	}
	if _, ok := got.OpOutcomes[operationID]; ok {
		t.Fatal("wrong-item detail reply was attributed as the operation verdict")
	}
	item, _ := c.Router().Items().Resolve(session, "other")
	if item.Text != "OTHER..." {
		t.Fatalf("wrong-item reply replaced another card: %+v", item)
	}
}

func TestPublicationSettlement_ComposerSuccessWaitsForEchoButRefusalTerminates(t *testing.T) {
	for _, tc := range []struct {
		name      string
		op        string
		errorCode schema.ErrorCode
		wantPhase PublicationPhase
	}{
		{"accepted_waits_for_echo", controlOpOK, "", PublicationTerminal},
		{"refusal_is_terminal", controlOpError, schema.ErrorCode("input_busy"), PublicationTerminal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memStore{}
			if err := store.Save(settlementState()); err != nil {
				t.Fatalf("seed state: %v", err)
			}
			c, err := Resume(Config{State: store})
			if err != nil {
				t.Fatalf("Resume: %v", err)
			}
			p := testComposerPublication("op-composer-outcome")
			p.Text, p.Composer.Text = "hello", "hello"
			prepareAdmittedPublication(t, c, p, 36)
			reply := schema.Control{
				Op: tc.op, OperationID: p.OperationID, SessionID: p.SessionID,
				ErrorCode: tc.errorCode,
			}
			if _, err := c.Router().AcceptCommit(sealFrame(t, testContentKey(), 1, marshalReply(t, reply)), 107); err != nil {
				t.Fatalf("AcceptCommit: %v", err)
			}
			got := store.Load().PendingPublications
			if len(got) != 1 || got[0].Phase != tc.wantPhase {
				t.Fatalf("composer settlement = %+v, want phase %q", got, tc.wantPhase)
			}
			if tc.errorCode != "" && got[0].TerminalCode != string(tc.errorCode) {
				t.Fatalf("terminal code = %q, want %q", got[0].TerminalCode, tc.errorCode)
			}
			if tc.errorCode == "" && got[0].TerminalCode != PublicationAccepted {
				t.Fatalf("accepted composer code = %q, want %q", got[0].TerminalCode, PublicationAccepted)
			}
		})
	}
}

func TestPublicationSettlement_NewExactVerdictsSurviveLaterTerminalizations(t *testing.T) {
	store := &memStore{}
	if err := store.Save(settlementState()); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	c, err := Resume(Config{State: store})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	a := testComposerPublication("op-long-lived-A")
	b := testComposerPublication("op-long-lived-B")
	a.Text, a.Composer.Text = "first admitted", "first admitted"
	b.Text, b.Composer.Text = "second admitted", "second admitted"
	prepareAdmittedPublication(t, c, a, 60)
	prepareAdmittedPublication(t, c, b, 61)
	st := c.State()
	if st.OpOutcomes == nil {
		st.OpOutcomes = map[string]schema.Control{}
	}
	for i := 0; i < maxPublicationResults; i++ {
		p := testComposerPublication(fmt.Sprintf("op-old-terminal-%02d", i))
		p.Phase, p.TerminalCode = PublicationTerminal, "policy"
		p.ResultOrder = uint64(i + 1)
		st.PendingPublications = append(st.PendingPublications, p)
		st.OpOutcomes[p.OperationID] = schema.Control{
			Op: controlOpError, OperationID: p.OperationID, ErrorCode: schema.CodePolicy,
		}
	}
	if err := c.Save(st); err != nil {
		t.Fatalf("seed old terminal results: %v", err)
	}
	replies := []schema.Control{
		{Op: controlOpError, OperationID: a.OperationID, SessionID: a.SessionID, ErrorCode: schema.CodeInputBusy},
		{Op: controlOpOK, OperationID: b.OperationID, SessionID: b.SessionID},
	}
	for i, reply := range replies {
		if _, err := c.Router().AcceptCommit(
			sealFrame(t, testContentKey(), uint64(i+1), marshalReply(t, reply)), uint64(200+i),
		); err != nil {
			t.Fatalf("AcceptCommit %s: %v", reply.OperationID, err)
		}
	}
	got := store.Load()
	want := map[string]struct {
		terminal string
		outcome  schema.ErrorCode
	}{
		a.OperationID: {terminal: string(schema.CodeInputBusy), outcome: schema.CodeInputBusy},
		b.OperationID: {terminal: PublicationAccepted},
	}
	for _, p := range []PendingPublication{a, b} {
		var retained *PendingPublication
		for i := range got.PendingPublications {
			if got.PendingPublications[i].OperationID == p.OperationID {
				retained = &got.PendingPublications[i]
				break
			}
		}
		if retained == nil || retained.TerminalCode != want[p.OperationID].terminal || retained.ResultOrder <= maxPublicationResults {
			t.Errorf("new exact result %q was evicted/not newest: %+v", p.OperationID, retained)
		}
		if outcome, ok := got.OpOutcomes[p.OperationID]; !ok || outcome.ErrorCode != want[p.OperationID].outcome {
			t.Errorf("new exact outcome %q was pruned: %+v, %v", p.OperationID, outcome, ok)
		}
	}
	for _, evicted := range []string{"op-old-terminal-00", "op-old-terminal-01"} {
		if _, ok := got.OpOutcomes[evicted]; ok {
			t.Errorf("older result %q survived ahead of new exact verdicts", evicted)
		}
	}
}

func phoneEcho(session, operationID, text, source string) schema.JournalRecord {
	body, _ := json.Marshal(map[string]any{
		"v": 1, "item_id": "echo-" + operationID, "kind": KindUserMessage,
		"status": StatusCompleted, "text": text, "source": source, "operation_id": operationID,
	})
	return schema.JournalRecord{Cursor: 8, SessionID: session, Type: RecordTypeInteraction, Item: body}
}

func TestPublicationSettlement_OnlyExactDaemonPhoneEchoMarksComposerDelivered(t *testing.T) {
	for _, tc := range []struct {
		name, session, operation, text, source string
		wantDelivered                          bool
	}{
		{"exact", "m1/s1", "op-echo", "hello", "phone", true},
		{"wrong_session", "m1/other", "op-echo", "hello", "phone", false},
		{"wrong_operation", "m1/s1", "op-other", "hello", "phone", false},
		{"wrong_text", "m1/s1", "op-echo", "different", "phone", false},
		{"terminal_source", "m1/s1", "op-echo", "hello", "terminal", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memStore{}
			if err := store.Save(settlementState()); err != nil {
				t.Fatalf("seed state: %v", err)
			}
			c, err := Resume(Config{State: store})
			if err != nil {
				t.Fatalf("Resume: %v", err)
			}
			p := testComposerPublication("op-echo")
			p.Text, p.Composer.Text = "hello", "hello"
			prepareAdmittedPublication(t, c, p, 35)
			st := c.State()
			st.OpOutcomes = map[string]schema.Control{
				p.OperationID:       {Op: controlOpOK, OperationID: p.OperationID, SessionID: p.SessionID},
				"unrelated-outcome": {Op: controlOpError, OperationID: "unrelated-outcome", ErrorCode: schema.CodePolicy},
			}
			if err := c.Save(st); err != nil {
				t.Fatalf("seed accepted proof: %v", err)
			}
			rec := phoneEcho(tc.session, tc.operation, tc.text, tc.source)
			plain, err := json.Marshal(rec)
			if err != nil {
				t.Fatalf("marshal echo: %v", err)
			}
			if _, err := c.Router().AcceptCommit(sealFrameFrom(t, testContentKey(), machineSender, 7, 1, plain), 106); err != nil {
				t.Fatalf("AcceptCommit: %v", err)
			}
			got := store.Load().PendingPublications
			if delivered := len(got) == 0; delivered != tc.wantDelivered {
				t.Fatalf("settlement = %+v, delivered=%v want %v", got, delivered, tc.wantDelivered)
			}
			_, acceptedRemains := store.Load().OpOutcomes[p.OperationID]
			if acceptedRemains == tc.wantDelivered {
				t.Fatalf("accepted proof retention = %v, want %v after delivered=%v",
					acceptedRemains, !tc.wantDelivered, tc.wantDelivered)
			}
			if _, ok := store.Load().OpOutcomes["unrelated-outcome"]; !ok {
				t.Fatal("echo settlement removed an unrelated outcome")
			}
		})
	}
}

func TestPublicationSettlement_MalformedPhoneEchoCannotMarkComposerDelivered(t *testing.T) {
	for _, malformed := range []map[string]any{
		{"v": 0, "item_id": "echo-op-malformed", "kind": KindUserMessage},
		{"v": 1, "item_id": "", "kind": KindUserMessage},
		{"v": 1, "item_id": "echo-op-malformed", "kind": "future_kind"},
	} {
		store := &memStore{}
		if err := store.Save(settlementState()); err != nil {
			t.Fatalf("seed state: %v", err)
		}
		c, err := Resume(Config{State: store})
		if err != nil {
			t.Fatalf("Resume: %v", err)
		}
		p := testComposerPublication("op-malformed")
		p.Text, p.Composer.Text = "hello", "hello"
		prepareAdmittedPublication(t, c, p, 39)
		malformed["status"] = StatusCompleted
		malformed["text"] = "hello"
		malformed["source"] = "phone"
		malformed["operation_id"] = p.OperationID
		body, err := json.Marshal(malformed)
		if err != nil {
			t.Fatalf("marshal malformed echo: %v", err)
		}
		rec := schema.JournalRecord{
			Cursor: 9, SessionID: p.SessionID, Type: RecordTypeInteraction, Item: body,
		}
		plain, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal journal record: %v", err)
		}
		if _, err := c.Router().AcceptCommit(
			sealFrameFrom(t, testContentKey(), machineSender, 7, 1, plain), 110,
		); err != nil {
			t.Fatalf("AcceptCommit: %v", err)
		}
		if got := store.Load().PendingPublications; len(got) != 1 || got[0].OperationID != p.OperationID {
			t.Fatalf("malformed echo settled publication: %+v", got)
		}
		if got := c.Router().Items().Session(p.SessionID); len(got) != 0 {
			t.Fatalf("malformed echo entered transcript: %+v", got)
		}
	}
}

func TestPublicationSettlement_RepairReseedSettlesAnExactEchoFirstSeenBehindGap(t *testing.T) {
	store := &memStore{}
	if err := store.Save(settlementState()); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	c, err := Resume(Config{State: store})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	p := testComposerPublication("op-gap-echo")
	p.Text, p.Composer.Text = "survive repair", "survive repair"
	prepareAdmittedPublication(t, c, p, 40)
	echo := phoneEcho(p.SessionID, p.OperationID, p.Text, "phone")
	plain, err := json.Marshal(echo)
	if err != nil {
		t.Fatalf("marshal echo: %v", err)
	}
	if receipt, err := c.Router().AcceptCommit(
		sealFrameFrom(t, testContentKey(), machineSender, 7, 2, plain), 111,
	); err != nil || !receipt.Gap {
		t.Fatalf("gapped echo = %+v, %v", receipt, err)
	}
	if len(store.Load().Items) != 0 {
		t.Fatal("gapped echo became durable before repair")
	}
	if len(store.Load().PendingPublications) != 1 {
		t.Fatal("gapped echo settled publication before repair")
	}

	reseedPlain, err := json.Marshal(reseedFrame{
		Kind: kindJournalReseed,
		JournalReseed: schema.JournalReseed{
			Roster: []schema.JournalRecord{}, Events: []schema.JournalRecord{echo}, Cursor: echo.Cursor,
		},
	})
	if err != nil {
		t.Fatalf("marshal reseed: %v", err)
	}
	if _, err := c.Router().AcceptCommit(
		sealFrameFrom(t, testContentKey(), machineSender, 7, 3, reseedPlain), 112,
	); err != nil {
		t.Fatalf("repair reseed: %v", err)
	}
	got := store.Load()
	if len(got.Items) != 1 || got.Items[0].OperationID != p.OperationID {
		t.Fatalf("repaired durable transcript = %+v", got.Items)
	}
	if len(got.PendingPublications) != 0 {
		t.Fatalf("repaired exact echo did not settle publication: %+v", got.PendingPublications)
	}
	if err := c.ExpirePublications(time.Now().Add(2 * PublicationOutcomeTimeout)); err != nil {
		t.Fatalf("late expiry sweep: %v", err)
	}
	if outcome, ok := store.Load().OpOutcomes[p.OperationID]; ok && outcome.ErrorCode == schema.ErrorCode(PublicationOutcomeUnknown) {
		t.Fatalf("delivered repaired echo later became outcome_unknown: %+v", outcome)
	}
}

func TestPublicationSettlement_UnrelatedReplyDoesNotEraseLiveOnlyGapContent(t *testing.T) {
	store := &memStore{}
	if err := store.Save(settlementState()); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	c, err := Resume(Config{State: store})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	rec := r2item("m1/s1", 8, "live-gap-item", KindAgentMessage, "visible but stale", StatusCompleted, false)
	plain, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal interaction: %v", err)
	}
	// seq 2 over an empty durable high-water leaves a hole: commitReceive deliberately keeps
	// the content live-only and marks the stream stale.
	if receipt, err := c.Router().AcceptCommit(
		sealFrameFrom(t, testContentKey(), machineSender, 7, 2, plain), 201,
	); err != nil || !receipt.Gap {
		t.Fatalf("gapped interaction = %+v, %v", receipt, err)
	}
	if _, ok := c.Router().Items().Resolve("m1/s1", "live-gap-item"); !ok {
		t.Fatal("gapped interaction did not reach the documented live-only view")
	}
	ordinary := schema.Control{Op: controlOpOK, OperationID: "unrelated-op", SessionID: "m1/s1"}
	if _, err := c.Router().AcceptCommit(sealFrame(t, testContentKey(), 1, marshalReply(t, ordinary)), 202); err != nil {
		t.Fatalf("ordinary reply: %v", err)
	}
	if _, ok := c.Router().Items().Resolve("m1/s1", "live-gap-item"); !ok {
		t.Fatal("unrelated reply reset the transcript to durable state and erased live-only gap content")
	}
}
