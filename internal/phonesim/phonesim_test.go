package phonesim

// Phone-side mailbox-consumer tests (committee re-audit findings). The phone reads ONE
// relay mailbox that multiplexes journal records, terminal snapshots, command replies,
// and the recipient-sealed bootstrap frame; the relay is the untrusted adversary that
// decides what each read returns. These tests drive the phone with a scripted relay to
// pin three behaviours: Observe and ReadReply share ONE drain (a reply Observe pulls off
// the shared cursor is still returned by ReadReply, and a journal frame is never dropped);
// NewFromMailbox loops across bounded pages; and NewFromMailbox skips a poison bootstrap
// frame and opens the first VALID grant.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
)

const testEpoch = uint32(1)

// fakeRelay is a scripted stand-in for *relay.Client: an in-memory, cursor-ordered
// mailbox the phone drains. pageSize bounds each MailboxReadPage, so a test can force a
// frame onto a LATER page exactly as the real bounded relay page does (CR-4). pageSize<=0
// means one unbounded page. It is the untrusted adversary: it decides what each read hands
// back.
type fakeRelay struct {
	items    []relay.Item // cursor-ascending
	pageSize int
	acked    uint64
	appends  [][]byte
}

func (f *fakeRelay) MailboxReadPage(_ context.Context, cursor uint64, limit int) ([]relay.Item, bool, error) {
	n := limit
	if n <= 0 {
		n = f.pageSize
	}
	var rem []relay.Item
	for _, it := range f.items {
		if it.Cursor > cursor {
			rem = append(rem, it)
		}
	}
	if n > 0 && len(rem) > n {
		return rem[:n], true, nil
	}
	return rem, false, nil
}

func (f *fakeRelay) MailboxAppend(_ context.Context, _ string, env []byte) (uint64, error) {
	f.appends = append(f.appends, env)
	return 0, nil
}

func (f *fakeRelay) MailboxAck(_ context.Context, cursor uint64) error {
	f.acked = cursor
	return nil
}

// sealJournal seals a kind-less JournalRecord under key. The router decodes a kind-less
// plaintext straight into the session cache (byte-identical to the journal path), so this
// stands in for a gateway-sealed roster/journal frame.
func sealJournal(t *testing.T, key crypto.ContentKey, seq uint64, rec protocol.JournalRecord) []byte {
	t.Helper()
	pt, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal journal record: %v", err)
	}
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{Version: crypto.VersionV1, EpochID: testEpoch, Seq: seq}, pt)
	if err != nil {
		t.Fatalf("seal journal frame: %v", err)
	}
	return env.Marshal()
}

// validBootstrap mints a real recipient-sealed, machine-signed grant (SealEpochGrant
// bypasses the pairing dance) and wraps it in the tagged bootstrap frame the gateway
// appends. It returns the frame plus the keystore + pinned sign pub the phone needs to
// open it, and the epoch keys so a test can assert the recovered ContentKey.
func validBootstrap(t *testing.T) (frame []byte, ks crypto.KeyStore, signPub ed25519.PublicKey, keys crypto.EpochKeys) {
	t.Helper()
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("phone keystore: %v", err)
	}
	signPub, signPriv, _ := ed25519.GenerateKey(nil)
	keys, err = crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	g, err := crypto.SealEpochGrant(signPriv, ks.RecipientPublic(), testEpoch, 1, keys)
	if err != nil {
		t.Fatalf("seal epoch grant: %v", err)
	}
	frame, err = grant.MarshalBootstrap(g)
	if err != nil {
		t.Fatalf("marshal bootstrap frame: %v", err)
	}
	return frame, ks, signPub, keys
}

