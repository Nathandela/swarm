package remotegw

// ADR-016 "profile" (FAILING FIRST, RED phase, bd agents-tracker-hggx.3.5): closes a
// WIRING GAP this ADR is the first to need. RelayConfig already carries a Profile field
// (relaysink.go:56, folded into every reconcile record at relaysink.go:365), but
// ServiceConfig -- the config NewService actually TAKES, and the one every real caller
// (cmd/swarm-remote) assembles -- has no Profile field at all, so NewService's
// `RelayConfig{...}` literal (service.go) never sets one. RelayConfig.Profile has been
// reachable from nowhere but a test since it was added: exactly B34's "a fence guarding a
// path production did not take" shape, one layer up.
//
// This test is the first to require ServiceConfig.Profile to exist and to thread through
// to the sealed record, so RelayConfig.Profile finally has a production-reachable caller.

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// TestADR016Profile_ServiceConfigProfileReachesTheSealedRecord proves ServiceConfig.Profile
// is not a second, unwired copy of RelayConfig.Profile: constructing a Service through the
// SAME NewService every production caller uses, with a populated Profile, must seal that
// profile onto the reconcile record -- the same assertion
// TestRelaySink_ReconcileCarriesTheConfiguredProfile makes one layer down, now proven
// through the layer every real caller actually goes through.
func TestADR016Profile_ServiceConfigProfileReachesTheSealedRecord(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 3)
	}
	want := protocol.RemoteProfileV1{
		Version:        1,
		RelayTLSPolicy: "webpki",
		RelayHost:      "swarm-relay.example.com",
	}
	mb := &scriptedMailbox{}
	svc := NewService(ServiceConfig{
		DaemonSocket: "/nonexistent/remote.sock",
		Relay:        mb,
		PhoneTarget:  "phone",
		Machine:      "m1",
		Key:          key,
		EpochID:      1,
		Now:          func() time.Time { return time.Unix(1_700_000_000, 0) },
		Profile:      want,
	})

	if err := svc.sink.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(mb.appends) != 1 {
		t.Fatalf("appended %d envelopes; want 1", len(mb.appends))
	}
	_, plain := openPlaintext(t, key, mb.appends[0])
	var got struct {
		Profile protocol.RemoteProfileV1 `json:"profile"`
	}
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("decode reconcile plaintext: %v", err)
	}
	if got.Profile.RelayTLSPolicy != want.RelayTLSPolicy || got.Profile.RelayHost != want.RelayHost {
		t.Fatalf("sealed profile = %+v; want %+v -- ServiceConfig.Profile did not reach the "+
			"reconcile record NewService seals", got.Profile, want)
	}
	if !reflect.DeepEqual(got.Profile, want) {
		t.Fatalf("sealed profile = %+v; want %+v", got.Profile, want)
	}
}
