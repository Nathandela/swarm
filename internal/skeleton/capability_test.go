package skeleton

// FAILING-FIRST (TDD RED, GG-5) tests for ADR-017 T2 / playbook 6.2's per-session
// capability record, authored DAEMON-SIDE at session launch from the adapter seam:
// structured_chat=true ONLY when the adapter implements adapter.InteractionSource
// (adapter.AsInteractionSource), discovered exactly as ADR-010 §5 already discovers it
// for interaction capture -- "ABSENCE IS A SIGNAL", never a defect, and never a
// hardcoded per-provider list. Claude is the only adapter in this repo that implements
// it today (internal/adapter/claude/interaction.go); codex, hermes, opencode, and agy do
// not, so they get structured_chat=false, terminal_fallback=true.
//
// WHY THIS LIVES IN internal/skeleton: it is the only package that imports the adapter
// contract, the adapter registry AND the wire schema (protocol) -- exactly the reason
// interaction.go's producer lives here (see that file's package doc), and precisely
// mirrors composeLaunchSpec's "adapters into launch" seam in api.go.
//
// THE SEAM this test pins (undefined symbol -> compile-fail RED):
//
//	func deriveSessionCapabilities(provider string, a adapter.Adapter, providerVersion, adapterRevision string) protocol.SessionCapabilities

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/agy"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/adapter/hermes"
	"github.com/Nathandela/swarm/internal/adapter/opencode"
)

// TestDeriveSessionCapabilities_PerAdapter is the table-driven pin over the five
// production adapters (internal/adapter/registry.production): the ONE adapter that
// implements InteractionSource gets structured_chat=true and -- per ADR-017 T2 rule 4,
// "no route to the fallback from a healthy structured session" -- terminal_fallback=
// false; the other four get the generic-fallback pair.
func TestDeriveSessionCapabilities_PerAdapter(t *testing.T) {
	cases := []struct {
		provider             string
		ad                   adapter.Adapter
		wantStructuredChat   bool
		wantTerminalFallback bool
	}{
		{"claude", claude.New(), true, false},
		{"codex", codex.New(), false, true},
		{"hermes", hermes.New(), false, true},
		{"opencode", opencode.New(), false, true},
		{"agy", agy.New(), false, true},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			got := deriveSessionCapabilities(tc.provider, tc.ad, "9.9.9", "rev-test", false)
			if got.Provider != tc.provider {
				t.Errorf("Provider = %q; want %q", got.Provider, tc.provider)
			}
			if got.ProviderVersion != "9.9.9" {
				t.Errorf("ProviderVersion = %q; want the DETECTED version passed through verbatim (%q)", got.ProviderVersion, "9.9.9")
			}
			if got.AdapterRevision != "rev-test" {
				t.Errorf("AdapterRevision = %q; want %q", got.AdapterRevision, "rev-test")
			}
			if got.StructuredChat != tc.wantStructuredChat {
				t.Errorf("StructuredChat = %v; want %v", got.StructuredChat, tc.wantStructuredChat)
			}
			if got.TerminalFallback != tc.wantTerminalFallback {
				t.Errorf("TerminalFallback = %v; want %v", got.TerminalFallback, tc.wantTerminalFallback)
			}
		})
	}
}

// TestDeriveSessionCapabilities_TracksAsInteractionSourceExactly is the mechanical pin
// behind the table above: derivation must key off adapter.AsInteractionSource itself,
// never a hardcoded provider-name switch, so a future adapter that gains
// InteractionSource (e.g. Codex, per RC-D4) flips to structured_chat=true with NO change
// to this function -- only to the adapter.
//
// This pins AsInteractionSource as NECESSARY for structured_chat=true; it does not pin
// sufficiency. ADR-017 T3's mandatory-row gate (version skew, MessageSink/ApprovalSink/
// LifecycleSink) is deferred to bd agents-tracker-hggx.2.1 -- adding it will need a new
// RED pass, since a real version gate would have to reject the fake provider versions
// ("9.9.9", "v") this table currently asserts true against.
// WAVE R7 NARROWED THE CLAIM, and only for a provider whose structured plane lives in a SIDE
// PROCESS (ADR-013 §R7.7). The seam is still NECESSARY -- that is what this test pins, and it is
// unchanged -- but for an adapter that also proves a BackendSource it is no longer SUFFICIENT:
// the session must have a live backend too. Without that, the day the Codex adapter gained
// Interactions, every PRE-UPGRADE Codex session (argv `codex`, no --remote, no backend child)
// would have claimed structured_chat=true and the phone would have shown a composer whose every
// send is refused. `liveBackend` is passed true here so the seam remains the only variable, and
// the per-session-instance behavior is fenced by
// TestR7Capabilities_StructuredChatIsSeamANDLiveBackendPerSessionInstance.
func TestDeriveSessionCapabilities_TracksAsInteractionSourceExactly(t *testing.T) {
	for _, ad := range []adapter.Adapter{claude.New(), codex.New(), hermes.New(), opencode.New(), agy.New()} {
		_, wantSource := adapter.AsInteractionSource(ad)
		got := deriveSessionCapabilities("x", ad, "v", "r", true)
		if got.StructuredChat != wantSource {
			t.Errorf("%s: StructuredChat = %v but AsInteractionSource ok=%v; derivation must track the seam, not a hardcoded provider name", ad.Name(), got.StructuredChat, wantSource)
		}
	}
}
