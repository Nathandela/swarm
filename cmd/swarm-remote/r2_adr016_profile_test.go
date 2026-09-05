package main

// ADR-016 "profile" (FAILING FIRST, RED phase, bd agents-tracker-hggx.3.5): the FIRST
// real (non-test) publisher of any RemoteProfileV1 field. schema/profile.go's own comment
// records that "no production caller sets Version from this constant yet"; this file makes
// resolveGatewayParams -- the pure config assembler every real `swarm-remote` process runs
// through (main.go's serviceConfigFromParams) -- the first one that does, for the three
// ADR-016 fields specifically, built from the SAME relaycfg.Config the machine's dial
// policy already reads.
//
// gatewayParams gains a Profile protocol.RemoteProfileV1 field; resolveGatewayParams
// builds it from relayCfg (TLSPolicy -> RelayTLSPolicy, the RelayURL's hostname ->
// RelayHost, the decoded pin -> RelaySPKIPin); serviceConfigFromParams (main.go) carries
// gatewayParams.Profile into remotegw.ServiceConfig.Profile, closing the wiring gap
// internal/remotegw/r2_adr016_profile_wiring_test.go pins one layer down.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/remote/relaycfg"
)

// TestADR016Profile_ResolveGatewayParamsPublishesTheRelayTLSProfile pins the end-to-end
// claim: a machine provisioned with `swarm remote init --relay-tls-policy webpki
// --relay-pin-compat <spki>` (relaycfg.Config{TLSPolicy, SPKIPin}) produces a gatewayParams
// whose Profile carries the SAME policy, the relay URL's hostname, and the decoded pin --
// the first REAL machine-side value RemoteProfileV1 has ever carried.
func TestADR016Profile_ResolveGatewayParamsPublishesTheRelayTLSProfile(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	pin := "cGluLWJ5dGVzLXBpbi1ieXRlcy1waW4tYnl0ZXMtcDE="
	if err := relaycfg.Save(stateDir, relaycfg.Config{
		RelayURL:          "wss://swarm-relay.example.com:8443",
		OperatorNamespace: "owner",
		TLSPolicy:         relaycfg.PolicyWebPKI,
		SPKIPin:           pin,
	}); err != nil {
		t.Fatalf("relaycfg.Save: %v", err)
	}
	addPairedDevice(t, stateDir)

	got, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock")
	if err != nil {
		t.Fatalf("resolveGatewayParams: %v", err)
	}

	if got.Profile.RelayTLSPolicy != "webpki" {
		t.Errorf("Profile.RelayTLSPolicy = %q, want %q", got.Profile.RelayTLSPolicy, "webpki")
	}
	if got.Profile.RelayHost != "swarm-relay.example.com" {
		t.Errorf("Profile.RelayHost = %q, want %q", got.Profile.RelayHost, "swarm-relay.example.com")
	}
	if len(got.Profile.RelaySPKIPin) == 0 {
		t.Errorf("Profile.RelaySPKIPin is empty; the configured compatibility pin must reach the profile")
	}
}

// TestADR016Profile_ResolveGatewayParamsWithNoRelayPublishesNoProfile: an unprovisioned
// relay (a state this resolver already fails closed on for everything ELSE it needs)
// must not manufacture a policy from nothing -- Profile stays the zero value on that
// error path, which is already the resolver's existing contract for every other field.
func TestADR016Profile_ResolveGatewayParamsWithNoRelayPublishesNoProfile(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	addPairedDevice(t, stateDir)

	if _, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock"); err == nil {
		t.Fatalf("resolveGatewayParams succeeded with no relay.json; want the existing " +
			"fail-closed refusal (unchanged by this ADR)")
	}
}
