package relay_test

// ADR-007 B87 / residual 4.23 -- PB-PUSH-3 MEASURED AT THE CHANNEL, NOT AT A COMPONENT.
//
// PB-PUSH-3's subject is an EXTERNAL OBSERVER: "the provider observes only token, timing and
// size", and size is a benign disclosure only while it is CONSTANT. That quantifies the
// property over EVERYTHING that reaches the provider. The fence that pins it today
// (remotegw.TestPBPUSH3_WakeEnvelopeIsExactlyTheFixedContentFreeSize) has a COMPONENT as its
// subject -- one PushNotifier -- so every other producer into the same channel is unfenced by
// construction. Three producers exist and they put three shapes on the wire:
//
//	the gateway wake      crypto.SealWake over an empty plaintext -- 78 bytes of ciphertext
//	the presence sweep    PushPayload{Alert: GenericPushAlert} with NO ciphertext at all,
//	                      and it ships in NORMAL OPERATION meaning exactly "the machine went
//	                      silent" -- no adversary and no key required to read it
//	push_trigger          whatever the caller put in `envelope`; the relay applies no schema
//
// So the provider separates the two wake kinds BY SHAPE without touching crypto, which is the
// exact defeat the two-tier key design (PB-KEY-2) exists to prevent.
//
// WHY THIS FILE IS AN EXTERNAL TEST PACKAGE, and why the seam is where it is. The channel is
// the bytes that reach the provider, and those bytes are formed by internal/remote/push --
// which imports internal/remote/relay, so no in-package relay test can ever link the real
// marshaller. `package relay_test` can. Every hop below is a PRODUCTION call site: the real
// remotegw.PushNotifier, the real relay.Client, the real relay.Server handlers and sweep, the
// real push.FCM sender. Only the provider's HTTP endpoint is a loopback fake, and it records
// RAW REQUEST BYTES -- which is the one number PB-PUSH-3 concedes.
//
// THE ASSERTION IS PROPERTY-SHAPED, NOT MECHANISM-SHAPED, because several remedies are
// plausible and the tests must pass under any correct one: padding to a constant at the sink,
// giving the sweep a real sealed envelope, refusing an unschema'd trigger payload, or dropping
// the sweep's wake entirely. So the rule is: EVERY payload that reaches the provider is the
// same number of bytes. A producer that is refused puts nothing on the channel and discloses
// nothing, which satisfies it; a producer that is delivered must be indistinguishable in size
// from the canonical wake.
//
// ANTI-VACUITY IS EXPLICIT IN EVERY TEST. A harness whose pairing or token registration
// silently failed would record zero requests and "all sizes equal" would pass over nothing.
// Each test therefore requires the gateway wake to reach the provider before it judges anyone
// else, and a remedy that answers "send nothing at all" fails that arm.
//
// WHAT THIS FILE DOES NOT COVER, stated so a reader does not take it for more than it is:
//   - It says nothing about DELIVERY. PB-E2E-5 (real provider, real handset, Doze, reboot)
//     stays DEFERRED; the endpoint here is httptest on loopback and answers 200 to anything.
//   - It pins SIZE, which is the property PB-PUSH-3 concedes and the one the three producers
//     disagree on. Timing and token are separate conceded disclosures and are not measured.
//   - It measures the shapes the ENUMERATION in pbpush3_producers_test.go says exist. A fourth
//     producer is that file's job to catch; nothing here can see one.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/push"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
	"github.com/Nathandela/swarm/internal/status"
)

// --- the provider end of the channel ----------------------------------------

// providerBytes is one request as the push provider receives it: the raw body length is the
// number PB-PUSH-3 concedes, and the decoded envelope length is kept only so a failure names
// WHAT differed rather than merely that something did.
type providerBytes struct {
	producer string
	bodyLen  int
	envLen   int
}

func (p providerBytes) String() string {
	return fmt.Sprintf("%s: %d body bytes (envelope %d)", p.producer, p.bodyLen, p.envLen)
}

