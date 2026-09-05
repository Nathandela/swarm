// FAILING-FIRST (TDD RED, GG-5) for Wave R3's Android/phone slice, ROUND 4: the review
// findings against the round-3 GREEN. Each test states one finding as a permanent
// assertion.
//
//   - BLOCKING: a clock skew of ~60 seconds silently minted a DUPLICATE installation and
//     swapped the phone's durable identity while reporting success. gatewayError mapped
//     EVERY 401 onto errGatewayUnauthorized, discarding the body's code; pushgw returns 401
//     `request_expired` WITH `server_time` precisely so the client can correct its clock and
//     retry (PG-AUTH-3), and EnsurePushRegistration read that 401 as "the gateway forgot the
//     installation" and registered afresh. The registration proof is clock-independent,
//     so the second registration SUCCEEDS while the clock is still wrong: the
//     first installation is orphaned for 180 days holding a live FCM token, every address
//     under it dies at the inactivity floor, and every live pairing silently stops waking
//     with the phone reporting healthy.
//   - MEDIUM: a clock-skewed MACHINE silently and permanently killed the wake path, and the
//     only trace was the opaque WakeDrops total -- indistinguishable from someone forging
//     wakes. The guard is right and stays; what was missing is a surface that separates
//     "peer clock ahead" from "bad MAC".
//   - LOW: WakeDrops undercounted in a long-lived process (only the FIRST refusal per
//     process ever persisted), and the durable assertion was a bound rather than a value.
package phonecore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/pushgw"
)

// r3ar4SkewedGateway spins the REAL pushgw.Server with an injected clock offset by skew, so
// the phone's signed requests carry an expiry the gateway reads as already past
// (PG-AUTH-3's 120-second horizon). Nothing about the client is faked: the 401 the phone
// sees is the gateway's own errRequestExpired, server_time and all.
func r3ar4SkewedGateway(t *testing.T, sender pushgw.WakeSender, skew time.Duration) *httptest.Server {
	t.Helper()
	srv, err := pushgw.NewServer(pushgw.Config{
		DBPath: filepath.Join(t.TempDir(), "pushgw.db"),
		Sender: sender,
		Attest: &r3aAttestVerifier{licensed: true},
		Now:    func() time.Time { return time.Now().Add(skew) },
	})
	if err != nil {
		t.Fatalf("pushgw.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	return hs
}

// TestR3AR4_AClockSkewNeverMintsASecondInstallation is the BLOCKING finding, driven against
// the REAL gateway with a ten-minute clock offset -- far outside PG-AUTH-3's 120-second
// horizon and outside the client's own 60-second expiry window, which is the exact regime
// the probe used.
//
// The phone registers once (registration proof is clock-independent, so it is accepted
// whatever the clock says). The SECOND EnsurePushRegistration signs a rotation PUT that
// the gateway refuses as request_expired -- and the phone must consume server_time as a
// clock offset and retry the ROTATION, never fall through to a fresh registration. One
// installation, one register POST, one durable identity.
func TestR3AR4_AClockSkewNeverMintsASecondInstallation(t *testing.T) {
	sender := &r3aSender{}
	hs := r3ar4SkewedGateway(t, sender, 10*time.Minute)
	rt := &r3aRecordingTransport{inner: hs.Client().Transport}
	client := NewGatewayClient(hs.URL, newR3ASigner(t), r3aAttestor(t), &http.Client{Transport: rt})

	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)

	first, err := core.EnsurePushRegistration(context.Background(), client, staticToken("fcm-token-alpha"))
	if err != nil {
		t.Fatalf("first EnsurePushRegistration: %v", err)
	}
	second, err := core.EnsurePushRegistration(context.Background(), client, staticToken("fcm-token-alpha"))
	if err != nil {
		t.Fatalf("second EnsurePushRegistration under a skewed gateway clock: %v", err)
	}
	if second.InstallationID != first.InstallationID {
		t.Errorf("the skewed clock swapped this phone's durable identity: %q -> %q; the "+
			"first installation is orphaned for 180 days holding a live FCM token",
			first.InstallationID, second.InstallationID)
	}
	if got := core.PushInstallationID(); got != first.InstallationID {
		t.Errorf("durable installation id = %q, want the original %q", got, first.InstallationID)
	}

	posts := 0
	for _, r := range rt.recorded() {
		if r.method == http.MethodPost && strings.HasSuffix(r.path, "/v1/installations") {
			posts++
		}
	}
	if posts != 1 {
		t.Errorf("register POSTs = %d, want exactly 1 (a request_expired 401 must correct the "+
			"clock and retry, never re-register)", posts)
	}
}

// TestR3AR4_RequestExpiredIsNotTheUndiscriminatedUnauthorized pins the discrimination
// itself against a gateway that stays expired however the clock is corrected: the caller
// must see a DISTINCT error, and it must not satisfy errors.Is(err, errGatewayUnauthorized)
// -- that sentinel is the only thing EnsurePushRegistration's re-registration fallback acts
// on.
func TestR3AR4_RequestExpiredIsNotTheUndiscriminatedUnauthorized(t *testing.T) {
	var attempts int
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":        "request_expired",
			"message":     "the signed request has expired",
			"retryable":   false,
			"server_time": time.Now().Add(9 * time.Minute).UTC().Format(time.RFC3339),
		})
	}))
	defer hs.Close()

	client := NewGatewayClient(hs.URL, newR3ASigner(t), r3aAttestor(t), hs.Client())
	err := client.RotateToken(context.Background(), "installation-abc", "fcm-token")
	if err == nil {
		t.Fatal("a 401 request_expired was reported as success")
	}
	if errors.Is(err, errGatewayUnauthorized) {
		t.Errorf("request_expired satisfied errGatewayUnauthorized (%v); that sentinel is the "+
			"re-registration trigger and a clock skew must never reach it", err)
	}
	if !errors.Is(err, errRequestExpired) {
		t.Errorf("err = %v, want errRequestExpired", err)
	}
	if attempts != 2 {
		t.Errorf("wire attempts = %d, want 2 (one original + exactly one clock-corrected retry)", attempts)
	}
}

