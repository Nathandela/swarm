package swarmmobile_test

// Slice S11 REVIEW ROUND 4 -- the recurrence fence for the phone -> machine bucket's ordering
// lock.
//
// WHY A FENCE AND NOT JUST THE BEHAVIOURAL TEST. The inversion has now been found twice: once
// on the input frames (round 3) and once on the command sealers the round-3 fix did not cover
// (round 4). Both times the behavioural test that caught it had to drive the exact producer
// pair involved, and both times the pair that was NOT driven stayed broken. The property is
// not "these two producers are serialised", it is "every append on this bucket draws its seq
// inside the section that appends it" -- which is a statement about the source, and there are
// only ever a handful of append sites.
//
// It is the same shape as the round-3 reachability fence: read off the source, so a NEW append
// site is covered the moment it is written rather than when someone thinks to race it.
//
// This file contains NO implementation.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestS11R4_LockedAppendHelperExceptionRequiresAHeldBucketLock(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{name: "held", body: `a.bucketMu.Lock(); defer a.bucketMu.Unlock(); a.flushPendingPublicationsLocked()`, want: true},
		{name: "missing", body: `a.flushPendingPublicationsLocked()`, want: false},
		{name: "released", body: `a.bucketMu.Lock(); a.bucketMu.Unlock(); a.flushPendingPublicationsLocked()`, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package p; func f() {"+tc.body+"}", 0)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			fd := f.Decls[0].(*ast.FuncDecl)
			calls, got := s11r4LockedHelperCallIsProtected(fd.Body, s11r4LockedAppendHelper)
			if calls != 1 || got != tc.want {
				t.Fatalf("calls=%d protected=%v, want calls=1 protected=%v", calls, got, tc.want)
			}
		})
	}
}

// s11r4 names the three identifiers the rule is written in terms of.
const (
	s11r4BucketLock = "bucketMu"
	s11r4Append     = "MailboxAppend"
	// The durable publication pump is intentionally factored as a Locked helper because
	// its callers must keep the same critical section after draining while a direct producer
	// draws its next sequence. The exception below is exact-name and verifies every call.
	s11r4LockedAppendHelper = "flushPendingPublicationsLocked"
)

// s11r4SeqDraws is every allocator that hands out a seq for this bucket. Both kinds are here
// because ONE Sequencer numbers both: phonecore.Sequencer's doc records that "Commands AND
// input frames draw from ONE Sequencer per epoch because they share a single MailboxReceiver
// key", which is precisely the fact round 3's input-only lock did not act on.
var s11r4SeqDraws = []string{"NextCommand", "NextInput"}

// s11r4SelectorCallPos returns the position of the first call whose callee selector is sel,
// or token.NoPos.
func s11r4SelectorCallPos(body *ast.BlockStmt, sel string) token.Pos {
	found := token.NoPos
	ast.Inspect(body, func(n ast.Node) bool {
		if found != token.NoPos {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if se, ok := call.Fun.(*ast.SelectorExpr); ok && se.Sel.Name == sel {
			found = call.Lparen
			return false
		}
		return true
	})
	return found
}

// s11r4LockPos returns the position of the first `<x>.bucketMu.Lock()` in body.
func s11r4LockPos(body *ast.BlockStmt) token.Pos {
	found := token.NoPos
	ast.Inspect(body, func(n ast.Node) bool {
		if found != token.NoPos {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		lock, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || lock.Sel.Name != "Lock" {
			return true
		}
		if mu, ok := lock.X.(*ast.SelectorExpr); ok && mu.Sel.Name == s11r4BucketLock {
			found = call.Lparen
			return false
		}
		return true
	})
	return found
}

// s11r4LockedHelperCallIsProtected checks the exact lexical contract of the one factored
// append helper: a bucket lock precedes the call and no explicit unlock intervenes. A defer
// Unlock is after the call lexically and therefore remains accepted. This is deliberately not
// a generic "name ends in Locked" exemption; a new append helper must earn its own review.
func s11r4LockedHelperCallIsProtected(body *ast.BlockStmt, helper string) (calls int, protected bool) {
	protected = true
	var lockAt, unlockAt []token.Pos
	var callAt []token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		if _, deferred := n.(*ast.DeferStmt); deferred {
			return false // a deferred Unlock runs after the helper, irrespective of lexical position
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == helper {
			callAt = append(callAt, call.Lparen)
		}
		if mu, ok := sel.X.(*ast.SelectorExpr); ok && mu.Sel.Name == s11r4BucketLock {
			switch sel.Sel.Name {
			case "Lock":
				lockAt = append(lockAt, call.Lparen)
			case "Unlock":
				unlockAt = append(unlockAt, call.Lparen)
			}
		}
		return true
	})
	for _, at := range callAt {
		guard := token.NoPos
		for _, pos := range lockAt {
			if pos < at && pos > guard {
				guard = pos
			}
		}
		if guard == token.NoPos {
			protected = false
			continue
		}
		for _, pos := range unlockAt {
			if pos > guard && pos < at {
				protected = false
			}
		}
	}
	return len(callAt), protected
}

func s11r4EveryLockedHelperCallerIsProtected(src *facadeSource, helper string) bool {
	calls := 0
	for _, f := range src.Files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || fd.Name.Name == helper {
				continue
			}
			n, protected := s11r4LockedHelperCallIsProtected(fd.Body, helper)
			calls += n
			if !protected {
				return false
			}
		}
	}
	return calls > 0
}

