package voice

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSwitchManagerKeepsPriorConversationAndFencesOldAuthority(t *testing.T) {
	store, prior, dir := testStore(t)
	cfg := testConfig()
	cfg.ConversationID = prior.ConversationID
	cfg.Model = "codex-selected-model"
	cfg.DeviceListen = "127.0.0.1:0"
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	mustBegin(t, store, prior, "prior-id")
	if _, err := store.FinalizeUtterance(prior, "prior-id", "pending text"); err != nil {
		t.Fatal(err)
	}
	old, err := store.AdmitUtterance(prior, "prior-id")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	active, err := lockPrivateService(dir, "controller.lock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SwitchManager(dir, prior.ConversationID, "claude", ""); err == nil || !strings.Contains(err.Error(), "stop the voice service") {
		t.Fatalf("active switch = %v", err)
	}
	if err := active.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := SwitchManager(dir, "wrong-conversation", "claude", ""); err == nil {
		t.Fatal("accepted inaccurate --from")
	}

	next, err := SwitchManager(dir, prior.ConversationID, "claude", "")
	if err != nil {
		t.Fatal(err)
	}
	if next.ConversationID == prior.ConversationID || next.Provider != "claude" || next.WorkDir != cfg.WorkDir ||
		next.Model != "" || next.PlaceID != cfg.PlaceID || next.DaemonSocket != cfg.DaemonSocket || next.DeviceListen != cfg.DeviceListen {
		t.Fatalf("incorrectly switched manager: %+v", next)
	}
	if managerIntentName(next.ConversationID) == managerIntentName(cfg.ConversationID) ||
		managerBridgeAuthName(next) == managerBridgeAuthName(cfg) || deviceRegistryName(next) == deviceRegistryName(cfg) {
		t.Fatal("new conversation inherited launch, bridge or device authority")
	}
	if _, err := SwitchManager(dir, prior.ConversationID, "claude", ""); err == nil {
		t.Fatal("retried stale switch changed configuration")
	}

	store, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	binding := prior
	binding.ConversationID, binding.Provider = next.ConversationID, next.Provider
	binding.SessionID, binding.NativeSessionID = "machine/new-session", "new-native"
	newBinding, err := store.BindManager(binding)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	previousReceipt := state.Admissions[admissionKey(prior.ConversationID, old.Sequence)]
	if previousReceipt.Sequence != old.Sequence || previousReceipt.SourceID != old.SourceID ||
		state.CurrentConversation != next.ConversationID || state.Conversations[next.ConversationID].LastAdmission != 0 {
		t.Fatal("switch lost the old receipt or copied its authority")
	}
	_, err = store.BeginUtterance(prior, Utterance{ID: "lost-authority", DeviceID: "fixture", VoiceSessionID: "fixture", MediaEpoch: 1})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("old conversation still has authority after the new bind: %v", err)
	}
	fresh := mustAdmit(t, store, newBinding, "new-id")
	if fresh.Sequence != 1 || fresh.ConversationID == old.ConversationID {
		t.Fatalf("new admission did not get an independent namespace: %+v", fresh)
	}
}

func TestSwitchManagerRefusesUnconfirmedEffects(t *testing.T) {
	store, binding, dir := testStore(t)
	cfg := testConfig()
	cfg.ConversationID = binding.ConversationID
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	a := mustAdmit(t, store, binding, "uncertain")
	if err := store.update(func(state *State) error {
		a.Phase = "uncertain"
		putRecord(state, "admissions", &state.Admissions, admissionKey(binding.ConversationID, a.Sequence), a)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := SwitchManager(dir, binding.ConversationID, "claude", ""); err == nil || !strings.Contains(err.Error(), "reconcile") {
		t.Fatalf("unconfirmed switch = %v", err)
	}
	got, err := LoadConfig(dir)
	if err != nil || got != cfg {
		t.Fatalf("refused switch changed config: %+v, %v", got, err)
	}
}

func TestSwitchManagerRefusesUnconfirmedLaunchWithoutBinding(t *testing.T) {
	dir := privateServerDir(t)
	cfg := testConfig()
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	intent := managerLaunchIntent{Version: 1, ConversationID: cfg.ConversationID, OperationID: "unconfirmed-launch", Phase: "executing"}
	if err := saveManagerIntent(dir, managerIntentName(cfg.ConversationID), intent); err != nil {
		t.Fatal(err)
	}
	if _, err := SwitchManager(dir, cfg.ConversationID, "claude", ""); err == nil || !strings.Contains(err.Error(), "reconcile") {
		t.Fatalf("unconfirmed launch switch = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, managerIntentName(cfg.ConversationID))); err != nil {
		t.Fatalf("refused switch edited launch recovery state: %v", err)
	}
}
