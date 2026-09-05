package relay

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func advertiseMailboxRecovery(t *testing.T, gateway *Client) {
	t.Helper()
	_, caps, err := gateway.Hello(testCtx(t), ProtocolVersion, []string{CapabilityMailboxRecovery})
	if err != nil {
		t.Fatalf("gateway recovery hello: %v", err)
	}
	if len(caps) != 1 || caps[0] != CapabilityMailboxRecovery {
		t.Fatalf("gateway recovery hello agreed %v, want %q", caps, CapabilityMailboxRecovery)
	}
}

// The destructive mailbox recovery is deliberately narrower than mailbox_ack: it can only
// compact the authenticated caller's own mailbox, it requires the exact mailbox incarnation,
// and it reports the durable cursor the caller must persist before resuming its drain.
func TestMailboxDiscard_CompactsOnlyTheAuthenticatedCallersMailbox(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, phoneA, ridA, sp := mailboxFixture(t, srv, clk)
	advertiseMailboxRecovery(t, machine)

	bPub, bPriv := newRelayAuthKey(t)
	phoneB := dialAuthed(t, srv.URL(), authFor(bPub, bPriv))
	ridB := RoutingID(bPub)
	if err := machine.AuthorizeDevice(testCtx(t), bPub, consentTo(bPriv, machine.RoutingID())); err != nil {
		t.Fatalf("authorize phone B: %v", err)
	}
	for i := uint64(1); i <= 3; i++ {
		if _, err := machine.MailboxAppend(testCtx(t), ridA, sp.sealMailbox(t, i, []byte("a"), clk)); err != nil {
			t.Fatalf("append phone A #%d: %v", i, err)
		}
		if _, err := machine.MailboxAppend(testCtx(t), ridB, sp.sealMailbox(t, i+10, []byte("b"), clk)); err != nil {
			t.Fatalf("append phone B #%d: %v", i, err)
		}
	}
	if _, err := phoneA.MailboxRead(testCtx(t), 0); err != nil { // adopt the exact incarnation
		t.Fatalf("phone A read: %v", err)
	}
	result, err := phoneA.MailboxDiscard(testCtx(t), machine.RoutingID())
	if err != nil {
		t.Fatalf("MailboxDiscard: %v", err)
	}
	if result.ThroughCursor != 3 {
		t.Fatalf("discard through_cursor = %d, want 3", result.ThroughCursor)
	}
	if result.MailboxIncarnation == "" || result.MailboxIncarnation != phoneA.MailboxIncarnation() {
		t.Fatalf("discard incarnation = %q, client holds %q", result.MailboxIncarnation, phoneA.MailboxIncarnation())
	}
	leftA, err := phoneA.MailboxRead(testCtx(t), result.ThroughCursor)
	if err != nil || len(leftA) != 0 {
		t.Fatalf("phone A after discard: items=%d err=%v, want empty", len(leftA), err)
	}
	leftB, err := phoneB.MailboxRead(testCtx(t), 0)
	if err != nil || len(leftB) != 3 {
		t.Fatalf("phone B was crossed by phone A's self-discard: items=%d err=%v", len(leftB), err)
	}
}

func TestMailboxDiscard_DownlevelGatewayCannotAuthorizeDeletion(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, phone, rid, sp := mailboxFixture(t, srv, clk)
	appendContinuityItems(t, machine, rid, sp, clk, 2)
	if _, err := phone.MailboxRead(testCtx(t), 0); err != nil {
		t.Fatalf("adopt mailbox incarnation: %v", err)
	}

	// This is the mixed-version deployment: the gateway is authenticated and online but
	// never negotiated the recovery capability, exactly as a v0.13.9 gateway behaves.
	if _, err := phone.MailboxDiscard(testCtx(t), machine.RoutingID()); !errors.Is(err, ErrPeerCapabilityUnavailable) {
		t.Fatalf("discard with downlevel gateway = %v, want ErrPeerCapabilityUnavailable", err)
	}
	items, err := phone.MailboxRead(testCtx(t), 0)
	if err != nil || len(items) != 2 {
		t.Fatalf("downlevel refusal changed mailbox: items=%d err=%v, want both items retained", len(items), err)
	}
}

func TestMailboxDiscard_DisconnectedGatewayCannotAuthorizeDeletion(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, phone, rid, sp := mailboxFixture(t, srv, clk)
	advertiseMailboxRecovery(t, machine)
	appendContinuityItems(t, machine, rid, sp, clk, 2)
	if _, err := phone.MailboxRead(testCtx(t), 0); err != nil {
		t.Fatalf("adopt mailbox incarnation: %v", err)
	}
	if err := machine.CloseNow(); err != nil {
		t.Fatalf("disconnect gateway: %v", err)
	}
	select {
	case <-machine.Done():
	case <-testCtx(t).Done():
		t.Fatal("gateway connection did not close")
	}
	// Local CloseNow/Done is not an acknowledgement of server-side removal.
	// Assert the disconnected-peer rule only after the relay observes that fact.
	awaitGatewayDrop(t, srv, machine.RoutingID())

	if _, err := phone.MailboxDiscard(testCtx(t), machine.RoutingID()); !errors.Is(err, ErrPeerCapabilityUnavailable) {
		t.Fatalf("discard with disconnected gateway = %v, want ErrPeerCapabilityUnavailable", err)
	}
	items, err := phone.MailboxRead(testCtx(t), 0)
	if err != nil || len(items) != 2 {
		t.Fatalf("disconnect refusal changed mailbox: items=%d err=%v, want both items retained", len(items), err)
	}
}

