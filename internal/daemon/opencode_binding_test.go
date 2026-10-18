package daemon

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func openCodeMeta() persist.Meta {
	return persist.Meta{ID: "native", AgentType: "opencode", Cwd: "/project", ConversationID: "ses_" + strings.Repeat("a", 32), RequiredRuntime: persist.RequiredRuntimeOpenCode,
		OpenCodeBinding: &persist.OpenCodeBinding{Revision: 1, Mode: persist.OpenCodeModeCreate, Phase: persist.OpenCodePhasePlanned, Generation: strings.Repeat("b", 32), ProfileEnv: []string{"XDG_DATA_HOME=/native"},
			Initial: &persist.OpenCodeInitial{OperationID: strings.Repeat("c", 32), RequestHash: strings.Repeat("d", 64), Phase: persist.OpenCodeInitialPrepared}}}
}

func TestOpenCodeBindingSnapshotsAndCASOwnTheirMemory(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	original := openCodeMeta()
	if err := d.saveMeta(original); err != nil {
		t.Fatal(err)
	}
	original.OpenCodeBinding.ProfileEnv[0] = "XDG_DATA_HOME=/mutated"
	original.OpenCodeBinding.Initial.OperationID = "mutated"
	for _, get := range []func() persist.Meta{func() persist.Meta { m, _ := d.Get("native"); return m }, func() persist.Meta { return d.List()[0] }} {
		m := get()
		if m.OpenCodeBinding.ProfileEnv[0] != "XDG_DATA_HOME=/native" || m.OpenCodeBinding.Initial.OperationID != strings.Repeat("c", 32) {
			t.Fatal("stored binding aliases input")
		}
		m.OpenCodeBinding.ProfileEnv[0] = "XDG_DATA_HOME=/changed"
		m.OpenCodeBinding.Initial.OperationID = "changed"
	}
	m, _ := d.Get("native")
	next := *m.OpenCodeBinding
	next.Revision, next.Phase, next.NativeCreatedAtMS = 2, persist.OpenCodePhaseBound, 123
	if err := d.SetOpenCodeBinding("native", 1, next); err != nil {
		t.Fatal(err)
	}
	next.ProfileEnv[0] = "XDG_DATA_HOME=/alias"
	next.Initial.OperationID = "alias"
	got, _ := d.Get("native")
	if got.OpenCodeBinding.ProfileEnv[0] != "XDG_DATA_HOME=/native" || got.OpenCodeBinding.Initial.OperationID != strings.Repeat("c", 32) {
		t.Fatal("CAS retained caller pointers")
	}
	if err := d.SetOpenCodeBinding("native", 1, *got.OpenCodeBinding); err == nil {
		t.Fatal("stale revision accepted")
	}
	bad := *got.OpenCodeBinding
	bad.Revision = 3
	bad.Generation = strings.Repeat("e", 32)
	if err := d.SetOpenCodeBinding("native", 2, bad); err == nil {
		t.Fatal("generation changed under live binding")
	}
	bad = *got.OpenCodeBinding
	bad.Revision = 3
	bad.NativeCreatedAtMS++
	if err := d.SetOpenCodeBinding("native", 2, bad); err == nil {
		t.Fatal("native creation timestamp changed")
	}
	bad = *got.OpenCodeBinding
	bad.Revision = 3
	bad.Phase = persist.OpenCodePhasePlanned
	if err := d.SetOpenCodeBinding("native", 2, bad); err == nil {
		t.Fatal("binding phase regressed")
	}
}

func TestOpenCodeStatusFencesGenerationAndClosedDaemon(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	m := openCodeMeta()
	m.Status = status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle}
	if err := d.saveMeta(m); err != nil {
		t.Fatal(err)
	}
	active := status.Status{Process: status.ProcessRunning, Turn: status.TurnActive}
	if err := d.SetOpenCodeStatus(m.ID, strings.Repeat("e", 32), active); err == nil {
		t.Fatal("stale generation wrote status")
	}
	got, _ := d.Get(m.ID)
	if got.Status != m.Status {
		t.Fatal("stale status mutation")
	}
	if err := d.SetOpenCodeStatus(m.ID, m.OpenCodeBinding.Generation, active); err != nil {
		t.Fatal(err)
	}
	got, _ = d.Get(m.ID)
	if got.Status.Turn != active.Turn {
		t.Fatal("current generation not published")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.SetOpenCodeStatus(m.ID, m.OpenCodeBinding.Generation, m.Status); err == nil {
		t.Fatal("closed daemon wrote status")
	}
}
