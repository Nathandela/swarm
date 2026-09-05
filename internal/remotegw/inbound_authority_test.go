package remotegw

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

var (
	authorityOne = RelayAuthority{
		Home:       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		PhoneRID:   "0123456789abcdef0123456789abcdef",
		Generation: 7,
	}
	authorityTwo = RelayAuthority{
		Home:       authorityOne.Home,
		PhoneRID:   authorityOne.PhoneRID,
		Generation: 8,
	}
)

func TestInboundStateBindRelayPersistsBeforeAdoptingAndResetsRelayCoordinates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbound.json")
	state, err := OpenInboundState(path, "machine")
	if err != nil {
		t.Fatal(err)
	}
	stream := InboundStream{Epoch: 3}
	if err := state.Save(InboundCheckpoint{Cursor: 9, Incarnation: "0123456789abcdef0123456789abcdef", Highest: map[InboundStream]uint64{stream: 4}}); err != nil {
		t.Fatal(err)
	}
	if err := state.BindRelay(authorityOne); err != nil {
		t.Fatal(err)
	}
	got := state.Load()
	if got.Relay != authorityOne || got.Cursor != 0 || got.Incarnation != "" || got.Highest[stream] != 4 {
		t.Fatalf("bound checkpoint = %+v", got)
	}
	reopened, err := OpenInboundState(path, "machine")
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Load(); got.Relay != authorityOne || got.Cursor != 0 || got.Highest[stream] != 4 {
		t.Fatalf("reopened checkpoint = %+v", got)
	}
	if err := os.Rename(filepath.Dir(path), filepath.Dir(path)+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := state.BindRelay(authorityOne); err != nil {
		t.Fatalf("identical BindRelay performed I/O: %v", err)
	}

	// A persist failure must leave the in-memory authority untouched.
	failing, err := OpenInboundState(filepath.Join(t.TempDir(), "missing", "inbound.json"), "machine")
	if err != nil {
		t.Fatal(err)
	}
	if err := failing.BindRelay(authorityOne); err == nil {
		t.Fatal("BindRelay unexpectedly persisted into a missing directory")
	}
	if got := failing.Load(); got.Relay != (RelayAuthority{}) {
		t.Fatalf("failed BindRelay adopted %+v", got.Relay)
	}
}

func TestInboundStateBindRelayChangesInEitherIdentityResetCoordinates(t *testing.T) {
	for name, changed := range map[string]RelayAuthority{
		"home":  {Home: "1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", PhoneRID: authorityOne.PhoneRID, Generation: 1},
		"phone": {Home: authorityOne.Home, PhoneRID: "1123456789abcdef0123456789abcdef", Generation: 1},
	} {
		t.Run(name, func(t *testing.T) {
			state, err := OpenInboundState("", "machine")
			if err != nil {
				t.Fatal(err)
			}
			if err := state.BindRelay(authorityOne); err != nil {
				t.Fatal(err)
			}
			stream := InboundStream{Epoch: 1}
			if err := state.Save(InboundCheckpoint{Relay: authorityOne, Cursor: 5, Incarnation: "AAAAAAAAAAAAAAAAAAAAAA", Highest: map[InboundStream]uint64{stream: 3}}); err != nil {
				t.Fatal(err)
			}
			if err := state.BindRelay(changed); err != nil {
				t.Fatal(err)
			}
			got := state.Load()
			if got.Relay != changed || got.Cursor != 0 || got.Incarnation != "" || got.Highest[stream] != 3 {
				t.Fatalf("changed binding = %+v", got)
			}
		})
	}
}

func TestInboundStateBindRelayFencesGenerationAndValidatesWholeTuple(t *testing.T) {
	state, err := OpenInboundState("", "machine")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.BindRelay(authorityTwo); err != nil {
		t.Fatal(err)
	}
	if err := state.BindRelay(authorityOne); err == nil {
		t.Fatal("accepted a lower generation for the same home and phone")
	}
	for _, bad := range []RelayAuthority{
		{},
		{Home: authorityOne.Home, PhoneRID: authorityOne.PhoneRID},
		{Home: "A" + authorityOne.Home[1:], PhoneRID: authorityOne.PhoneRID, Generation: 1},
		{Home: authorityOne.Home, PhoneRID: "g" + authorityOne.PhoneRID[1:], Generation: 1},
	} {
		if err := state.BindRelay(bad); err == nil {
			t.Fatalf("accepted invalid authority %+v", bad)
		}
	}
}

func TestInboundStateBoundCheckpointRequiresNativeIncarnationAndAuthority(t *testing.T) {
	state, err := OpenInboundState("", "machine")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.BindRelay(authorityOne); err != nil {
		t.Fatal(err)
	}
	for _, ck := range []InboundCheckpoint{
		{Relay: RelayAuthority{}, Incarnation: "AAAAAAAAAAAAAAAAAAAAAA", Highest: map[InboundStream]uint64{}},
		{Relay: authorityOne, Incarnation: "0123456789abcdef0123456789abcdef", Highest: map[InboundStream]uint64{}},
	} {
		if err := state.Save(ck); err == nil {
			t.Fatalf("accepted mismatched checkpoint %+v", ck)
		}
	}
	if err := state.Save(InboundCheckpoint{Relay: authorityOne, Incarnation: "AAAAAAAAAAAAAAAAAAAAAA", Highest: map[InboundStream]uint64{}}); err != nil {
		t.Fatalf("native checkpoint: %v", err)
	}
}

