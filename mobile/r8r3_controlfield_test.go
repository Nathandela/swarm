package swarmmobile

// WAVE R8 / ROUND 3 -- THE FACADE HANDS KOTLIN THE ROUTER'S ANSWER, NOT THE RECORD'S FIELD.
//
// MAJOR 4. `app.go` read `out.TerminalControl = rec.TerminalControl` VERBATIM, three lines
// under the paragraph that rules exactly this out for the strictly WEAKER composer predicate:
// "StructuredChat is the COMPOSER's predicate, not the record's raw field, and the difference
// is load-bearing ... A screen reading the raw boolean would offer a composer over a record
// the router already refused."
//
// THE CONSEQUENCE NEEDS NO ATTACKER AND NO MALFORMED RECORD. A perfectly valid opencode record
// on a machine whose profile carries `terminal_view_version == 0` -- which is every machine
// deployed before this wave, and the state of every phone before its first successful
// reconcile -- routes to the STATUS CARD through `RouteSession`, while the facade hands Kotlin
// `terminalControl = true`. A keyboard offered over a session the router refused is the
// composer defect one destination over.
//
// The test drives `App.session`, which is the ONE place a `Session` is built for Kotlin, with
// a bare App -- whose profile is the zero value by construction, which is precisely the
// deployed state under test.

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// r8r3GrantingRecord is a VALID record that grants terminal control: nothing about it is
// malformed, stale or hostile.
func r8r3GrantingRecord() *schema.SessionCapabilities {
	return &schema.SessionCapabilities{
		Provider: "opencode", ProviderVersion: "0.9.0", AdapterRevision: "rev-test",
		SessionInstance: "inst-1", TerminalFallback: true, TerminalControl: true,
	}
}

// TestR8R3Facade_AZeroProfileHandsKotlinNoTerminalControl is the measured consequence.
func TestR8R3Facade_AZeroProfileHandsKotlinNoTerminalControl(t *testing.T) {
	a := &App{}
	got := a.session(phonecore.CachedSession{
		SessionID:    "mach1/sess1",
		Present:      true,
		Capabilities: r8r3GrantingRecord(),
	})

	if got.Destination != phonecore.DestinationStatusCard.String() {
		t.Fatalf("a bare App routed this session to %q; the zero profile is T5-a's 'no fallback "+
			"exists on this machine' and this test is about what the facade says ALONGSIDE that",
			got.Destination)
	}
	if got.TerminalControl {
		t.Fatalf("the facade handed Kotlin terminalControl=true for a session it routed to the " +
			"STATUS CARD.\n" +
			"The record is valid and says terminal_control; the MACHINE says it publishes no " +
			"TerminalView at all. A screen that reads this boolean would draw a control banner " +
			"and a keyboard over a session with no terminal behind it, and every byte would be " +
			"refused at a daemon the phone never told the user about.")
	}
	if got.StructuredChat {
		t.Fatalf("the composer predicate regressed on the same path")
	}
}

// TestR8R3Facade_AnInconsistentRecordHandsKotlinNoTerminalControl is the other arm: the
// facade must not resolve a record the router refuses, whatever the profile says.
func TestR8R3Facade_AnInconsistentRecordHandsKotlinNoTerminalControl(t *testing.T) {
	for _, tc := range []struct {
		name string
		rec  *schema.SessionCapabilities
	}{
		{"structured and fallback both true", &schema.SessionCapabilities{
			Provider: "claude", ProviderVersion: "1", AdapterRevision: "r",
			SessionInstance: "inst-1", StructuredChat: true, TerminalFallback: true, TerminalControl: true,
		}},
		{"control without fallback", &schema.SessionCapabilities{
			Provider: "opencode", ProviderVersion: "1", AdapterRevision: "r",
			SessionInstance: "inst-1", TerminalControl: true,
		}},
		{"no session instance", &schema.SessionCapabilities{
			Provider: "opencode", ProviderVersion: "1", AdapterRevision: "r",
			TerminalFallback: true, TerminalControl: true,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{}
			if a.session(phonecore.CachedSession{SessionID: "m/s", Capabilities: tc.rec}).TerminalControl {
				t.Fatalf("the facade granted terminal control over an inconsistent record")
			}
		})
	}
}

// TestR8R3Facade_TheControlFieldIsResolvedThroughThePredicate is the rule-4 source half,
// written over `App.session`'s own body rather than over the file: it names the predicate the
// behavioural tests above cannot distinguish from a lucky constant, and it is the assertion
// that fails on the day someone re-inlines the raw field for a session that DOES route to the
// fallback -- a case a bare App cannot construct.
func TestR8R3Facade_TheControlFieldIsResolvedThroughThePredicate(t *testing.T) {
	body := funcBody(readMobileSource(t, "app.go"), "func (a *App) session(")
	if !strings.Contains(body, "phonecore.TerminalControlAvailable(") {
		t.Errorf("App.session does not resolve TerminalControl through " +
			"phonecore.TerminalControlAvailable. The record's raw field is not the predicate: it " +
			"says what the MACHINE authored, and what Kotlin needs is what the ROUTER concluded " +
			"over that record AND the machine's published profile.")
	}
	if strings.Contains(body, "out.TerminalControl = rec.TerminalControl") {
		t.Errorf("App.session assigns the record's raw terminal_control field to the bound " +
			"Session. That is the exact line round-3 major 4 names.")
	}
	if !strings.Contains(body, "phonecore.RouteSession(") {
		t.Fatalf("the body extractor did not find App.session; every assertion above is vacuous")
	}
}
