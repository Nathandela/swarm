package skeleton

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// Owner Kill/Delete and automatic auth recycling are two different authorities
// over the same terminal transition. If the owner's operation linearizes first,
// authwatch must not publish a kill claim or later resurrect the session.
func TestOwnerEndWinsBeforeAuthRecycleClaim(t *testing.T) {
	d := &Daemon{}
	ownerEntered := make(chan struct{})
	releaseOwner := make(chan struct{})
	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- d.withOwnerSessionEnd("s1", func() error {
			close(ownerEntered)
			<-releaseOwner
			return nil
		})
	}()
	select {
	case <-ownerEntered:
	case <-time.After(time.Second):
		t.Fatal("owner end never reached its serialized action")
	}

	var attempted atomic.Bool
	authDone := make(chan error, 1)
	go func() {
		authDone <- d.withAuthRecycleFence("s1", func() error {
			attempted.Store(true)
			return nil
		})
	}()
	select {
	case err := <-authDone:
		close(releaseOwner)
		<-ownerDone
		t.Fatalf("auth recycle crossed a live owner end: err=%v attempted=%v", err, attempted.Load())
	case <-time.After(100 * time.Millisecond):
		// The auth operation is correctly parked behind the owner's end authority.
	}
	close(releaseOwner)
	if err := <-ownerDone; err != nil {
		t.Fatalf("owner end: %v", err)
	}
	select {
	case err := <-authDone:
		if !errors.Is(err, errAuthOwnerEnding) {
			t.Fatalf("auth after owner end = %v, want owner-ending refusal", err)
		}
	case <-time.After(time.Second):
		t.Fatal("auth recycle did not settle after owner end completed")
	}
	if attempted.Load() {
		t.Fatal("auth kill callback ran after the owner had already ended the session")
	}
}

func TestFailedOwnerEndReleasesAuthRecycleAuthority(t *testing.T) {
	d := &Daemon{}
	want := errors.New("owner kill failed")
	if err := d.withOwnerSessionEnd("s1", func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("owner end = %v, want %v", err, want)
	}
	called := false
	if err := d.withAuthRecycleFence("s1", func() error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("auth recycle after failed owner end: %v", err)
	}
	if !called {
		t.Fatal("failed owner end permanently blocked auth recycling")
	}
}

// The inverse order is intentional: once authwatch publishes its recycle
// embargo, a later owner terminal action is refused. Letting it run after the
// auth signal would create a crash window where a persisted auth claim could
// resurrect a session the owner meant to end.
func TestAuthRecycleRefusesAConcurrentOwnerEnd(t *testing.T) {
	d := &Daemon{}
	authEntered := make(chan struct{})
	releaseAuth := make(chan struct{})
	authDone := make(chan error, 1)
	go func() {
		authDone <- d.withAuthRecycleFence("s1", func() error {
			close(authEntered)
			<-releaseAuth
			return nil
		})
	}()
	select {
	case <-authEntered:
	case <-time.After(time.Second):
		t.Fatal("auth recycle never reached its serialized action")
	}

	ownerAction := make(chan struct{})
	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- d.withOwnerSessionEnd("s1", func() error {
			close(ownerAction)
			return nil
		})
	}()
	select {
	case <-ownerAction:
		close(releaseAuth)
		<-authDone
		<-ownerDone
		t.Fatal("owner end entered while auth still held the end fence")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseAuth)
	if err := <-authDone; err != nil {
		t.Fatalf("auth recycle: %v", err)
	}
	select {
	case err := <-ownerDone:
		if !errors.Is(err, errAuthRecycleInProgress) {
			t.Fatalf("owner end after auth = %v, want recycle-in-progress refusal", err)
		}
	case <-time.After(time.Second):
		t.Fatal("owner end did not settle after auth released its fence")
	}
	select {
	case <-ownerAction:
		t.Fatal("refused owner end ran its terminal action")
	default:
	}
}

func TestCoreAPILifecycleUsesAssemblyOwnerSeams(t *testing.T) {
	wantKill := errors.New("kill seam")
	wantDelete := errors.New("delete seam")
	a := &coreAPI{
		killFn:   func(string) error { return wantKill },
		deleteFn: func(string) error { return wantDelete },
	}
	if err := a.Kill("s1"); !errors.Is(err, wantKill) {
		t.Fatalf("Kill = %v, want assembly seam", err)
	}
	if err := a.Delete("s1"); !errors.Is(err, wantDelete) {
		t.Fatalf("Delete = %v, want assembly seam", err)
	}
}

func TestOwnerEndAfterAuthSignalIsRefusedAndReplacementRemainsOwed(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000023"))
	f.killNoExit = true
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	d := &Daemon{}
	w.withRecycleFence = d.withAuthRecycleFence

	waitingForExit := make(chan struct{})
	releasePoll := make(chan struct{})
	var pauseOnce atomic.Bool
	w.pause = func(time.Duration) {
		if pauseOnce.CompareAndSwap(false, true) {
			close(waitingForExit)
		}
		<-releasePoll
	}
	done := make(chan struct{})
	go func() {
		w.tick()
		close(done)
	}()
	select {
	case <-waitingForExit:
	case <-time.After(time.Second):
		t.Fatal("auth recycle never reached its post-signal exit wait")
	}
	if !w.state.Killed["s1"] || len(f.killed) != 1 {
		t.Fatalf("precondition: claim=%v kills=%v, want durable claim and one signal", w.state.Killed, f.killed)
	}
	ownerRan := false
	if err := d.withOwnerSessionEnd("s1", func() error {
		ownerRan = true
		return nil
	}); !errors.Is(err, errAuthRecycleInProgress) {
		t.Fatalf("owner end after auth signal = %v, want recycle-in-progress refusal", err)
	}
	if ownerRan {
		t.Fatal("refused owner end ran its terminal action")
	}
	// The process exit is the asynchronous consequence of authwatch's own signal.
	f.sessions["s1"].Status.Process = status.ProcessExited
	close(releasePoll)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("auth recycle did not settle after owner terminal status")
	}
	if len(f.launched) != 1 {
		t.Fatalf("auth-owned session was replaced %d time(s), want 1", len(f.launched))
	}
	if len(w.state.Killed) != 0 || len(w.state.Pending["codex"]) != 0 {
		t.Fatalf("completed auth replacement left state killed=%v pending=%v", w.state.Killed, w.state.Pending)
	}
}

