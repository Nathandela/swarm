// PB-NET-3 (FAILING FIRST): the transport handles only opaque sealed frames and
// never holds content keys.
//
// Two complementary assertions, because either alone is weak:
//
//   - Behavioural: a known plaintext marker sealed by the CALLER never appears in
//     the bytes the client writes to the wire. A negative control sends the same
//     marker unsealed through the same path, proving the tap would have caught it.
//
//   - Structural: no exported transport type transitively holds a crypto content
//     or wake key. A behavioural test alone passes an implementation that holds the
//     key and merely happens not to leak it on this path.
package transport_test

import (
	"bytes"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/transport"
)

// wireMarker is the plaintext that must never reach the relay.
var wireMarker = []byte("PB-NET-3-PLAINTEXT-MARKER-DO-NOT-LEAK")

// TestSealedPlaintextNeverReachesTheWire seals a marker under the caller's content
// key, sends it through the session, and asserts the marker is absent from every
// byte written to the socket -- in raw form and in the base64 form the relay's JSON
// control frames would carry it as.
func TestSealedPlaintextNeverReachesTheWire(t *testing.T) {
	srv, _ := startRelay(t, nil)
	p := newPeers(t, srv)
	tap := newWireTap(t, srv.URL())
	s := devSession(t, tap.URL(), p, nil)

	sealed := p.seal(t, 1, wireMarker)
	if err := s.SendOp(testCtx(t), p.machineRID, sealed); err != nil {
		t.Fatalf("SendOp(sealed): %v", err)
	}

	on := tap.Sent()
	if len(on) == 0 {
		t.Fatalf("wire tap recorded nothing; the test would be vacuous")
	}
	assertAbsent(t, on, wireMarker, "sealed op")
}

// TestWireTapWouldCatchAPlaintextLeak is the negative control for the test above:
// the same marker handed to the transport UNSEALED does show up on the wire. If
// this ever fails, the leak assertion is vacuous and must be repaired, not deleted.
func TestWireTapWouldCatchAPlaintextLeak(t *testing.T) {
	srv, _ := startRelay(t, nil)
	p := newPeers(t, srv)
	tap := newWireTap(t, srv.URL())
	s := devSession(t, tap.URL(), p, nil)

	if err := s.SendOp(testCtx(t), p.machineRID, wireMarker); err != nil {
		t.Fatalf("SendOp(raw): %v", err)
	}

	on := tap.Sent()
	encoded := []byte(base64.StdEncoding.EncodeToString(wireMarker))
	if !bytes.Contains(on, wireMarker) && !bytes.Contains(on, encoded) {
		t.Fatalf("the wire tap did not observe an UNSEALED marker; the PB-NET-3 leak assertion would be vacuous")
	}
}

// TestLiveFramePlaintextNeverReachesTheWire covers the second send path: live-only
// frames go through a different code path from queued ops and must be equally blind.
func TestLiveFramePlaintextNeverReachesTheWire(t *testing.T) {
	srv, _ := startRelay(t, nil)
	p := newPeers(t, srv)
	tap := newWireTap(t, srv.URL())
	s := devSession(t, tap.URL(), p, nil)

	sealed := p.seal(t, 1, wireMarker)
	if err := s.SendLive(testCtx(t), p.machineRID, sealed); err != nil {
		t.Fatalf("SendLive(sealed): %v", err)
	}
	assertAbsent(t, tap.Sent(), wireMarker, "live frame")
}

