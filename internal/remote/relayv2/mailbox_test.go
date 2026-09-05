package relayv2

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/coder/websocket"
)

const (
	testMachineRID  = "88564c8ede170d2ed321e21e61354184"
	testPhoneRID    = "6019466df50bcada1f8bcd23f7a9e4ee"
	testIncarnation = "AAAAAAAAAAAAAAAAAAAAAA"
)

func machineSubscription() (*Conn, *Subscription) {
	binding := Binding{MachineRID: testMachineRID, PeerRID: testPhoneRID, Generation: 7}
	c := &Conn{
		role: RoleMachine, purpose: PurposeStream, machineRID: testMachineRID, rid: testMachineRID,
		deliveries: make(chan queuedFrame, 64), done: make(chan struct{}),
	}
	return c, &Subscription{conn: c, binding: binding, peer: testPhoneRID, incarnation: testIncarnation}
}

func queueDelivery(t *testing.T, c *Conn, cursor uint64, ciphertext []byte, size int) {
	t.Helper()
	if !c.enqueueDelivery(queuedFrame{size: size, frame: wireFrame{
		Type: "DELIVER", PeerRID: testPhoneRID, Generation: "7", Incarnation: testIncarnation,
		Cursor: formatUint64(cursor), MessageID: "m" + formatUint64(cursor), Ciphertext: encode64(ciphertext),
	}}) {
		t.Fatalf("enqueue cursor %d", cursor)
	}
}

