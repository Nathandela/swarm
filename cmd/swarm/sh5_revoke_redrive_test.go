package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
	"github.com/Nathandela/swarm/internal/remote/relaypurge"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
)

// TestSH5_TheCLIRecordsRelayV2EvidenceDurably checks the CLI-side producer.
// relaypurge's own tests cover the store's locking, retries, and crash durability.
func TestSH5_TheCLIRecordsRelayV2EvidenceDurably(t *testing.T) {
	stateDir := t.TempDir()
	if err := relaycfg.Save(stateDir, relaycfg.Config{
		RelayURL: "https://relay.example", OperatorNamespace: "owner", TLSPolicy: relaycfg.PolicyWebPKI,
	}); err != nil {
		t.Fatal(err)
	}
	phonePub := bytes.Repeat([]byte{7}, 32)
	routingID := relayv2.RoutingID(phonePub)
	consent := bytes.Repeat([]byte{8}, 100)

	outcome, attemptID := recordPurgeObligation(stateDir, routingID, phonePub, consent, &bytes.Buffer{})
	if outcome != purgeObligationRecorded || attemptID == "" {
		t.Fatalf("record outcome = (%v, %q), want durable attempt", outcome, attemptID)
	}
	store, err := relaypurge.Open(relaypurge.StorePath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.Pending()
	if err != nil || len(pending) != 1 || pending[0].RoutingID != routingID || pending[0].AttemptID != attemptID {
		t.Fatalf("pending after reopen = %+v, %v", pending, err)
	}
}

// TestSH5_PairDrivesTheDeferredPurgeBeforeTheCeremony pins the safety ordering:
// old authority is removed before a new pairing can grant authority.
func TestSH5_PairDrivesTheDeferredPurgeBeforeTheCeremony(t *testing.T) {
	body := functionSource(t, "runRemotePair")
	drive := strings.Index(body, "driveRelayPurgeObligations(")
	start := strings.Index(body, "StartPairing(")
	if drive < 0 || (start >= 0 && drive > start) {
		t.Fatalf("runRemotePair must drive deferred relay purges before StartPairing (drive=%d, start=%d)", drive, start)
	}
}

// TestSH5_TheObligationIsRecordedBeforeTheDestructiveLocalRevoke pins the
// crash boundary: the only record carrying the phone route must not be deleted first.
func TestSH5_TheObligationIsRecordedBeforeTheDestructiveLocalRevoke(t *testing.T) {
	body := functionSource(t, "performRevoke")
	registry := strings.Index(body, "deviceRecordErr(")
	record := strings.Index(body, "recordPurgeObligation(")
	revoke := strings.Index(body, "client.RevokeDevice(")
	if registry < 0 || record < 0 || revoke < 0 || registry > record || record > revoke {
		t.Fatalf("performRevoke must read registry, record purge, then revoke (registry=%d, record=%d, revoke=%d)", registry, record, revoke)
	}
}

func TestSH5_ADriveNeverPurgesARoutingIDThatIsPairedAgain(t *testing.T) {
	stateDir, _ := sh5ConfiguredState(t)
	pub := sh5AddDevice(t, stateDir, 7)
	sh5Record(t, stateDir, relayv2.RoutingID(pub), "https://relay.example", "", pub)
	logs := sh5Drive(t, stateDir, 1)
	if !strings.Contains(logs, "routing id is paired") {
		t.Fatalf("live-pairing guard was not reported: %s", logs)
	}
}

func TestSH5_APairedMachineNeverDialsForAForeignObligation(t *testing.T) {
	stateDir, _ := sh5ConfiguredState(t)
	sh5AddDevice(t, stateDir, 7)
	pub := bytes.Repeat([]byte{9}, 32)
	sh5Record(t, stateDir, relayv2.RoutingID(pub), "https://relay.example", "", pub)
	logs := sh5Drive(t, stateDir, 1)
	if !strings.Contains(logs, "while a device is paired") {
		t.Fatalf("paired-machine dial guard was not reported: %s", logs)
	}
}

func TestSH5_AMismatchedObligationRetiresLoudlyEvenOnAPairedMachine(t *testing.T) {
	stateDir, _ := sh5ConfiguredState(t)
	sh5AddDevice(t, stateDir, 7)
	pub := bytes.Repeat([]byte{9}, 32)
	sh5Record(t, stateDir, relayv2.RoutingID(pub), "https://old-relay.example", "", pub)
	logs := sh5Drive(t, stateDir, 0)
	if !strings.Contains(logs, "https://old-relay.example") || !strings.Contains(logs, "WITHOUT landing") {
		t.Fatalf("relay mismatch was not retired loudly: %s", logs)
	}
}

func TestSH5_AProvisioningReadErrorKeepsTheObligation(t *testing.T) {
	stateDir, _ := sh5ConfiguredState(t)
	pub := bytes.Repeat([]byte{9}, 32)
	sh5Record(t, stateDir, relayv2.RoutingID(pub), "https://relay.example", "", pub)
	if err := os.WriteFile(filepath.Join(stateDir, "remote", "relay.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := sh5Drive(t, stateDir, 1)
	if !strings.Contains(logs, "provisioning could not be read") {
		t.Fatalf("provisioning failure was not kept loudly: %s", logs)
	}
}

func TestSH5_AnObligationUnderAPreviousMachineIdentityRetiresLoudly(t *testing.T) {
	stateDir, machineRID := sh5ConfiguredState(t)
	pub := bytes.Repeat([]byte{9}, 32)
	sh5Record(t, stateDir, relayv2.RoutingID(pub), "https://relay.example", "not-"+machineRID, pub)
	logs := sh5Drive(t, stateDir, 0)
	if !strings.Contains(logs, "previous machine identity") || !strings.Contains(logs, "WITHOUT landing") {
		t.Fatalf("previous identity was not retired loudly: %s", logs)
	}
}

func TestSH5_ARegistryReadErrorFailsTheRevokeClosed(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv(daemon.EnvStateDir, stateDir)
	deviceDir := filepath.Join(stateDir, "devices")
	if err := os.MkdirAll(deviceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deviceDir, "devices.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if exit := performRevoke(nil, "stranded-phone", &out, &out); exit == 0 {
		t.Fatalf("revoke succeeded over unreadable registry: %s", out.String())
	}
	if !strings.Contains(out.String(), "registry could not be read") {
		t.Fatalf("registry refusal was not actionable: %s", out.String())
	}
	if _, err := os.Stat(relaypurge.StorePath(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("revoke crossed the fail-closed boundary and wrote a purge ledger: %v", err)
	}
}

func TestSH5_StatusSurfacesTheDeferredPurgeLedger(t *testing.T) {
	stateDir := t.TempDir()
	store, err := relaypurge.Open(relaypurge.StorePath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	phonePub := bytes.Repeat([]byte{7}, 32)
	owedID := relayv2.RoutingID(phonePub)
	if _, err := store.Record(owedID, "https://relay.example", "", phonePub, bytes.Repeat([]byte{8}, 100)); err != nil {
		t.Fatal(err)
	}
	refusedPub := bytes.Repeat([]byte{9}, 32)
	refusedID := relayv2.RoutingID(refusedPub)
	attempt, err := store.Record(refusedID, "https://relay.example", "", refusedPub, bytes.Repeat([]byte{10}, 100))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(refusedID, attempt.AttemptID, "relay refused"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	reportRelayPurgeState(stateDir, &out)
	if got := out.String(); !strings.Contains(got, "deferred relay purge OWED: routing id "+owedID) ||
		!strings.Contains(got, "relay purge REFUSED: routing id "+refusedID) || !strings.Contains(got, "relay refused") {
		t.Fatalf("status ledger output:\n%s", got)
	}
}

func sh5ConfiguredState(t *testing.T) (string, string) {
	t.Helper()
	stateDir := t.TempDir()
	if err := relaycfg.Save(stateDir, relaycfg.Config{
		RelayURL: "https://relay.example", OperatorNamespace: "owner", TLSPolicy: relaycfg.PolicyWebPKI,
	}); err != nil {
		t.Fatal(err)
	}
	id, err := machineid.Generate("sh5.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := id.Save(filepath.Join(stateDir, "remote", remoteIdentityFile)); err != nil {
		t.Fatal(err)
	}
	return stateDir, relayv2.RoutingID(id.RelayAuthPublic())
}

func sh5AddDevice(t *testing.T, stateDir string, keyByte byte) []byte {
	t.Helper()
	pub := bytes.Repeat([]byte{keyByte}, 32)
	cmd := bytes.Repeat([]byte{keyByte + 1}, 32)
	rec := device.Record{
		DeviceID: device.DeviceIDFor(cmd), Name: "phone", NoiseStaticPub: bytes.Repeat([]byte{keyByte + 2}, 32),
		RelayAuthPub: pub, CommandSignPub: cmd, RecipientPub: bytes.Repeat([]byte{keyByte + 3}, 32),
		RoutingID: []byte(relayv2.RoutingID(pub)), Capability: device.CapFull,
		PairedAt: time.Now(), GrantedEpoch: 1, ConsentSig: bytes.Repeat([]byte{keyByte + 4}, 100),
	}
	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(rec); err != nil {
		t.Fatal(err)
	}
	return pub
}

func sh5Record(t *testing.T, stateDir, routingID, relayURL, machineRID string, pub []byte) {
	t.Helper()
	store, err := relaypurge.Open(relaypurge.StorePath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(routingID, relayURL, machineRID, pub, bytes.Repeat([]byte{8}, 100)); err != nil {
		t.Fatal(err)
	}
}

func sh5Drive(t *testing.T, stateDir string, wantLeft int) string {
	t.Helper()
	var logs strings.Builder
	left := relaypurge.DriveMachineObligations(stateDir, func(format string, args ...any) {
		fmt.Fprintf(&logs, format+"\n", args...)
	})
	if left != wantLeft {
		t.Fatalf("pending after drive = %d, want %d; logs:\n%s", left, wantLeft, logs.String())
	}
	return logs.String()
}

func functionSource(t *testing.T, name string) string {
	t.Helper()
	src, err := os.ReadFile("remote.go")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(src), "func "+name+"(")
	if start < 0 {
		t.Fatalf("%s not found in remote.go", name)
	}
	body := string(src)[start:]
	if end := strings.Index(body[1:], "\nfunc "); end >= 0 {
		body = body[:end+1]
	}
	return body
}
