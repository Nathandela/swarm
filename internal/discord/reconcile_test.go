package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	bolt "go.etcd.io/bbolt"
)

type receiptWorkers struct {
	*fakeWorkers
	receipt    protocol.SemanticMessageReceipt
	readErr    error
	reads      int
	receipts   map[string]protocol.SemanticMessageReceipt
	block      bool
	cancel     context.CancelFunc
	operations []string
}

func (w *receiptWorkers) Receipt(ctx context.Context, _ string, operation string) (protocol.SemanticMessageReceipt, error) {
	w.reads++
	w.operations = append(w.operations, operation)
	if w.block {
		if w.cancel != nil {
			w.cancel()
		}
		<-ctx.Done()
		return protocol.SemanticMessageReceipt{}, ctx.Err()
	}
	if w.receipts != nil {
		return w.receipts[operation], w.readErr
	}
	return w.receipt, w.readErr
}
func uncertainRig(t *testing.T) (*Service, *receiptWorkers, Request) {
	t.Helper()
	s, w, _ := serviceRig(t)
	w.err = errors.New("lost submission acknowledgement")
	reader := &receiptWorkers{fakeWorkers: w}
	s.Workers = reader
	r, e := s.Handle(context.Background(), incoming())
	if e == nil || r.DeliveryState != StateUncertain {
		t.Fatal(r, e)
	}
	reader.receipt = protocol.SemanticMessageReceipt{OperationID: r.OperationID, Session: r.Target.ID, SessionInstance: r.Target.Instance}
	return s, reader, r
}
func TestReconcileReadsExactReceiptWithoutResending(t *testing.T) {
	for _, native := range []string{"submitted", "admitted", "completed", "failed", "cancelled", "declined", "rejected", "prepared", "uncertain"} {
		t.Run(native, func(t *testing.T) {
			s, w, r := uncertainRig(t)
			w.receipt.State = native
			status, e := s.Reconcile(context.Background(), r.Ref)
			if e != nil {
				t.Fatal(e)
			}
			want := StateSent
			if native == "rejected" {
				want = StateFailed
			}
			if native == "prepared" || native == "uncertain" {
				want = StateUncertain
			}
			got, e := s.Store.Request(r.Ref, s.Now())
			if e != nil || got.DeliveryState != want || status.DeliveryState != want || len(w.sends) != 1 || w.reads != 1 {
				t.Fatal(status, got.DeliveryState, e, len(w.sends), w.reads)
			}
		})
	}
}