// TestInboundPlaintextIsNeverOpenedByTheTransport asserts the receive direction:
// Drain hands the caller the sealed envelope BYTES, unchanged, and never a
// plaintext. The transport has no key with which to open it, so the item it yields
// must still parse and open as a sealed envelope in the caller's hands.
func TestInboundPlaintextIsNeverOpenedByTheTransport(t *testing.T) {
	srv, _ := startRelay(t, nil)
	p := newPeers(t, srv)
	s := devSession(t, srv.URL(), p, nil)

	sealed := p.seal(t, 1, wireMarker)
	if _, err := p.machine.MailboxAppend(testCtx(t), p.deviceRID, sealed); err != nil {
		t.Fatalf("MailboxAppend: %v", err)
	}

	var got [][]byte
	if _, err := s.Drain(testCtx(t), func(it relay.Item) error {
		got = append(got, it.Envelope)
		return nil
	}); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("drained %d items, want 1", len(got))
	}
	if bytes.Contains(got[0], wireMarker) {
		t.Fatalf("the drained item carries the plaintext marker; it was not sealed")
	}
	if !bytes.Equal(got[0], sealed) {
		t.Fatalf("Drain mutated the sealed envelope; the transport must forward opaque bytes byte-for-byte")
	}
	env, err := crypto.ParseEnvelope(got[0])
	if err != nil {
		t.Fatalf("drained item is not a parseable envelope: %v", err)
	}
	pt, err := crypto.OpenMailbox(p.party.keys.ContentKey, env)
	if err != nil {
		t.Fatalf("caller could not open the drained envelope: %v", err)
	}
	if !bytes.Equal(pt, wireMarker) {
		t.Fatalf("plaintext round-trip mismatch")
	}
}

// TestTransportTypesHoldNoContentKeys walks the transport's exported types and
// fails if any field, at any depth, is a content or wake key. This is the
// structural half of "never holds content keys": it fails an implementation that
// accepts a key for a "convenience" helper even if no test exercises that helper.
func TestTransportTypesHoldNoContentKeys(t *testing.T) {
	forbidden := map[reflect.Type]string{
		reflect.TypeOf(crypto.ContentKey{}): "crypto.ContentKey",
		reflect.TypeOf(crypto.WakeKey{}):    "crypto.WakeKey",
		reflect.TypeOf(crypto.EpochKeys{}):  "crypto.EpochKeys",
	}
	roots := map[string]reflect.Type{
		"transport.Options": reflect.TypeOf(transport.Options{}),
		"transport.Session": reflect.TypeOf((*transport.Session)(nil)).Elem(),
	}
	for name, rt := range roots {
		seen := map[reflect.Type]bool{}
		if path := findForbidden(rt, forbidden, seen, name, 0); path != "" {
			t.Errorf("%s holds key material at %s: the transport must never hold a content key (PB-NET-3)", name, path)
		}
	}
}

// findForbidden returns the field path at which rt reaches a forbidden key type.
func findForbidden(rt reflect.Type, forbidden map[reflect.Type]string, seen map[reflect.Type]bool, path string, depth int) string {
	if rt == nil || depth > 6 || seen[rt] {
		return ""
	}
	seen[rt] = true
	if name, bad := forbidden[rt]; bad {
		return path + " (" + name + ")"
	}
	switch rt.Kind() {
	case reflect.Ptr, reflect.Slice, reflect.Array, reflect.Chan, reflect.Map:
		return findForbidden(rt.Elem(), forbidden, seen, path+"[]", depth+1)
	case reflect.Struct:
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if strings.HasPrefix(f.Type.String(), "func(") {
				continue
			}
			if hit := findForbidden(f.Type, forbidden, seen, path+"."+f.Name, depth+1); hit != "" {
				return hit
			}
		}
	}
	return ""
}

// assertAbsent fails if marker appears on the wire, raw or base64-encoded.
func assertAbsent(t *testing.T, on, marker []byte, what string) {
	t.Helper()
	if bytes.Contains(on, marker) {
		t.Fatalf("%s: plaintext marker appeared verbatim in %d bytes written to the relay", what, len(on))
	}
	if enc := []byte(base64.StdEncoding.EncodeToString(marker)); bytes.Contains(on, enc) {
		t.Fatalf("%s: plaintext marker appeared base64-encoded in the bytes written to the relay", what)
	}
}