// TestR3AR4_AnUndiscriminatedUnauthorizedStillReReaches TheFallback is the other direction:
// a 401 whose code is NOT request_expired (an unknown installation, a bad signature -- the
// gateway's own `unauthorized`) must still be errGatewayUnauthorized, or the fix would have
// bricked the legitimate re-registration path.
func TestR3AR4_AnUndiscriminatedUnauthorizedStillReachesTheFallback(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "unauthorized", "message": "the credential presented was not accepted",
		})
	}))
	defer hs.Close()

	client := NewGatewayClient(hs.URL, newR3ASigner(t), r3aAttestor(t), hs.Client())
	err := client.RotateToken(context.Background(), "installation-abc", "fcm-token")
	if !errors.Is(err, errGatewayUnauthorized) {
		t.Errorf("err = %v, want errGatewayUnauthorized (the re-registration fallback's trigger)", err)
	}
	if errors.Is(err, errRequestExpired) {
		t.Errorf("an undiscriminated 401 was read as a clock skew: %v", err)
	}
}

// TestR3AR4_TheInstallationSignerIsDurableAndTheRealGatewayAcceptsIt closes the other half
// of the zero-production-callers finding: the client's InstallationSigner seam had no
// production implementation at all, so nothing on a handset could sign a rotation. The
// signer is now the phone's own durable P-256 key, sealed in the push container under the
// WAKE tier (installationkey.go states the PG-AUTH-2 hardware-residency deviation).
//
// Both halves are asserted against the REAL gateway: its own verifyInstallationSignature
// accepts what this signer produces, and the key SURVIVES process death -- a phone that
// re-minted it would be an installation it can no longer authenticate as, with the 180-day
// inactivity floor running.
func TestR3AR4_TheInstallationSignerIsDurableAndTheRealGatewayAcceptsIt(t *testing.T) {
	sender := &r3aSender{}
	hs := r3aGateway(t, sender, &r3aAttestVerifier{licensed: true})

	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	signer, err := core.InstallationSigner()
	if err != nil {
		t.Fatalf("InstallationSigner: %v", err)
	}
	if got := len(signer.PublicKey()); got != 65 {
		t.Fatalf("PublicKey is %d bytes, want the 65-byte SEC1 uncompressed P-256 point", got)
	}
	if got := len(mustSign(t, signer)); got != 64 {
		t.Fatalf("Sign returned %d bytes, want the 64-byte IEEE P1363 r||s", got)
	}

	client := NewGatewayClient(hs.URL, signer, r3aAttestor(t), hs.Client())
	reg, err := core.EnsurePushRegistration(context.Background(), client, staticToken("fcm-token-alpha"))
	if err != nil {
		t.Fatalf("EnsurePushRegistration with the production signer: %v", err)
	}

	// Process death. The SAME key must come back, and the gateway must still accept the
	// signature it produces for the installation it minted.
	restarted := phone.resume(t)
	again, err := restarted.InstallationSigner()
	if err != nil {
		t.Fatalf("InstallationSigner after process death: %v", err)
	}
	if !bytes.Equal(again.PublicKey(), signer.PublicKey()) {
		t.Fatal("the installation key was re-minted across process death; the gateway holds the " +
			"old public key, so every later request is unauthorized and the installation is orphaned")
	}
	reg2, err := restarted.EnsurePushRegistration(context.Background(),
		NewGatewayClient(hs.URL, again, r3aAttestor(t), hs.Client()), staticToken("fcm-token-beta"))
	if err != nil {
		t.Fatalf("rotation after process death: %v", err)
	}
	if reg2.InstallationID != reg.InstallationID {
		t.Errorf("installation id changed across process death: %q -> %q",
			reg.InstallationID, reg2.InstallationID)
	}
}

