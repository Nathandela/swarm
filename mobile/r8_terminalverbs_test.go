package swarmmobile

// WAVE R8 / SLICES S4 + S7 -- THE FACADE VERBS, AND THE ONE THEY MUST NOT BECOME.
// Failing-first (TDD RED, GG-5).
//
// The gomobile facade is where a Kotlin screen meets the wire, so it is where two of this
// wave's rules are enforceable as CODE rather than as UI review:
//
//   - T4: a watch is a READ and grants no input authority. The watch verbs must be reachable
//     without any lease, any generation and any control state, and must not touch the input
//     coalescer at all.
//   - T6-f / D-NOQUEUE: input the phone accepted from the user and will not send is resolved
//     as an explicit undelivered record and NEVER enters the coalescer's buffer. `refuseInput`
//     (commands.go:478-489) already does exactly this for the lease path and says why:
//     "buffering input for a lease or a link that is gone is the queue ADR-007 D7 makes
//     structurally impossible". The terminal-control path inherits that rule verbatim; the
//     failure mode is that it inherits `Resize`'s instead, which is an explicit FLUSHER
//     (commands.go:492-509, PB-INPUT-6).
//
// THE SEAMS (undefined symbols -> compile-fail RED):
//
//	func (a *App) TerminalViewWatch(session string) error
//	func (a *App) TerminalViewUnwatch(session string) error
//	func (a *App) TerminalViewRenew(session string) error
//	func (a *App) TerminalControlBegin(session string) error
//	func (a *App) TerminalControlEnd(session string) error
//	func (a *App) TerminalInput(session string, text string) error
//	func (a *App) TerminalControlKeepalive(session string) error
//
// GOMOBILE BINDING NOTE, stated so GREEN does not discover it at aar-build time: every
// exported facade method must use gomobile-bindable types only (string, int, bool, error,
// []byte, and bound struct pointers). `TerminalInput` takes a string rather than a byte
// slice for the same reason `Paste` does.

import (
	"os"
	"strings"
	"testing"
)

// TestR8Facade_WatchVerbsExistAndGrantNoInputAuthority is T4's "watch grants no input
// authority", asserted where the authority would have to be acquired.
func TestR8Facade_WatchVerbsExistAndGrantNoInputAuthority(t *testing.T) {
	src := readMobileSource(t, "commands.go")
	for _, verb := range []string{"TerminalViewWatch", "TerminalViewUnwatch", "TerminalViewRenew"} {
		if !strings.Contains(src, "func (a *App) "+verb+"(") {
			t.Errorf("ADR-017 T4: the facade declares no %s. The fallback screen has no way to ask for a "+
				"snapshot, so R8a ships a screen with nothing on it.", verb)
		}
	}
	// The watch verbs must not require a lease. `Leases().Require` is the input plane's gate;
	// a watch that called it would make READING a session conditional on holding the input
	// authority T4 says a watch does not grant.
	watchBody := funcBody(src, "func (a *App) TerminalViewWatch(")
	for _, forbidden := range []string{"Leases()", "Require(", "coalesce."} {
		if strings.Contains(watchBody, forbidden) {
			t.Errorf("ADR-017 T4: TerminalViewWatch names %q. A watch is a READ: it acquires no lease, "+
				"mints no generation and never touches the input coalescer. Gating a read on the input "+
				"plane is how a monitoring surface becomes a control surface by accident.", forbidden)
		}
	}
}

// TestR8Facade_RefusedTerminalInputIsUndeliveredAndNeverBuffered is D-NOQUEUE at the facade.
//
// The two wrong answers are symmetrical and both ship a lie: buffering makes the UI look
// successful and delivers the bytes later (the offline queue B43 proved unbuildable and
// ADR-011 re-affirms), and dropping silently makes the UI look successful and never delivers
// them (PB-INPUT-1). The right answer already exists one path over, in `refuseInput`.
func TestR8Facade_RefusedTerminalInputIsUndeliveredAndNeverBuffered(t *testing.T) {
	src := readMobileSource(t, "commands.go")
	body := funcBody(src, "func (a *App) TerminalInput(")
	if body == "" {
		t.Fatalf("ADR-017 T6: the facade declares no TerminalInput, so R8b's input plane does not exist")
	}
	if !strings.Contains(body, "refuse") {
		t.Errorf("ADR-017 T6-f / PB-INPUT-1: TerminalInput has no refusal path. Input the phone accepted " +
			"from the user and will not send must be resolved as an explicit undelivered record; " +
			"refuseInput (commands.go:478-489) is the shape, and it keeps the bytes out of the " +
			"coalescer's buffer on purpose.")
	}
	if strings.Contains(body, "Flush(") {
		t.Errorf("ADR-017 T6-f: TerminalInput reaches a Flush. Resize is this file's one explicit flusher " +
			"(PB-INPUT-6) and it exists so a resize never overtakes the bytes typed against the old grid; " +
			"flushing on the CONTROL path is the opposite move -- it releases bytes whose authority has " +
			"just been withdrawn.")
	}
}

// TestR8Facade_ThePhoneStillNeverAuthorsAnApprovingKeystroke is ADR-017 T6's last bullet,
// re-asserted at the facade this wave adds a raw-byte verb to.
//
// `mobile/interaction_screencoverage_test.go:100-137` bans `approvekeystroke`, `answerprompt`,
// `typeapproval` and their siblings because "the phone must never author the approving
// keystroke". `terminal_input` is a raw-byte verb, so it is the first thing in the app that
// COULD satisfy that ban by another name -- T6 says it "is not an approval verb and must
// never become one". This is the fence that says so at the level where the bytes are.
func TestR8Facade_ThePhoneStillNeverAuthorsAnApprovingKeystroke(t *testing.T) {
	src := readMobileSource(t, "commands.go")
	lowered := strings.ToLower(src)
	for _, banned := range []string{"approvekeystroke", "answerprompt", "typeapproval", "approveterminal", "terminalapprove"} {
		if strings.Contains(lowered, banned) {
			t.Errorf("ADR-017 T6: the facade names %q. An approval answered from a fallback screen still "+
				"travels as the signed ActionApprove of IS-LIFE-4, or the button is not shown; a raw-byte "+
				"verb that answers an approval is the facade ban satisfied by another name.", banned)
		}
	}
	// And the reverse direction: the approval path must not acquire a terminal generation.
	if body := funcBody(src, "func (a *App) Approve("); body != "" {
		for _, forbidden := range []string{"TerminalControl", "controlGeneration", "TerminalInput"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("ADR-017 T6 / ADR-013:227-228: Approve names %q. The phone sends a signed decision "+
					"id and the daemon types; an approval that reached for the raw-byte plane would be the "+
					"phone authoring the keystroke.", forbidden)
			}
		}
	}
}

// funcBody returns the source between a function's declaration and the next top-level
// declaration, which is enough to say which symbols that function reaches for.
func funcBody(src, decl string) string {
	i := strings.Index(src, decl)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	if j := strings.Index(rest[1:], "\nfunc "); j >= 0 {
		return rest[:j+1]
	}
	return rest
}

// readMobileSource reads one file of this package as text. These obligations are about which
// code paths a verb reaches for, which no single call can report.
func readMobileSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
