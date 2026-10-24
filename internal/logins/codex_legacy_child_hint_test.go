package logins

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCodexTransferRetainsUnprovedLegacyChildWithoutBlockingHealthyCohort(t *testing.T) {
	s, a, _, op, child := transferFixture(t)
	healthy := handoverNative("healthy", "healthy-native")
	r, h, err := s.TransferCodexMigration(snapshot(t, s).Revision, op.ID, a.ID, []NativeMember{healthy}, nil)
	if err != nil {
		t.Fatalf("unproved old child blocked healthy cohort: %v", err)
	}
	raw, _ := json.Marshal(h.Bootstrap[0])
	if r.SchemaVersion != 5 || !strings.Contains(string(raw), `"legacy_child_id":"child"`) || h.Bootstrap[0].ChildID != "" || h.Bootstrap[0].Phase != "ambiguous" || h.Bootstrap[0].Launch != nil || len(h.Members) != 1 || h.Members[0].Native != healthy {
		t.Fatal("hint became authority or lost live cohort")
	}
	if _, _, err = s.ClaimCodexBootstrapLaunch(r.Revision, h.ID, "source", strings.Repeat("e", 32)); !errors.Is(err, ErrPending) {
		t.Fatal("hint authorized a new launch")
	}
	if _, _, err = s.RecordCodexBootstrapOutcome(r.Revision, h.ID, "source", "", "cancelled"); !errors.Is(err, ErrPending) {
		t.Fatal("hint authorized cancellation")
	}
	if _, err = s.CompleteCodexHandover(r.Revision, h.ID); !errors.Is(err, ErrPending) {
		t.Fatal("hint disappeared at completion")
	}
	root := s.StateRoot()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	r = snapshot(t, reopened)
	h = r.Handovers[ProviderCodex]
	wrong := child
	wrong.SessionID = "different-child"
	if _, _, err = reopened.RecordCodexBootstrapChild(r.Revision, h.ID, "source", "", wrong); !errors.Is(err, ErrConflict) {
		t.Fatal("hint bound another child")
	}
	r, h, err = reopened.RecordCodexBootstrapChild(r.Revision, h.ID, "source", "", child)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(h.Bootstrap[0])
	if strings.Contains(string(raw), "legacy_child_id") || h.Bootstrap[0].ChildID != child.SessionID || h.Bootstrap[0].Launch != nil || r.SchemaVersion != 5 {
		t.Fatal("binding retained hint, invented launch, or downgraded schema")
	}
}

func TestCodexTransferLegacyChildHintSchemaAndShapeFence(t *testing.T) {
	s, a, _, op, _ := transferFixture(t)
	r, h, err := s.TransferCodexMigration(snapshot(t, s).Revision, op.ID, a.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"old_schema", "pending", "bound", "launch", "invalid_hint", "source_hint", "duplicate_hint"} {
		t.Run(scenario, func(t *testing.T) {
			raw, _ := json.Marshal(r)
			var shape map[string]any
			_ = json.Unmarshal(raw, &shape)
			handover := shape["handovers"].(map[string]any)[ProviderCodex].(map[string]any)
			rows := handover["bootstrap"].([]any)
			row := rows[0].(map[string]any)
			switch scenario {
			case "old_schema":
				shape["schema_version"] = 4
			case "pending":
				row["phase"] = "pending"
			case "bound":
				row["phase"] = "bound"
			case "launch":
				row["launch"] = map[string]any{"id": strings.Repeat("a", 32), "to_id": h.ToID, "epoch": h.Epoch, "destination_credential_revision": h.DestinationCredentialRevision}
			case "invalid_hint":
				row["legacy_child_id"] = strings.Repeat("x", 257)
			case "source_hint":
				row["legacy_child_id"] = row["source_id"]
			case "duplicate_hint":
				rows[1].(map[string]any)["legacy_child_id"] = row["legacy_child_id"]
			}
			raw, _ = json.Marshal(shape)
			var altered Registry
			_ = json.Unmarshal(raw, &altered)
			if validateRegistry(altered) == nil {
				t.Fatalf("unsafe hint shape accepted: %s", scenario)
			}
		})
	}
	// Replacing an unrelated owned grant must preserve the new reader fence.
	c := add(t, s, ProviderCodex, "C", Credential{Auth: grantAuth("u-c", "c", time.Now().Add(time.Hour).Unix())})
	lease, err := s.AcquireCodexRelogin(c.ID, strings.Repeat("f", 32))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	r, _, err = lease.ReplaceCredential(snapshot(t, s).Revision, Credential{Auth: grantAuth("u-c", "c", time.Now().Add(2*time.Hour).Unix())}, strings.Repeat("f", 32))
	if err != nil || r.SchemaVersion != 5 {
		t.Fatalf("grant replacement lost schema floor: %v", err)
	}
}

