package conformance_test

// The S17 push rig: a REAL relay, the REAL FCM v1 sender, a FAKE FCM endpoint on loopback,
// and the phone facade over a real state directory.
//
// WHY A SECOND RIG RATHER THAN newHarness. PB-PUSH-9's stated acceptance is "an end-to-end
// test with a fake FCM and a real relay: rotate the token and assert delivery still works;
// restart the relay and assert re-registration restores it". The shared harness builds its
// relay with NO push sink, so a push has nowhere to land and is unobservable, and it offers no
// way to restart the relay -- both of which this requirement needs by name. Nothing here
// modifies the shared harness; another slice is using it in this tree.
//
// WHAT "FAKE FCM" MEANS HERE, said before the assertions rather than after. The endpoint is an
// httptest.Server on loopback that speaks the FCM v1 protocol: it issues an OAuth access token
// and accepts POST /v1/projects/{p}/messages:send. The SENDER above it is the real
// internal/remote/push.FCM, so the request that arrives is the request Google would receive,
// including the data block's one key `"e"`. That models the PROTOCOL. It models nothing about
// Google: no account exists in this project, no request leaves the loopback interface, and
// PB-E2E-5 -- real FCM delivery, real Doze, a real handset -- remains DEFERRED under section 13
// and is not claimed by any test in this package.
//
// The one thing the rig deliberately does NOT fake is the base64 hop. The phone is handed
// whatever the fake endpoint received, decoded exactly as the Android side decodes it, so the
// payload contract between internal/remote/push and the FirebaseMessagingService is measured
// rather than assumed. A test that hand-fed the phone an envelope it built itself would pass
// with the two ends disagreeing about the data key.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/push"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
	"github.com/Nathandela/swarm/internal/status"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

const (
	s17Machine = "machine-endpoint-s17"
	s17Epoch   = uint32(11)
)

// ---------------------------------------------------------------------------
// The fake provider.
// ---------------------------------------------------------------------------

// s17Delivery is one message the provider was asked to deliver, as the provider sees it: a
// device token and the base64 the Android data block will carry.
type s17Delivery struct {
	Token   string
	Data    map[string]string
	Android map[string]any
}

// s17FakeFCM is an FCM v1 endpoint on loopback. It authorises nothing and verifies no
// assertion; it records.
type s17FakeFCM struct {
	srv *httptest.Server

	mu         sync.Mutex
	deliveries []s17Delivery
	// unregistered is the set of tokens the provider reports as dead, so a test can drive the
	// one verdict the relay acts on (relay.ErrPushUnregistered -> prune).
	unregistered map[string]bool
}

