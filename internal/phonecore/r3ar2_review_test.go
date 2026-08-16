// FAILING-FIRST (TDD RED, GG-5) for Wave R3's Android/phone slice, ROUND 2: the review
// findings against the round-1 GREEN (docs/verification/r3-green/android-green.txt).
// Each test here states one finding the review PROVED with a temporary probe, as a
// permanent assertion:
//
//   - BLOCKING: two concurrent first-run EnsurePushRegistration calls both saw no durable
//     installation, both Registered under DISTINCT idempotency keys, and the second
//     silently orphaned the first -- a durable installation holding a live FCM token for
//     180 days. The two documented Android entry points (SwarmApplication.onCreate's
//     getToken and SwarmMessagingService.onNewToken) run on different threads, so the race
//     is reachable in production.
//   - BLOCKING: DropPushBinding deleted the per-address HIGH-WATER with the key, so a
//     forget + re-adopt of the same address walked the replay coordinate back to zero and
//     every captured wake for that address replayed. AdoptPushBinding's own comment ("a
//     coordinate never moves backwards") promised the opposite.
//   - MEDIUM: issued_at had no lower bound, so an envelope sealed a year in the future was
//     accepted and stayed acceptable for a year plus five minutes -- a forward-skewed
//     machine clock hands a network capture an arbitrarily long window (PG-WAKE-7's whole
//     point is that the bound tracks the FCM TTL).
//
// The gateway in the registration test is the REAL internal/pushgw.Server in process,
// same as round 1; the wake producer is the REAL remotegw.SealWakeV1.
package phonecore

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestR3AR2_EnsurePushRegistration_ConcurrentFirstRunsMintOneInstallation: the whole
// read-decide-network-write sequence must be serialised, so N racing first runs put
// exactly ONE register POST on the wire and every caller comes back with the SAME durable
// installation id -- never N installations with the last write winning.
func TestR3AR2_EnsurePushRegistration_ConcurrentFirstRunsMintOneInstallation(t *testing.T) {
	sender := &r3aSender{}
	hs := r3aGateway(t, sender, &r3aAttestVerifier{licensed: true})

	rt := &r3aRecordingTransport{inner: hs.Client().Transport}
	hc := &http.Client{Transport: rt}

	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)

	// A deliberately slow attestor: every UNSERIALISED Register is held in flight long
	// enough that all racers read the (still empty) durable id before the first one
	// writes it -- the exact interleaving the two Android entry points produce.
	slowAttest := func(requestHash [32]byte) (string, error) {
		time.Sleep(300 * time.Millisecond)
		return "attest:" + base64.RawURLEncoding.EncodeToString(requestHash[:]), nil
	}
	client := NewGatewayClient(hs.URL, newR3ASigner(t), slowAttest, hc)

	const racers = 4
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		ids []string
	)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			reg, err := core.EnsurePushRegistration(context.Background(), client, staticToken("fcm-token-alpha"))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("EnsurePushRegistration: %v", err)
				return
			}
			ids = append(ids, reg.InstallationID)
		}()
	}
	close(start)
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}

	posts := 0
	for _, r := range rt.recorded() {
		if r.method == http.MethodPost && strings.HasSuffix(r.path, "/v1/installations") {
			posts++
		}
	}
	if posts != 1 {
		t.Errorf("%d racing first runs put %d register POSTs on the wire, want exactly 1 "+
			"(each extra POST is a durable orphaned installation holding a live token for 180 days)",
			racers, posts)
	}
	durable := core.PushInstallationID()
	if durable == "" {
		t.Fatal("no durable installation id after the race")
	}
	for i, id := range ids {
		if id != durable {
			t.Errorf("racer %d returned installation id %q, want the one durable id %q", i, id, durable)
		}
	}
}

