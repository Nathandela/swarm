package logins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNativeProofCanonicalSchemaThreePreservesWireFields(t *testing.T) {
	native := NativeProcessInfo{SchemaVersion: NativeWriterSchemaVersion, Generation: strings.Repeat("a", 32), PID: 31, PGID: 31, StartTime: 41, ShimPID: 51, ShimStartTime: 61, Provider: ProviderClaude, Runtime: Runtime{Provider: ProviderClaude, Epoch: 1, Identity: strings.Repeat("b", 64), Home: "/private/native"}}
	raw, err := json.Marshal(native)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":3,"generation":"` + strings.Repeat("a", 32) + `","pid":31,"pgid":31,"start_time":41,"shim_pid":51,"shim_start_time":61,"provider":"claude","runtime":{"provider":"claude","epoch":1,"identity":"` + strings.Repeat("b", 64) + `","home":"/private/native"}}`
	if string(raw) != want {
		t.Fatalf("schema3 native proof changed: %s", raw)
	}
	if ClaudeNativeWriterSchemaVersion != 4 || ClaudeReservationSchemaVersion != 5 || NativeProcessFile != "native-process.json" || NativeStoppedFile != "native-writers-stopped.json" {
		t.Fatal("incompatible canonical proof constants")
	}
}

func claudeCustodyFixture(t *testing.T) (*Store, Login, map[string]any, NativeProcessInfo) {
	t.Helper()
	s, l, _ := profileFixture(t)
	if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectClaudeProfile(snapshot(t, s).Revision, l.ID); err != nil {
		t.Fatal(err)
	}
	home, err := s.ClaudeProfileHome(l.Identity)
	if err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Provider: ProviderClaude, Identity: l.Identity, Epoch: 1, Home: home}
	meta := map[string]any{"schema_version": ClaudeReservationSchemaVersion, "id": "source", "agent_type": ProviderClaude, "cwd": "/work", "agent_cwd": "", "shim_pid": 0, "shim_start_time": 0, "login_runtime": rt, "cli_identity": map[string]string{"path": "/qualified/claude", "version": "2.1.310", "fingerprint": strings.Repeat("e", 64)}}
	native := NativeProcessInfo{SchemaVersion: ClaudeNativeWriterSchemaVersion, Generation: strings.Repeat("f", 32), ShimPID: 536870912, ShimStartTime: 1, Provider: ProviderClaude, Runtime: rt, SessionID: "source", Cwd: "/work", CLIFingerprint: strings.Repeat("e", 64)}
	writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", meta)
	return s, l, meta, native
}

func writeClaudeCustodyJSON(t *testing.T, state, id, name string, value any) []byte {
	t.Helper()
	dir := filepath.Join(state, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestClaudeCustodyNeverInfersNoWriterFromMissingOrZeroPID(t *testing.T) {
	s, l, _, native := claudeCustodyFixture(t)
	for _, kind := range []string{"missing", "zero_pid", "positive_stopped"} {
		t.Run(kind, func(t *testing.T) {
			if kind != "missing" {
				writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeProcessFile, native)
			}
			if kind == "positive_stopped" {
				writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeStoppedFile, NativeStoppedInfo{SchemaVersion: ClaudeNativeWriterSchemaVersion, Native: native, WritersStopped: true, DescendantsContained: true})
			}
			s.mu.Lock()
			err := s.locked(func() error { return s.claudeAuthorityMutationAllowed(snapshotUnlockedClaudeTest(t, s), l) })
			s.mu.Unlock()
			if kind == "positive_stopped" {
				if err != nil {
					t.Fatalf("exact stopped unpublished scope retained authority: %v", err)
				}
			} else if err == nil {
				t.Fatal("missing PID/proof released authority")
			}
		})
	}
}

func snapshotUnlockedClaudeTest(t *testing.T, s *Store) Registry {
	t.Helper()
	r, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestClaudeCustodyProofRejectsRoutingMismatchAndAmbiguousMetadata(t *testing.T) {
	for _, kind := range []string{"session", "cwd", "fingerprint", "generation", "identity", "profile", "duplicate", "case_alias", "schema", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			s, _, meta, native := claudeCustodyFixture(t)
			proof := NativeStoppedInfo{SchemaVersion: ClaudeNativeWriterSchemaVersion, Native: native, WritersStopped: true, DescendantsContained: true}
			switch kind {
			case "session":
				native.SessionID = "foreign"
			case "cwd":
				native.Cwd = "/other"
			case "fingerprint":
				native.CLIFingerprint = strings.Repeat("d", 64)
			case "generation":
				proof.Native.Generation = strings.Repeat("d", 32)
			case "identity":
				native.Runtime.Identity = strings.Repeat("a", 64)
			case "profile":
				rt := meta["login_runtime"].(Runtime)
				rt.Home = s.Home(ProviderClaude)
				meta["login_runtime"] = rt
			case "schema":
				meta["schema_version"] = 99
			}
			raw := writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", meta)
			path := filepath.Join(s.StateRoot(), "source", "meta.json")
			if kind == "duplicate" {
				raw = append([]byte(`{"id":"source",`), raw[1:]...)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "case_alias" {
				raw = append([]byte(`{"Login_Runtime":null,`), raw[1:]...)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "symlink" {
				if err := os.Rename(path, path+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".real", path); err != nil {
					t.Fatal(err)
				}
			}
			writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeProcessFile, native)
			writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeStoppedFile, proof)
			if err := VerifyClaudeNativeWritersStopped(s.StateRoot(), "source"); err == nil {
				t.Fatal("ambiguous routing released authority")
			}
		})
	}
}

func TestClaudeMetadataSaveGuardsStoredOwnershipAgainstStrippedIncomingRow(t *testing.T) {
	s, _, meta, _ := claudeCustodyFixture(t)
	incoming := map[string]any{"schema_version": 1, "id": "source", "agent_type": ProviderClaude, "cwd": "/work"}
	raw, err := json.Marshal(incoming)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if err := WithClaudeMetadataSave(s.StateRoot(), "source", raw, func() error { called = true; return nil }); err == nil || called {
		t.Fatal("stale nil-runtime Save bypassed stored ownership")
	}
	meta["name"] = "ordinary status/name update"
	raw, err = json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := WithClaudeMetadataSave(s.StateRoot(), "source", raw, func() error { called = true; return nil }); err != nil || !called {
		t.Fatalf("unchanged owned routing rejected: %v", err)
	}
}

