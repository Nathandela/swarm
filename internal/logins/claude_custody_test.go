package logins

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

func TestClaudeOperationalCustodyReadsWithoutRegistryFlockAndRetainsErrors(t *testing.T) {
	s, _, _, _ := claudeCustodyFixture(t)
	s.mu.Lock()
	err := s.locked(func() error {
		yes, err := ClaudeOperationalCustody(s.StateRoot())
		if err != nil || !yes {
			t.Fatalf("readonly fence failed: %v %v", yes, err)
		}
		return nil
	})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.StateRoot(), "logins", "registry.json"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if yes, err := ClaudeOperationalCustody(s.StateRoot()); yes || err == nil {
		t.Fatalf("corrupt fence became absence: %v %v", yes, err)
	}
	if yes, err := ClaudeOperationalCustody(t.TempDir()); yes || err != nil {
		t.Fatalf("absent login registry changed: %v %v", yes, err)
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
