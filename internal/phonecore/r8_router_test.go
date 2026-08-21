package phonecore

// WAVE R8 / SLICES S3 + S7 + S8 -- THREE DESTINATIONS, THE SEVERANCE LIST, AND NO QUEUE.
// Failing-first (TDD RED, GG-5).
//
// THE ROUTER. ADR-017 T1 gives a session exactly one phone surface, chosen by the daemon:
// chat, capability-routed sanitized terminal fallback, or the honest status card -- "three
// destinations, nothing in between". T2 rule 3 makes the choice a read of the record and
// never an inference ("it never infers support from whether a transcript happens to be
// empty"), and T2-a/T2-b/T5-a make absence, inconsistency and a zero-valued profile all
// resolve to the SAME destination: the status card, with both verbs refused. That is one
// predicate, evaluated in one place, so there is one thing to get right rather than one per
// screen.
//
// THE SEVERANCE LIST. T8 is a list, not a timeout, and amendment T8-b makes backgrounding a
// trigger IN ITS OWN RIGHT. Today `lease.go:222-227` records the opposite in as many words:
// "BACKGROUNDING IS NOT ITSELF A TRIGGER ... A backgrounded app still loses its lease,
// because backgrounding DISCONNECTS the phone (ADR-007 B16) and the transport loss is what
// severs." That is an answer BY CONSEQUENCE, and it rests on a connectivity choice a later
// wave could revisit -- at which point a generation would quietly outlive the screen that
// owns it, on the one surface where "only the active foreground screen may send input" is a
// routing rule (T6).
//
// THE NO-QUEUE RULE, AT THE BUFFER THAT ACTUALLY EXISTS. `InputCoalescer` holds bytes for a
// pacing window; `Abandon` drops them and records each on the undelivered ledger; `Flush`
// releases them. T6-f: on ANY severance trigger the held bytes are DROPPED, never flushed --
// because the natural implementation of "release control" flushes, and a flush converts
// live-only input into a short offline queue at the one place a queue can actually form.
//
// THE SEAMS (undefined symbols -> compile-fail RED):
//
//	type SessionDestination int
//	const DestinationStatusCard, DestinationChat, DestinationTerminalFallback SessionDestination
//	func RouteSession(rec *schema.SessionCapabilities, profile schema.RemoteProfileV1) SessionDestination
//	type TerminalControlState struct{ ... }
//	func NewTerminalControlState(c *InputCoalescer) *TerminalControlState
//	func (*TerminalControlState) Begin(session, instance, generation string, expires time.Time)
//	func (*TerminalControlState) Live(session string, now time.Time) bool
//	func (*TerminalControlState) Sever(session, reason string)
//	func (*TerminalControlState) Background(reason string)

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// r8Profile is a profile that declares both versions, so the router's answer is about the
// RECORD rather than about the profile. The zero-profile rows below are the other half.
func r8Profile() schema.RemoteProfileV1 {
	return schema.RemoteProfileV1{
		Version:                 schema.CurrentProfileVersion,
		TerminalViewVersion:     1,
		CapabilityRecordVersion: 1,
	}
}