func TestMachineMailboxRetainsUnconsumedBatchAndDrainsQueuedTail(t *testing.T) {
	c, sub := machineSubscription()
	queueDelivery(t, c, 1, []byte("poison"), 10)
	mailbox, err := NewMachineMailbox(sub)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := mailbox.MailboxWait(context.Background(), 0)
	if err != nil || len(first) != 1 {
		t.Fatalf("first wait = (%v, %v)", first, err)
	}
	first[0].Envelope[0] = 'X'
	queueDelivery(t, c, 2, []byte("good"), 11)
	again, err := mailbox.MailboxRead(context.Background(), 0)
	if err != nil || len(again) != 2 || string(again[0].Envelope) != "poison" || string(again[1].Envelope) != "good" {
		t.Fatalf("reread + queued tail = (%q, %v)", []string{string(again[0].Envelope), string(again[1].Envelope)}, err)
	}

	backing := mailbox.retained[:cap(mailbox.retained)]
	if _, err := mailbox.MailboxRead(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	for i := range backing {
		if backing[i].item.Cursor != 0 || backing[i].item.Envelope != nil || backing[i].size != 0 {
			t.Fatalf("released retained backing slot %d still holds %+v", i, backing[i])
		}
	}
}

func TestMachineMailboxRetainedFramesShareConnBoundsWithQueuedTail(t *testing.T) {
	c, sub := machineSubscription()
	mailbox, err := NewMachineMailbox(sub)
	if err != nil {
		t.Fatal(err)
	}
	for cursor := uint64(1); cursor <= 64; cursor++ {
		queueDelivery(t, c, cursor, []byte{byte(cursor)}, 1)
	}
	if items, err := mailbox.MailboxRead(context.Background(), 0); err != nil || len(items) != 64 {
		t.Fatalf("drain 64 = (%d, %v)", len(items), err)
	}
	if c.enqueueDelivery(queuedFrame{size: 1}) {
		t.Fatal("65th queued+retained delivery was admitted")
	}
	if _, err := mailbox.MailboxRead(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	queueDelivery(t, c, 65, []byte("x"), 1)

	c2, sub2 := machineSubscription()
	mailbox2, _ := NewMachineMailbox(sub2)
	queueDelivery(t, c2, 1, []byte("one"), maxQueuedEventBytes/2+1)
	if _, err := mailbox2.MailboxRead(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if c2.enqueueDelivery(queuedFrame{size: maxQueuedEventBytes / 2}) {
		t.Fatal("queued+retained bytes above 1 MiB were admitted")
	}
}

func TestMachineMailboxCancellationAndZeroAck(t *testing.T) {
	_, sub := machineSubscription()
	mailbox, err := NewMachineMailbox(sub)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := mailbox.MailboxWait(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v", err)
	}
	if err := mailbox.MailboxAck(context.Background(), 0); err == nil {
		t.Fatal("zero ACK reached the subscription")
	}
}

func TestInvalidDeliveryFailsConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		body, _ := json.Marshal(map[string]any{
			"v": 2, "type": "DELIVER", "request_id": "delivery-1", "peer_rid": testPhoneRID,
			"generation": "7", "incarnation": testIncarnation, "cursor": "1", "msg_id": "m1", "ciphertext": "not+cannonical",
		})
		_ = ws.Write(r.Context(), websocket.MessageText, body)
		<-r.Context().Done()
	}))
	defer server.Close()
	c, err := dialRaw(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.role, c.purpose, c.machineRID, c.rid = RoleMachine, PurposeStream, testMachineRID, testMachineRID
	mailbox, err := NewMachineMailbox(&Subscription{
		conn: c, binding: Binding{MachineRID: testMachineRID, PeerRID: testPhoneRID, Generation: 7},
		peer: testPhoneRID, incarnation: testIncarnation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := mailbox.MailboxWait(context.Background(), 0); err == nil {
		t.Fatal("invalid delivery was silently evicted")
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("invalid delivery did not fail the connection")
	}
}

func TestMailboxMessageIDIsExactCiphertextSHA256(t *testing.T) {
	type captured struct {
		MessageID  string `json:"msg_id"`
		Ciphertext string `json:"ciphertext"`
		RequestID  string `json:"request_id"`
	}
	got := make(chan captured, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		_, body, err := ws.Read(r.Context())
		if err != nil {
			return
		}
		var request captured
		if json.Unmarshal(body, &request) != nil {
			return
		}
		got <- request
		response, _ := json.Marshal(map[string]any{
			"v": 2, "type": "APPENDED", "request_id": request.RequestID,
			"peer_rid": testPhoneRID, "generation": "7", "cursor": "1", "deduped": false,
		})
		_ = ws.Write(r.Context(), websocket.MessageText, response)
	}))
	defer server.Close()
	c, err := dialRaw(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.role, c.purpose, c.machineRID, c.rid = RoleMachine, PurposeStream, testMachineRID, testMachineRID
	binding := Binding{MachineRID: testMachineRID, PeerRID: testPhoneRID, Generation: 7}
	mailbox, err := NewMachineMailbox(&Subscription{conn: c, binding: binding, peer: testPhoneRID, incarnation: testIncarnation})
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := []byte{0, 1, 2, 3, 0xff}
	if _, err := mailbox.MailboxAppend(context.Background(), testPhoneRID, ciphertext); err != nil {
		t.Fatal(err)
	}
	request := <-got
	sum := sha256.Sum256(ciphertext)
	if request.MessageID != base64.RawURLEncoding.EncodeToString(sum[:]) || request.Ciphertext != base64.RawURLEncoding.EncodeToString(ciphertext) {
		t.Fatalf("append = %+v", request)
	}
	if _, err := mailbox.MailboxAppend(context.Background(), testMachineRID, ciphertext); err == nil {
		t.Fatal("adapter appended to a target outside its binding")
	}
}

func TestProtocolErrorClassifiesOnlyStableGatewayOutcomes(t *testing.T) {
	for code, target := range map[string]error{
		"mailbox_full": relay.ErrQuotaExceeded, "cleanup_pending": relay.ErrQuotaExceeded,
		"rate_limited": relay.ErrQuotaExceeded, "consent_retired": relay.ErrConsentRetired,
		"invalid_consent": relay.ErrConsentMalformed, "not_authorized": relay.ErrNotAuthorized,
		"stale_generation": relay.ErrNotAuthorized,
	} {
		if !errors.Is(&ProtocolError{Code: code}, target) {
			t.Errorf("code %q does not classify as %v", code, target)
		}
	}
	unknown := &ProtocolError{Code: "future_error"}
	for _, target := range []error{relay.ErrQuotaExceeded, relay.ErrConsentRetired, relay.ErrConsentMalformed, relay.ErrNotAuthorized} {
		if errors.Is(unknown, target) {
			t.Errorf("unknown code classified as %v", target)
		}
	}
}