func TestForgetInteractionsRetainsRecycleAuthorityUntilClaimClears(t *testing.T) {
	d := &Daemon{}
	d.restoreAuthRecycle("s1")
	d.forgetInteractions("s1")
	if !d.composerRecycleInFlight("s1") {
		t.Fatal("session retirement dropped a live auth-recycle embargo")
	}
	d.clearAuthRecycle("s1")
	if _, ok := d.composerLanes.Load("s1"); ok {
		t.Fatal("resolved auth-recycle claim retained its retired composer lane")
	}
}

func TestOwnerFirstPreventsAnOwedTerminalResume(t *testing.T) {
	d := &Daemon{}
	ownerEntered := make(chan struct{})
	releaseOwner := make(chan struct{})
	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- d.withOwnerSessionEnd("s1", func() error {
			close(ownerEntered)
			<-releaseOwner
			return nil
		})
	}()
	<-ownerEntered

	// Model a recovered terminal claim reaching its resume decision while the
	// owner's already-linearized terminal action is still in flight.
	d.restoreAuthRecycle("s1")
	launched := false
	resumeCaptured := make(chan struct{})
	resumeHook := func(string) { close(resumeCaptured) }
	testHookAuthResumeLaneCaptured.Store(&resumeHook)
	t.Cleanup(func() { testHookAuthResumeLaneCaptured.Store(nil) })
	type result struct {
		attempted bool
		retry     bool
	}
	authDone := make(chan result, 1)
	go func() {
		attempted, retry := d.withAuthResumeFence("s1", func() bool {
			launched = true
			return false
		})
		authDone <- result{attempted, retry}
	}()
	<-resumeCaptured
	close(releaseOwner)
	if err := <-ownerDone; err != nil {
		t.Fatalf("owner terminal action: %v", err)
	}
	got := <-authDone
	if got.attempted || got.retry || launched {
		t.Fatalf("owner-first resume = %+v launched=%v, want no replacement", got, launched)
	}
}

func TestOwedTerminalResumeFirstRefusesConcurrentOwnerEnd(t *testing.T) {
	d := &Daemon{}
	d.restoreAuthRecycle("s1")
	authEntered := make(chan struct{})
	releaseAuth := make(chan struct{})
	authDone := make(chan struct{}, 1)
	go func() {
		attempted, retry := d.withAuthResumeFence("s1", func() bool {
			close(authEntered)
			<-releaseAuth
			return false
		})
		if !attempted || retry {
			t.Errorf("auth resume = attempted %v retry %v, want true/false", attempted, retry)
		}
		authDone <- struct{}{}
	}()
	<-authEntered

	ownerRan := false
	ownerCaptured := make(chan struct{})
	ownerHook := func(string) { close(ownerCaptured) }
	testHookOwnerEndLaneCaptured.Store(&ownerHook)
	t.Cleanup(func() { testHookOwnerEndLaneCaptured.Store(nil) })
	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- d.withOwnerSessionEnd("s1", func() error {
			ownerRan = true
			return nil
		})
	}()
	<-ownerCaptured
	close(releaseAuth)
	<-authDone
	if err := <-ownerDone; !errors.Is(err, errAuthRecycleInProgress) {
		t.Fatalf("owner after owed resume = %v, want recycle refusal", err)
	}
	if ownerRan {
		t.Fatal("refused owner terminal action ran after auth resume")
	}
}

func TestSynchronousOwnerRetirementCannotReplaceItsLifecycleLane(t *testing.T) {
	d := &Daemon{}
	var before, during *composerLane
	if err := d.withOwnerSessionEnd("s1", func() error {
		before = d.composerLaneFor("s1")
		d.forgetInteractions("s1") // core.Delete invokes endSession synchronously
		during = d.composerLaneFor("s1")
		return nil
	}); err != nil {
		t.Fatalf("owner terminal action: %v", err)
	}
	if before != during {
		t.Fatal("synchronous session retirement replaced the lifecycle lane while its endMu was held")
	}
	if current, ok := d.composerLanes.Load("s1"); ok && current == before {
		t.Fatal("completed owner action retained its terminal lifecycle lane")
	}
}

func TestSuccessfulOwnerSignalBlocksAuthUntilAsynchronousRetirement(t *testing.T) {
	d := &Daemon{}
	if err := d.withOwnerSessionEnd("s1", func() error { return nil }); err != nil {
		t.Fatalf("owner signal: %v", err)
	}
	if _, ok := d.composerLanes.Load("s1"); !ok {
		t.Fatal("owner signal returned before exit but dropped its lifecycle authority")
	}
	authRan := false
	if err := d.withAuthRecycleFence("s1", func() error {
		authRan = true
		return nil
	}); !errors.Is(err, errAuthOwnerEnding) {
		t.Fatalf("auth before owner exit = %v, want owner-ending refusal", err)
	}
	if authRan {
		t.Fatal("auth kill ran while the owner's asynchronous exit was pending")
	}
	d.forgetInteractions("s1")
	if _, ok := d.composerLanes.Load("s1"); ok {
		t.Fatal("terminal retirement retained the completed owner lifecycle lane")
	}
}