func TestMailboxDiscard_RequiresTheCurrentMailboxIncarnation(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, phone, rid, sp := mailboxFixture(t, srv, clk)
	advertiseMailboxRecovery(t, machine)
	appendContinuityItems(t, machine, rid, sp, clk, 2)

	phone.SetMailboxIncarnation("11111111111111111111111111111111")
	if _, err := phone.MailboxDiscard(testCtx(t), machine.RoutingID()); !errors.Is(err, ErrMailboxCursorResetRequired) {
		t.Fatalf("discard under retired incarnation = %v, want ErrMailboxCursorResetRequired", err)
	}
	phone.ResetMailboxIncarnation()
	items, err := phone.MailboxRead(testCtx(t), 0)
	if err != nil || len(items) != 2 {
		t.Fatalf("mismatched discard changed mailbox: items=%d err=%v", len(items), err)
	}
}

func TestMailboxDiscard_RefusesAnUnboundClientBeforeTheWire(t *testing.T) {
	client := &Client{}
	if _, err := client.MailboxDiscard(testCtx(t), "machine"); !errors.Is(err, ErrMailboxCursorResetRequired) {
		t.Fatalf("unbound discard = %v, want ErrMailboxCursorResetRequired", err)
	}
}

func TestMailboxDiscard_RefusesAnUnauthenticatedWireRequest(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	raw := dialRaw(t, srv.URL())
	_, err := raw.control(testCtx(t), "mailbox_discard", map[string]any{
		"mailbox_incarnation": "0123456789abcdef0123456789abcdef",
		"peer":                "0123456789abcdef0123456789abcdef",
	})
	if !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("unauthenticated discard = %v, want ErrNotAuthorized", err)
	}
}

func TestMailboxDiscard_IgnoresAHostileVictimTargetAndStillCompactsOnlySelf(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, phoneA, ridA, sp := mailboxFixture(t, srv, clk)
	advertiseMailboxRecovery(t, machine)
	bPub, bPriv := newRelayAuthKey(t)
	phoneB := dialAuthed(t, srv.URL(), authFor(bPub, bPriv))
	ridB := RoutingID(bPub)
	if err := machine.AuthorizeDevice(testCtx(t), bPub, consentTo(bPriv, machine.RoutingID())); err != nil {
		t.Fatalf("authorize victim phone B: %v", err)
	}
	if _, err := machine.MailboxAppend(testCtx(t), ridA, sp.sealMailbox(t, 1, []byte("self"), clk)); err != nil {
		t.Fatalf("append caller mailbox: %v", err)
	}
	if _, err := machine.MailboxAppend(testCtx(t), ridB, sp.sealMailbox(t, 2, []byte("victim"), clk)); err != nil {
		t.Fatalf("append victim mailbox: %v", err)
	}
	if _, err := phoneA.MailboxRead(testCtx(t), 0); err != nil {
		t.Fatalf("adopt caller mailbox incarnation: %v", err)
	}

	// There is deliberately no target in the production request struct. JSON's unknown-field
	// tolerance is attacked explicitly here: smuggling a victim target must not influence
	// which nested bucket the authenticated operation deletes.
	if _, err := phoneA.conn.control(testCtx(t), "mailbox_discard", map[string]any{
		"mailbox_incarnation": phoneA.MailboxIncarnation(),
		"peer":                machine.RoutingID(),
		"target":              ridB,
		"cursor":              ^uint64(0),
	}); err != nil {
		t.Fatalf("hostile self-discard request: %v", err)
	}
	if self, err := phoneA.MailboxRead(testCtx(t), 0); err != nil || len(self) != 0 {
		t.Fatalf("caller mailbox after hostile request: items=%d err=%v, want compacted self", len(self), err)
	}
	if victim, err := phoneB.MailboxRead(testCtx(t), 0); err != nil || len(victim) != 1 {
		t.Fatalf("victim mailbox after hostile target: items=%d err=%v, want untouched", len(victim), err)
	}
}

