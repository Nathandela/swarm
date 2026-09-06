package main

// Obsolete sidecars must not reintroduce a second authority source, including through
// their former machine-revoke field.

import "testing"

func TestResolveGatewayParams_ObsoleteRevokeCapabilityIsIgnored(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	addPairedDevice(t, stateDir)
	writePushGatewayFile(t, stateDir, []byte(`{"machine_revoke_capability":"obsolete-secret"}`))
	params, err := resolveGatewayParams(stateDir, "/tmp/remote.sock")
	if err != nil || params.PushGateway != nil {
		t.Fatalf("obsolete revoke capability affected foreground-only startup: params=%+v err=%v", params.PushGateway, err)
	}
}
