package phonecore

// WAVE R8 / ROUND 3 -- THE CONTROL PREDICATE, AND THE DECODE SEAM THAT HAD NO FENCE.
//
// MAJOR 4. mobile/app.go read `out.TerminalControl = rec.TerminalControl` VERBATIM, three
// lines under the paragraph that rules exactly this out for the strictly WEAKER composer
// predicate: "StructuredChat is the COMPOSER's predicate, not the record's raw field, and the
// difference is load-bearing ... A screen reading the raw boolean would offer a composer over
// a record the router already refused."
//
// Two consequences, and the first needs no attacker and no malformed record:
//
//  1. a perfectly VALID opencode record on a machine whose profile carries
//     terminal_view_version == 0 -- which is EVERY machine deployed before this wave --
//     routes to the status card via RouteSession while Session.TerminalControl is handed to
//     Kotlin as `true`;
//  2. against an INCONSISTENT record the only guard was journal.go's Validate() at the phone
//     decode seam, and that condition could be replaced with `true` with the whole phonecore
//     package still green. The seam had no fence at all.
//
// Both are closed here: one predicate, resolved through the router exactly as
// ComposerAvailable is, and a decode-seam fence that fails when the validation is removed.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func r8r3Profile() schema.RemoteProfileV1 {
	return schema.RemoteProfileV1{
		Version:                 schema.CurrentProfileVersion,
		CapabilityRecordVersion: schema.CurrentCapabilityRecordVersion,
		TerminalViewVersion:     schema.CurrentTerminalViewVersion,
	}
}

func r8r3Fallback(control bool) *schema.SessionCapabilities {
	return &schema.SessionCapabilities{
		Provider: "opencode", ProviderVersion: "0.9.0", AdapterRevision: "rev-test",
		SessionInstance: "inst-1", TerminalFallback: true, TerminalControl: control,
	}
}

// TestR8R3_TerminalControlIsTheRoutersAnswerAndNotTheRecordsRawField is MAJOR 4's table. Every
// fail-closed row names the state the user is protected from, and the first row needs nothing
// hostile at all -- only a machine that has not shipped this wave yet.
func TestR8R3_TerminalControlIsTheRoutersAnswerAndNotTheRecordsRawField(t *testing.T) {
	inconsistent := &schema.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1", AdapterRevision: "r",
		SessionInstance: "inst-1", StructuredChat: true, TerminalFallback: true, TerminalControl: true,
	}
	noInstance := &schema.SessionCapabilities{
		Provider: "opencode", ProviderVersion: "1", AdapterRevision: "r",
		TerminalFallback: true, TerminalControl: true,
	}
	for _, tc := range []struct {
		name     string
		rec      *schema.SessionCapabilities
		profile  schema.RemoteProfileV1
		want     bool
		protects string
	}{
		{"a granted fallback session on a current machine", r8r3Fallback(true), r8r3Profile(), true, ""},
		{"a degraded fallback session may watch and not control", r8r3Fallback(false), r8r3Profile(), false,
			"T6-b: control is granted only where terminal_fallback was authored true at launch"},
		{"a machine that publishes no TerminalView version", r8r3Fallback(true), schema.RemoteProfileV1{
			Version: schema.CurrentProfileVersion, CapabilityRecordVersion: schema.CurrentCapabilityRecordVersion,
		}, false, "T5-a: a zero version means NO FALLBACK EXISTS, which is every pre-R8 machine"},
		{"a machine that publishes no capability-record version", r8r3Fallback(true), schema.RemoteProfileV1{
			Version: schema.CurrentProfileVersion, TerminalViewVersion: schema.CurrentTerminalViewVersion,
		}, false, "T5-a: a zero version means the record is UNTRUSTED"},
		{"the zero profile", r8r3Fallback(true), schema.RemoteProfileV1{}, false,
			"the state of every machine before its first successful reconcile"},
		{"an inconsistent record", inconsistent, r8r3Profile(), false,
			"T2-b: structured_chat && terminal_fallback is unrepresentable and is refused, not resolved"},
		{"a record binding no instance", noInstance, r8r3Profile(), false,
			"T8-a: a generation must bind an incarnation or it binds nothing"},
		{"an absent record", nil, r8r3Profile(), false,
			"T2-a: no record is the honest status card and a refusal of both verbs"},
		{"a structured session", &schema.SessionCapabilities{
			Provider: "claude", ProviderVersion: "1", AdapterRevision: "r",
			SessionInstance: "inst-1", StructuredChat: true,
		}, r8r3Profile(), false, "the wave's exit: Claude and Codex expose NO ROUTE to the terminal plane"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := TerminalControlAvailable(tc.rec, tc.profile); got != tc.want {
				t.Fatalf("TerminalControlAvailable = %v, want %v. %s", got, tc.want, tc.protects)
			}
		})
	}
}