type codexBootstrapDeleteAuthorizer interface {
	AuthorizeCodexBootstrapDelete(uint64, string, string, string, NativeMember) (Registry, Handover, error)
}

func TestCodexBootstrapDeleteAuthorizationIsExactDurableAndSchemaFenced(t *testing.T) {
	s, a, _, op, native := transferFixture(t)
	authorizer, ok := any(s).(codexBootstrapDeleteAuthorizer)
	if !ok {
		t.Fatal("missing durable bootstrap deletion authority")
	}
	r, h, err := s.TransferCodexMigration(snapshot(t, s).Revision, op.ID, a.ID, []NativeMember{native}, []BootstrapChild{{SourceID: "source", Native: native, Phase: "pending"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = authorizer.AuthorizeCodexBootstrapDelete(r.Revision, h.ID, "source", "", native); err == nil {
		t.Fatal("unsettled live member authorized deletion")
	}
	r, h, err = s.RecordCodexReceipt(r.Revision, handoverReceipt(h, native, "retired"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = authorizer.AuthorizeCodexBootstrapDelete(r.Revision, h.ID, "source", "", native); err == nil {
		t.Fatal("retirement alone authorized deletion")
	}
	r, h, err = s.RecordCodexBootstrapOutcome(r.Revision, h.ID, "source", "", "cancelled")
	if err != nil {
		t.Fatal(err)
	}
	wrong := native
	wrong.NativeStartTime++
	if _, _, err = authorizer.AuthorizeCodexBootstrapDelete(r.Revision, h.ID, "source", "", wrong); err == nil {
		t.Fatal("wrong native tuple authorized deletion")
	}
	if _, _, err = authorizer.AuthorizeCodexBootstrapDelete(r.Revision, h.ID, "source", strings.Repeat("a", 32), native); err == nil {
		t.Fatal("wrong claim authorized deletion")
	}
	r, h, err = authorizer.AuthorizeCodexBootstrapDelete(r.Revision, h.ID, "source", "", native)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(h.Bootstrap[0])
	if r.SchemaVersion != 6 || !strings.Contains(string(raw), `"owner_delete_authorized":true`) {
		t.Fatal("delete authority lacks old-reader fence")
	}
	for _, scenario := range []string{"old_schema", "unbound", "unsettled", "not_retired", "wrong_incarnation"} {
		t.Run(scenario, func(t *testing.T) {
			raw, _ := json.Marshal(r)
			var shape map[string]any
			_ = json.Unmarshal(raw, &shape)
			handover := shape["handovers"].(map[string]any)[ProviderCodex].(map[string]any)
			row := handover["bootstrap"].([]any)[0].(map[string]any)
			switch scenario {
			case "old_schema":
				shape["schema_version"] = 5
			case "unbound":
				delete(row, "child_id")
				delete(row, "child_incarnation")
			case "unsettled":
				row["phase"] = "bound"
			case "not_retired":
				handover["members"].([]any)[0].(map[string]any)["phase"] = "pending"
			case "wrong_incarnation":
				row["child_incarnation"] = "foreign"
			}
			raw, _ = json.Marshal(shape)
			var invalid Registry
			_ = json.Unmarshal(raw, &invalid)
			if validateRegistry(invalid) == nil {
				t.Fatal("invalid delete authority accepted")
			}
		})
	}
	root := s.StateRoot()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	r = snapshot(t, reopened)
	h = r.Handovers[ProviderCodex]
	c := add(t, reopened, ProviderCodex, "C", Credential{Auth: grantAuth("u-c", "c", time.Now().Add(time.Hour).Unix())})
	r, h, err = reopened.RetargetCodexHandover(snapshot(t, reopened).Revision, h.ID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(h.Bootstrap[0])
	if r.SchemaVersion != 6 || !strings.Contains(string(raw), `"owner_delete_authorized":true`) {
		t.Fatal("reopen/retarget lost exact deletion authority")
	}
}
