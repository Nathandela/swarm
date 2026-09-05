package swarmmobile

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
)

type probeConnFunc func()

func (f probeConnFunc) Close() { f() }

func TestProbeWebPKIUsesNativeAuthenticatedPhoneProbe(t *testing.T) {
	a := w9App(t, "wss://relay.example.com")
	machinePub := bytes.Repeat([]byte{0x42}, ed25519.PublicKeySize)
	if err := a.core.Mutate(func(st *phonecore.State) {
		st.MachineRelayAuthPub = machinePub
		st.OperatorNamespace = "owner"
	}); err != nil {
		t.Fatal(err)
	}
	a.setDestination(machinePub)

	original := relayV2DialProbe
	t.Cleanup(func() { relayV2DialProbe = original })
	var gotProfile relayv2.Profile
	var gotAuth relayv2.Auth
	closed := false
	relayV2DialProbe = func(_ context.Context, profile relayv2.Profile, auth relayv2.Auth) (relayV2ProbeConnection, error) {
		gotProfile, gotAuth = profile, auth
		return probeConnFunc(func() { closed = true }), nil
	}

	if err := a.probeWebPKI(context.Background(), "relay.example.com"); err != nil {
		t.Fatalf("probeWebPKI: %v", err)
	}
	if gotProfile.RelayURL != a.relayURL || gotProfile.MachineRID != relayv2.RoutingID(machinePub) || gotProfile.OperatorNamespace != "owner" {
		t.Fatalf("probe profile = %+v", gotProfile)
	}
	if len(gotProfile.Security.PinnedCert) != 0 || len(gotProfile.Security.PinnedSPKISHA256) != 0 {
		t.Fatalf("WebPKI probe unexpectedly carried a relay pin: %+v", gotProfile.Security)
	}
	wantPhonePub := a.core.KeyStore().RelayAuthPublic()
	if gotAuth.Role != relayv2.RolePhone || gotAuth.Purpose != relayv2.PurposeProbe || !bytes.Equal(gotAuth.PublicKey, wantPhonePub) || gotAuth.Sign == nil {
		t.Fatalf("probe auth = role %q purpose %q pub %x signer_nil=%t", gotAuth.Role, gotAuth.Purpose, gotAuth.PublicKey, gotAuth.Sign == nil)
	}
	if !closed {
		t.Fatal("successful probe connection was not closed")
	}
}