// TestR8Route_ThreeDestinationsAndNothingInBetween is the routing table, written so that
// every fail-closed row names the state it protects the user from.
func TestR8Route_ThreeDestinationsAndNothingInBetween(t *testing.T) {
	cases := []struct {
		name    string
		rec     *schema.SessionCapabilities
		profile schema.RemoteProfileV1
		want    SessionDestination
		why     string
	}{
		{
			name:    "healthy_structured_is_chat",
			rec:     &schema.SessionCapabilities{Provider: "claude", SessionInstance: "i", StructuredChat: true},
			profile: r8Profile(),
			want:    DestinationChat,
			why:     "T1: a structured_chat session keeps ADR-009 exactly, and never sees a terminal",
		},
		{
			name:    "fallback_provider_is_the_terminal",
			rec:     &schema.SessionCapabilities{Provider: "opencode", SessionInstance: "i", TerminalFallback: true},
			profile: r8Profile(),
			want:    DestinationTerminalFallback,
			why:     "playbook:649 / RC-D4: OpenCode and AGY are launchable and must be monitorable",
		},
		{
			name:    "neither_is_the_status_card",
			rec:     &schema.SessionCapabilities{Provider: "somecli", SessionInstance: "i"},
			profile: r8Profile(),
			want:    DestinationStatusCard,
			why:     "T1's third destination survives: a provider with no adapter and no PTY worth showing",
		},
		{
			name:    "absent_record_is_the_status_card",
			rec:     nil,
			profile: r8Profile(),
			want:    DestinationStatusCard,
			why: "T2-a: absence is the state of EVERY live session today (skeleton/capability.go:334-344 " +
				"records that no production path authors a record at all), so 'unknown therefore chat' " +
				"shows a composer whose every send is refused and 'unknown therefore terminal' opens the " +
				"peek on all of them",
		},
		{
			name: "inconsistent_record_is_the_status_card",
			rec: &schema.SessionCapabilities{
				Provider: "claude", SessionInstance: "i", StructuredChat: true, TerminalFallback: true,
			},
			profile: r8Profile(),
			want:    DestinationStatusCard,
			why: "T2-b: the router is written over BOTH booleans, so a malformed, stale or attacker-" +
				"supplied record cannot pick a destination the daemon did not author",
		},
		{
			name:    "zero_profile_has_no_fallback",
			rec:     &schema.SessionCapabilities{Provider: "opencode", SessionInstance: "i", TerminalFallback: true},
			profile: schema.RemoteProfileV1{},
			want:    DestinationStatusCard,
			why: "T5-a: terminal_view_version==0 means NO FALLBACK EXISTS on this machine -- which is " +
				"literally what every deployed machine sends today (cmd/swarm-remote/config.go:141-147) -- " +
				"so rendering a fallback would be rendering a view the machine never produces",
		},
		{
			name:    "untrusted_record_version_is_the_status_card",
			rec:     &schema.SessionCapabilities{Provider: "opencode", SessionInstance: "i", TerminalFallback: true},
			profile: schema.RemoteProfileV1{Version: 1, TerminalViewVersion: 1, CapabilityRecordVersion: 0},
			want:    DestinationStatusCard,
			why:     "T5-a: capability_record_version==0 means the record is untrusted, which composes with T2-a into one predicate",
		},
		{
			name:    "record_with_no_instance_is_the_status_card",
			rec:     &schema.SessionCapabilities{Provider: "opencode", TerminalFallback: true},
			profile: r8Profile(),
			want:    DestinationStatusCard,
			why:     "T8-a: a record that binds no instance cannot bind a watch or a generation to an incarnation",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RouteSession(tc.rec, tc.profile); got != tc.want {
				t.Errorf("RouteSession = %v, want %v.\nADR-017 %s", got, tc.want, tc.why)
			}
		})
	}
}

// TestR8Route_ComposerAvailabilityComesFromTheRecordNotFromTheTranscript is T2 rule 3 at the
// place the phone currently decides it.
//
// `SessionDetailPanel.kt:772` derives composer availability from `transcript.structureTorn`
// -- a fact about the TRANSCRIPT, which is exactly the inference T2 rule 3 forbids ("it never
// infers support from whether a transcript happens to be empty"). A torn transcript and a
// session that never had structured chat are different states with different explanations,
// and only the record knows which one the user is looking at.
func TestR8Route_ComposerAvailabilityComesFromTheRecordNotFromTheTranscript(t *testing.T) {
	structured := &schema.SessionCapabilities{Provider: "claude", SessionInstance: "i", StructuredChat: true}
	if !ComposerAvailable(structured, r8Profile()) {
		t.Errorf("a healthy structured session must offer the composer")
	}
	fallback := &schema.SessionCapabilities{Provider: "opencode", SessionInstance: "i", TerminalFallback: true}
	if ComposerAvailable(fallback, r8Profile()) {
		t.Errorf("ADR-017 T2 / mirror-program M5.5: a fallback session has no structured composer to hide, " +
			"because it has no MessageSink. Offering one there is a send that can only be refused.")
	}
	if ComposerAvailable(nil, r8Profile()) {
		t.Errorf("ADR-017 T2-a: an absent record offered the composer")
	}
}

// TestR8Control_BackgroundingSeversDirectly is amendment T8-b, and it is the one that
// requires an existing test to change.
//
// `s11_lease_test.go:218-224` pins the by-consequence answer today ("app backgrounding (via
// the disconnect it forces)"). That row is AMENDED IN THE SAME CHANGE AS A STRENGTHENING --
// backgrounding severs directly AND still severs through the disconnect -- which is the only
// shape such an edit is allowed to take: the assertion set grows, and no existing assertion
// is deleted.
func TestR8Control_BackgroundingSeversDirectly(t *testing.T) {
	c := NewInputCoalescer(func() time.Time { return time.Unix(1755600000, 0) })
	st := NewTerminalControlState(c)
	now := time.Unix(1755600000, 0)
	st.Begin("sess1", "inst-a", "gen-1", now.Add(15*time.Minute))
	if !st.Live("sess1", now) {
		t.Fatalf("a freshly begun generation must be live")
	}

	// NO transport event of any kind: the app merely went to the background.
	st.Background("the app went to the background")
	if st.Live("sess1", now) {
		t.Errorf("ADR-017 T8-b / ADR-009 (6): backgrounding did not sever the control generation on its " +
			"own. lease.go:222-227 answers this BY CONSEQUENCE -- 'backgrounding DISCONNECTS the phone ... " +
			"and the transport loss is what severs' -- which makes the guarantee rest on a connectivity " +
			"choice rather than on the rule. T6 makes 'only the active foreground screen may send input' " +
			"a routing rule; a generation that outlives the foreground screen is a generation with no " +
			"screen displaying that it is live.")
	}
}