func mustSign(t *testing.T, s InstallationSigner) []byte {
	t.Helper()
	sig, err := s.Sign([]byte("swarm-pg-v1|PUT|/v1/installations/x/token|h|n|1"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return sig
}

// TestR3AR4_WakeDropCounts_SeparateAPeerClockAheadFromABadMAC is the skew-diagnosability
// finding. remotegw stamps issued_at from the MACHINE clock and replays the SAME sealed
// bytes on every retry (PG-WAKE-12), so a machine three minutes ahead has 100% of its wakes
// refused forever by the wakeV1MaxFutureSkew guard -- correctly, and until now
// indistinguishably from someone forging wakes. The guard does not move; the counter gains
// enough structure to tell an operator which one is happening.
func TestR3AR4_WakeDropCounts_SeparateAPeerClockAheadFromABadMAC(t *testing.T) {
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	addr, key := r3aBinding(t, core, 0xD4)

	// A machine three minutes ahead: genuine bytes, genuine key, a future issued_at.
	ahead := r3aSeal(t, key, addr, 1, time.Now().Add(3*time.Minute))
	if err := core.AcceptWakeV1(ahead); !errors.Is(err, ErrWakePeerClockAhead) {
		t.Fatalf("a future-dated genuine wake refused with %v, want ErrWakePeerClockAhead", err)
	}
	// A forgery: the tag does not verify.
	forged := r3aSeal(t, key, addr, 2, time.Now())
	forged[70] ^= 0x01
	if err := core.AcceptWakeV1(forged); err == nil {
		t.Fatal("a tag-flipped envelope was accepted")
	}
	// A genuinely stale capture stays ErrWakeExpired: the two-sided bound keeps both sides.
	stale := r3aSeal(t, key, addr, 3, time.Now().Add(-WakeV1MaxAge-time.Minute))
	if err := core.AcceptWakeV1(stale); !errors.Is(err, ErrWakeExpired) {
		t.Fatalf("a stale capture refused with %v, want ErrWakeExpired", err)
	}

	counts := core.WakeDropCounts()
	if counts.Total != 3 {
		t.Errorf("WakeDropCounts().Total = %d, want 3", counts.Total)
	}
	if counts.PeerClockAhead != 1 {
		t.Errorf("PeerClockAhead = %d, want 1", counts.PeerClockAhead)
	}
	if counts.Unauthenticated != 1 {
		t.Errorf("Unauthenticated = %d, want 1 (the forgery)", counts.Unauthenticated)
	}
	if counts.Expired != 1 {
		t.Errorf("Expired = %d, want 1 (the stale capture)", counts.Expired)
	}
	if counts.Total != core.WakeDrops() {
		t.Errorf("WakeDropCounts().Total = %d but WakeDrops() = %d; the structured counter must "+
			"be the same counter", counts.Total, core.WakeDrops())
	}

	// It is DURABLE, like the total: the operator surface is read from a process that did
	// not see the refusals.
	restarted := phone.resume(t)
	if got := restarted.WakeDropCounts().PeerClockAhead; got != 1 {
		t.Errorf("PeerClockAhead after process death = %d, want 1", got)
	}
}

// TestR3AR4_WakeDrops_ConvergeInALongLivedProcess is the LOW finding. The per-process latch
// was sound for the wake-drop-die FCM topology and dishonest in a foreground process, where
// an unbounded number of refusals could accumulate behind a single persisted "1". The write
// cost stays bounded -- one container re-seal per wakeDropPersistEvery refusals, on top of
// the first -- and the durable value is asserted EXACTLY rather than as a floor.
func TestR3AR4_WakeDrops_ConvergeInALongLivedProcess(t *testing.T) {
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	addr, key := r3aBinding(t, core, 0xD5)

	forged := r3aSeal(t, key, addr, 1, time.Now())
	forged[70] ^= 0x01

	const refusals = 2*wakeDropPersistEvery + 5
	for i := 0; i < refusals; i++ {
		if err := core.AcceptWakeV1(forged); err == nil {
			t.Fatal("a tag-flipped envelope was accepted")
		}
	}
	if got := core.WakeDrops(); got != refusals {
		t.Fatalf("in-memory WakeDrops = %d, want %d", got, refusals)
	}

	// EXACT, with the topology rationale stated: this process persisted on its FIRST refusal
	// (the wake-drop-die FCM receipt is a process that only ever has one) and then whenever
	// wakeDropPersistEvery refusals had accumulated unpersisted -- so at refusals 1,
	// 1+E and 1+2E. The 4 after the last write are still only in memory.
	const wantDurable = 2*wakeDropPersistEvery + 1
	restarted := phone.resume(t)
	if got := restarted.WakeDrops(); got != wantDurable {
		t.Fatalf("durable WakeDrops after process death = %d, want %d (the first refusal, then "+
			"one write per %d unpersisted refusals)", got, wantDurable, wakeDropPersistEvery)
	}
	if drift := core.WakeDrops() - restarted.WakeDrops(); drift > wakeDropPersistEvery {
		t.Errorf("the durable counter lags the in-memory one by %d, want at most %d", drift, wakeDropPersistEvery)
	}
}
