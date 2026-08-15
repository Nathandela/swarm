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
// ADR-016 is the currently known co-owner: it adds relay_tls_policy, relay_host and the
// pin set to this same struct (ADR-016:194) and joins this SAME version number when it
// lands, rather than bumping it -- ADR-016's three fields are not yet declared here, and
// their GG-7 field-table obligation is ADR-016's own, not discharged by this constant.
// Any later ADR that adds a RemoteProfileV1 field joins this version on the same terms.
// No production caller sets Version from this constant yet (bd agents-tracker-hggx.2.2).
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
}
