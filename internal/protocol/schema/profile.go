package schema

// RemoteProfileV1 is the machine-authored remote semantic profile ADR-017 T5 / playbook
// §6.3 seals to the phone during reconciliation. The asynchronous E2EE mailbox has no
// local `hello`, so this is the only channel a phone learns which action/body versions,
// interaction-schema version, TerminalView version and capability-record version the
// machine currently accepts. It rides the EXISTING ReconcileRecord (PB-SYNC-7) as a named
// field -- no new mailbox frame kind, no envelope change (IS-LAYER-1).
//
// The profile VERSION is the compatibility unit, not this struct: companion decision
// records may add fields to it (ADR-017 T5), each carrying its own GG-7 obligation.
//
// No field may carry omitempty, for the same reason ReconcileRecord's authorities may
// not: every field here is a live authority a phone routes on, and an absent key must
// stay distinguishable from a legitimately-zero one.
//
// CurrentProfileVersion is the R1 profile version this ADR's two fields
// (InteractionSchemaVersion, TerminalViewVersion) and the rest of the companion R1 set
// share, per T5's "taken once across the R1 set rather than independently per ADR".
// ADR-016 is a co-owner: it adds relay_tls_policy, relay_host and the pin set to this
// same struct (ADR-016:194) and joined this SAME version number rather than bumping it;
// its three fields ARE declared below, and their GG-7 field-table obligation is ADR-016's
// own, not discharged by this constant. ADR-017 T5 is the second co-owner: it adds the
// three terminal_view_* bounds on the same terms.
//
// WAVE R8 IS THE FIRST WAVE TO PUBLISH A NON-ZERO PROFILE VERSION (amendment T5-a), and
// that ENDS the "no deployed reader to break" argument that let R8's own additions join
// version 1 rather than bump it. The next ADR that adds a field here inherits a real
// compatibility decision, not R8's free window.
const CurrentProfileVersion = 1

type RemoteProfileV1 struct {
	Version                  int            `json:"version"`
	AcceptedActions          []string       `json:"accepted_actions"`
	AcceptedBodyVersions     map[string]int `json:"accepted_body_versions"`
	InteractionSchemaVersion int            `json:"interaction_schema_version"`
	TerminalViewVersion      int            `json:"terminal_view_version"`
	CapabilityRecordVersion  int            `json:"capability_record_version"`
	// The three ADR-016 W1 fields: the machine's authoritative relay TLS policy, joining
	// this same R1 profile version rather than bumping it (ADR-016:194). RelayTLSPolicy and
	// RelaySPKIPin are INDEPENDENT -- a pin's presence never implies pinned_spki and a
	// pin's absence never implies webpki -- and none of the three carries omitempty: this
	// struct rides EVERY reconcile, and an absent key must stay distinguishable from a
	// legitimately-zero one (W9 step 6's compatibility-pin withdrawal depends on exactly
	// that distinction being resolvable).
	RelayTLSPolicy string `json:"relay_tls_policy"`
	RelayHost      string `json:"relay_host"`
	RelaySPKIPin   []byte `json:"relay_spki_pin"`
	// The three ADR-017 T5 TerminalView bounds: "size, line and rate bounds declared in
	// the remote profile, so a phone knows the ceiling it is rendering under rather than
	// discovering it". Zero on any of them means CLAMP TO THE PHONE'S CONSERVATIVE
	// BUILT-IN, never "unlimited" (T5-a), and none carries omitempty for the same reason
	// nothing else here does. TerminalViewBounds() is the one resolver.
	TerminalViewMaxLineBytes int `json:"terminal_view_max_line_bytes"`
	TerminalViewMaxRows      int `json:"terminal_view_max_rows"`
	TerminalViewMaxRateHz    int `json:"terminal_view_max_rate_hz"`
}