func TestInboundStateSchemaThreeRequiresExplicitRelayAuthority(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"v1":        `{"schema_version":1,"machine":"machine","cursor":0,"streams":[]}`,
		"v2":        `{"schema_version":2,"machine":"machine","cursor":0,"streams":[]}`,
		"v4":        `{"schema_version":4,"machine":"machine","cursor":0,"relay_authority":{},"streams":[]}`,
		"missing":   `{"schema_version":3,"machine":"machine","cursor":0,"streams":[]}`,
		"malformed": `{"schema_version":3,"machine":"machine","cursor":0,"relay_authority":{"home":"bad","phone_rid":"bad","generation":1},"streams":[]}`,
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenInboundState(path, "machine"); !errors.Is(err, errCorruptInboundState) {
			t.Fatalf("OpenInboundState(%s) = %v", name, err)
		}
	}
	path := filepath.Join(dir, "zero")
	if err := os.WriteFile(path, []byte(`{"schema_version":3,"machine":"machine","cursor":0,"relay_authority":{},"streams":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := OpenInboundState(path, "machine")
	if err != nil || state.Load().Relay != (RelayAuthority{}) {
		t.Fatalf("explicit legacy-unbound authority = (%+v, %v)", state, err)
	}

	if err := state.BindRelay(authorityOne); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file inboundFile
	if err := json.Unmarshal(data, &file); err != nil || file.SchemaVersion != 3 || file.Relay == nil || *file.Relay != authorityOne {
		t.Fatalf("written schema = %+v, err=%v", file, err)
	}
}

func TestCommandBridgeRefusesCustodyAfterRelayAuthorityChanges(t *testing.T) {
	state := &memInboundState{}
	if err := state.BindRelay(authorityOne); err != nil {
		t.Fatal(err)
	}
	bridge := NewCommandBridge(CommandBridgeConfig{Inbound: state})
	if err := state.BindRelay(authorityTwo); err != nil {
		t.Fatal(err)
	}
	if err := bridge.saveCheckpoint(); !errors.Is(err, errRelayAuthorityChanged) {
		t.Fatalf("stale save = %v", err)
	}
	if err := bridge.rewindMailboxCursor(); !errors.Is(err, errRelayAuthorityChanged) {
		t.Fatalf("stale rewind = %v", err)
	}
	if err := bridge.restoreReceiverFromCheckpoint(); !errors.Is(err, errRelayAuthorityChanged) {
		t.Fatalf("stale restore = %v", err)
	}
}

func TestCommandBridgeDurableReplayIsBoundToCapturedRelayAuthority(t *testing.T) {
	key := inboundKey(41)
	stream := InboundStream{Epoch: 7}
	item := relay.Item{Cursor: 5, Envelope: sealAt(t, key, 7, 1, killCmd("m/s", "op"))}

	newState := func(t *testing.T) *memInboundState {
		t.Helper()
		state := &memInboundState{}
		if err := state.BindRelay(authorityOne); err != nil {
			t.Fatal(err)
		}
		if err := state.Save(InboundCheckpoint{
			Relay: authorityOne, Cursor: 4, Incarnation: "AAAAAAAAAAAAAAAAAAAAAA",
			Highest: map[InboundStream]uint64{stream: 1},
		}); err != nil {
			t.Fatal(err)
		}
		return state
	}

	currentState := newState(t)
	current := NewCommandBridge(CommandBridgeConfig{Inbound: currentState, Key: key})
	if err := current.consumeDurableReplay(item); err != nil || current.Cursor() != 5 {
		t.Fatalf("current replay = (%d, %v), want cursor 5", current.Cursor(), err)
	}

	staleState := newState(t)
	stale := NewCommandBridge(CommandBridgeConfig{Inbound: staleState, Key: key})
	if err := staleState.BindRelay(authorityTwo); err != nil {
		t.Fatal(err)
	}
	if err := stale.consumeDurableReplay(item); !errors.Is(err, errRelayAuthorityChanged) || err.Error() != errRelayAuthorityChanged.Error() {
		t.Fatalf("stale durable replay = %v, want exact %q", err, errRelayAuthorityChanged)
	}
}

func TestMemInboundStateBindRelayMatchesPersistenceAndGenerationFences(t *testing.T) {
	persistErr := errors.New("disk full")
	state := &memInboundState{failSave: persistErr}
	if err := state.BindRelay(authorityOne); !errors.Is(err, persistErr) {
		t.Fatalf("changed authority persistence = %v", err)
	}
	state.failSave = nil
	if err := state.BindRelay(authorityTwo); err != nil {
		t.Fatal(err)
	}
	state.failSave = persistErr
	if err := state.BindRelay(authorityTwo); err != nil {
		t.Fatalf("identical authority performed persistence: %v", err)
	}
	if err := state.BindRelay(authorityOne); err == nil || errors.Is(err, persistErr) {
		t.Fatalf("generation regression = %v", err)
	}
}