func TestMailboxDiscard_LateReplyCannotCrossALocalReset(t *testing.T) {
	client := &Client{}
	const incarnation = "0123456789abcdef0123456789abcdef"
	client.SetMailboxIncarnation(incarnation)
	generation := client.MailboxGeneration()
	client.ResetMailboxIncarnation()

	if err := client.adoptMailboxDiscard(MailboxDiscardResult{
		ThroughCursor:      53,
		MailboxIncarnation: incarnation,
	}, generation); !errors.Is(err, ErrMailboxCursorResetRequired) {
		t.Fatalf("late discard reply = %v, want reset verdict", err)
	}
	if got := client.MailboxIncarnation(); got != "" {
		t.Fatalf("late discard reply restored incarnation %q", got)
	}
}

func TestMailboxDiscard_SuccessRetiresEveryPreDiscardDeliveryGeneration(t *testing.T) {
	client := &Client{}
	const incarnation = "0123456789abcdef0123456789abcdef"
	client.SetMailboxIncarnation(incarnation)
	preDiscardGeneration := client.MailboxGeneration()

	if err := client.adoptMailboxDiscard(MailboxDiscardResult{
		ThroughCursor:      53,
		MailboxIncarnation: incarnation,
	}, preDiscardGeneration); err != nil {
		t.Fatalf("adopt successful discard: %v", err)
	}
	if got := client.MailboxGeneration(); got != preDiscardGeneration+1 {
		t.Fatalf("post-discard generation = %d, want %d", got, preDiscardGeneration+1)
	}

	// The generation mismatch must be decided before touching client.conn (nil here).
	// In production this is what prevents a read/ack token captured before the discard
	// from purging a frame appended into the now-empty mailbox afterwards.
	if err := client.MailboxAckGeneration(context.Background(), 53, preDiscardGeneration); !errors.Is(err, ErrMailboxCursorResetRequired) {
		t.Fatalf("pre-discard ack token = %v, want ErrMailboxCursorResetRequired", err)
	}
}

func TestMailboxDiscard_AnEmptyMailboxRetryIsIdempotent(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, phone, rid, sp := mailboxFixture(t, srv, clk)
	advertiseMailboxRecovery(t, machine)
	appendContinuityItems(t, machine, rid, sp, clk, 3)
	if _, err := phone.MailboxRead(testCtx(t), 0); err != nil {
		t.Fatalf("adopt mailbox incarnation: %v", err)
	}
	first, err := phone.MailboxDiscard(testCtx(t), machine.RoutingID())
	if err != nil {
		t.Fatalf("first discard: %v", err)
	}
	second, err := phone.MailboxDiscard(testCtx(t), machine.RoutingID())
	if err != nil {
		t.Fatalf("idempotent empty-mailbox retry: %v", err)
	}
	if second != first {
		t.Fatalf("retry result = %#v, want stable %#v", second, first)
	}
	items, err := phone.MailboxRead(testCtx(t), second.ThroughCursor)
	if err != nil || len(items) != 0 {
		t.Fatalf("mailbox after retry: items=%d err=%v, want empty", len(items), err)
	}
}

func TestMailboxDiscard_SerializesAtTheAppendBoundary(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.close() })
	var incarnation string
	if err := st.db.View(func(tx *bolt.Tx) error {
		incarnation = mailboxIncarnation(tx)
		return nil
	}); err != nil {
		t.Fatalf("read mailbox incarnation: %v", err)
	}

	const source = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for i := 0; i < 100; i++ {
		rid := fmt.Sprintf("%032x", i+1)
		if cursor, err := st.appendItem(rid, source, []byte("before"), 1); err != nil || cursor != 1 {
			t.Fatalf("iteration %d initial append = cursor %d err %v", i, cursor, err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var appendCursor, through uint64
		var appendErr, discardErr error
		go func() {
			defer wg.Done()
			<-start
			appendCursor, appendErr = st.appendItem(rid, source, []byte("boundary"), 2)
		}()
		go func() {
			defer wg.Done()
			<-start
			through, _, discardErr = st.discardItemsForIncarnation(rid, incarnation)
		}()
		close(start)
		wg.Wait()
		if appendErr != nil || discardErr != nil || appendCursor != 2 {
			t.Fatalf("iteration %d append/discard = cursor %d append_err %v discard_err %v", i, appendCursor, appendErr, discardErr)
		}
		items, _, reset, err := st.readItemsPage(rid, 0, 10, 4096)
		if err != nil || reset {
			t.Fatalf("iteration %d read after boundary = reset %v err %v", i, reset, err)
		}
		switch through {
		case 1: // discard committed first; the later cursor-2 append must survive.
			if len(items) != 1 || items[0].Cursor != 2 {
				t.Fatalf("iteration %d discard-before-append left %#v, want only cursor 2", i, items)
			}
		case 2: // append committed first; through_cursor includes and compacts it.
			if len(items) != 0 {
				t.Fatalf("iteration %d append-before-discard left %#v, want empty", i, items)
			}
		default:
			t.Fatalf("iteration %d discard through = %d, want serialized boundary 1 or 2", i, through)
		}
	}
}
