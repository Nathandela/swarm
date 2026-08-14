package conformance_test

// PB-NET-3 over the SHIPPED PHONE PATH (ADR-007 B98 step 3, fence 1 of 3).
//
// WHY THIS FILE EXISTS. PB-NET-3 -- "the transport handles only opaque sealed frames and
// never holds content keys" -- was fenced exclusively by internal/remote/transport's
// opaque_test.go, which drives transport.Session. That type has ZERO production
// constructions (B94), so the requirement proved its property of an object that never
// ships, and the phone the user actually holds was unmeasured. B98 records that deleting
// the dead package without this file first would convert a misaimed fence into no fence.
//
// THE SUBJECT IS THE FACADE, NOT A HARNESS. The bytes asserted on are the ones
// swarmmobile.App writes to a relay through mobile/relay.go and relay.Client -- the shipped
// chain, driven through App.SendInput with a confirmed lease, exactly as a user typing
// produces them.
//
// WHY IT IS NOT VACUOUS, which is the failure mode this requirement's own evidence file
// warned about ("A test that would have been vacuous"): a recorder that captured nothing
// would pass an absence assertion perfectly. Three things are asserted together --
//
//	(1) the recorder actually captured the phone's appends (it names the wire op),
//	(2) the marker actually travelled (the MACHINE opens it and reads the plaintext back),
//	(3) and the marker is absent from every byte the phone wrote, raw and base64.
//
// (2) is what makes (3) mean "sealed" rather than "never sent".
//
// WHAT IT DOES NOT DECIDE. It observes the phone->machine direction on the input path. The
// journal and terminal directions are the gateway's seals and are fenced in remotegw; the
// push channel is PB-PUSH-3's. This file does not re-prove those.

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// pbnet3Marker is the plaintext that must never reach the relay. It is deliberately long
// and unmistakable: a short marker could collide with sealed ciphertext by chance and turn
// a real leak into a flake, or worse, a chance collision into a false alarm.
var pbnet3Marker = []byte("PB-NET-3-PHONE-PLAINTEXT-MARKER-DO-NOT-LEAK")

// wireRecorder is a websocket proxy that records every frame the PHONE writes toward the
// relay. It records payloads as the websocket layer decodes them -- already unmasked -- so
// the assertion is on what the relay receives rather than on an interface the phone offers.
type wireRecorder struct {
	srv *httptest.Server

	mu   sync.Mutex
	sent bytes.Buffer
}

func newWireRecorder(t *testing.T, upstream string) *wireRecorder {
	t.Helper()
	w := &wireRecorder{}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		down, err := websocket.Accept(rw, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = down.CloseNow() }()
		down.SetReadLimit(relay.MaxFrame + 64)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		up, _, err := websocket.Dial(ctx, upstream, nil)
		if err != nil {
			return
		}
		defer func() { _ = up.CloseNow() }()
		up.SetReadLimit(relay.MaxFrame + 64)

		done := make(chan struct{}, 2)
		go func() { // relay -> phone, not recorded: this fence is about what the phone SENDS
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := up.Read(ctx)
				if err != nil {
					return
				}
				if err := down.Write(ctx, mt, data); err != nil {
					return
				}
			}
		}()
		go func() { // phone -> relay, recorded verbatim
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := down.Read(ctx)
				if err != nil {
					return
				}
				w.mu.Lock()
				w.sent.Write(data)
				w.mu.Unlock()
				if err := up.Write(ctx, mt, data); err != nil {
					return
				}
			}
		}()
		<-done
	}))
	t.Cleanup(w.srv.Close)
	return w
}

func (w *wireRecorder) URL() string {
	return strings.Replace(w.srv.URL, "http://", "ws://", 1)
}

func (w *wireRecorder) Sent() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.sent.Bytes()...)
}

