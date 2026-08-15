package relaycfg_test

// ADR-016 W1 (FAILING FIRST, RED phase, bd agents-tracker-hggx.3.5): "the policy and the
// pin are independent... A pin's presence never implies pinned_spki and a pin's absence
// never implies webpki."
//
// relaycfg.Config is the FIRST of the four durable/wire artifacts W1 names
// (relay.json, pairing.MachinePayload, phonecore.State, RemoteProfileV1). This file pins
// the config-file half: a new TLSPolicy field that round-trips on its own JSON key,
// completely independently of SPKIPin, plus the two named policy values ("webpki",
// "pinned_spki") as exported constants other packages (cmd/swarm's CLI, the profile
// publisher) key their own logic on rather than re-typing the literal string.
//
// relaycfg.Config.Security() IS also scoped by this field -- see
// TestADR016W3_SecurityConsultsThePinOnlyUnderPinnedSPKI below and Security()'s own doc
// comment. W3's "consulted iff pinned_spki... not by anything" is not a phone-only rule: it
// governs every place Security.PinnedSPKISHA256 gets populated from a (policy, pin) pair,
// which includes the machine's own dial. W2's "Desktop is unchanged" is the narrower, true
// claim it is often mistaken for: it is about the TRUST-ROOT SOURCE (TrustRootsSystem, never
// the platform delegate), not about whether a compatibility pin gets consulted.

import (
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/relaycfg"
)

