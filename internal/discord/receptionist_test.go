package discord

import (
	"context"
	"errors"
	"github.com/Nathandela/swarm/internal/adapter"
	"strings"
	"testing"
	"time"
)

func TestReceptionistSelectsCheapModelAndClaimsOnce(t *testing.T) {
	b, w := controlsRig(t)
	oldProbe, oldPath := receptionistModels, receptionistLookPath
	t.Cleanup(func() { receptionistModels = oldProbe; receptionistLookPath = oldPath })
	receptionistModels = func(string) (string, []adapter.ModelChoice) {
		return "expensive", []adapter.ModelChoice{{ID: "gpt-luna", Display: "Luna"}, {ID: "expensive"}}
	}
	receptionistLookPath = func(string) (string, error) { return "/mock/codex", nil }
	b.Service.Config.ReceptionistCLI = "codex"
	input := sampleRequest()
	input.Target.ID = "routing"
	r := mustAdmit(t, b.Service.Store, input)
	b.Service.Now = func() time.Time { return testNow }
	for range 2 {
		ok, e := b.receptionist(context.Background(), r)
		if e != nil || !ok {
			t.Fatal(ok, e)
		}
	}
	if len(w.launches) != 1 || w.launches[0].Options["model"] != "gpt-luna" || w.launches[0].Env != nil || !strings.Contains(w.launches[0].InitialPrompt, r.Ref) || !strings.Contains(w.launches[0].InitialPrompt, "propose") {
		t.Fatal(w.launches)
	}
}
func TestReceptionistUnavailableNeverFallsBackToExpensive(t *testing.T) {
	b, w := controlsRig(t)
	oldProbe, oldPath := receptionistModels, receptionistLookPath
	t.Cleanup(func() { receptionistModels = oldProbe; receptionistLookPath = oldPath })
	receptionistModels = func(string) (string, []adapter.ModelChoice) {
		return "expensive", []adapter.ModelChoice{{ID: "expensive"}}
	}
	receptionistLookPath = func(string) (string, error) { return "", errors.New("absent") }
	b.Service.Config.ReceptionistCLI = "codex"
	ok, e := b.receptionist(context.Background(), Request{})
	if e != nil || ok || len(w.launches) != 0 {
		t.Fatal(ok, e, w.launches)
	}
}
