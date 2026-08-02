// ADR-007 B57/B58 part 2 (FAILING FIRST): no observer of `paired` may see a world in which the
// pairing's durable effects have not landed.
//
// Pairing.finish() sets the label under p.mu, UNLOCKS, and only then calls App.pin(). Every
// observer reasonably reads `paired` as "the effects have landed" -- the transport loop was one
// that acted destructively on the answer, which part 1 fixed, and what remains is the
// correctness of the label itself.
//
// THE FENCE HAS TO OBSERVE THE ORDERING, NOT THE END STATE. A test that waits for the durable
// pin passes whether or not the ordering is right -- that is exactly the mistake that made the
// B54 fence flaky, and the lesson is that "eventually correct" cannot distinguish "correct
// when published" from "correct shortly afterwards".
//
// SO THE WRITE IS WIDENED THROUGH THE PRODUCT'S OWN SEAM. Every seal and open goes back to the
// injected KeyCustody (mobile/app.go builds one sealer per tier over it), so a custody that
// takes its time makes the durable write slow WITHOUT touching the product. A watcher then
// samples the pairing label continuously and, the first time it reads `paired`, asks whether
// the effects are visible. With the label published first, ~50ms of samples answer no.
package conformance_test

import (
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// slowCustody is the phone's custody with a delay on every KEK request. It decides nothing and
// refuses nothing: it only makes the durable write take long enough to observe.
type slowCustody struct {
	inner *testCustody
	delay time.Duration
}

func (s *slowCustody) WakeKEK() ([]byte, error) {
	time.Sleep(s.delay)
	return s.inner.WakeKEK()
}

func (s *slowCustody) ContentKEK() ([]byte, error) {
	time.Sleep(s.delay)
	return s.inner.ContentKEK()
}

// labelWatcher samples the pairing label and records what was durable when it first read
// `paired`.
type labelWatcher struct {
	mu sync.Mutex
	// sawPairedWithoutEffects counts samples that read `paired` while the pin the pairing
	// published was still not readable.
	sawPairedWithoutEffects int
	sawPaired               bool
}

// TestB58_TheLabelIsPublishedAfterTheDurableWrite is the ordering itself.
func TestB58_TheLabelIsPublishedAfterTheDurableWrite(t *testing.T) {
	_, relayURL := s16FreshRelay(t)

	dir := t.TempDir()
	inner := newTestCustody(t)
	custody := &slowCustody{inner: inner, delay: 25 * time.Millisecond}

	app, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir: dir, RelayURL: relayURL, MachineID: testMachineID,
	}, custody)
	if err != nil {
		t.Fatalf("swarmmobile.NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	// The app is deliberately NOT started: the transport loop would call the custody on every
	// dial, and the only writes this test wants to be slow are the pairing's.
	machine := newB54Machine(t, relayURL)
	want := sha256.Sum256([]byte("the pin this pairing publishes"))
	p := s16BeginConfirmed(t, app, machine.offer(t, want[:]))
	s16AwaitSAS(t, p)

	w := &labelWatcher{}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			st, serr := p.State()
			if serr != nil || st != "paired" {
				continue
			}
			// The label says the pairing is done. Ask the durable state the same question.
			pin := b54PersistedPin(t, dir, inner)
			w.mu.Lock()
			w.sawPaired = true
			if len(pin) == 0 {
				w.sawPairedWithoutEffects++
			}
			w.mu.Unlock()
			return
		}
	}()

	if err := p.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	s16AwaitState(t, p, "paired")
	close(stop)
	<-done

	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.sawPaired {
		t.Fatal("the watcher never observed `paired`, so this fence asserted nothing")
	}
	if w.sawPairedWithoutEffects > 0 {
		t.Fatalf("an observer read `paired` while the pairing's durable pin was not yet " +
			"readable. Every observer takes that label to mean the effects have landed -- the " +
			"transport loop is one, and it acts on the answer. finish() must complete pin() " +
			"before it publishes (ADR-007 B58)")
	}
}