// TestR8Control_SeveranceDropsHeldBytesAndNeverFlushesThem is amendment T6-f, at the buffer
// that actually exists.
func TestR8Control_SeveranceDropsHeldBytesAndNeverFlushesThem(t *testing.T) {
	triggers := []struct {
		name string
		fire func(*TerminalControlState)
	}{
		{"leaving the fallback screen", func(s *TerminalControlState) { s.Sever("sess1", "left the screen") }},
		{"app backgrounding", func(s *TerminalControlState) { s.Background("backgrounded") }},
		{"transport loss", func(s *TerminalControlState) { s.SeverAll("the connection to the machine was lost") }},
		{"horizon expiry", func(s *TerminalControlState) { s.Sever("sess1", "the control horizon expired") }},
		{"kill switch", func(s *TerminalControlState) { s.SeverAll("remote control is disabled") }},
		{"device revocation", func(s *TerminalControlState) { s.SeverAll("this device was revoked") }},
		{"session replacement", func(s *TerminalControlState) { s.Sever("sess1", "the session was replaced") }},
	}
	for _, tr := range triggers {
		t.Run(tr.name, func(t *testing.T) {
			c := NewInputCoalescer(func() time.Time { return time.Unix(1755600000, 0) })
			st := NewTerminalControlState(c)
			now := time.Unix(1755600000, 0)
			st.Begin("sess1", "inst-a", "gen-1", now.Add(15*time.Minute))

			// Bytes accepted from the user and held for the pacing window. TWO bursts,
			// because the FIRST is the burst's leading edge and is emitted immediately --
			// the held bytes this rule is about are the ones paced behind that edge, which
			// is the state a severance actually catches a user in.
			c.Type("sess1", []byte("rm -rf "))
			c.Type("sess1", []byte("/"))
			if c.Buffered("sess1") == 0 {
				t.Fatalf("fixture: nothing was held, so this trigger proves nothing")
			}

			before := len(c.Undelivered())
			tr.fire(st)

			if n := c.Buffered("sess1"); n != 0 {
				t.Errorf("ADR-017 T6-f: %d byte(s) were still held after %q. Held bytes are DROPPED on "+
					"every severance trigger; releasing them turns live-only input into a short offline "+
					"queue at the one place a queue can form, and the natural implementation of 'release "+
					"control' is exactly the flush that does it.", n, tr.name)
			}
			if after := len(c.Undelivered()); after <= before {
				t.Errorf("ADR-017 T6-f / PB-INPUT-1: the dropped bytes were not recorded as undelivered "+
					"after %q (%d -> %d). A silent drop and a queued replay are the two failures this rule "+
					"sits between: the user must be told the bytes did not land, and must never have them "+
					"land later.", tr.name, before, after)
			}
			if st.Live("sess1", now) {
				t.Errorf("the generation is still live after %q", tr.name)
			}
		})
	}
}

// TestR8Control_TheHorizonIsTheOneAlreadyImplemented is T7's "the number already implemented"
// clause: adopting `TakeControlTTL` costs no migration and leaves ONE 15-minute wall in the
// system rather than two nearly-equal ones. It also pins the withdrawn-requirement repair
// T7 obliges: lease.go's comment must stop citing the biometric freshness that B133 withdrew
// and start citing this ADR.
func TestR8Control_TheHorizonIsTheOneAlreadyImplemented(t *testing.T) {
	if TerminalControlTTL != TakeControlTTL {
		t.Errorf("ADR-017 T7: the terminal-control horizon is %v and TakeControlTTL is %v. T7 adopts the "+
			"number already implemented precisely so the system has one 15-minute wall rather than two "+
			"nearly-equal ones that will drift apart.", TerminalControlTTL, TakeControlTTL)
	}
	if TerminalControlTTL*2 != MaxControlSessionTTL {
		t.Errorf("ADR-017 T7: the horizon must stay half MaxControlSessionTTL (%v), so a control session "+
			"can never outlive the control-session cap on the strength of its horizon alone; got %v",
			MaxControlSessionTTL, TerminalControlTTL)
	}
}
