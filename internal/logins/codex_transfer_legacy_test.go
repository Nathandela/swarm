package logins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Persist the former native-owned schema-two migration layout, not the current
// CommitSwitch output, which already knows the desired credential revision.
func persistedZeroBaselineMigration(t *testing.T, phase string) (*Store, Login, Switch, []NativeMember, []BootstrapChild) {
	t.Helper()
	s, a := persistedSchemaTwoOwner(t)
	r := snapshot(t, s)
	op := Switch{OwnershipMigration: true, ID: strings.Repeat("c", 32), Provider: ProviderCodex, ToID: a.ID, FromEpoch: 0, ToEpoch: r.Providers[ProviderCodex].Epoch, Phase: phase, AttemptedIDs: []string{a.ID}, Sessions: []Restart{{SourceID: "unbound", ConversationID: "thread-late", Phase: "stopped"}}}
	members := []NativeMember{handoverNative("other", "native-other")}
	var bindings []BootstrapChild
	if phase == "resuming" {
		child := handoverNative("child", "native-child")
		op.Sessions = append(op.Sessions, Restart{SourceID: "source", ConversationID: child.ConversationID, ChildID: child.SessionID, Phase: "launched"})
		members = append(members, child)
		bindings = []BootstrapChild{{SourceID: "source", Native: child, Phase: "pending"}}
	}
	r.Switches[ProviderCodex] = op
	raw, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(s.path, "registry.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	state := s.StateRoot()
	scan := s.grantWriters
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(state)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopened.grantWriters = scan
	loaded := snapshot(t, reopened)
	if loaded.SchemaVersion != 2 || loaded.Providers[ProviderCodex].DesiredCredentialRevision != 0 || loaded.Switches[ProviderCodex].Phase != phase || len(loaded.Logins) != 1 {
		t.Fatal("fixture did not persist exact legacy zero-baseline migration")
	}
	return reopened, a, op, members, bindings
}
func TestCodexPersistedZeroBaselineMigrationTransfersFreshSelection(t *testing.T) {
	for _, phase := range []string{"committed", "resuming"} {
		t.Run(phase, func(t *testing.T) {
			s, a, op, members, bindings := persistedZeroBaselineMigration(t, phase)
			before := snapshot(t, s)
			if _, e := s.CommitSwitch(op.ID); !errors.Is(e, ErrPending) {
				t.Fatal("legacy zero baseline was silently equated with current revision")
			}
			r, h, e := s.TransferCodexMigration(before.Revision, op.ID, a.ID, members, bindings)
			if e != nil {
				t.Fatal(e)
			}
			if r.SchemaVersion != 4 || h.Version != 2 || len(r.Switches) != 0 || h.Epoch != op.ToEpoch+1 || h.DestinationCredentialRevision != a.CredentialRevision || r.Providers[ProviderCodex].DesiredCredentialRevision != a.CredentialRevision {
				t.Fatal("explicit legacy transfer did not create fresh revision-bound selection")
			}
			if !reflect.DeepEqual(h.HeldLoginIDs, []string{a.ID}) || h.MigrationSwitchID != op.ID || len(h.Members) != len(members) {
				t.Fatal("legacy transfer lost held grant or native cohort")
			}
			for _, m := range h.Members {
				if m.Phase != "pending" || m.AdoptedID != "" || m.AdoptedEpoch != 0 || m.AdoptedCredentialRevision != 0 {
					t.Fatal("zero baseline fabricated prior adoption")
				}
			}
			want := "pending"
			if phase == "resuming" {
				want = "ambiguous"
				if h.Bootstrap[1].ChildID != "child" || h.Bootstrap[1].ChildIncarnation != "native-child" {
					t.Fatal("legacy child lineage lost")
				}
			}
			if h.Bootstrap[0].SourceID != "unbound" || h.Bootstrap[0].ConversationID != "thread-late" || h.Bootstrap[0].Phase != want {
				t.Fatal("legacy source launch state reinterpreted")
			}
			if _, e = s.Remove(r.Revision, a.ID); !errors.Is(e, ErrInUse) {
				t.Fatal("legacy held authority released")
			}
			if _, _, e = s.TransferCodexMigration(r.Revision, op.ID, a.ID, members, bindings); !errors.Is(e, ErrPending) {
				t.Fatal("legacy migration consumed twice")
			}
			if snapshot(t, s).Providers[ProviderCodex].Epoch != h.Epoch {
				t.Fatal("duplicate legacy transfer advanced epoch")
			}
			if phase == "resuming" {
				if _, _, e = s.ClaimCodexBootstrapLaunch(r.Revision, h.ID, "unbound", strings.Repeat("d", 32)); !errors.Is(e, ErrPending) {
					t.Fatal("legacy resuming ambiguity authorized replay")
				}
			} else if _, _, e = s.ClaimCodexBootstrapLaunch(r.Revision, h.ID, "unbound", strings.Repeat("d", 32)); e != nil {
				t.Fatal(e)
			}
		})
	}
}