// TestS11R4_EveryBucketAppendAllocatesItsSeqUnderTheBucketLock is the rule, applied to every
// append site the facade has.
//
// THE ORDER IS THE ASSERTION, not merely the presence of a lock. Round 3's defect was not a
// missing mutex -- sendInputFrame had one -- it was that the OTHER producers on the same
// stream drew their seq with nothing held. A lock taken after the draw serialises the append
// and leaves the numbering racy, which is the same inversion with an extra lock in it.
func TestS11R4_EveryBucketAppendAllocatesItsSeqUnderTheBucketLock(t *testing.T) {
	src := loadFacade(t)

	sites := 0
	for _, f := range src.Files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			appendPos := s11r4SelectorCallPos(fd.Body, s11r4Append)
			if appendPos == token.NoPos {
				continue
			}
			sites++
			label := s11FuncLabel(receiverTypeName(fd.Recv), fd.Name.Name)

			lockPos := s11r4LockPos(fd.Body)
			if lockPos == token.NoPos {
				if fd.Name.Name == s11r4LockedAppendHelper &&
					s11r4EveryLockedHelperCallerIsProtected(src, s11r4LockedAppendHelper) {
					continue
				}
				t.Errorf("%s appends to the phone -> machine bucket without taking a.%s.\n"+
					"Commands and input frames draw from ONE Sequencer per epoch, so every append site "+
					"is a producer on one stream. An unserialised one lets a LATER seq reach the relay "+
					"first; the machine then drops the low frame (crypto.ErrStaleSeq) while "+
					"MailboxAppend returned nil, so a keystroke vanishes mid-line or a command is never "+
					"answered and its op stays pending forever.", label, s11r4BucketLock)
				continue
			}

			for _, draw := range s11r4SeqDraws {
				drawPos := s11r4SelectorCallPos(fd.Body, draw)
				if drawPos == token.NoPos {
					continue
				}
				if lockPos > drawPos {
					t.Errorf("%s calls %s at %s BEFORE taking a.%s at %s.\n"+
						"Serialising the append alone is not the property: the seq must be drawn inside "+
						"the section that appends it, or two producers still number their frames in one "+
						"order and put them on the wire in another.",
						label, draw, src.Fset.Position(drawPos), s11r4BucketLock,
						src.Fset.Position(lockPos))
				}
				if drawPos > appendPos {
					t.Errorf("%s appends at %s before drawing its seq at %s; the append site and the "+
						"allocation have come apart", label, src.Fset.Position(appendPos),
						src.Fset.Position(drawPos))
				}
			}
		}
	}

	// NON-VACUITY. The facade has three append sites today (sendInputFrame,
	// sealSignedCommand, unsignedCommand). A refactor that routed them through one helper is
	// welcome and would leave one; zero means this guard found nothing to guard and every
	// assertion above passed for free.
	if sites == 0 {
		t.Fatalf("no function in the facade calls %s, so this guard asserted nothing. Either the "+
			"append moved behind a name this fence does not know, or the phone no longer sends.",
			s11r4Append)
	}
}
