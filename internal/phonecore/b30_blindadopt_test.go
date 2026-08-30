package phonecore

// FAILING-FIRST (TDD RED, GG-5) for the blind whole-blob adopt at the facade's durable
// update sites.
//
// THE DEFECT. Core.Save adopts the State it is handed WHOLESALE. Seven gomobile facade sites
// read the state, did work with the core lock RELEASED, and Saved the snapshot back:
// mobile/pairing.go App.pin, mobile/relay.go App.adoptReconcile, and five verbs in
// mobile/app.go. Any core-internal persist in that window was silently reverted by the write
// that followed.
//
// THE WINDOW IS THE NORMAL PAIRING SEQUENCE, not a corner. pin runs on the pairing goroutine
// while the relay drain runs on its own, and the machine's epoch grant lands immediately after
// a pairing: cmd/swarm-remote/deliver.go appends the bootstrap frame once per gateway session
// and the drain consumes it through MailboxRouter.AcceptCommit -> installGrant.
//
// WHAT IS ACTUALLY LOST, measured rather than assumed. fileStore.mergeGuards raises the
// REPLAY-GUARD coordinates monotonically -- SendSeq, Receive, GrantEpoch/GrantSeq, WakeReplay,
// RelayCursor -- so durable custody refuses to rewind any of them and no seq is ever
// re-issued. What it does NOT protect is everything adopted as given, and two of those are
// epoch-scoped: State.EpochID and State.Keys. App.pin ZEROES the keys itself when the pairing
// lands in a new epoch, which is right against its own snapshot and destroys the key the grant
// installed behind its back; resealTier then writes no content-key field at all, so the
// destruction reaches disk.
//
// WHY IT IS TERMINAL. The watermark survives -- monotonically merged -- at the coordinates of
// the grant whose key was just destroyed. crypto.GrantReceiver enforces strict (epoch, seq)
// monotonicity, so the gateway re-appending the very same bootstrap frame next session is
// refused as a replay, for good. The phone holds no content key, cannot obtain one, and the
// only exit is a machine-side re-grant at a higher seq.
//
// THE SEAM THESE TESTS PIN: Core.Mutate, a read-modify-write applied under the core lock. Save
// KEEPS its whole-blob adopt -- a reseed and a fixture restore both mean all of it, and making
// every caller pay for the rare case is the wrong trade -- so the two verbs are pinned side by
// side below, and a source fence keeps the blind one out of the call sites.

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// b30Paired is a handset that has completed a real pairing under real tier KEKs and has
// consumed the machine's bootstrap grant for epoch 7, so it holds a durable content key. The
// store is the REAL fileStore: mergeGuards is part of what is under test, and a memStore
// would make the loss look larger than it is.
func b30Paired(t *testing.T) (c *Core, m s10Machine, keys7 crypto.EpochKeys) {
	t.Helper()
	m = newS10Machine(t)
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	s14aSeedDeviceKeys(t, dir, wake, content)

	c, err := Resume(Config{Dir: dir, Machine: "m1", Ack: &recordingAcker{}, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	st := c.State()
	st.MachineSignPub, st.EpochID = m.pub, 7
	if err := c.Save(st); err != nil {
		t.Fatalf("persist the paired state: %v", err)
	}
	frame, keys7 := m.bootstrapFor(t, c.KeyStore(), 7, 1)
	if _, err := c.Router().AcceptCommit(frame, 500); err != nil {
		t.Fatalf("consume the epoch-7 bootstrap grant: %v", err)
	}
	if c.State().Keys.ContentKey != keys7.ContentKey {
		t.Fatalf("precondition: the fixture holds no epoch-7 content key")
	}
	return c, m, keys7
}

// b30Grant8 is the machine's epoch-8 bootstrap grant, consumed IN FULL by the drain. It is
// the T1 of every interleaving below: keys, epoch id, watermark and relay cursor, committed
// and rebound, while some other goroutine holds a snapshot taken before it.
func b30Grant8(t *testing.T, c *Core, m s10Machine) (frame []byte, keys8 crypto.EpochKeys) {
	t.Helper()
	frame, keys8 = m.bootstrapFor(t, c.KeyStore(), 8, 2)
	if _, err := c.Router().AcceptCommit(frame, 900); err != nil {
		t.Fatalf("the drain could not consume the epoch-8 bootstrap grant: %v", err)
	}
	if got := c.State().Keys.ContentKey; got != keys8.ContentKey {
		t.Fatalf("precondition: the epoch-8 grant did not install its content key")
	}
	return frame, keys8
}

// TestB30_TheWholeBlobAdoptRevertsWhatLandedSinceTheRead is the REPRODUCTION, and it stays
// green forever because it pins Save's INTENDED semantics: an adopt means all of it.
//
// It is here so the reason the facade may not use Save is executable rather than asserted,
// and so that anyone tempted to "fix" the hazard by making Save merge has to delete a test
// that says why it must not: a reseed, a fixture restore and a whole-blob rollback all mean
// exactly what they carry, and a Save that quietly kept a field the caller cleared is a
// second, quieter defect for the callers that legitimately adopt.
//
// The interleaving is driven EXPLICITLY -- no sleeps, no goroutine timing. The three points
// are the three the production race passes through, in the order the race puts them in.
func TestB30_TheWholeBlobAdoptRevertsWhatLandedSinceTheRead(t *testing.T) {
	c, m, keys7 := b30Paired(t)

	// T0 -- the pairing goroutine reads durable state.
	st := c.State()

	// T1 -- the drain consumes the machine's epoch-8 grant, in full.
	frame8, keys8 := b30Grant8(t, c, m)

	// T2 -- the pairing goroutine commits from its T0 snapshot. This is App.pin's body
	// verbatim: a pairing that lands in a DIFFERENT epoch invalidates the tier keys, because
	// against the snapshot they belong to the old one.
	st.MachineStatic = []byte("machine-noise-static")
	st.MachineRelayAuthPub = []byte("machine-relay-auth")
	if st.EpochID != 8 {
		st.Keys = crypto.EpochKeys{}
	}
	st.EpochID = 8
	if err := c.Save(st); err != nil {
		t.Fatalf("Save: %v", err)
	}

	after := c.State()
	if after.Keys.ContentKey != (crypto.ContentKey{}) {
		t.Errorf("Save no longer adopts State.Keys as given (have %x, the grant's key was %x, the "+
			"snapshot carried %x). Save is the WHOLE-BLOB adopt and the callers that use it -- a "+
			"reseed, a fixture restore -- mean all of it; a Save that keeps a field the caller "+
			"cleared is a quieter defect than the one it was changed to fix. The read-modify-write "+
			"belongs in Core.Mutate, and the source fence below is what keeps it out of the facade",
			after.Keys.ContentKey[:8], keys8.ContentKey[:8], keys7.ContentKey[:8])
	}

	// THE REPLAY-GUARD HALF, which is what makes it terminal rather than merely lossy: durable
	// custody refuses to rewind the watermark, so it stands at the coordinates of the grant
	// whose key the adopt just destroyed.
	if after.GrantEpoch != 8 || after.GrantSeq != 2 {
		t.Fatalf("the grant watermark was rewound to (%d, %d); fileStore.mergeGuards is what "+
			"forbids that, and this test's account of why the loss is terminal depends on it",
			after.GrantEpoch, after.GrantSeq)
	}
	if _, err := c.Router().AcceptCommit(frame8, 900); !errors.Is(err, crypto.ErrGrantReplay) {
		t.Fatalf("re-appending the epoch-8 bootstrap frame gave %v, want crypto.ErrGrantReplay: the "+
			"gateway re-appends it once per session and it is the ONLY thing that can deliver the "+
			"key this adopt destroyed", err)
	}
	if !c.State().StaleStreams[StreamGrant] {
		t.Error("the phone did not reach PB-KEY-3's terminal state after the key was destroyed and " +
			"the redelivery refused; grantLossDetected is what decides that, and this test's " +
			"account of the user-visible outcome depends on it")
	}
}

// TestB30_AFieldUpdateUnderTheCoreLockPreservesAConcurrentGrant is the fix, at the same three
// points and on the same fixture. Nothing about the interleaving changes; only the verb does.
//
// It pins that the closure's changes are the ones COMMITTED and that they are applied to the
// state durable custody actually holds. It deliberately does not claim to catch a Mutate whose
// read happens outside the lock: here the grant lands before the call, so such an
// implementation would read post-grant state and pass. TestB30_MutateIsOneTransaction is the
// fence for that.
func TestB30_AFieldUpdateUnderTheCoreLockPreservesAConcurrentGrant(t *testing.T) {
	c, m, _ := b30Paired(t)

	// T0 -- App.pin now forms no snapshot at all; the machine coordinates it is going to
	// write are the ones the handshake authenticated, and they are already in hand.
	machineStatic := []byte("machine-noise-static")
	machineRelay := []byte("machine-relay-auth")

	// T1 -- the drain consumes the machine's epoch-8 grant, in full.
	_, keys8 := b30Grant8(t, c, m)

	// T2 -- the pairing goroutine commits. Read and write are ONE transaction, so the epoch
	// it compares against is the one durable state actually holds.
	var newEpoch bool
	err := c.Mutate(func(st *State) {
		st.MachineStatic = machineStatic
		st.MachineRelayAuthPub = machineRelay
		newEpoch = st.EpochID != 8
		if newEpoch {
			st.Keys = crypto.EpochKeys{}
		}
		st.EpochID = 8
	})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	after := c.State()
	if len(after.MachineRelayAuthPub) == 0 {
		t.Error("the pairing coordinates did not land; the update must still do its own job")
	}
	if newEpoch {
		t.Error("the pairing was treated as landing in a NEW epoch. It compared against a snapshot " +
			"taken before the grant, which is the read-modify-write this verb exists to remove")
	}
	if after.EpochID != 8 {
		t.Errorf("EpochID is %d, want 8", after.EpochID)
	}
	if after.Keys.ContentKey != keys8.ContentKey {
		t.Errorf("the epoch-8 content key was reverted by a field update.\n  have %x\n  want %x",
			after.Keys.ContentKey[:8], keys8.ContentKey[:8])
	}
	if after.StaleStreams[StreamGrant] {
		t.Error("the phone reached PB-KEY-3's terminal state -- it believes the epoch grant is lost " +
			"and only a machine-side re-grant clears it")
	}
}

// TestB30_MutateIsOneTransaction is the half the interleaving above cannot see. A Mutate
// implemented as State() -> apply -> Save() passes that test, because there the grant lands
// BEFORE the update rather than inside it; only a lost-update count catches the snapshot.
//
// Each iteration draws PushPreference.Version + 1 and writes it back, which is exactly what
// mobile.App.SetPushPreference does, and PB-PUSH-10 makes it load-bearing: the machine refuses
// any update whose version does not STRICTLY exceed the stored one, so two toggles that both
// read N and both claim N+1 leave the second silently dropped while the settings screen shows
// its value. A correct Mutate gives exactly one version per call, always; a snapshot-based one
// loses updates as soon as two goroutines overlap.
func TestB30_MutateIsOneTransaction(t *testing.T) {
	c, err := Resume(Config{State: &memStore{}})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}

	const writers, each = 8, 64
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				if err := c.Mutate(func(st *State) {
					st.PushPreference.Version++
				}); err != nil {
					t.Errorf("Mutate: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if got := c.State().PushPreference.Version; got != writers*each {
		t.Errorf("PushPreference.Version is %d after %d increments: %d update(s) were lost, so the "+
			"closure is being applied to a SNAPSHOT taken outside the core lock rather than to the "+
			"state the write commits", got, writers*each, writers*each-got)
	}
}

// TestB30_MutateRebindsTheDerivedComponents pins the half Save's own doc calls out: a write
// that persisted the coordinates but left the live objects on the old epoch would keep
// reserving seqs against a stream that no longer exists. Mutate changes the same epoch-scoped
// fields Save does, so it owes the same rebind.
func TestB30_MutateRebindsTheDerivedComponents(t *testing.T) {
	c, err := Resume(Config{State: &memStore{}})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := c.Mutate(func(st *State) { st.EpochID = 5 }); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if _, err := c.Seq().NextCommand(); err != nil {
		t.Fatalf("NextCommand: %v", err)
	}
	if got := c.State().SendSeq; got[5] == 0 {
		t.Errorf("after Mutate moved the epoch to 5 the sequencer reserved under %v instead: the "+
			"derived components were not rebound, so every seq is drawn against a stream that no "+
			"longer exists and the gateway stale-drops the lot", got)
	}
}

// ---------------------------------------------------------------------------
// The call-site inventory, as a fence.
// ---------------------------------------------------------------------------

// TestB30_NoCallerOutsideTheCoreAdoptsTheWholeDurableBlob walks every non-test Go file that
// IMPORTS this package and fails on any call to a method named Save. Outside the core there is
// no legitimate whole-blob adopt: the facade only ever changes fields, and every one of those
// call sites is a read-modify-write across a released lock unless it goes through Mutate.
//
// This is the part that makes the fix stick. Core.Save keeps its blind adopt on purpose, so
// nothing in the type system stops the next caller from reaching for it, and the shape
// type-checks -- which is precisely why only a source fence catches it.
//
// It asserts a FLOOR on the files it scanned. A fence that finds nothing exits 0 while
// guarding nothing, which is a defect class this project has already shipped.
func TestB30_NoCallerOutsideTheCoreAdoptsTheWholeDurableBlob(t *testing.T) {
	root := s14aRepoRoot(t)
	self, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	var scanned []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			switch d.Name() {
			// `.claude` and `.codex` hold per-agent git worktrees -- full checkouts of this repository that
			// `git worktree add` leaves behind. A walk from the repo root treats them as source
			// and reports findings about an agent's private copy as findings about this tree.
			// Adding the directory to .gitignore does NOT prevent this: gitignore governs what
			// git tracks and has no effect on filepath.WalkDir. Two gates were red for this
			// reason before it was understood.
			case ".git", ".claude", ".codex", ".gradle", "vendor", "testdata", "build", "dist", "node_modules":
				return fs.SkipDir
			}
			if path == self {
				return fs.SkipDir // the core itself: persistLocked and Save live here
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // not our business to police unparseable files
		}
		if !b30ImportsPhonecore(f) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		scanned = append(scanned, rel)

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Save" {
				return true
			}
			t.Errorf("%s:%d: %s.Save(...) -- a whole-blob adopt outside the core.\n\n"+
				"Save takes a State the caller read earlier and adopts ALL of it, so every "+
				"core-internal persist between that read and this write is reverted. On the pairing "+
				"path that is the epoch content key the relay drain just installed, with the "+
				"monotonically-merged grant watermark left standing at its coordinates -- so the "+
				"gateway's re-appended bootstrap frame is refused as a replay forever and the "+
				"handset can never obtain a key again. Use phonecore.Core.Mutate, which applies the "+
				"change under the core lock.",
				rel, fset.Position(call.Pos()).Line, b30Expr(sel.X))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	// The importers of this package outside it, at the time of writing: internal/phonesim and
	// six mobile facade files. A floor of five keeps the fence honest against a walk that
	// silently stops finding them.
	if len(scanned) < 5 {
		t.Errorf("the fence scanned only %d file(s) importing this package (%v); it is measuring "+
			"almost nothing and would pass with every call site restored", len(scanned), scanned)
	}
}

func b30ImportsPhonecore(f *ast.File) bool {
	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err == nil && p == "github.com/Nathandela/swarm/internal/phonecore" {
			return true
		}
	}
	return false
}

// b30Expr renders a receiver expression for the diagnostic. Only the shapes a Core is ever
// held under here need naming; anything else is reported structurally rather than guessed at.
func b30Expr(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return b30Expr(v.X) + "." + v.Sel.Name
	default:
		return fmt.Sprintf("%T", e)
	}
}