// TestR3AR2_DropPushBinding_ReAdoptionCannotRewindTheCoordinate: the phone-initiated
// forget must leave the per-address replay coordinate behind as a tombstone, durably --
// otherwise anyone who captured earlier wakes for that address replays ALL of them after
// a forget/re-pair, which is exactly what HonorMachineRevoke's arm already refuses.
func TestR3AR2_DropPushBinding_ReAdoptionCannotRewindTheCoordinate(t *testing.T) {
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	addr, key := r3aBinding(t, core, 0xD1)

	captured := r3aSeal(t, key, addr, 9, time.Now())
	if err := core.AcceptWakeV1(captured); err != nil {
		t.Fatalf("the genuine seq-9 wake before the forget: %v", err)
	}

	if err := core.DropPushBinding(addr); err != nil {
		t.Fatalf("DropPushBinding: %v", err)
	}
	if err := core.AdoptPushBinding(addr, key); err != nil {
		t.Fatalf("AdoptPushBinding after the forget: %v", err)
	}

	// The byte-identical captured envelope must STILL be a replay: the coordinate never
	// moves backwards, including through a forget.
	if err := core.AcceptWakeV1(captured); !errors.Is(err, ErrWakeReplay) {
		t.Fatalf("the captured seq-9 envelope after forget+re-adopt: got %v, want ErrWakeReplay "+
			"(the drop walked the coordinate back)", err)
	}
	// The live path is unharmed: the machine's next real seq lands.
	if err := core.AcceptWakeV1(r3aSeal(t, key, addr, 10, time.Now())); err != nil {
		t.Fatalf("seq 10 after the re-adopt: %v", err)
	}

	// And the tombstone is DURABLE: forget, die, re-adopt -- the coordinate still holds.
	if err := core.DropPushBinding(addr); err != nil {
		t.Fatalf("second DropPushBinding: %v", err)
	}
	restarted := phone.resume(t)
	if err := restarted.AdoptPushBinding(addr, key); err != nil {
		t.Fatalf("AdoptPushBinding after process death: %v", err)
	}
	if err := restarted.AcceptWakeV1(r3aSeal(t, key, addr, 10, time.Now())); !errors.Is(err, ErrWakeReplay) {
		t.Fatalf("seq 10 after forget+death+re-adopt: got %v, want ErrWakeReplay (the tombstone was not durable)", err)
	}
	if err := restarted.AcceptWakeV1(r3aSeal(t, key, addr, 11, time.Now())); err != nil {
		t.Fatalf("seq 11 after the durable tombstone held: %v", err)
	}
}

// TestR3AR2_AcceptWakeV1_AFutureDatedWakeIsRefused: the freshness bound is TWO-SIDED. A
// wake issued beyond a bounded clock-skew allowance in the future is refused and counted
// (a forward-skewed machine clock, or one bug in the obligation's issued_at stamp, must
// not hand a capture an arbitrarily long validity window); ordinary skew stays
// deliverable; the authenticated refusal does not advance the coordinate.
func TestR3AR2_AcceptWakeV1_AFutureDatedWakeIsRefused(t *testing.T) {
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	addr, key := r3aBinding(t, core, 0xD2)

	future := r3aSeal(t, key, addr, 5, time.Now().Add(time.Hour))
	if err := core.AcceptWakeV1(future); !errors.Is(err, ErrWakeExpired) {
		t.Fatalf("a wake issued an hour in the future: got %v, want ErrWakeExpired (two-sided bound)", err)
	}
	if drops := core.WakeDrops(); drops != 1 {
		t.Errorf("WakeDrops = %d after the future-dated wake, want 1", drops)
	}

	// The refusal was authenticated but must not have advanced the coordinate: the
	// machine's real seq 1 still lands.
	if err := core.AcceptWakeV1(r3aSeal(t, key, addr, 1, time.Now())); err != nil {
		t.Fatalf("genuine seq 1 after the refused future wake: %v (the refusal advanced the coordinate)", err)
	}
	// Ordinary clock skew is a healthy machine, not an attack: thirty seconds ahead lands.
	if err := core.AcceptWakeV1(r3aSeal(t, key, addr, 2, time.Now().Add(30*time.Second))); err != nil {
		t.Fatalf("a wake 30s ahead (ordinary skew): %v", err)
	}
}