func s17NewFakeFCM(t *testing.T) *s17FakeFCM {
	t.Helper()
	f := &s17FakeFCM{unregistered: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"s17-access-token","expires_in":3600,"token_type":"Bearer"}`)
	})
	mux.HandleFunc("/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var msg struct {
			Message struct {
				Token   string            `json:"token"`
				Data    map[string]string `json:"data"`
				Android map[string]any    `json:"android"`
			} `json:"message"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		dead := f.unregistered[msg.Message.Token]
		if !dead {
			f.deliveries = append(f.deliveries, s17Delivery{
				Token: msg.Message.Token, Data: msg.Message.Data, Android: msg.Message.Android,
			})
		}
		f.mu.Unlock()
		if dead {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"status":"NOT_FOUND","message":"gone",`+
				`"details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError",`+
				`"errorCode":"UNREGISTERED"}]}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"projects/swarm-test-project/messages/1"}`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *s17FakeFCM) Deliveries() []s17Delivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]s17Delivery(nil), f.deliveries...)
}

func (f *s17FakeFCM) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliveries = nil
}

// MarkUnregistered makes the provider answer UNREGISTERED for token, which is the one verdict
// the relay acts on: it prunes. A rotated token's PREDECESSOR is exactly this case on a real
// handset.
func (f *s17FakeFCM) MarkUnregistered(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unregistered[token] = true
}

// s17ServiceAccount is a syntactically real credential over a freshly generated RSA key. It
// authorises nothing anywhere: the fake endpoint never verifies the assertion and no Google
// project exists.
func s17ServiceAccount(t *testing.T, tokenURI string) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("x509.MarshalPKCS8PrivateKey: %v", err)
	}
	doc, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "swarm-test-project",
		"private_key_id": "s17",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "pusher@swarm-test-project.iam.gserviceaccount.com",
		"token_uri":      tokenURI,
	})
	if err != nil {
		t.Fatalf("marshal service account: %v", err)
	}
	return doc
}

// ---------------------------------------------------------------------------
// The rig.
// ---------------------------------------------------------------------------

type s17Rig struct {
	t   *testing.T
	ctx context.Context

	Dir     string
	CoreDir string
	Custody *testCustody
	App     *swarmmobile.App
	Keys    crypto.EpochKeys

	FCM      *s17FakeFCM
	relayCfg relay.Config
	relaySrv *relay.Server
	RelayURL string

	phoneTarget  string
	machinePub   ed25519.PublicKey
	machinePriv  ed25519.PrivateKey
	machineRelay *relay.Client

	// notifier is the REAL gateway-side trigger, so every wake in this package is the wake
	// production emits: content-free, zero key ids, a durable seq, sealed under the wake key.
	notifier *remotegw.PushNotifier
	seqPath  string

	// keyless seeds a phone that is PAIRED and has never received an epoch grant. It is
	// PB-APP-10's third state (e1ab559) and it is the state a push is most likely to find a
	// phone in during the first-pairing window: no key, no user present, nothing on screen.
	keyless bool
}

// s17FreePort reserves a loopback port and releases it, so the relay can be restarted on the
// SAME address. The phone's relay URL is fixed at NewApp, so a restart on a new port would
// test nothing about re-registration -- the phone would simply never reconnect.
func s17FreePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}
	return addr
}

func s17NewRig(t *testing.T) *s17Rig { return s17NewRigWith(t, false) }

// s17NewKeylessRig is a PAIRED phone that has never received an epoch grant: PB-APP-10's third
// state. The MACHINE still holds the epoch keys and still fires real wakes -- the phone simply
// cannot open them yet, and the remedy is waiting, not a re-pair.
func s17NewKeylessRig(t *testing.T) *s17Rig { return s17NewRigWith(t, true) }

func s17NewRigWith(t *testing.T, keyless bool) *s17Rig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	r := &s17Rig{t: t, ctx: ctx, Dir: t.TempDir(), FCM: s17NewFakeFCM(t), keyless: keyless}

	// The real sender over the fake endpoint. RetryDelay 0 keeps a provider hiccup from
	// putting a real sleep in the suite; DefaultMaxAttempts still bounds it.
	acct, err := push.LoadServiceAccount(s17ServiceAccount(t, r.FCM.srv.URL+"/token"))
	if err != nil {
		t.Fatalf("push.LoadServiceAccount: %v", err)
	}
	sender, err := push.NewFCM(push.FCMConfig{Account: acct, BaseURL: r.FCM.srv.URL})
	if err != nil {
		t.Fatalf("push.NewFCM: %v", err)
	}

	r.relayCfg = relay.DefaultConfig()
	r.relayCfg.Listen = s17FreePort(t)
	r.relayCfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	r.startRelay(sender)

	if r.Keys, err = crypto.NewEpochKeys(); err != nil {
		t.Fatalf("crypto.NewEpochKeys: %v", err)
	}

	// The phone's device custody, provisioned before the facade so the rig knows the phone's
	// relay routing id -- which is the address the machine pushes to.
	r.Custody = newTestCustody(t)
	r.CoreDir = pairedNamespace(t, r.Dir, s17Machine)
	provision, err := phonecore.Resume(phonecore.Config{
		Dir: r.CoreDir, Machine: s17Machine,
		WakeSealer: r.Custody.wakeSealer(), ContentSealer: r.Custody.contentSealer(),
	})
	if err != nil {
		t.Fatalf("provisioning the phone: %v", err)
	}
	ks := provision.KeyStore()
	r.phoneTarget = relay.RoutingID(ks.RelayAuthPublic())

	if r.machinePub, r.machinePriv, err = ed25519.GenerateKey(rand.Reader); err != nil {
		t.Fatalf("machine relay-auth key: %v", err)
	}
	r.dialMachine()
	r.seedState(ks)

	// The durable wake seq source, on disk, exactly as the gateway holds it: a per-process
	// counter restarting at 1 is the defect PB-PUSH-3's sender half already closed.
	r.seqPath = filepath.Join(t.TempDir(), "wake.seq")
	r.newNotifier()
	return r
}

func (r *s17Rig) startRelay(sink relay.PushSink) {
	r.t.Helper()
	srv, err := relay.New(r.relayCfg, relay.WithPushSink(sink))
	if err != nil {
		r.t.Fatalf("relay.New: %v", err)
	}
	if err := srv.Start(r.ctx); err != nil {
		r.t.Fatalf("relay start: %v", err)
	}
	r.relaySrv, r.RelayURL = srv, srv.URL()
	r.t.Cleanup(func() { _ = srv.Close() })
}

// RestartRelay closes the relay and brings a new one up on the SAME address. keepStore=false
// gives it an EMPTY store, which is the only configuration in which PB-PUSH-9's
// "re-registration restores it" can fail -- see the test that uses it.
func (r *s17Rig) RestartRelay(keepStore bool) {
	r.t.Helper()
	if err := r.relaySrv.Close(); err != nil {
		r.t.Fatalf("relay close: %v", err)
	}
	if !keepStore {
		r.relayCfg.DBPath = filepath.Join(r.t.TempDir(), "relay-restarted.db")
	}
	acct, err := push.LoadServiceAccount(s17ServiceAccount(r.t, r.FCM.srv.URL+"/token"))
	if err != nil {
		r.t.Fatalf("push.LoadServiceAccount: %v", err)
	}
	sender, err := push.NewFCM(push.FCMConfig{Account: acct, BaseURL: r.FCM.srv.URL})
	if err != nil {
		r.t.Fatalf("push.NewFCM: %v", err)
	}
	r.startRelay(sender)
	r.dialMachine()
}

// dialMachine (re)connects the machine side and re-authorizes the phone's mailbox.
func (r *s17Rig) dialMachine() {
	r.t.Helper()
	cl, err := relay.Dial(r.ctx, r.RelayURL, relay.ClientAuth{
		RelayAuthPub: r.machinePub,
		Sign:         func(c []byte) ([]byte, error) { return ed25519.Sign(r.machinePriv, c), nil },
	})
	if err != nil {
		r.t.Fatalf("machine dial: %v", err)
	}
	r.machineRelay = cl
	r.t.Cleanup(func() { _ = cl.Close() })
	if err := cl.AuthorizeDevice(r.ctx, r.phonePub(), r.phoneConsent()); err != nil {
		r.t.Fatalf("machine authorize phone: %v", err)
	}
	r.newNotifier()
}

func (r *s17Rig) phonePub() ed25519.PublicKey {
	r.t.Helper()
	core, err := phonecore.Resume(phonecore.Config{
		Dir: r.CoreDir, Machine: s17Machine,
		WakeSealer: r.Custody.wakeSealer(), ContentSealer: r.Custody.contentSealer(),
	})
	if err != nil {
		r.t.Fatalf("reading the phone's relay-auth pub: %v", err)
	}
	return ed25519.PublicKey(core.KeyStore().RelayAuthPublic())
}

// phoneConsent is the phone's relay-route consent for this rig's machine (ADR-007
// B27/B38), signed through the phone's own custody -- the same statement mobile/pairing.go
// signs into msg3, so the machine's authorize below is the production call.
func (r *s17Rig) phoneConsent() []byte {
	r.t.Helper()
	core, err := phonecore.Resume(phonecore.Config{
		Dir: r.CoreDir, Machine: s17Machine,
		WakeSealer: r.Custody.wakeSealer(), ContentSealer: r.Custody.contentSealer(),
	})
	if err != nil {
		r.t.Fatalf("resuming the phone to sign its relay-route consent: %v", err)
	}
	return consentFrom(r.t, core.KeyStore(), relay.RoutingID(r.machinePub))
}

func (r *s17Rig) seedState(ks crypto.KeyStore) {
	r.t.Helper()
	store, err := phonecore.OpenStore(filepath.Join(r.CoreDir, phonecore.StateFileName), s17Machine,
		r.Custody.wakeSealer(), r.Custody.contentSealer())
	if err != nil {
		r.t.Fatalf("open phone state: %v", err)
	}
	st := phonecore.State{
		Machine:             s17Machine,
		MachineRelayAuthPub: r.machinePub,
		OperatorNamespace:   "owner",
		RoutingID:           relay.RoutingID(ks.RelayAuthPublic()),
		EpochID:             s17Epoch,
		Keys:                r.Keys,
	}
	if r.keyless {
		// Paired, and no grant has ever been delivered. Not a purge and not a loss: the phone
		// has simply not been told yet.
		st.Keys = crypto.EpochKeys{}
	}
	if err := store.Save(st); err != nil {
		r.t.Fatalf("seed phone state: %v", err)
	}
}

// newNotifier rebuilds the gateway-side trigger over the current machine connection, keeping
// the DURABLE seq file, so a relay restart does not restart the wake counter.
func (r *s17Rig) newNotifier() {
	if r.seqPath == "" {
		return
	}
	seq, err := remotegw.OpenSeqSource(r.seqPath)
	if err != nil {
		r.t.Fatalf("remotegw.OpenSeqSource: %v", err)
	}
	r.notifier = remotegw.NewPushNotifier(s17PassthroughSink{}, remotegw.PushConfig{
		Pusher:  r.machineRelay,
		Target:  r.phoneTarget,
		WakeKey: r.Keys.WakeKey,
		EpochID: s17Epoch,
		Seq:     seq,
		Prefs:   s17AllEnabled{},
		Window:  time.Nanosecond, // per-session coalescing is S12's and is tested there
	})
}

// OpenApp constructs the facade over the seeded directory. It does NOT Start: a push-woken
// process has no relay connection yet, and that is the state PB-PUSH-4 describes.
func (r *s17Rig) OpenApp() *swarmmobile.App {
	r.t.Helper()
	app, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir: r.Dir, RelayURL: r.RelayURL, MachineID: s17Machine,
	}, r.Custody)
	if err != nil {
		r.t.Fatalf("swarmmobile.NewApp: %v", err)
	}
	r.t.Cleanup(func() { _ = app.Close() })
	r.App = app
	return app
}

// StartApp is OpenApp plus the transport loop, for the token-lifecycle tests, which need an
// authenticated connection.
func (r *s17Rig) StartApp() *swarmmobile.App {
	r.t.Helper()
	app := r.OpenApp()
	if err := app.Start(); err != nil {
		r.t.Fatalf("App.Start: %v", err)
	}
	r.AwaitOnline(app)
	return app
}

// AwaitOnline waits for the transport loop to report a live connection.
//
// IT IS NOT ENOUGH ON ITS OWN, and the reason is worth stating because it produced two false
// failures while this file was being written. relay.go's run() calls setConn(connOnline)
// BEFORE onConnected, so "online" means the socket is up, not that the per-connection work --
// AuthorizeDevice and the PB-PUSH-9 token re-registration -- has finished. Every assertion
// about what the relay holds therefore goes through PushReaches or Settle below, never
// through this alone.
func (r *s17Rig) AwaitOnline(app *swarmmobile.App) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if state, err := app.ConnectionState(); err == nil && state == "online" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	state, _ := app.ConnectionState()
	r.t.Fatalf("the phone never reached \"online\"; last state %q", state)
}

// PushReaches reports whether a wake fired now is delivered to token, retrying until it is or
// the deadline passes. It is how every POSITIVE delivery assertion is made: the alternative --
// one wake immediately after AwaitOnline -- measures the race described above rather than the
// requirement.
//
// Each attempt uses a fresh session id so PB-PUSH-0's per-session coalescing window cannot
// suppress the retry, which would turn a slow reconnect into a permanent false negative.
func (r *s17Rig) PushReaches(token string) bool {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for i := 0; ; i++ {
		r.Wake(fmt.Sprintf("m/await-%d", i))
		for _, d := range r.FCM.Deliveries() {
			if d.Token == token {
				return true
			}
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Settle waits past the post-connect work AwaitOnline does not cover, so that an assertion
// about the ABSENCE of a delivery is an absence and not a race. AuthorizeDevice and
// TokenRegister are two round trips on loopback; this waits well past them.
func (r *s17Rig) Settle() { time.Sleep(500 * time.Millisecond) }

// RegisterThenBackground registers token through a running app and then CLOSES that app,
// leaving the relay holding the token and no live phone.
//
// That is the state every wake actually arrives in -- ADR-007 B16 disconnects on
// backgrounding, which is why push is the sole background path -- and it is also the only
// state in which the tests below are sound: two live Cores over one state directory would
// interleave their Saves, and an assertion about the durable replay coordinate would then be
// measuring which of the two wrote last.
func (r *s17Rig) RegisterThenBackground(token string) {
	r.t.Helper()
	app := r.StartApp()
	if err := app.RegisterPushToken(token); err != nil {
		r.t.Fatalf("App.RegisterPushToken: %v", err)
	}
	if err := app.Close(); err != nil {
		r.t.Fatalf("App.Close: %v", err)
	}
	r.App = nil
	r.FCM.Reset()
}

// Wake fires ONE real gateway wake for session, by feeding the real PushNotifier a journal
// transition into needs_input. Everything downstream is production: the notifier seals it, the
// relay looks the token up, the real FCM sender posts it, the fake endpoint records it.
func (r *s17Rig) Wake(session string) {
	r.t.Helper()
	if err := r.notifier.Event(protocol.JournalRecord{
		SessionID: session, Type: "agent_stopped", Group: status.GroupNeedsInput,
	}); err != nil {
		r.t.Fatalf("PushNotifier.Event: %v", err)
	}
	if err := r.notifier.Err(); err != nil {
		r.t.Fatalf("the gateway's push path reported %v", err)
	}
}

// LastPayload is the base64 the Android data block carries, from the message the provider
// actually received. It is what the FirebaseMessagingService reads out of RemoteMessage.data
// and hands to the facade unchanged.
func (r *s17Rig) LastPayload() string {
	r.t.Helper()
	all := r.FCM.Deliveries()
	if len(all) == 0 {
		r.t.Fatal("the provider received no message; there is no payload to hand the phone")
	}
	last := all[len(all)-1]
	payload, ok := last.Data["e"]
	if !ok {
		var keys []string
		for k := range last.Data {
			keys = append(keys, k)
		}
		r.t.Fatalf("PB-PUSH-3: the FCM data block carries %v and no \"e\". The Android side reads "+
			"exactly that key; a rename here is a phone that receives every wake and opens none", keys)
	}
	return payload
}

// s17PassthroughSink is the journal sink the notifier wraps. The journal itself is not this
// slice's subject; what matters is that a record reaches maybeWake.
type s17PassthroughSink struct{}

func (s17PassthroughSink) Snapshot([]protocol.JournalRecord, uint64) error { return nil }
func (s17PassthroughSink) Event(protocol.JournalRecord) error              { return nil }
func (s17PassthroughSink) Terminal(protocol.TerminalViewV1) error          { return nil }

// s17AllEnabled is the user's push preference with both categories on. PB-PUSH-8's suppression
// is S12's requirement and is tested at the sender there; suppressing here would only make an
// S17 failure look like a preference bug.
type s17AllEnabled struct{}

func (s17AllEnabled) LoadPrefs() (remotegw.PushPrefs, error) {
	return remotegw.PushPrefs{NeedsInput: true, Finished: true}, nil
}

func (s17AllEnabled) SavePrefs(remotegw.PushPrefs) error { return nil }

// s17DecodePayload is the Android side's base64 decode, so a test can assert on the envelope
// the phone will be handed.
func s17DecodePayload(t *testing.T, payload string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("the FCM data payload is not standard base64: %v", err)
	}
	return raw
}

// s17ForgedPayload is what the relay can build with everything it legitimately holds: a
// well-formed wake envelope under a key it invented, carrying a seq of its choosing. The relay
// never sees a wake key -- it routes by push_trigger target and hands the opaque bytes on --
// so this is exactly the strongest wake a hostile relay can produce.
func s17ForgedPayload(t *testing.T) string {
	t.Helper()
	var key crypto.WakeKey
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatalf("forging a wake key: %v", err)
	}
	env, err := crypto.SealWake(key, crypto.EnvelopeHeader{
		Version:  crypto.VersionV1,
		EpochID:  s17Epoch,
		Seq:      1 << 62, // pins the replay window at the top if it is trusted before the tag
		IssuedAt: time.Now().UnixMilli(),
	}, nil)
	if err != nil {
		t.Fatalf("crypto.SealWake: %v", err)
	}
	return base64.StdEncoding.EncodeToString(env.Marshal())
}

// s17ContainsAny reports the first needle present in haystack, or "".
func s17ContainsAny(haystack string, needles ...string) string {
	for _, n := range needles {
		if n != "" && strings.Contains(haystack, n) {
			return n
		}
	}
	return ""
}