// TestADR016W1_ConfigCarriesTLSPolicyIndependentlyOfThePin is the Conformance table's
// "Policy / pin independence" row, at the config-file layer: a Config with BOTH a policy
// and a pin set round-trips through JSON with both values intact, and setting one leaves
// the other exactly as it was -- neither field may be derived from the other.
func TestADR016W1_ConfigCarriesTLSPolicyIndependentlyOfThePin(t *testing.T) {
	cfg := relaycfg.Config{
		RelayURL:  "wss://relay.example.com:8443",
		TLSPolicy: relaycfg.PolicyPinnedSPKI,
		SPKIPin:   "cGluLWJ5dGVzLXBpbi1ieXRlcy1waW4tYnl0ZXMtcDE=",
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got relaycfg.Config
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.TLSPolicy != relaycfg.PolicyPinnedSPKI {
		t.Errorf("TLSPolicy round-tripped as %q, want %q", got.TLSPolicy, relaycfg.PolicyPinnedSPKI)
	}
	if got.SPKIPin != cfg.SPKIPin {
		t.Errorf("SPKIPin round-tripped as %q, want %q", got.SPKIPin, cfg.SPKIPin)
	}

	// A policy with NO pin: the pin's absence must not be readable as "policy is webpki" --
	// it must survive as literally what was written.
	webpkiOnly := relaycfg.Config{RelayURL: "wss://relay.example.com:8443", TLSPolicy: relaycfg.PolicyWebPKI}
	b2, err := json.Marshal(webpkiOnly)
	if err != nil {
		t.Fatalf("marshal webpki-only: %v", err)
	}
	var got2 relaycfg.Config
	if err := json.Unmarshal(b2, &got2); err != nil {
		t.Fatalf("unmarshal webpki-only: %v", err)
	}
	if got2.TLSPolicy != relaycfg.PolicyWebPKI {
		t.Errorf("TLSPolicy = %q, want %q", got2.TLSPolicy, relaycfg.PolicyWebPKI)
	}
	if got2.SPKIPin != "" {
		t.Errorf("a webpki-only config decoded a non-empty pin %q from nothing", got2.SPKIPin)
	}

	// And the mutation control the Conformance table names explicitly: an implementation
	// that DERIVES the policy from pin presence must fail this case, not pass it. A pin
	// with no policy field at all (the legacy shape every relay.json on disk today has)
	// must decode with TLSPolicy == "" -- never silently promoted to "pinned_spki" by the
	// decoder itself. (cmd/swarm's CLI is where the legacy INFERENCE belongs -- see
	// cmd/swarm/r2_adr016_w1_test.go -- config decoding must stay a literal reader.)
	legacy := `{"relay_url":"wss://relay.example.com:8443","relay_spki_pin":"cGluLWJ5dGVzLXBpbi1ieXRlcy1waW4tYnl0ZXMtcDE="}`
	var got3 relaycfg.Config
	if err := json.Unmarshal([]byte(legacy), &got3); err != nil {
		t.Fatalf("unmarshal legacy relay.json: %v", err)
	}
	if got3.TLSPolicy != "" {
		t.Errorf("decoding a legacy relay.json (no relay_tls_policy key) produced TLSPolicy = %q; "+
			"a decoder that infers pinned_spki from the pin's presence is the exact defect W1's "+
			"Conformance mutation control names", got3.TLSPolicy)
	}
	if got3.SPKIPin == "" {
		t.Fatalf("test bug: legacy fixture decoded no pin")
	}
}

// TestADR016W1_TLSPolicyJSONKeyIsRelayTLSPolicy pins the exact wire key name every other
// artifact (MachinePayload, phonecore.State, RemoteProfileV1) must share, per W1: "its own
// value in every durable and wire artifact that carries the pin".
func TestADR016W1_TLSPolicyJSONKeyIsRelayTLSPolicy(t *testing.T) {
	b, err := json.Marshal(relaycfg.Config{RelayURL: "wss://relay.example.com", TLSPolicy: relaycfg.PolicyWebPKI})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if got, ok := m["relay_tls_policy"]; !ok || got != "webpki" {
		t.Errorf(`relay.json wire shape carries %v under "relay_tls_policy"; want "webpki"`, m)
	}
}

// TestADR016W1_PolicyConstantsAreTheTwoADRNames pins the exact two spellings W1's table
// names ("webpki", "pinned_spki") -- the values `swarm remote init --relay-tls-policy`
// accepts and the phone's migration ladder (W4/W9) switches on. A third spelling, or a
// drifted one, would make a machine's CLI invocation and a phone's parser agree on nothing.
func TestADR016W1_PolicyConstantsAreTheTwoADRNames(t *testing.T) {
	if relaycfg.PolicyWebPKI != "webpki" {
		t.Errorf("relaycfg.PolicyWebPKI = %q, want %q", relaycfg.PolicyWebPKI, "webpki")
	}
	if relaycfg.PolicyPinnedSPKI != "pinned_spki" {
		t.Errorf("relaycfg.PolicyPinnedSPKI = %q, want %q", relaycfg.PolicyPinnedSPKI, "pinned_spki")
	}
}

// TestADR016W3_SecurityConsultsThePinOnlyUnderPinnedSPKI is W3's single rule applied to the
// MACHINE's own dial: a webpki machine that also publishes a W9 compatibility pin (over
// --relay-pin-compat, for un-migrated phones) must not pin ITS OWN connection to that byte --
// a rekeyed ACME renewal would otherwise take the machine offline during the exact
// compatibility window this ADR opens to keep things working, and W3's "not by anything"
// would be false for the one caller that was never named as an exception.
func TestADR016W3_SecurityConsultsThePinOnlyUnderPinnedSPKI(t *testing.T) {
	pin := "cGluLWJ5dGVzLXBpbi1ieXRlcy1waW4tYnl0ZXMtcDE="

	for _, tc := range []struct {
		name       string
		policy     string
		wantPinned bool
	}{
		{"pinned_spki: the machine's own dial is pinned", relaycfg.PolicyPinnedSPKI, true},
		{"legacy (no policy field): today's behaviour, the machine's own dial is pinned", "", true},
		{"webpki with a compatibility pin: the machine's own dial is NOT pinned", relaycfg.PolicyWebPKI, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := relaycfg.Config{
				RelayURL:  "wss://relay.example.com:8443",
				TLSPolicy: tc.policy,
				SPKIPin:   pin,
			}
			sec, err := cfg.Security()
			if err != nil {
				t.Fatalf("Security(): %v", err)
			}
			gotPinned := len(sec.PinnedSPKISHA256) > 0
			if gotPinned != tc.wantPinned {
				t.Errorf("policy %q: PinnedSPKISHA256 set = %v, want %v", tc.policy, gotPinned, tc.wantPinned)
			}
		})
	}
}