// TestPBNET3_TheShippedPhoneNeverPutsInputPlaintextOnTheWire is the behavioural half.
func TestPBNET3_TheShippedPhoneNeverPutsInputPlaintextOnTheWire(t *testing.T) {
	h := newHarness(t)

	// Re-open the phone against the recorder. Same state directory and same relay-auth key,
	// so the same mailbox: this is the process-death path openApp already exists for.
	rec := newWireRecorder(t, h.RelayURL)
	if err := h.App.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.AppRelayURL = rec.URL()
	h.App = h.openApp()
	eventually(t, "the phone never came online through the recorder", func() bool {
		st, err := h.App.ConnectionState()
		return err == nil && st == "online"
	})

	// The content key is the phone's sealing tier; without it the facade has nothing to seal
	// WITH, and the frame this fence asserts on would never be produced.
	if err := h.App.InstallContentKey(h.Keys.ContentKey[:]); err != nil {
		t.Fatalf("InstallContentKey: %v", err)
	}

	// PB-SYNC-7: a mutating op is refused until the machine publishes its rollback
	// authorities, so the lease this fence needs cannot be taken before the reconcile lands.
	h.PushReconcile()
	eventually(t, "the phone never adopted the reconcile record", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})

	if _, err := h.App.TakeControl(testSession); err != nil {
		t.Fatalf("TakeControl: %v", err)
	}
	h.AwaitCommand(protocol.ActionTakeControl)
	h.AwaitLease(testSession)

	before := len(rec.Sent())
	if err := h.App.SendInput(testSession, pbnet3Marker); err != nil {
		t.Fatalf("SendInput: %v", err)
	}

	// (2) The marker really travelled: the MACHINE opened the sealed frame and read the
	// plaintext back. Without this, "absent from the wire" is satisfied by never sending.
	in := h.AwaitInput("data")
	if !bytes.Equal(in.Data, pbnet3Marker) {
		t.Fatalf("the machine opened %q, want the marker %q -- the frame under test is not the one asserted on",
			in.Data, pbnet3Marker)
	}

	onWire := rec.Sent()

	// (1) The recorder is not blind. An absence assertion over an empty buffer proves
	// nothing, and a recorder that silently stopped proxying would produce exactly that.
	if len(onWire) == 0 {
		t.Fatal("the recorder captured NO bytes from the phone; the absence assertion below would be vacuous")
	}
	// The append rides MsgMailboxAppend, a dedicated BINARY tag rather than a named JSON op,
	// so the op name never appears on the wire. What does appear is the request body's
	// "envelope" field, and the buffer must have GROWN across the send by at least a frame.
	if !bytes.Contains(onWire, []byte("envelope")) {
		t.Fatalf("the recorder captured %d bytes but no append envelope: it is not observing the "+
			"phone's send path, so the absence assertion below would be vacuous", len(onWire))
	}
	if len(onWire) <= before+len(pbnet3Marker) {
		t.Fatalf("the recorder grew by only %d bytes across a %d-byte keystroke: the frame under test "+
			"did not pass through it, so the absence assertion below would be vacuous",
			len(onWire)-before, len(pbnet3Marker))
	}

	// (3) The property.
	if bytes.Contains(onWire, pbnet3Marker) {
		t.Errorf("PB-NET-3: the phone's input plaintext appeared VERBATIM in the %d bytes it wrote to the "+
			"relay. The relay is the declared adversary and just read a keystroke", len(onWire))
	}
	if enc := []byte(base64.StdEncoding.EncodeToString(pbnet3Marker)); bytes.Contains(onWire, enc) {
		t.Errorf("PB-NET-3: the phone's input plaintext appeared BASE64-ENCODED in the bytes it wrote to " +
			"the relay -- the relay's control frames carry binary as base64, so this is the same leak")
	}
}

// TestPBNET3_TheShippedTransportHoldsNoContentKeys is the structural half, and it is needed
// because the behavioural half alone passes an implementation that holds the content key and
// merely happens not to leak it on the one path a test drives.
//
// THE SUBJECT IS relay.Client, which is what the shipped phone actually sends through
// (mobile/relay.go), and what the old fence's transport.Session stood in for. swarmmobile.App
// is deliberately NOT a root here: the App owns phonecore.Core, which holds the content key
// BY DESIGN -- it is the thing that seals. The requirement is about the TRANSPORT.
func TestPBNET3_TheShippedTransportHoldsNoContentKeys(t *testing.T) {
	forbidden := map[reflect.Type]string{
		reflect.TypeOf(crypto.ContentKey{}): "crypto.ContentKey",
		reflect.TypeOf(crypto.WakeKey{}):    "crypto.WakeKey",
		reflect.TypeOf(crypto.EpochKeys{}):  "crypto.EpochKeys",
	}
	roots := map[string]reflect.Type{
		"relay.Client":     reflect.TypeOf((*relay.Client)(nil)).Elem(),
		"relay.ClientAuth": reflect.TypeOf(relay.ClientAuth{}),
	}
	for name, rt := range roots {
		if path := pbnet3FindForbidden(rt, forbidden, map[reflect.Type]bool{}, name, 0); path != "" {
			t.Errorf("%s holds key material at %s: the shipped transport must never hold a content key (PB-NET-3)",
				name, path)
		}
	}
}

// pbnet3FindForbidden returns the field path at which rt reaches a forbidden key type.
func pbnet3FindForbidden(rt reflect.Type, forbidden map[reflect.Type]string, seen map[reflect.Type]bool, path string, depth int) string {
	if rt == nil || depth > 6 || seen[rt] {
		return ""
	}
	seen[rt] = true
	if name, bad := forbidden[rt]; bad {
		return path + " (" + name + ")"
	}
	switch rt.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan, reflect.Map:
		return pbnet3FindForbidden(rt.Elem(), forbidden, seen, path+"[]", depth+1)
	case reflect.Struct:
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if strings.HasPrefix(f.Type.String(), "func(") {
				continue
			}
			if hit := pbnet3FindForbidden(f.Type, forbidden, seen, path+"."+f.Name, depth+1); hit != "" {
				return hit
			}
		}
	}
	return ""
}