// TestR8R3_TheControlPredicateNeverOutrunsTheRouter is the invariant the table above is one
// sampling of: control is a strictly narrower authority than the destination, so no record and
// no profile may answer true for control while routing anywhere but the fallback.
func TestR8R3_TheControlPredicateNeverOutrunsTheRouter(t *testing.T) {
	profiles := []schema.RemoteProfileV1{r8r3Profile(), {}, {Version: schema.CurrentProfileVersion}}
	recs := []*schema.SessionCapabilities{
		nil, r8r3Fallback(true), r8r3Fallback(false),
		{Provider: "claude", ProviderVersion: "1", AdapterRevision: "r", SessionInstance: "i", StructuredChat: true},
		{Provider: "x", ProviderVersion: "1", AdapterRevision: "r", SessionInstance: "i"},
		{Provider: "x", ProviderVersion: "1", AdapterRevision: "r", TerminalFallback: true, TerminalControl: true},
	}
	for _, p := range profiles {
		for _, r := range recs {
			if TerminalControlAvailable(r, p) && RouteSession(r, p) != DestinationTerminalFallback {
				t.Fatalf("control was granted over a session routed to %s (record %+v, profile %+v)",
					RouteSession(r, p), r, p)
			}
			if ComposerAvailable(r, p) && TerminalControlAvailable(r, p) {
				t.Fatalf("one session answered true for BOTH the composer and raw terminal " +
					"control; T1 gives a session exactly one surface")
			}
		}
	}
}

// TestR8R3_ThePhoneDecodeSeamRejectsAnInconsistentRecord is amendment T2-b's THIRD seam,
// fenced. Replacing journal.go's `rec.Capabilities.Validate() == nil` with `true` left
// `go test ./internal/phonecore/` fully green; this is the test that stops being green.
func TestR8R3_ThePhoneDecodeSeamRejectsAnInconsistentRecord(t *testing.T) {
	for _, tc := range []struct {
		name string
		rec  *schema.SessionCapabilities
	}{
		{"structured and fallback both true", &schema.SessionCapabilities{
			Provider: "claude", ProviderVersion: "1", AdapterRevision: "r",
			SessionInstance: "inst-1", StructuredChat: true, TerminalFallback: true,
		}},
		{"control without fallback", &schema.SessionCapabilities{
			Provider: "opencode", ProviderVersion: "1", AdapterRevision: "r",
			SessionInstance: "inst-1", TerminalControl: true,
		}},
		{"no session instance", &schema.SessionCapabilities{
			Provider: "opencode", ProviderVersion: "1", AdapterRevision: "r", TerminalFallback: true,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewSessionCache()
			if !c.Apply(schema.JournalRecord{
				Cursor: 1, SessionID: "m/s1", Type: "launched", Capabilities: tc.rec,
			}) {
				t.Fatalf("the cache refused the record outright; this test is about the CAPABILITY " +
					"record being dropped, not the journal record")
			}
			got, ok := c.Get("m/s1")
			if !ok {
				t.Fatalf("the session did not reach the cache at all")
			}
			if got.Capabilities != nil {
				t.Fatalf("an inconsistent capability record survived the phone's decode seam "+
					"(%+v).\nAmendment T2-b names this as one of the three places the record is "+
					"validated. Keeping it means every downstream predicate has to re-derive the "+
					"same refusal, and the one that forgets is the one that ships.", *got.Capabilities)
			}
		})
	}
}

// TestR8R3_ThePhoneDecodeSeamKeepsAValidRecord is the vacuity guard: a seam that dropped
// everything would pass the test above and route every session to the status card.
func TestR8R3_ThePhoneDecodeSeamKeepsAValidRecord(t *testing.T) {
	c := NewSessionCache()
	rec := r8r3Fallback(true)
	if !c.Apply(schema.JournalRecord{Cursor: 1, SessionID: "m/s1", Type: "launched", Capabilities: rec}) {
		t.Fatalf("the cache refused a valid record")
	}
	got, _ := c.Get("m/s1")
	if got.Capabilities == nil {
		t.Fatalf("the phone dropped a VALID capability record; every session routes to the status card")
	}
	if *got.Capabilities != *rec {
		t.Fatalf("the phone altered the record: %+v vs %+v", *got.Capabilities, *rec)
	}
}
