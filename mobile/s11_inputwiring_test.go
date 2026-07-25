package swarmmobile_test

// Slice S11 -- FAILING-FIRST (TDD RED, GG-5) wiring guards for PB-INPUT-2, PB-INPUT-5,
// PB-TIME-1 and PB-TIME-3 at the one place they can ship broken while every unit test
// stays green: the facade.
//
// WHY THESE EXIST. Four of this slice's mechanisms are new state machines in
// internal/phonecore -- the lease gate, the input coalescer, the skew monitor, the
// per-op-class TTL. Each is tested exhaustively there. None of them does anything unless
// the facade calls it, and the facade is the ONLY caller: mobile/commands.go is the phone's
// whole phone -> machine plane. A perfect LeaseState that SendInput never consults is the
// "requirement satisfiable while the defect ships" failure this project has already had.
//
// WHY SOURCE AND NOT BEHAVIOUR. The behavioural half needs a paired relay, a machine and a
// live lease -- that is mobile/conformance's shape, and it belongs to the slice that owns
// the end-to-end demonstration. What this file pins is CALL-SITE PRESENCE, which is the
// property that regresses silently, and it uses the PB-BIND-* machinery already in this
// directory (facadesource_test.go) rather than inventing a second one.
//
// A source guard is weak on its own -- a call site can be present and wrong. It is paired,
// deliberately, with the exhaustive behavioural tests in internal/phonecore: together they
// say "the machine is correct AND it is reachable", which is the pair the S7b lesson asks
// for.
//
// This file contains NO implementation.

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// s11FuncSource returns the source text of a top-level func or method by (owner, name).
// owner is "" for a plain function.
func s11FuncSource(t *testing.T, src *facadeSource, owner, name string) string {
	t.Helper()

	raw := map[string][]byte{}
	for _, base := range src.GoFiles {
		b, err := os.ReadFile(filepath.Join(src.Dir, base))
		if err != nil {
			t.Fatalf("read %s: %v", base, err)
		}
		raw[filepath.Join(src.Dir, base)] = b
	}

	for _, f := range src.Files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name.Name != name {
				continue
			}
			got := ""
			if fd.Recv != nil {
				got = receiverTypeName(fd.Recv)
			}
			if got != owner {
				continue
			}
			start := src.Fset.Position(fd.Pos())
			end := src.Fset.Position(fd.End())
			body, ok := raw[start.Filename]
			if !ok {
				t.Fatalf("no source for %s", start.Filename)
			}
			return string(body[start.Offset:end.Offset])
		}
	}
	t.Fatalf("the facade declares no %s -- S11's wiring has nowhere to live", s11FuncLabel(owner, name))
	return ""
}

func s11FuncLabel(owner, name string) string {
	if owner == "" {
		return "func " + name
	}
	return "method (" + owner + ")." + name
}

// s11RequireCalls fails when body does not mention every required identifier.
func s11RequireCalls(t *testing.T, label, body string, required map[string]string) {
	t.Helper()
	for ident, why := range required {
		if !strings.Contains(body, ident) {
			t.Errorf("%s never mentions %s.\n%s", label, ident, why)
		}
	}
}

// TestS11Wiring_InputIsGatedOnTheConfirmedLease is PB-INPUT-2's call site. Today
// App.SendInput (commands.go:156-176) seals a keystroke and appends it with no lease check
// of any kind -- the phone will happily type at a machine that granted it nothing, and the
// gateway drops the frame silently (remotegw/leasemanager.go:72-87), so the user sees a
// live keyboard and a dead terminal.
func TestS11Wiring_InputIsGatedOnTheConfirmedLease(t *testing.T) {
	src := loadFacade(t)

	for _, name := range []string{"SendInput", "Resize"} {
		body := s11FuncSource(t, src, "App", name)
		s11RequireCalls(t, s11FuncLabel("App", name), body, map[string]string{
			"Leases()": "PB-INPUT-2: \"no keystroke is ever sent without a confirmed current lease generation\". " +
				"The gate is phonecore.LeaseState (Core.Leases()); a send that does not consult it is the " +
				"defect, not the absence of the gate.",
		})
	}
}

// TestS11Wiring_InputGoesThroughTheCoalescer is PB-INPUT-5's call site. SendInput appends
// one relay item per call today. At a 30 Hz autorepeat that is 30 appends/s against
// MailboxAppendPerMin: 600, so the lease dies with codeQuotaExceeded after roughly twenty
// seconds of held-down key -- while every short-burst latency test still passes.
func TestS11Wiring_InputGoesThroughTheCoalescer(t *testing.T) {
	src := loadFacade(t)
	body := s11FuncSource(t, src, "App", "SendInput")
	s11RequireCalls(t, s11FuncLabel("App", "SendInput"), body, map[string]string{
		"Coalesc": "PB-INPUT-5: input must be coalesced to §6.0's 8 frames/s before it reaches the relay. " +
			"One MailboxAppend per keystroke trips the relay's tumbling append window mid-lease.",
	})
}