func TestReconcileObservesClaudeQueueDropAfterSubmittedReceipt(t *testing.T) {
	for _, native := range []string{"admitted", "rejected"} {
		t.Run(native, func(t *testing.T) {
			s, w, _ := serviceRig(t)
			w.rows[0].Agent = "claude"
			reader := &receiptWorkers{fakeWorkers: w}
			s.Workers = reader
			r, err := s.Handle(context.Background(), incoming())
			if err != nil {
				t.Fatal(err)
			}
			pending, err := s.Store.PendingDeliveries(s.Now(), MaxRequests)
			if err != nil || len(pending) != 1 {
				t.Fatal("submitted native queue was no longer observed", pending, err)
			}
			reader.receipt = protocol.SemanticMessageReceipt{OperationID: r.OperationID, Session: r.Target.ID, SessionInstance: r.Target.Instance, State: native}
			got, err := s.Reconcile(context.Background(), r.Ref)
			want := StateSent
			if native == "rejected" {
				want = StateFailed
			}
			if err != nil || got.DeliveryState != want || got.NativeState != native || len(w.sends) != 1 {
				t.Fatal("native queue outcome hidden or input replayed", got, err)
			}
			pending, err = s.Store.PendingDeliveries(s.Now(), MaxRequests)
			if err != nil || len(pending) != 0 {
				t.Fatal("terminal native queue was still polled", pending, err)
			}
		})
	}
}
func TestReconcileRejectsWrongScopeAndRetainsGuard(t *testing.T) {
	for _, field := range []string{"operation", "session", "instance"} {
		t.Run(field, func(t *testing.T) {
			s, w, r := uncertainRig(t)
			w.receipt.State = "submitted"
			switch field {
			case "operation":
				w.receipt.OperationID = "other"
			case "session":
				w.receipt.Session = "local/other"
			case "instance":
				w.receipt.SessionInstance = "other"
			}
			if _, e := s.Reconcile(context.Background(), r.Ref); !errors.Is(e, ErrConflict) {
				t.Fatal("receipt scope accepted", e)
			}
			got, e := s.Store.Request(r.Ref, s.Now())
			if e != nil || got.DeliveryState != StateUncertain || len(w.sends) != 1 {
				t.Fatal(got, e, w.sends)
			}
		})
	}
}
func TestPendingReceiptMetadataIsBoundedAndPrivate(t *testing.T) {
	s, w, r := uncertainRig(t)
	pending, e := s.Store.PendingDeliveries(s.Now(), 1)
	if e != nil || len(pending) != 1 || pending[0].Ref != r.Ref {
		t.Fatal(pending, e)
	}
	raw, _ := json.Marshal(pending)
	if strings.Contains(string(raw), r.Text) || strings.Contains(string(raw), r.Author.Name) || strings.Contains(string(raw), "channel_id") {
		t.Fatal("pending receipt API exposed source", string(raw))
	}
	s.Now = func() time.Time { return r.ExpiresAt }
	if status, e := s.Reconcile(context.Background(), r.Ref); !errors.Is(e, ErrExpired) || status.Ref != "" || w.reads != 0 {
		t.Fatal("expired context queried/exposed", status, e, w.reads)
	}
	if pending, e = s.Store.PendingDeliveries(s.Now(), MaxRequests); e != nil || len(pending) != 0 {
		t.Fatal(pending, e)
	}
	if e = s.Store.Prune(s.Now().Add(GuardTTL)); e != nil {
		t.Fatal(e)
	}
	if e = s.Store.db.View(func(tx *bolt.Tx) error {
		record, e := loadRequest(tx, r.Ref)
		if e != nil {
			return e
		}
		if record.Request.DeliveryState != StateUncertain {
			t.Fatal("unknown guard evicted")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
func TestRevokedReceiptCannotExposeOrQuerySource(t *testing.T) {
	s, w, r := uncertainRig(t)
	if e := s.Store.SetOptOut(r.Target, true); e != nil {
		t.Fatal(e)
	}
	if status, e := s.Reconcile(context.Background(), r.Ref); !errors.Is(e, ErrOptedOut) || status.Ref != "" || w.reads != 0 {
		t.Fatal(status, e, w.reads)
	}
	if pending, e := s.Store.PendingDeliveries(s.Now(), MaxRequests); e != nil || len(pending) != 0 {
		t.Fatal(pending, e)
	}
}

func TestUncertainInitialReceiptDoesNotClaimDelivery(t *testing.T) {
	s, w, _ := serviceRig(t)
	w.state = "uncertain"
	r, e := s.Handle(context.Background(), incoming())
	if !errors.Is(e, ErrDeliveryUncertain) || r.DeliveryState != StateUncertain {
		t.Fatal("uncertain receipt claimed sent", r.DeliveryState, e)
	}
}
func TestGatewayReplayCanReconcileWithoutNativeResubmission(t *testing.T) {
	s, w, r := uncertainRig(t)
	w.receipt.State = "submitted"
	again, e := s.Handle(context.Background(), incomingWithEvent(r))
	if e != nil || again.DeliveryState != StateSent || again.Ref != r.Ref || len(w.sends) != 1 {
		t.Fatal("replay did not use exact native receipt", again, e, len(w.sends))
	}
}
func incomingWithEvent(r Request) Input {
	return Input{EventID: r.EventID, Actor: r.Author, Route: r.Route, Text: r.Text, TargetID: r.Target.ID, Addressed: true, At: r.EventAt, Snapshot: r.Snapshot}
}

func TestExplicitLocalContextShowsReconciledReceiptOnlyForNamedRef(t *testing.T) {
	s, w, r := uncertainRig(t)
	w.receipt.State = "submitted"
	response := httptest.NewRecorder()
	s.localHandler(response, httptest.NewRequest("GET", "http://local/context?ref="+url.QueryEscape(r.Ref)+"&offset=0", nil))
	var result LocalContext
	if e := json.Unmarshal(response.Body.Bytes(), &result); e != nil || response.Code != 200 || result.Request.Ref != r.Ref || result.Request.OperationID != r.OperationID || result.Request.DeliveryState != StateSent || len(w.sends) != 1 {
		t.Fatal(response.Code, response.Body.String(), e)
	}
	response = httptest.NewRecorder()
	s.localHandler(response, httptest.NewRequest("GET", "http://local/context?ref=other&offset=0", nil))
	if response.Code == 200 || strings.Contains(response.Body.String(), r.Text) {
		t.Fatal("unknown ref exposed another source", response.Body.String())
	}
}
func TestReconcilePendingNeverDispatchesInputs(t *testing.T) {
	s, w, first := uncertainRig(t)
	in := incoming()
	in.EventID = "second"
	in.Route.MessageID = "second"
	second, e := s.Handle(context.Background(), in)
	if e == nil {
		t.Fatal("fixture not uncertain")
	}
	w.receipts = map[string]protocol.SemanticMessageReceipt{}
	for _, r := range []Request{first, second} {
		w.receipts[r.OperationID] = protocol.SemanticMessageReceipt{OperationID: r.OperationID, Session: r.Target.ID, SessionInstance: r.Target.Instance, State: "submitted"}
	}
	if e = s.ReconcilePending(context.Background()); e != nil {
		t.Fatal(e)
	}
	if w.reads != 2 || len(w.sends) != 2 {
		t.Fatal("receipt polling dispatched inputs", w.reads, len(w.sends))
	}
	for _, r := range []Request{first, second} {
		record, e := s.Store.Request(r.Ref, s.Now())
		if e != nil || record.DeliveryState != StateSent {
			t.Fatal(record, e)
		}
	}
}

func TestPendingReceiptPollingContinuesPastSlowOperation(t *testing.T) {
	s, w, _ := uncertainRig(t)
	in := incoming()
	in.EventID = "second"
	in.Route.MessageID = "second"
	if _, e := s.Handle(context.Background(), in); e == nil {
		t.Fatal("fixture not uncertain")
	}
	w.block = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.cancel = cancel
	_ = s.ReconcilePending(ctx)
	if len(w.operations) != 1 {
		t.Fatal(w.operations)
	}
	first := w.operations[0]
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	w.cancel = cancel2
	_ = s.ReconcilePending(ctx2)
	if len(w.operations) != 2 || w.operations[1] == first {
		t.Fatal("slow receipt starved later operation", w.operations)
	}
	if len(w.sends) != 2 {
		t.Fatal("poller resent input")
	}
}
