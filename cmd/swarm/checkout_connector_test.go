package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/discord"
	"github.com/Nathandela/swarm/internal/teammatemanager"
	"github.com/Nathandela/swarm/internal/voice"
)

func TestCheckoutInventoryReconcilesCachedParkedRowAcrossLocalSocket(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "sw-ci-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	const endpoint, install, channel, discussion = "ep-inventory", "inventory-fixture", "202", "dt_fixture"
	discussionsDir := filepath.Join(root, "discussions")
	ledgerID := teammatemanager.CheckoutDiscussionLedgerID(discussionsDir, endpoint, install, "", channel, discussion)
	dir := filepath.Join(discussionsDir, ledgerID)
	store, err := voice.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager := voice.ManagerBinding{
		ConversationID: voice.DiscussionConversationPrefix + ledgerID,
		Provider:       "codex", SessionID: endpoint + "/manager", SessionInstance: "native-instance",
		NativeSessionID: "native-id", NativeGeneration: "native-generation",
		ObservationGeneration: "observation-generation", LoginEpoch: "native-login",
	}
	manager, err = store.BindManager(manager)
	if err != nil {
		t.Fatal(err)
	}
	input := voice.Utterance{ID: "pending", DeviceID: "fixture", VoiceSessionID: "fixture", MediaEpoch: 1}
	if _, err = store.BeginUtterance(manager, input); err != nil {
		t.Fatal(err)
	}
	if _, err = store.FinalizeUtterance(manager, input.ID, "Private note"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AdmitUtterance(manager, input.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	ledger := filepath.Join(dir, "voice.db")
	before, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "bridge.sock")
	local, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	if err = os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/checkout-status" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(discord.TeammateStatus{
			Discussions: []discord.TeammateDiscussionStatus{{
				ID: discussion, Thread: discord.Route{ChannelID: channel},
				Native: &discord.TeammateNativeStatus{State: "parked", ManagerSession: manager.SessionID, ManagerInstance: "cached-instance"},
				Outbox: []discord.TeammateOutputStatus{{ID: "connector-publication"}},
			}},
		}); err != nil {
			t.Error(err)
		}
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = server.Serve(local)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		<-served
	})

	rows, issues := inspectCheckoutConnector(t.Context(), root, install, map[string]string{"": endpoint})
	if len(issues) != 0 || len(rows) != 1 || len(rows[0].Unknowns) != 0 {
		t.Fatalf("unexpected inventory: rows %+v issues %v", rows, issues)
	}
	row := rows[0]
	if row.ID != discussion || row.State != "active" || row.ManagerSession != manager.SessionID || row.ManagerInstance != manager.SessionInstance {
		t.Fatalf("cached row overrode durable manager identity: %+v", row)
	}
	receipts := make(map[string]string)
	for _, receipt := range row.Receipts {
		receipts[receipt.Kind] = receipt.ID + ":" + receipt.State
	}
	if receipts["native_admissions"] != ":1 unresolved" || receipts["discord_publication"] != "connector-publication:pending" {
		t.Fatalf("native or connector receipts missing: %+v", row.Receipts)
	}
	after, err := os.ReadFile(ledger)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("inspection altered the voice ledger: %v", err)
	}
}