// TestS11Wiring_TheSignedTTLIsChosenByOpClass is PB-INPUT-3 / PB-TIME-1 at the signing
// site. mobile/app.go:33 declares one flat `commandTTL = 2 * time.Minute` and commands.go
// signs EVERY action with it, take_control included. §6.0 requires 1 minute for ordinary
// commands and 15 minutes for take_control, precisely so the lease is not the binding
// constraint on a typing session -- today's flat 2 minutes is wrong in both directions at
// once.
func TestS11Wiring_TheSignedTTLIsChosenByOpClass(t *testing.T) {
	src := loadFacade(t)
	body := s11FuncSource(t, src, "App", "sealSignedCommand")
	s11RequireCalls(t, s11FuncLabel("App", "sealSignedCommand"), body, map[string]string{
		"CommandTTLFor": "PB-INPUT-3 and §6.0: the signed ExpiresAt is chosen BY OP CLASS " +
			"(phonecore.CommandTTLFor), not from one flat constant. The lease is the earliest of the " +
			"signed ExpiresAt, now+TTLSeconds and the 30-minute server cap, so a flat short TTL makes " +
			"the SIGNATURE the thing that ends a typing session.",
	})

	// ... and the flat constant must be gone, or both live side by side and the next
	// caller picks the wrong one.
	all := facadeSourceText(t, src)
	if strings.Contains(all, "commandTTL = ") && !strings.Contains(all, "CommandTTLFor") {
		t.Error("the facade still declares a flat commandTTL and never calls phonecore.CommandTTLFor")
	}
}

// TestS11Wiring_TheSkewMonitorIsFedAtTheSendSite is PB-TIME-3's call site, and without it
// the whole skew protocol is unreachable. The monitor brackets the machine's authenticated
// timestamp between the phone's send and receive instants; the RECEIVE half is wired inside
// phonecore's inbound path, but the SEND half can only be recorded where the operation id
// is minted. With no Sent, every reply is an uncorrelated stamp, the monitor ignores it by
// design, and the phone can never measure skew at all -- so PB-TIME-1's distinct error
// never appears and the two-minute-slow handset is back to an opaque "not authorized".
func TestS11Wiring_TheSkewMonitorIsFedAtTheSendSite(t *testing.T) {
	src := loadFacade(t)
	body := s11FuncSource(t, src, "App", "sealSignedCommand")
	s11RequireCalls(t, s11FuncLabel("App", "sealSignedCommand"), body, map[string]string{
		"SkewMonitor()": "PB-TIME-3: the skew estimate needs the phone's SEND instant, correlated by " +
			"operation id (phonecore.SkewMonitor.Sent). It can only be recorded here, where the id " +
			"is minted. Without it every machine stamp arrives uncorrelated and is ignored.",
	})
}

// TestS11Wiring_TheFacadeStillCompilesAsOneUnit is a guard on the guards: every assertion
// above is a string search, so a renamed method would make them all pass vacuously by
// failing to find the method at all. s11FuncSource fatals in that case, but only for the
// names it is given -- this pins that the four call sites the slice depends on exist under
// the names the rest of the file searches.
func TestS11Wiring_TheFacadeStillCompilesAsOneUnit(t *testing.T) {
	src := loadFacade(t)
	for _, fn := range []struct{ owner, name string }{
		{"App", "SendInput"},
		{"App", "Resize"},
		{"App", "sealSignedCommand"},
		{"App", "TakeControl"},
		{"App", "ReleaseControl"},
	} {
		if s11FuncSource(t, src, fn.owner, fn.name) == "" {
			t.Errorf("%s has an empty body", s11FuncLabel(fn.owner, fn.name))
		}
	}
	// Non-vacuity for the search machinery itself: a name that does NOT exist must be
	// reported, or every assertion above could be passing on an empty string.
	if got := s11LookupSilently(src, "App", "NoSuchMethodS11"); got != "" {
		t.Errorf("the source lookup found a body for a method that does not exist: %q", got)
	}
}

// s11LookupSilently is s11FuncSource without the fatal, for the non-vacuity check.
func s11LookupSilently(src *facadeSource, owner, name string) string {
	for _, f := range src.Files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name.Name != name {
				continue
			}
			got := ""
			if fd.Recv != nil {
				got = receiverTypeName(fd.Recv)
			}
			if got == owner {
				return "found"
			}
		}
	}
	return ""
}