// fakeProvider is the push provider's HTTP surface: an OAuth token endpoint and the v1 send
// endpoint. It answers 200 to everything and records the exact bytes it was sent. It models
// the PROTOCOL and nothing about delivery (PB-E2E-5 stays deferred).
type fakeProvider struct {
	mu   sync.Mutex
	sent []providerBytes
	srv  *httptest.Server
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	p := &fakeProvider{}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-token-1","token_type":"Bearer","expires_in":3600}`))
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.sent = append(p.sent, providerBytes{bodyLen: len(raw), envLen: envelopeLenOf(raw)})
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

// envelopeLenOf digs the ciphertext length out of a recorded body FOR DIAGNOSTICS ONLY. The
// assertions are over bodyLen, because that is what the provider counts; this exists so the
// failure message can say "0 bytes of ciphertext" instead of leaving the reader to decode it.
func envelopeLenOf(raw []byte) int {
	var body struct {
		Message struct {
			Data map[string]string `json:"data"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return -1
	}
	env, err := base64.StdEncoding.DecodeString(body.Message.Data["e"])
	if err != nil {
		return -1
	}
	return len(env)
}

// since returns the requests recorded after mark, labelled with the producer that caused them.
func (p *fakeProvider) since(mark int, producer string) []providerBytes {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]providerBytes, 0, len(p.sent)-mark)
	for _, s := range p.sent[mark:] {
		s.producer = producer
		out = append(out, s)
	}
	return out
}

func (p *fakeProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sent)
}

// --- the machine end --------------------------------------------------------

type channelClock struct {
	mu sync.Mutex
	t  time.Time
}

func newChannelClock() *channelClock {
	return &channelClock{t: time.Unix(1_700_000_000, 0).UTC()}
}

func (c *channelClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *channelClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// channelSink is the journal sink the PushNotifier wraps. The gateway's mailbox path is not
// under test here; only the wake it additionally raises is.
type channelSink struct{}

func (channelSink) Snapshot([]protocol.JournalRecord, uint64) error { return nil }
func (channelSink) Event(protocol.JournalRecord) error              { return nil }
func (channelSink) Terminal(protocol.TerminalViewV1) error          { return nil }

// channelPrefs enables both push categories, so a suppressed wake is never mistaken for a
// constant-size one.
type channelPrefs struct{}

func (channelPrefs) LoadPrefs() (remotegw.PushPrefs, error) {
	return remotegw.PushPrefs{Version: 1, NeedsInput: true, Finished: true}, nil
}
func (channelPrefs) SavePrefs(remotegw.PushPrefs) error { return nil }

// pushChannel is a whole live path: machine -> relay -> FCM sender -> provider.
type pushChannel struct {
	srv        *relay.Server
	clk        *channelClock
	provider   *fakeProvider
	machine    *relay.Client
	machineRID string
	device     *relay.Client
	deviceRID  string
	wakeKey    crypto.WakeKey
}

func newPushChannel(t *testing.T) *pushChannel {
	t.Helper()
	provider := newFakeProvider(t)

	acct, err := push.LoadServiceAccount(channelServiceAccount(t, provider.srv.URL+"/token"))
	if err != nil {
		t.Fatalf("LoadServiceAccount: %v", err)
	}
	// The REAL sender: it is what turns a PushPayload into provider-bound bytes, and it is
	// the reason this file cannot live in package relay.
	sender, err := push.NewFCM(push.FCMConfig{
		Account:    acct,
		BaseURL:    provider.srv.URL,
		RetryDelay: 0, // no real sleep in the suite
	})
	if err != nil {
		t.Fatalf("NewFCM: %v", err)
	}

	cfg := relay.DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.TLSMode = "off"
	cfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	cfg.PresenceTimeout = time.Second

	clk := newChannelClock()
	srv, err := relay.New(cfg, relay.WithClock(clk), relay.WithPushSink(sender))
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("relay.Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	dPub, dPriv := channelKey(t)
	device := channelDial(t, srv.URL(), dPub, dPriv)
	if err := device.TokenRegister(channelCtx(t), "fcm-token-handset"); err != nil {
		t.Fatalf("TokenRegister: %v", err)
	}

	mPub, mPriv := channelKey(t)
	machine := channelDial(t, srv.URL(), mPub, mPriv)
	if err := machine.AuthorizeDevice(channelCtx(t), dPub, channelConsent(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}

	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("NewEpochKeys: %v", err)
	}
	return &pushChannel{
		srv:        srv,
		clk:        clk,
		provider:   provider,
		machine:    machine,
		machineRID: machine.RoutingID(),
		device:     device,
		deviceRID:  device.RoutingID(),
		wakeKey:    keys.WakeKey,
	}
}

func channelCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func channelKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return pub, priv
}

func channelDial(t *testing.T, url string, pub ed25519.PublicKey, priv ed25519.PrivateKey) *relay.Client {
	t.Helper()
	c, err := relay.Dial(channelCtx(t), url, relay.ClientAuth{
		RelayAuthPub: pub,
		Sign:         func(challenge []byte) ([]byte, error) { return ed25519.Sign(priv, challenge), nil },
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// channelConsent is the device's own consent for the machine's routing id, the credential
// handleAuthorizeDevice verifies before it records a pairing. Production obtains it in the
// SAS-authenticated ceremony; signing the same statement with the same key produces identical
// wire bytes.
func channelConsent(devPriv ed25519.PrivateKey, machineRID string) []byte {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic("relay channel test: crypto/rand: " + err.Error())
	}
	ceremonyID := hex.EncodeToString(id[:])
	return relay.MarshalConsent(ceremonyID, ed25519.Sign(devPriv, relay.ConsentMessage(ceremonyID, machineRID)))
}

// channelServiceAccount is a syntactically real credential over a throwaway RSA key. It
// authorises nothing anywhere: the loopback endpoint never verifies the assertion and no
// Google project exists.
func channelServiceAccount(t *testing.T, tokenURI string) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	doc, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "swarm-test-project",
		"private_key_id": "kid-1",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "pusher@swarm-test-project.iam.gserviceaccount.com",
		"token_uri":      tokenURI,
	})
	if err != nil {
		t.Fatalf("marshal service account: %v", err)
	}
	return doc
}

// --- the three producers ----------------------------------------------------

// gatewayWake drives PRODUCER 1 through the real remotegw.PushNotifier: a needs_input
// transition on a live journal, sealed under the wake key and handed to the relay through the
// real client. This is the shape PB-PUSH-3's existing fence pins, and it is the canonical one
// every other producer must be indistinguishable from.
func (c *pushChannel) gatewayWake(t *testing.T, session string) []providerBytes {
	t.Helper()
	mark := c.provider.count()
	n := remotegw.NewPushNotifier(channelSink{}, remotegw.PushConfig{
		Pusher:  c.machine,
		Target:  c.deviceRID,
		WakeKey: c.wakeKey,
		EpochID: 7,
		Now:     c.clk.Now,
		Prefs:   channelPrefs{},
	})
	if err := n.Event(protocol.JournalRecord{
		Cursor: 1, SessionID: session, Type: "status", Group: status.GroupNeedsInput,
	}); err != nil {
		t.Fatalf("gateway Event: %v", err)
	}
	if err := n.Err(); err != nil {
		t.Fatalf("the gateway's push path failed: %v -- the harness is not measuring a channel", err)
	}
	return c.provider.since(mark, "gateway wake")
}

// presenceSweep drives PRODUCER 2: the relay's own machine-went-silent wake. It needs no
// adversary and no attacker-chosen input -- it is what the relay does when a socket drops,
// which is normal operation.
//
// IT WAITS ON THE TRANSITION, NOT ON A PUSH, and the difference is the difference between a
// fence and a fence-shaped nothing. The relay decides when it has observed the drop, so a
// loop that gave up after N tries would, on a loaded machine, return an empty set that
// "all sizes equal" accepts -- reporting the defect fixed because the sweep never ran. The
// exit condition is therefore the relay declaring the machine OFFLINE, which is the same
// SweepPresence pass that makes the push decision. Whether it pushed is then the measurement.
func (c *pushChannel) presenceSweep(t *testing.T) []providerBytes {
	t.Helper()
	mark := c.provider.count()
	ctx := channelCtx(t)
	_ = c.machine.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.clk.advance(2 * time.Second)
		c.srv.SweepPresence(ctx)
		// Read through the DEVICE, which is still connected and paired: the sweep's push
		// (if any) is delivered inside the SweepPresence call above, so a machine that reads
		// offline has already been through the push decision.
		if info, err := c.device.Presence(ctx, c.machineRID); err == nil && info.State == relay.PresenceOffline {
			return c.provider.since(mark, "presence sweep")
		}
		if !time.Now().Before(deadline) {
			t.Fatal("the relay never transitioned the machine to offline, so SweepPresence never " +
				"reached its push decision. This test would then report nothing either way -- an " +
				"empty channel satisfies every size assertion below")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// unschemadTrigger drives PRODUCER 3: push_trigger with a caller-chosen envelope. The relay
// applies NO schema to that field, so any size the framing admits reaches the provider.
//
// The sizes are swept rather than sampled at one point, because a remedy that only bounds the
// MAXIMUM would pass a single 4096-byte probe while leaving every shorter shape distinguishable.
func (c *pushChannel) unschemadTrigger(t *testing.T, size int) []providerBytes {
	t.Helper()
	mark := c.provider.count()
	env := make([]byte, size)
	for i := range env {
		env[i] = byte(i)
	}
	// A refusal is a PASS for this property: an envelope the relay declines puts nothing on
	// the channel. What must not happen is a delivery of a different size.
	_ = c.machine.PushTrigger(channelCtx(t), c.deviceRID, env)
	return c.provider.since(mark, "push_trigger envelope of "+strconv.Itoa(size)+" bytes")
}

// --- the invariant ----------------------------------------------------------

// requireCanonicalWake is the ANTI-VACUITY arm every test runs first. If the gateway wake does
// not reach the provider, the harness is broken (or the remedy silenced push altogether) and
// every "all sizes equal" assertion below would pass over an empty set.
func requireCanonicalWake(t *testing.T, got []providerBytes) int {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("the gateway wake put %d requests on the provider channel, want exactly 1: "+
			"with no canonical wake on the wire there is nothing for the other producers to be "+
			"compared against, and every size assertion in this file would pass vacuously", len(got))
	}
	if got[0].bodyLen == 0 {
		t.Fatalf("the gateway wake reached the provider as an EMPTY body; the harness is not measuring the channel")
	}
	return got[0].bodyLen
}

// assertConstantShape is PB-PUSH-3 stated over the channel: every payload that reaches the
// provider is the same number of bytes as the canonical wake.
func assertConstantShape(t *testing.T, want int, observed []providerBytes) {
	t.Helper()
	var offenders []string
	for _, o := range observed {
		if o.bodyLen != want {
			offenders = append(offenders, o.String())
		}
	}
	if len(offenders) == 0 {
		return
	}
	sort.Strings(offenders)
	t.Errorf("%d payload(s) reached the push provider at a size the canonical wake does not have "+
		"(canonical = %d body bytes):\n  %s\n\n"+
		"PB-PUSH-3 concedes the provider observes token, timing and SIZE, and size is a benign "+
		"disclosure only while it is CONSTANT. A second shape lets the provider separate WHAT "+
		"happened from THAT something happened without touching crypto -- which is the exact "+
		"defeat the two-tier wake/content key split exists to prevent (PB-KEY-2, ADR-007 B87). "+
		"Any remedy is acceptable: pad to a constant, give the producer a real sealed envelope, "+
		"or refuse it outright -- a refused payload puts nothing on the channel and discloses "+
		"nothing. What is not acceptable is delivering it at a different length.",
		len(offenders), want, joinLines(offenders))
}

func joinLines(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += "\n  "
		}
		out += v
	}
	return out
}

// TestPBPUSH3_ThePresenceSweepIsTheSameSizeOnTheChannelAsAWake is the producer that needs no
// adversary. SweepPresence sends PushPayload{Alert: GenericPushAlert} with NO ciphertext, and
// it fires in normal operation whenever a machine's socket drops -- so the provider gets a
// keyless, reliable signal that means exactly one thing: the machine went silent.
func TestPBPUSH3_ThePresenceSweepIsTheSameSizeOnTheChannelAsAWake(t *testing.T) {
	c := newPushChannel(t)
	want := requireCanonicalWake(t, c.gatewayWake(t, "build-box-17.local/refactor-the-auth-middleware"))
	assertConstantShape(t, want, c.presenceSweep(t))
}

// TestPBPUSH3_AnUnschemadTriggerEnvelopeIsTheSameSizeOnTheChannel is the producer with no
// schema at all: handlePushTrigger copies the caller's `envelope` field into the payload
// unexamined, so the size of what the provider sees is chosen by whoever calls push_trigger.
//
// It probes several sizes because the disclosure is a DIFFERENCE, not a magnitude: a remedy
// that only caps the maximum leaves every shape below the cap separable.
//
// 77 is in the list and is EXPECTED to pass even unremedied, which is worth knowing rather
// than worth hiding: the sender base64s the envelope, and base64 quantises to 3-byte groups,
// so 77 and 78 bytes of ciphertext are the same number of bytes to the provider. The property
// is about what the provider COUNTS, so that is the correct answer -- and it is why every
// assertion here is over raw request bytes rather than over len(PushPayload.Ciphertext).
func TestPBPUSH3_AnUnschemadTriggerEnvelopeIsTheSameSizeOnTheChannel(t *testing.T) {
	c := newPushChannel(t)
	want := requireCanonicalWake(t, c.gatewayWake(t, "m/s1"))

	// REACHABILITY, before anything is judged. A wake-shaped envelope through push_trigger must
	// arrive at the canonical size -- that is the path the gateway itself uses, so no correct
	// remedy can close it. Without this arm a remedy that refused EVERY trigger would leave the
	// sweep below with nothing to observe and this test would pass over an empty set.
	canonical := c.unschemadTrigger(t, remotegw.PushWakeEnvelopeSize)
	if len(canonical) != 1 || canonical[0].bodyLen != want {
		t.Fatalf("a wake-shaped (%d-byte) envelope through push_trigger produced %v, want exactly "+
			"one request of %d body bytes: push_trigger is the gateway's own path onto the channel, "+
			"so this test is not reaching the producer it names",
			remotegw.PushWakeEnvelopeSize, canonical, want)
	}

	// A payload the relay or the sink REFUSES puts nothing on the channel and is allowed here.
	var observed []providerBytes
	for _, size := range []int{0, 1, 77, 79, 512, 4096} {
		observed = append(observed, c.unschemadTrigger(t, size)...)
	}
	assertConstantShape(t, want, observed)
}

// TestPBPUSH3_EveryProducerIsTheSameSizeOnTheProviderChannel is the requirement's own
// quantifier: over EVERYTHING that reaches the provider, not over one component's output. It
// drives all three producers into one channel and judges the whole set.
//
// It is deliberately not a superset-of-the-other-two convenience: the other two fail
// individually so each producer is attributable, and this one is what a reviewer reads to see
// the property stated the way PB-PUSH-3 states it.
func TestPBPUSH3_EveryProducerIsTheSameSizeOnTheProviderChannel(t *testing.T) {
	c := newPushChannel(t)

	observed := c.gatewayWake(t, "build-box-17.local/refactor-the-auth-middleware")
	want := requireCanonicalWake(t, observed)

	// A second wake, for a differently-shaped session: the existing component fence already
	// covers this, and it is here so a failure distinguishes "the gateway varies" from "the
	// other producers differ".
	second := c.gatewayWake(t, "m/x")
	if len(second) != 1 {
		t.Fatalf("the second gateway wake put %d requests on the channel, want exactly 1: the two "+
			"wakes are what make this a comparison rather than a single observation", len(second))
	}
	observed = append(observed, second...)
	observed = append(observed, c.unschemadTrigger(t, 4096)...)
	observed = append(observed, c.presenceSweep(t)...)

	assertConstantShape(t, want, observed)
}