func TestClaudeMetadataUnmanagedRowsKeepExistingPathAndSchemaBehavior(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"schema_version":1,"id":"plain","agent_type":"fake","status":"opaque-additive-ui-state"}`)
	for _, state := range []string{root, alias} {
		called := false
		if err := WithClaudeMetadataSave(state, "plain", plain, func() error { called = true; return nil }); err != nil || !called {
			t.Fatalf("unmanaged plain Save changed: %v", err)
		}
	}
	writeClaudeCustodyJSON(t, root, "plain", "meta.json", map[string]any{"schema_version": 1, "id": "plain"})
	if err := WithClaudeReservationRetirement(alias, "plain", nil, func(marker ClaudeReservationRetirement) error {
		if marker != (ClaudeReservationRetirement{}) {
			t.Fatal("unrelated row received owned retirement")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeReservationRetirementStampsExactScopeAndPreservesMarker(t *testing.T) {
	s, _, meta, native := claudeCustodyFixture(t)
	called := false
	if err := WithClaudeReservationRetirement(s.StateRoot(), "source", nil, func(ClaudeReservationRetirement) error { called = true; return nil }); !errors.Is(err, ErrPending) || called {
		t.Fatalf("missing scope supplied no-start proof: %v %v", called, err)
	}
	writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeProcessFile, native)
	writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeStoppedFile, NativeStoppedInfo{SchemaVersion: ClaudeNativeWriterSchemaVersion, Native: native, WritersStopped: true, DescendantsContained: true})
	var marker ClaudeReservationRetirement
	if err := WithClaudeReservationRetirement(s.StateRoot(), "source", nil, func(got ClaudeReservationRetirement) error {
		marker = got
		meta["claude_retirement"] = got
		writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", meta)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if marker.Native != native || marker.Source != ClaudeRetirementStopped {
		t.Fatalf("wrong retirement: %+v", marker)
	}
	if err := os.Remove(filepath.Join(s.StateRoot(), "source", NativeProcessFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.StateRoot(), "source", NativeStoppedFile)); err != nil {
		t.Fatal(err)
	}
	if err := WithClaudeReservationRetirement(s.StateRoot(), "source", nil, func(got ClaudeReservationRetirement) error {
		if got != marker {
			t.Fatal("partial deletion lost exact retirement")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	delete(meta, "claude_retirement")
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := WithClaudeMetadataSave(s.StateRoot(), "source", raw, func() error { t.Fatal("stale metadata cleared retirement"); return nil }); err == nil {
		t.Fatal("retirement not preserved")
	}
}

func TestClaudeReservationOriginalNoForkAuthorityCannotBeReconstructed(t *testing.T) {
	s, _, meta, _ := claudeCustodyFixture(t)
	issuerCalled := false
	if err := WithClaudeReservationRetirement(s.StateRoot(), "source", func() error { issuerCalled = true; return nil }, func(marker ClaudeReservationRetirement) error {
		if marker.Source != ClaudeRetirementNoFork || marker.Issuer.PID != os.Getpid() || marker.Issuer.StartTime <= 0 {
			t.Fatalf("no issuing-process proof: %+v", marker)
		}
		meta["claude_retirement"] = marker
		writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", meta)
		return nil
	}); err != nil || !issuerCalled {
		t.Fatalf("original pre-fork retirement: %v", err)
	}
}

func TestClaudeSavedMutationProtectsNonselectedReservedAuthorityAtEveryBoundary(t *testing.T) {
	for _, seam := range []string{"auth", "metadata", "saved", "remove"} {
		t.Run(seam, func(t *testing.T) {
			s, a, _, _ := claudeCustodyFixture(t)
			b := add(t, s, ProviderClaude, "B", claudeCredential("b", "b"))
			if _, err := s.SelectClaudeProfile(snapshot(t, s).Revision, b.ID); err != nil {
				t.Fatal(err)
			}
			before, err := s.claudeAuth(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			replacement := claudeCredential("a", "replacement")
			var mutationErr error
			if seam == "remove" {
				_, mutationErr = s.Remove(snapshot(t, s).Revision, a.ID)
			} else {
				s.mu.Lock()
				mutationErr = s.locked(func() error {
					switch seam {
					case "auth":
						return s.writeClaudeAuth(a.ID, replacement.Auth)
					case "metadata":
						out := a
						out.CredentialRevision++
						return s.writeSavedMetadata(out, replacement)
					default:
						return s.writeSaved(a, replacement)
					}
				})
				s.mu.Unlock()
			}
			if mutationErr == nil {
				t.Fatal("nonselected zero-PID reservation lost authority")
			}
			after, err := s.claudeAuth(a.ID)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("custody failure changed saved grant: %v", err)
			}
			if findLogin := findRegistryLoginForClaudeTest(t, s, a.ID); findLogin == nil {
				t.Fatal("custody failure removed registry login")
			}
		})
	}
}

func findRegistryLoginForClaudeTest(t *testing.T, s *Store, id string) *Login {
	t.Helper()
	r := snapshot(t, s)
	return find(&r, id)
}

func TestClaudeRemovalCASFailureKeepsExactSettledHelperBudget(t *testing.T) {
	s, _, b, ref := newSettledReadyClaudeAuthTest(t)
	s.writeOps.rename = func(old, next string) error {
		if next == "registry.json" {
			return syscall.ENOSPC
		}
		return s.root.Rename(old, next)
	}
	if _, err := s.Remove(snapshot(t, s).Revision, b.ID); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("removal CAS write: %v", err)
	}
	s.writeOps.rename = nil
	held, err := s.ReadClaudeAuth(ref)
	if err != nil || held.Phase != "ready" {
		t.Fatalf("failed removal erased only helper budget: %+v %v", held, err)
	}
	if findRegistryLoginForClaudeTest(t, s, b.ID) == nil {
		t.Fatal("failed registry save removed saved login")
	}
}

func TestClaudeRemovalRegistryCommitPrecedesHelperRetirementAndOrphanReopens(t *testing.T) {
	s, a, b, ref := newSettledReadyClaudeAuthTest(t)
	s.writeOps.rename = func(old, next string) error {
		if next == claudeAuthPath(b.ID) {
			return syscall.ENOSPC
		}
		return s.root.Rename(old, next)
	}
	_, err := s.Remove(snapshot(t, s).Revision, b.ID)
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("expected post-removal cleanup fault: %v", err)
	}
	s.writeOps.rename = nil
	if findRegistryLoginForClaudeTest(t, s, b.ID) != nil {
		t.Fatal("helper retirement preceded durable removal")
	}
	reopened, err := OpenExisting(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	reopened.keychain = nil
	for range 2 {
		if err := reopened.Reconcile(); err != nil {
			t.Fatalf("recognized helper orphan blocked unrelated reopen: %v", err)
		}
	}
	refs, err := reopened.ListClaudeAuth()
	if err != nil || len(refs) != 1 || refs[0].Ref != ref {
		t.Fatalf("orphan lost exact budget: %+v %v", refs, err)
	}
	if got := snapshot(t, reopened).Providers[ProviderClaude].ActiveID; got != a.ID {
		t.Fatal("orphan cleanup changed healthy selected account")
	}
	if _, err := os.Stat(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json")); err != nil {
		t.Fatal("orphan grant erased before exact helper retirement")
	}
	if err := reopened.RetireClaudeAuth(ref); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.savedNativeHome(b.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resolved orphan not cleaned: %v", err)
	}
}

func TestClaudeOwnedReloginPersistsPendingPerLoginUntilExactCustodyStops(t *testing.T) {
	s, a, _, native := claudeCustodyFixture(t)
	b := add(t, s, ProviderClaude, "B", claudeCredential("b", "b"))
	r, err := s.SelectClaudeProfile(snapshot(t, s).Revision, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	selected := r.Providers[ProviderClaude]
	job, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReloginJob(r.Revision, a.ID, claudeCredential("a", "replacement"), job); !errors.Is(err, ErrPending) {
		t.Fatalf("unresolved old A relogin: %v", err)
	}
	if _, err := s.readRelogin(a); err != nil {
		t.Fatalf("pending relogin intent not retained: %v", err)
	}
	if err := s.Reconcile(); err != nil {
		t.Fatalf("valid pending relogin blocked unrelated reopen: %v", err)
	}
	if got := snapshot(t, s).Providers[ProviderClaude]; got != selected {
		t.Fatal("pending old A relogin changed B epoch")
	}
	writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeProcessFile, native)
	writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeStoppedFile, NativeStoppedInfo{SchemaVersion: ClaudeNativeWriterSchemaVersion, Native: native, WritersStopped: true, DescendantsContained: true})
	if err := s.Reconcile(); err != nil {
		t.Fatal(err)
	}
	current := snapshot(t, s)
	updated := find(&current, a.ID)
	if updated == nil || updated.CredentialRevision != a.CredentialRevision+1 || updated.LastLoginJobID != job || current.Providers[ProviderClaude] != selected {
		t.Fatalf("old A replay relabeled current B: %+v", current)
	}
	if err := s.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if findRegistryLoginForClaudeTest(t, s, a.ID).CredentialRevision != a.CredentialRevision+1 {
		t.Fatal("replay applied revision twice")
	}
}

func TestClaudeUnmanagedExecutionOnlyDeletionRemainsUnrelated(t *testing.T) {
	root := t.TempDir()
	writeClaudeCustodyJSON(t, root, "execution-only", "execution.json", map[string]any{})
	if err := os.WriteFile(filepath.Join(root, "execution-only", "execution.pending"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := WithClaudeReservationRetirement(root, "execution-only", nil, func(marker ClaudeReservationRetirement) error {
		called = true
		if marker != (ClaudeReservationRetirement{}) {
			t.Fatal("execution-only row received owned marker")
		}
		return nil
	}); err != nil || !called {
		t.Fatalf("unmanaged execution deletion rejected: %v", err)
	}
	writeClaudeCustodyJSON(t, root, "execution-only", NativeProcessFile, map[string]any{})
	called = false
	if err := WithClaudeReservationRetirement(root, "execution-only", nil, func(ClaudeReservationRetirement) error { called = true; return nil }); err == nil || called {
		t.Fatal("native proof hint bypassed missing managed metadata")
	}
}

func TestClaudeLegacyRetirementRetainsUnresolvedAndSurvivesPartialDeletion(t *testing.T) {
	for _, kind := range []string{"zero_shim", "unbound_native3", "stopped", "no_fork", "no_cli"} {
		t.Run(kind, func(t *testing.T) {
			s, _, meta, native := claudeCustodyFixture(t)
			rt := meta["login_runtime"].(Runtime)
			rt.Home = s.Home(ProviderClaude)
			meta["schema_version"], meta["login_runtime"] = 4, rt
			native.SchemaVersion, native.Runtime = NativeWriterSchemaVersion, rt
			native.SessionID, native.Cwd, native.CLIFingerprint = "", "", ""
			if kind == "stopped" {
				meta["shim_pid"], meta["shim_start_time"] = native.ShimPID, native.ShimStartTime
			}
			writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", meta)
			if kind == "no_cli" {
				delete(meta, "cli_identity")
				writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", meta)
			}
			if kind == "stopped" || kind == "unbound_native3" {
				writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeProcessFile, native)
				writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeStoppedFile, NativeStoppedInfo{SchemaVersion: 3, Native: native, WritersStopped: true, DescendantsContained: true})
			}
			var noFork func() error
			if kind == "no_fork" || kind == "no_cli" {
				noFork = func() error { return nil }
			}
			var marker ClaudeReservationRetirement
			err := WithClaudeReservationRetirement(s.StateRoot(), "source", noFork, func(got ClaudeReservationRetirement) error { marker = got; return nil })
			if kind == "zero_shim" || kind == "unbound_native3" {
				if err == nil || marker != (ClaudeReservationRetirement{}) {
					t.Fatalf("legacy uncertainty bypassed retirement: %+v %v", marker, err)
				}
				return
			}
			if err != nil || marker == (ClaudeReservationRetirement{}) {
				t.Fatalf("legacy positive retirement missing: %+v %v", marker, err)
			}
			meta["schema_version"], meta["claude_retirement"] = 5, marker
			raw := writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", meta)
			for _, name := range []string{NativeProcessFile, NativeStoppedFile} {
				if err := os.Remove(filepath.Join(s.StateRoot(), "source", name)); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			}
			if err := WithClaudeReservationRetirement(s.StateRoot(), "source", nil, func(got ClaudeReservationRetirement) error {
				if got != marker {
					t.Fatal("changed retained marker")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := WithClaudeMetadataSave(s.StateRoot(), "source", raw, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			delete(meta, "claude_retirement")
			raw, _ = json.Marshal(meta)
			if err := WithClaudeMetadataSave(s.StateRoot(), "source", raw, func() error { t.Fatal("stripped legacy marker published"); return nil }); err == nil {
				t.Fatal("legacy marker stripping accepted")
			}
		})
	}
}

func TestClaudeLegacyOperationalSaveCannotEraseUnresolvedRuntime(t *testing.T) {
	s, _, meta, _ := claudeCustodyFixture(t)
	rt := meta["login_runtime"].(Runtime)
	rt.Home = s.Home(ProviderClaude)
	meta["schema_version"], meta["login_runtime"] = 4, rt
	writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", meta)
	raw := []byte(`{"schema_version":1,"id":"source","agent_type":"claude","cwd":"/work"}`)
	if err := WithClaudeMetadataSave(s.StateRoot(), "source", raw, func() error { t.Fatal("unresolved legacy routing erased"); return nil }); err == nil {
		t.Fatal("unresolved legacy downgrade accepted")
	}
}

func TestClaudeRetirementRejectsNullAndUnrecognizedNoForkProof(t *testing.T) {
	for _, kind := range []string{"native_null", "native_unknown", "shim_null", "runtime_external_null"} {
		t.Run(kind, func(t *testing.T) {
			s, _, meta, _ := claudeCustodyFixture(t)
			if err := WithClaudeReservationRetirement(s.StateRoot(), "source", func() error { return nil }, func(marker ClaudeReservationRetirement) error { meta["claude_retirement"] = marker; return nil }); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(meta)
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			marker := doc["claude_retirement"].(map[string]any)
			switch kind {
			case "native_null":
				marker["native"] = nil
			case "native_unknown":
				marker["native"].(map[string]any)["unknown"] = true
			case "shim_null":
				doc["shim_pid"] = nil
			case "runtime_external_null":
				doc["login_runtime"].(map[string]any)["external_auth"] = nil
			}
			raw, _ = json.Marshal(doc)
			if _, err := parseClaudeSessionMeta(raw, s.StateRoot(), "source"); err == nil {
				t.Fatal("ambiguous no-fork authority accepted")
			}
		})
	}
}

func TestClaudeLegacyUnknownRuntimeCannotBecomeUnrelatedAtOperationalFloor(t *testing.T) {
	for _, kind := range []string{"missing", "external", "foreign"} {
		t.Run(kind, func(t *testing.T) {
			s, _, meta, _ := claudeCustodyFixture(t)
			rt := meta["login_runtime"].(Runtime)
			rt.Home = s.Home(ProviderClaude)
			meta["schema_version"], meta["login_runtime"] = 4, rt
			switch kind {
			case "missing":
				delete(meta, "login_runtime")
			case "external":
				rt.ExternalAuth = true
				meta["login_runtime"] = rt
			case "foreign":
				rt.Home = "/unknown/common"
				meta["login_runtime"] = rt
			}
			writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", meta)
			called := false
			if err := WithClaudeReservationRetirement(s.StateRoot(), "source", nil, func(ClaudeReservationRetirement) error { called = true; return nil }); err == nil || called {
				t.Fatalf("unknown legacy routing classified unrelated: %v %v", called, err)
			}
		})
	}
}

func TestClaudeAbsentMetadataAndDeletionFenceConcurrentBootstrap(t *testing.T) {
	for _, kind := range []string{"save", "delete"} {
		t.Run(kind, func(t *testing.T) {
			state := t.TempDir()
			if err := os.Chmod(state, 0700); err != nil {
				t.Fatal(err)
			}
			if kind == "delete" {
				writeClaudeCustodyJSON(t, state, "source", "execution.json", map[string]int{"schema_version": 1})
			}
			entered, release := make(chan struct{}), make(chan struct{})
			callbackDone := make(chan error, 1)
			callback := func() error {
				close(entered)
				<-release
				if kind == "delete" {
					return os.RemoveAll(filepath.Join(state, "source"))
				}
				return os.WriteFile(filepath.Join(state, "source", "meta.json"), []byte(`{"schema_version":1,"id":"source","agent_type":"fake"}`), 0600)
			}
			if err := os.MkdirAll(filepath.Join(state, "source"), 0700); err != nil {
				t.Fatal(err)
			}
			go func() {
				if kind == "save" {
					callbackDone <- WithClaudeMetadataSave(state, "source", []byte(`{"schema_version":1,"id":"source","agent_type":"fake"}`), callback)
				} else {
					callbackDone <- WithClaudeReservationRetirement(state, "source", nil, func(ClaudeReservationRetirement) error { return callback() })
				}
			}()
			<-entered
			probe, err := os.Open(state)
			if err != nil {
				t.Fatal(err)
			}
			fenceErr := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if fenceErr == nil {
				_ = syscall.Flock(int(probe.Fd()), syscall.LOCK_UN)
			}
			_ = probe.Close()
			bootDone := make(chan error, 1)
			go func() {
				s, err := Open(state)
				if err != nil {
					bootDone <- err
					return
				}
				defer func() { _ = s.Close() }()
				s.keychain = nil
				r, l, err := s.Add(snapshot(t, s).Revision, ProviderClaude, "A", claudeCredential("a", "a"))
				if err != nil {
					bootDone <- err
					return
				}
				r, err = s.SelectClaudeProfile(r.Revision, l.ID)
				if err != nil {
					bootDone <- err
					return
				}
				rt := Runtime{Provider: ProviderClaude, Epoch: r.Providers[ProviderClaude].Epoch, Identity: l.Identity, Home: filepath.Join(state, "logins", "provider", "claude", "profiles", l.Identity)}
				meta := map[string]any{"schema_version": 5, "id": "source", "agent_type": "claude", "cwd": "/work", "login_runtime": rt, "cli_identity": map[string]string{"path": "/qualified/claude", "version": "2.1.310", "fingerprint": strings.Repeat("e", 64)}}
				raw, _ := json.Marshal(meta)
				s.mu.Lock()
				err = s.locked(func() error {
					if err := os.MkdirAll(filepath.Join(state, "source"), 0700); err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(state, "source", "meta.json"), raw, 0600)
				})
				s.mu.Unlock()
				bootDone <- err
			}()
			early := false
			select {
			case err := <-bootDone:
				early = true
				if err != nil {
					t.Error(err)
				}
			case <-time.After(time.Second):
			}
			close(release)
			if err := <-callbackDone; err != nil {
				t.Fatal(err)
			}
			if !early {
				if err := <-bootDone; err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(filepath.Join(state, "source", "meta.json"))
			if err != nil {
				t.Fatal(err)
			}
			if early || !errors.Is(fenceErr, syscall.EWOULDBLOCK) || !bytes.Contains(raw, []byte(`"schema_version":5`)) {
				t.Fatalf("unmanaged callback raced bootstrap: early=%v inode_fence=%v final=%s", early, fenceErr, raw)
			}
		})
	}
}

func TestClaudeInitializedMetadataDoesNotWaitForStateDirectoryLock(t *testing.T) {
	s := testStore(t)
	file, err := os.Open(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- WithClaudeMetadataSave(s.StateRoot(), "plain", []byte(`{"schema_version":1,"id":"plain","agent_type":"fake"}`), func() error { return nil })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("initialized metadata waited for root-directory flock")
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

func TestClaudeAbsentFenceRechecksPublishedAndPartialBootstrap(t *testing.T) {
	for _, kind := range []string{"initialized", "partial"} {
		t.Run(kind, func(t *testing.T) {
			s, _, meta, _ := claudeCustodyFixture(t)
			state := s.StateRoot()
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			stage := filepath.Join(t.TempDir(), "logins")
			if err := os.Rename(filepath.Join(state, "logins"), stage); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Join(state, "source")); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(state)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
				t.Fatal(err)
			}
			started, called := make(chan struct{}), make(chan struct{}, 1)
			done := make(chan error, 1)
			go func() {
				close(started)
				done <- WithClaudeMetadataSave(state, "source", []byte(`{"schema_version":1,"id":"source","agent_type":"fake"}`), func() error { called <- struct{}{}; return nil })
			}()
			<-started
			select {
			case <-called:
				t.Error("absent callback bypassed held inode fence")
			case <-time.After(100 * time.Millisecond):
			}
			if kind == "initialized" {
				if err := os.Rename(stage, filepath.Join(state, "logins")); err != nil {
					t.Fatal(err)
				}
				writeClaudeCustodyJSON(t, state, "source", "meta.json", meta)
			} else {
				if err := os.Mkdir(filepath.Join(state, "logins"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			if err := <-done; err == nil {
				t.Fatal("new initialized/partial bootstrap fell through unmanaged")
			}
			select {
			case <-called:
				t.Fatal("stale callback ran after login publication")
			default:
			}
		})
	}
}

func claudeLegacyCleanupFixture(t *testing.T, floor int, stopped bool) (*Store, Login, map[string]any, NativeProcessInfo) {
	t.Helper()
	s := testStore(t)
	a := add(t, s, ProviderClaude, "A", claudeCredential("a", "a"))
	s.mu.Lock()
	err := s.locked(func() error {
		r, err := s.load()
		if err != nil {
			return err
		}
		r.SchemaVersion = floor
		r.Providers[ProviderClaude] = ProviderState{Provider: ProviderClaude, ActiveID: a.ID, Epoch: 7}
		return s.save(r)
	})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Provider: ProviderClaude, Identity: a.Identity, Epoch: 7, Home: s.Home(ProviderClaude)}
	meta := map[string]any{"schema_version": 4, "id": "source", "agent_type": "claude", "cwd": "/work", "login_runtime": rt, "shim_pid": 0, "shim_start_time": 0}
	native := NativeProcessInfo{SchemaVersion: 3, Generation: strings.Repeat("f", 32), ShimPID: 536870912, ShimStartTime: 1, Provider: ProviderClaude, Runtime: rt}
	if stopped {
		meta["shim_pid"], meta["shim_start_time"] = native.ShimPID, native.ShimStartTime
		writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeProcessFile, native)
		writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeStoppedFile, NativeStoppedInfo{SchemaVersion: 3, Native: native, WritersStopped: true, DescendantsContained: true})
	}
	writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", meta)
	return s, a, meta, native
}

func TestClaudeLegacyCleanupPromotesAllFloorsOnlyAfterPositiveProof(t *testing.T) {
	for _, floor := range []int{1, 9} {
		for _, kind := range []string{"unresolved", "stopped", "no_fork"} {
			t.Run(fmt.Sprintf("%d_%s", floor, kind), func(t *testing.T) {
				s, _, meta, _ := claudeLegacyCleanupFixture(t, floor, kind == "stopped")
				before := snapshot(t, s)
				var noFork func() error
				if kind == "no_fork" {
					noFork = func() error { return nil }
				}
				called := false
				err := WithClaudeReservationRetirement(s.StateRoot(), "source", noFork, func(marker ClaudeReservationRetirement) error {
					called = true
					r := snapshot(t, s)
					want := before
					want.SchemaVersion = 10
					want.Revision++
					if !reflect.DeepEqual(r, want) || marker == (ClaudeReservationRetirement{}) {
						t.Fatalf("cleanup precedes exact durable promotion: %+v marker=%+v", r, marker)
					}
					meta["schema_version"], meta["claude_retirement"] = 5, marker
					writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", meta)
					return nil
				})
				if kind == "unresolved" {
					if err == nil || called || !reflect.DeepEqual(snapshot(t, s), before) {
						t.Fatalf("unproved cleanup changed floor: %v %v", called, err)
					}
					return
				}
				if err != nil || !called {
					t.Fatalf("positive cleanup failed: %v %v", called, err)
				}
				promoted := snapshot(t, s)
				if err := WithClaudeReservationRetirement(s.StateRoot(), "source", nil, func(marker ClaudeReservationRetirement) error {
					if marker == (ClaudeReservationRetirement{}) {
						t.Fatal("retry lost marker")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(snapshot(t, s), promoted) {
					t.Fatal("retry promoted revision twice")
				}
			})
		}
	}
}

func TestClaudeLegacyImmutableSaveAndMissingStorageRefuseBeforeOperationalFloor(t *testing.T) {
	for _, kind := range []string{"floor1", "floor9", "missing", "missing_external", "partial"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, _ := claudeLegacyCleanupFixture(t, 1, false)
			if kind == "floor9" {
				s.mu.Lock()
				err := s.locked(func() error {
					r, err := s.load()
					if err != nil {
						return err
					}
					r.SchemaVersion = 9
					return s.save(r)
				})
				s.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind == "missing_external" {
				path := filepath.Join(s.StateRoot(), "source", "meta.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var m map[string]any
				if json.Unmarshal(raw, &m) != nil {
					t.Fatal("invalid fixture")
				}
				m["login_runtime"].(map[string]any)["external_auth"] = true
				writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", m)
			}
			if kind == "missing" || kind == "missing_external" || kind == "partial" {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(filepath.Join(s.StateRoot(), "logins")); err != nil {
					t.Fatal(err)
				}
				if kind == "partial" {
					if err := os.Mkdir(filepath.Join(s.StateRoot(), "logins"), 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			called := false
			if err := WithClaudeMetadataSave(s.StateRoot(), "source", []byte(`{"schema_version":1,"id":"source","agent_type":"fake"}`), func() error { called = true; return nil }); err == nil || called {
				t.Fatalf("legacy routing erased before custody floor: %v %v", called, err)
			}
			if err := WithClaudeReservationRetirement(s.StateRoot(), "source", nil, func(ClaudeReservationRetirement) error { called = true; return nil }); err == nil || called {
				t.Fatalf("legacy missing/unproved custody became unrelated: %v %v", called, err)
			}
			if kind == "missing" || kind == "missing_external" || kind == "partial" {
				if err := WithClaudeReservationRetirement(s.StateRoot(), "source", nil, func(ClaudeReservationRetirement) error { return nil }); !errors.Is(err, ErrPending) {
					t.Fatalf("incomplete storage lost pending classification: %v", err)
				}
			}
		})
	}
}

func TestClaudeLegacyPromotionFailureRetainsAuthorityAndRetries(t *testing.T) {
	for _, kind := range []string{"enospc", "uncertain", "overflow", "marker"} {
		t.Run(kind, func(t *testing.T) {
			s, _, meta, _ := claudeLegacyCleanupFixture(t, 9, true)
			before := snapshot(t, s)
			if kind == "overflow" {
				s.mu.Lock()
				err := s.locked(func() error {
					r, err := s.load()
					if err != nil {
						return err
					}
					r.Revision = ^uint64(0)
					return s.save(r)
				})
				s.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				before = snapshot(t, s)
			}
			state, err := openClaudeCustodyState(s.StateRoot())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = state.Close() }()
			calls := 0
			markerFailure := errors.New("marker rename failed")
			s.mu.Lock()
			err = s.locked(func() error {
				r, err := s.load()
				if err != nil {
					return err
				}
				bytes := int64(0)
				m, err := readClaudeMeta(state, s.StateRoot(), "source", &bytes)
				if err != nil {
					return err
				}
				switch kind {
				case "enospc":
					s.writeOps.rename = func(string, string) error { return syscall.ENOSPC }
				case "uncertain":
					s.writeOps.syncDir = func(string) error { return syscall.EIO }
				}
				return s.retireClaudeReservation(state, &r, m, nil, func(ClaudeReservationRetirement) error { calls++; return markerFailure })
			})
			s.mu.Unlock()
			s.writeOps = writeOps{}
			if err == nil {
				t.Fatal("failed promotion/marker authorized cleanup")
			}
			if kind == "enospc" && !errors.Is(err, syscall.ENOSPC) || kind == "uncertain" && !errors.Is(err, ErrDurabilityUncertain) || kind == "marker" && err != markerFailure {
				t.Fatalf("wrong failure: %v", err)
			}
			if kind != "marker" && calls != 0 {
				t.Fatal("callback ran before successful registry durability")
			}
			got := snapshot(t, s)
			if kind == "enospc" || kind == "overflow" {
				if !reflect.DeepEqual(got, before) {
					t.Fatal("refused floor change mutated registry")
				}
			} else {
				want := before
				want.SchemaVersion = 10
				want.Revision++
				if !reflect.DeepEqual(got, want) {
					t.Fatal("promotion changed unrelated state")
				}
			}
			raw, err := os.ReadFile(filepath.Join(s.StateRoot(), "source", "meta.json"))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte("claude_retirement")) {
				t.Fatal("failed marker appeared durable")
			}
			if kind == "overflow" {
				return
			}
			if err := WithClaudeReservationRetirement(s.StateRoot(), "source", nil, func(marker ClaudeReservationRetirement) error {
				meta["schema_version"], meta["claude_retirement"] = 5, marker
				writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", meta)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			final := snapshot(t, s)
			want := before
			want.SchemaVersion = 10
			want.Revision++
			if !reflect.DeepEqual(final, want) {
				t.Fatal("retry changed selection or advanced promotion twice")
			}
		})
	}
}

func TestClaudeLegacyPromotionOrdersCleanupAgainstSelectAndStaleSave(t *testing.T) {
	for _, floor := range []int{1, 9} {
		t.Run(fmt.Sprint(floor), func(t *testing.T) {
			s, a, meta, _ := claudeLegacyCleanupFixture(t, floor, true)
			before := snapshot(t, s)
			oldRaw, _ := json.Marshal(meta)
			other, err := OpenExisting(s.StateRoot())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = other.Close() }()
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- WithClaudeReservationRetirement(s.StateRoot(), "source", nil, func(marker ClaudeReservationRetirement) error {
					if marker == (ClaudeReservationRetirement{}) {
						return errors.New("zero marker bypassed promotion")
					}
					close(entered)
					<-release
					meta["schema_version"], meta["claude_retirement"] = 5, marker
					writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", meta)
					return nil
				})
			}()
			<-entered
			promoted := snapshot(t, s)
			if promoted.SchemaVersion != 10 || promoted.Revision != before.Revision+1 || promoted.Providers[ProviderClaude] != before.Providers[ProviderClaude] {
				t.Fatal("cleanup callback lacks durable unchanged selection")
			}
			if err := syscall.Flock(int(other.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
				if err == nil {
					_ = syscall.Flock(int(other.lock.Fd()), syscall.LOCK_UN)
				}
				t.Fatal("cleanup callback escaped registry fence")
			}
			selected, saved := make(chan error, 1), make(chan error, 1)
			go func() { _, err := other.SelectClaudeProfile(before.Revision, a.ID); selected <- err }()
			go func() {
				saved <- WithClaudeMetadataSave(s.StateRoot(), "source", oldRaw, func() error { return errors.New("stale metadata callback ran") })
			}()
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if err := <-selected; !errors.Is(err, ErrConflict) {
				t.Fatalf("promotion did not invalidate stale Select CAS: %v", err)
			}
			if err := <-saved; !errors.Is(err, ErrConflict) {
				t.Fatalf("stale Save crossed terminal promotion: %v", err)
			}
		})
	}
}

func TestClaudeLegacyPromotionLeavesOtherProviderAndUnmanagedRowsUnchanged(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprint(managed), func(t *testing.T) {
			state := t.TempDir()
			var s *Store
			if managed {
				s = testStore(t)
				state = s.StateRoot()
			}
			meta := map[string]any{"schema_version": 4, "id": "other", "agent_type": "codex", "cwd": "/work", "login_runtime": Runtime{Provider: ProviderCodex, Epoch: 1, Identity: strings.Repeat("a", 64), Home: "/unrelated/native"}}
			raw := writeClaudeCustodyJSON(t, state, "other", "meta.json", meta)
			var before Registry
			if s != nil {
				before = snapshot(t, s)
			}
			if err := WithClaudeReservationRetirement(state, "other", nil, func(marker ClaudeReservationRetirement) error {
				if marker != (ClaudeReservationRetirement{}) {
					t.Fatal("other provider promoted to Claude")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := WithClaudeMetadataSave(state, "other", raw, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			if s != nil && !reflect.DeepEqual(snapshot(t, s), before) {
				t.Fatal("other provider rewrote registry")
			}
		})
	}
}

func TestClaudeLegacyMissingStorageAliasRetainsExactCommonHome(t *testing.T) {
	s, _, _, _ := claudeLegacyCleanupFixture(t, 1, false)
	state := s.StateRoot()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(state, "logins")); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(state, alias); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := WithClaudeReservationRetirement(alias, "source", nil, func(ClaudeReservationRetirement) error { called = true; return nil }); !errors.Is(err, ErrPending) || called {
		t.Fatalf("alias lost managed common-home routing: %v %v", called, err)
	}
	if _, err := os.Lstat(filepath.Join(state, "logins")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retirement bootstrapped absent login storage: %v", err)
	}
}

// Leave the exact post-rename registry state visible to a fresh Store handle.
func claudeUnsettledRegistryTest(t *testing.T, s *Store) Registry {
	t.Helper()
	s.mu.Lock()
	err := s.locked(func() error {
		r, err := s.load()
		if err != nil {
			return err
		}
		r.SchemaVersion = 9
		if err := s.save(r); err != nil {
			return err
		}
		r.SchemaVersion = 10
		r.Revision++
		s.writeOps.syncDir = func(string) error { return syscall.EIO }
		return s.save(r)
	})
	s.mu.Unlock()
	s.writeOps = writeOps{}
	if !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("fixture did not expose renamed/unsettled registry: %v", err)
	}
	visible := snapshot(t, s)
	if visible.SchemaVersion != 10 {
		t.Fatal("promotion rename not visible")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return visible
}

func claudeFreshStoreTest(t *testing.T, state string) *Store {
	t.Helper()
	s, err := OpenExisting(state)
	if err != nil {
		t.Fatal(err)
	}
	s.keychain = nil
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestClaudeFreshHandleRetirementMustSettleVisibleRegistryBeforeMarker(t *testing.T) {
	s, _, meta, _ := claudeLegacyCleanupFixture(t, 9, true)
	stateRoot := s.StateRoot()
	visible := claudeUnsettledRegistryTest(t, s)
	fresh := claudeFreshStoreTest(t, stateRoot)
	fresh.writeOps.syncDir = func(string) error { return syscall.EIO }
	calls := 0
	state, err := openClaudeCustodyState(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	fresh.mu.Lock()
	err = fresh.locked(func() error {
		r, err := fresh.load()
		if err != nil {
			return err
		}
		bytes := int64(0)
		m, err := readClaudeMeta(state, stateRoot, "source", &bytes)
		if err != nil {
			return err
		}
		return fresh.retireClaudeReservation(state, &r, m, nil, func(ClaudeReservationRetirement) error { calls++; return nil })
	})
	fresh.mu.Unlock()
	if !errors.Is(err, ErrDurabilityUncertain) || calls != 0 {
		t.Fatalf("fresh handle consumed unsettled registry: %d %v", calls, err)
	}
	if !reflect.DeepEqual(snapshot(t, fresh), visible) {
		t.Fatal("failed settlement changed registry")
	}
	fresh = claudeFreshStoreTest(t, stateRoot)
	if err := WithClaudeReservationRetirement(stateRoot, "source", nil, func(marker ClaudeReservationRetirement) error {
		calls++
		meta["schema_version"], meta["claude_retirement"] = 5, marker
		writeClaudeCustodyJSON(t, stateRoot, "source", "meta.json", meta)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !reflect.DeepEqual(snapshot(t, fresh), visible) {
		t.Fatal("successful settlement rewrote revision or repeated marker")
	}
}

func TestClaudeFreshHandleReloginMustSettleBeforeIntentAndRecoveryApply(t *testing.T) {
	for _, kind := range []string{"intent", "recovery"} {
		t.Run(kind, func(t *testing.T) {
			s, a, _, native := claudeCustodyFixture(t)
			auth, err := s.claudeAuth(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			s.keychain = nil
			if err := os.WriteFile(filepath.Join(s.savedNativeHome(a.ID), ".credentials.json"), auth, 0600); err != nil {
				t.Fatal(err)
			}
			job := strings.Repeat("a", 32)
			replacement := claudeCredential("a", "replacement")
			if kind == "recovery" {
				if _, _, err := s.ReloginJob(snapshot(t, s).Revision, a.ID, replacement, job); !errors.Is(err, ErrPending) {
					t.Fatal(err)
				}
				writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeProcessFile, native)
				writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeStoppedFile, NativeStoppedInfo{SchemaVersion: 4, Native: native, WritersStopped: true, DescendantsContained: true})
			}
			stateRoot := s.StateRoot()
			visible := claudeUnsettledRegistryTest(t, s)
			authPath := filepath.Join(s.savedNativeHome(a.ID), ".credentials.json")
			beforeAuth, err := os.ReadFile(authPath)
			if err != nil {
				t.Fatal(err)
			}
			credentialPath := filepath.Join(s.savedNativeHome(a.ID), "credential.json")
			beforeMeta, err := os.ReadFile(credentialPath)
			if err != nil {
				t.Fatal(err)
			}
			fresh := claudeFreshStoreTest(t, stateRoot)
			fresh.writeOps.syncDir = func(dir string) error {
				if dir == "." {
					return syscall.EIO
				}
				return nil
			}
			if kind == "intent" {
				_, _, err = fresh.ReloginJob(visible.Revision, a.ID, replacement, job)
			} else {
				err = fresh.Reconcile()
			}
			if !errors.Is(err, ErrDurabilityUncertain) {
				t.Fatalf("fresh relogin consumed unsettled registry: %v", err)
			}
			afterAuth, _ := os.ReadFile(authPath)
			afterMeta, _ := os.ReadFile(credentialPath)
			if !bytes.Equal(beforeAuth, afterAuth) || !bytes.Equal(beforeMeta, afterMeta) || !reflect.DeepEqual(snapshot(t, fresh), visible) {
				t.Fatal("failed settlement mutated authority")
			}
			_, intentErr := os.Stat(filepath.Join(stateRoot, "logins", reloginPath(a.ID)))
			if kind == "intent" && !errors.Is(intentErr, os.ErrNotExist) {
				t.Fatal("intent published before registry settlement")
			}
			if kind == "recovery" && intentErr != nil {
				t.Fatal("failed settlement erased pending recovery")
			}
			fresh = claudeFreshStoreTest(t, stateRoot)
			if kind == "intent" {
				if _, _, err := fresh.ReloginJob(visible.Revision, a.ID, replacement, job); !errors.Is(err, ErrPending) {
					t.Fatal(err)
				}
				if _, err := fresh.readRelogin(a); err != nil {
					t.Fatal("settled intent not retained")
				}
				if !reflect.DeepEqual(snapshot(t, fresh), visible) {
					t.Fatal("settlement alone advanced revision")
				}
			} else {
				if err := fresh.Reconcile(); err != nil {
					t.Fatal(err)
				}
				r := snapshot(t, fresh)
				if r.Revision != visible.Revision+1 || find(&r, a.ID).CredentialRevision != a.CredentialRevision+1 {
					t.Fatal("recovery failed to apply exactly once")
				}
				if err := fresh.Reconcile(); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(snapshot(t, fresh), r) {
					t.Fatal("recovery repeated effect")
				}
			}
		})
	}
}

func TestClaudeFreshHandleMetadataSaveSettlesOnlyManagedClaude(t *testing.T) {
	s, _, meta, _ := claudeCustodyFixture(t)
	stateRoot := s.StateRoot()
	visible := claudeUnsettledRegistryTest(t, s)
	meta["name"] = "one update"
	raw, _ := json.Marshal(meta)
	state, err := openClaudeCustodyState(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	fresh := claudeFreshStoreTest(t, stateRoot)
	fresh.writeOps.syncDir = func(string) error { return syscall.EIO }
	calls := 0
	fresh.mu.Lock()
	err = fresh.locked(func() error {
		r, err := fresh.load()
		if err != nil {
			return err
		}
		return fresh.saveClaudeMetadata(state, &r, stateRoot, "source", raw, func() error { calls++; return nil })
	})
	fresh.mu.Unlock()
	if !errors.Is(err, ErrDurabilityUncertain) || calls != 0 {
		t.Fatalf("managed metadata consumed unsettled registry: %d %v", calls, err)
	}
	fresh = claudeFreshStoreTest(t, stateRoot)
	if err := WithClaudeMetadataSave(stateRoot, "source", raw, func() error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !reflect.DeepEqual(snapshot(t, fresh), visible) {
		t.Fatal("metadata settlement rewrote revision or repeated callback")
	}
	fresh.writeOps.syncDir = func(string) error { return syscall.EIO }
	plain := []byte(`{"schema_version":1,"id":"plain","agent_type":"codex"}`)
	fresh.mu.Lock()
	err = fresh.locked(func() error {
		r, err := fresh.load()
		if err != nil {
			return err
		}
		return fresh.saveClaudeMetadata(state, &r, stateRoot, "plain", plain, func() error { calls++; return nil })
	})
	fresh.mu.Unlock()
	if err != nil || calls != 2 {
		t.Fatalf("unrelated metadata acquired Claude sync requirement: %d %v", calls, err)
	}
}

func TestClaudeSuccessfulPromotionSettlesRegistryOnceBeforeMarker(t *testing.T) {
	s, _, _, _ := claudeLegacyCleanupFixture(t, 9, true)
	state, err := openClaudeCustodyState(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	syncs, calls := 0, 0
	s.writeOps.syncDir = func(dir string) error {
		if dir == "." {
			syncs++
		}
		return syncRootDir(s.root, dir)
	}
	s.mu.Lock()
	err = s.locked(func() error {
		r, err := s.load()
		if err != nil {
			return err
		}
		bytes := int64(0)
		m, err := readClaudeMeta(state, s.StateRoot(), "source", &bytes)
		if err != nil {
			return err
		}
		return s.retireClaudeReservation(state, &r, m, nil, func(ClaudeReservationRetirement) error {
			calls++
			if syncs != 1 {
				t.Fatalf("same-call successful promotion synced registry %d times", syncs)
			}
			return nil
		})
	})
	s.mu.Unlock()
	if err != nil || calls != 1 || syncs != 1 {
		t.Fatalf("promotion settlement: callbacks=%d syncs=%d err=%v", calls, syncs, err)
	}
}