// FINDING 1 (C8): Observe and ReadReply are two consumers of ONE shared cursor. An Observe
// that runs first drains the reply into the router's reply cache AND advances the cursor
// past it; ReadReply must still return that reply (drain the reply cache), and the journal
// frame Observe also drained must remain in the session cache. Before the fix ReadReply
// re-scanned raw items from the already-advanced cursor and found nothing, so the reply was
// lost.
func TestPhone_ObserveThenReadReply_ReturnsReplyAndKeepsJournal(t *testing.T) {
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	key := keys.ContentKey

	journal := sealJournal(t, key, 1, protocol.JournalRecord{Cursor: 1, SessionID: "sess-A"})
	reply, err := remotegw.SealControlReply(key, testEpoch, 2, protocol.Control{Op: protocol.OpOK, OperationID: "op-1"})
	if err != nil {
		t.Fatalf("seal control reply: %v", err)
	}
	fake := &fakeRelay{items: []relay.Item{
		{Cursor: 1, Envelope: journal},
		{Cursor: 2, Envelope: reply},
	}}

	phone := &Phone{
		relay:   fake,
		router:  phonecore.NewMailboxRouter(key),
		content: key,
		epochID: testEpoch,
	}

	// OBSERVE FIRST: this drains BOTH frames off the one shared cursor -- journal into the
	// session cache, reply into the router reply cache -- and advances the cursor past both.
	if _, err := phone.Observe(context.Background()); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if cs, ok := phone.Session("sess-A"); !ok || !cs.Present {
		t.Fatalf("journal frame lost from the session cache: present=%v ok=%v", cs.Present, ok)
	}

	// READREPLY must still return the reply Observe already drained -- not miss it because the
	// shared cursor advanced past the frame (the C8 hang).
	ctrl, ok, err := phone.ReadReply(context.Background())
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if !ok {
		t.Fatal("reply lost: Observe drained it into the reply cache but ReadReply never returned it (C8 -- two consumers, one cursor)")
	}
	if ctrl.Op != protocol.OpOK || ctrl.OperationID != "op-1" {
		t.Fatalf("wrong reply returned: op=%q operation_id=%q", ctrl.Op, ctrl.OperationID)
	}
}

// FINDING 2: NewFromMailbox must loop across bounded pages. A gateway restart can re-append
// a fresh bootstrap at the tail, beyond the first page; a single read misses it and returns
// errNoBootstrap.
func TestPhone_NewFromMailbox_FindsBootstrapOnLaterPage(t *testing.T) {
	bootstrap, ks, signPub, keys := validBootstrap(t)

	// pageSize 2 with two non-bootstrap head frames puts the real bootstrap on page TWO.
	fake := &fakeRelay{
		pageSize: 2,
		items: []relay.Item{
			{Cursor: 1, Envelope: []byte("not-a-bootstrap-1")},
			{Cursor: 2, Envelope: []byte("not-a-bootstrap-2")},
			{Cursor: 3, Envelope: bootstrap},
		},
	}
	cfg := Config{KeyStore: ks, MachineSignPub: signPub, Relay: fake}

	phone, err := NewFromMailbox(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewFromMailbox missed the bootstrap on a later page: %v", err)
	}
	if phone.content != keys.ContentKey {
		t.Fatal("phone bootstrapped with the wrong ContentKey")
	}
}

// FINDING 3: NewFromMailbox must skip a poison bootstrap frame (well-formed JSON shape that
// grant.ParseBootstrap accepts, but which no key opens) and continue to the first VALID
// grant. A hostile relay planting one poison frame ahead of the real one must not block
// pairing forever. pageSize 0 keeps both frames on one page, so paging is not the variable.
func TestPhone_NewFromMailbox_SkipsPoisonBootstrap(t *testing.T) {
	bootstrap, ks, signPub, keys := validBootstrap(t)

	poison, err := grant.MarshalBootstrap(&crypto.EpochGrant{}) // shape-valid, unopenable
	if err != nil {
		t.Fatalf("marshal poison bootstrap: %v", err)
	}
	fake := &fakeRelay{items: []relay.Item{
		{Cursor: 1, Envelope: poison},
		{Cursor: 2, Envelope: bootstrap},
	}}
	cfg := Config{KeyStore: ks, MachineSignPub: signPub, Relay: fake}

	phone, err := NewFromMailbox(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewFromMailbox blocked by a poison bootstrap frame: %v", err)
	}
	if phone.content != keys.ContentKey {
		t.Fatal("phone bootstrapped with the wrong ContentKey (did not open the real grant)")
	}
}
