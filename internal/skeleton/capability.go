package skeleton

// THE CAPABILITY PRODUCER: derives a session's daemon-authored per-session capability
// record (ADR-017 T2, playbook §6.2) at launch, from the adapter seam.
//
// WHY IT LIVES HERE: internal/skeleton is the only package that imports both the adapter
// contract/registry and the wire schema (protocol) -- exactly why interaction.go's
// producer lives here too. It mirrors api.go's "adapters into launch" seam.
//
// structured_chat is derived from adapter.AsInteractionSource, discovered exactly as
// ADR-010 §5 already discovers it for interaction capture: "ABSENCE IS A SIGNAL", never a
// hardcoded per-provider list. Per ADR-017 T2 rule 4 ("no route to the fallback from a
// healthy structured session"), a structured session gets terminal_fallback=false; every
// other adapter gets the generic fallback pair.
//
// ADR-017 T3 IS NOT FULLY ENFORCED YET. AsInteractionSource is NECESSARY for
// structured_chat=true but not, by itself, SUFFICIENT: T3 requires every mandatory row to
// pass against the recorded provider_version, including the Version-skew row ("unknown
// provider versions fail to the terminal fallback ... not optimistic structured mode"),
// and playbook §6.2 names four seams beyond InteractionSource -- MessageSink, ApprovalSink,
// LifecycleSink, TerminalFallback -- none of which have an adapter interface yet. This
// derivation checks only the one seam that exists today; the version-skew gate and the
// remaining seam checks are deferred, tracked in bd agents-tracker-hggx.2.1, and land as
// their own RED+GREEN slice since they change what the existing capability-record tests
// pin.
//
// interrupt is left at its zero value (false) for every adapter: nothing in
// internal/adapter exposes a LifecycleSink-style interrupt seam to consult, so there is
// nothing to derive it from. T6's own closing paragraph gives the honest default for an
// unset record: "This provider version has no safe remote interrupt." Guessing interrupt
// from structured_chat would assert a capability no seam proves, which is exactly what T2
// rule 3 ("the phone renders from the record and infers nothing") forbids one layer up.
// Tracked in bd agents-tracker-hggx.2.1 alongside the LifecycleSink seam itself.

import (
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
)

// deriveSessionCapabilities builds the capability record for a newly launched session
// instance. providerVersion is the detected CLI version and adapterRevision is the Swarm
// adapter revision that produced the record; both are carried through verbatim.
func deriveSessionCapabilities(provider string, a adapter.Adapter, providerVersion, adapterRevision string) protocol.SessionCapabilities {
	_, structured := adapter.AsInteractionSource(a)
	return protocol.SessionCapabilities{
		Provider:         provider,
		ProviderVersion:  providerVersion,
		AdapterRevision:  adapterRevision,
		StructuredChat:   structured,
		TerminalFallback: !structured,
		// Interrupt stays false (zero value): no LifecycleSink seam exists to derive it
		// from. See the package doc above and bd agents-tracker-hggx.2.1.
	}
}
