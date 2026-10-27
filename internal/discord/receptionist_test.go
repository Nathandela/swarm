package discord

import (
	"context"
	"errors"
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/bwmarrin/discordgo"
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
	input := sampleRequest()
	input.Target.ID = "routing"
	r := mustAdmit(t, b.Service.Store, input)
	b.Service.Now = func() time.Time { return testNow }
	ok, e := b.receptionist(context.Background(), r)
	if e != nil || ok || len(w.launches) != 0 {
		t.Fatal(ok, e, w.launches)
	}
	receptionistLookPath = func(string) (string, error) { return "/mock/codex", nil }
	ok, e = b.receptionist(context.Background(), r)
	if e != nil || ok || len(w.launches) != 0 {
		t.Fatal("configured expensive model used as fallback", ok, e)
	}
}

func TestReceptionistBypassesDirectTargetAndRejectsInvalidModel(t *testing.T) {
	b, w := controlsRig(t)
	oldProbe, oldPath := receptionistModels, receptionistLookPath
	t.Cleanup(func() { receptionistModels = oldProbe; receptionistLookPath = oldPath })
	calls := 0
	receptionistModels = func(string) (string, []adapter.ModelChoice) { calls++; return "", nil }
	receptionistLookPath = func(string) (string, error) { return "/mock/codex", nil }
	b.Service.Config.ReceptionistCLI = "codex"
	if handled, e := b.receptionist(context.Background(), sampleRequest()); handled || e != nil || calls != 0 || len(w.launches) != 0 {
		t.Fatal("direct target started receptionist", handled, e, calls)
	}
	input := sampleRequest()
	input.Target.ID = "routing"
	r := mustAdmit(t, b.Service.Store, input)
	b.Service.Now = func() time.Time { return testNow }
	b.Service.Config.ReceptionistModel = "--unexpected"
	if handled, e := b.receptionist(context.Background(), r); !errors.Is(e, ErrInvalid) || handled || len(w.launches) != 0 {
		t.Fatal("invalid model launched", handled, e)
	}
}

func TestReceptionistReusesOneTrustedRoleForLaterQuestions(t *testing.T) {
	b, w := controlsRig(t)
	old := receptionistLookPath
	t.Cleanup(func() { receptionistLookPath = old })
	receptionistLookPath = func(string) (string, error) { return "/mock/codex", nil }
	b.Service.Config.ReceptionistCLI = "codex"
	b.Service.Config.ReceptionistModel = "luna"
	b.Service.Now = func() time.Time { return testNow }
	input := sampleRequest()
	input.Target.ID = "routing"
	first := mustAdmit(t, b.Service.Store, input)
	if handled, e := b.receptionist(context.Background(), first); e != nil || !handled {
		t.Fatal(handled, e)
	}
	w.rows = append(w.rows, Worker{Target: Target{ID: "local/new", Instance: "real-role-instance", Name: "Completely renamed", Agent: "codex"}, State: "running"})
	input.EventID = "second"
	input.Route.MessageID = "second"
	input.Text = "Another question"
	second := mustAdmit(t, b.Service.Store, input)
	for range 2 {
		if handled, e := b.receptionist(context.Background(), second); e != nil || !handled {
			t.Fatal(handled, e)
		}
	}
	if len(w.launches) != 1 || len(w.sends) != 1 || w.sends[0].Session != "local/new" || w.sends[0].SessionInstance != "real-role-instance" || !strings.Contains(w.sends[0].Text, second.Ref) || w.sends[0].Origin.RequesterID != second.Author.ID {
		t.Fatal("receptionist process not reused exactly", w.launches, w.sends)
	}
	input.EventID = "third"
	third := mustAdmit(t, b.Service.Store, input)
	w.rows[len(w.rows)-1].Instance = "replaced-instance"
	if _, e := b.receptionist(context.Background(), third); e == nil || len(w.sends) != 1 || len(w.launches) != 1 {
		t.Fatal("stale role silently rerouted", e)
	}
}
func TestReceptionistUnknownLaunchNeverReplaced(t *testing.T) {
	b, w := controlsRig(t)
	old := receptionistLookPath
	t.Cleanup(func() { receptionistLookPath = old })
	receptionistLookPath = func(string) (string, error) { return "/mock/codex", nil }
	b.Service.Config.ReceptionistCLI = "codex"
	b.Service.Config.ReceptionistModel = "luna"
	b.Service.Now = func() time.Time { return testNow }
	w.launchErr = errors.New("lost launch ack")
	input := sampleRequest()
	input.Target.ID = "routing"
	for _, id := range []string{"one", "two"} {
		input.EventID = id
		r := mustAdmit(t, b.Service.Store, input)
		if _, e := b.receptionist(context.Background(), r); e == nil {
			t.Fatal("unknown role launch hidden")
		}
	}
	if len(w.launches) != 1 || len(w.sends) != 0 {
		t.Fatal("unknown role replaced", w.launches, w.sends)
	}
}

func TestDirectoryAndRecommendationsHaveIndependentPublicationIDs(t *testing.T) {
	b, w := controlsRig(t)
	r := mustAdmit(t, b.Service.Store, sampleRequest())
	b.Service.Now = func() time.Time { return testNow }
	w.rows = append(w.rows, Worker{Target: Target{ID: "local/two", Instance: "two", Name: "Second", Agent: "codex"}, State: "running"})
	if e := b.Service.Directory(context.Background(), r.Ref, []string{w.rows[0].ID, w.rows[1].ID}); e != nil {
		t.Fatal(e)
	}
	if e := b.Service.Propose(context.Background(), r.Ref, []string{w.rows[1].ID}); e != nil {
		t.Fatal("recommendation conflicted with directory", e)
	}
	if len(b.Service.Messenger.(*fakeMessenger).posts) != 2 {
		t.Fatal("missing independent picker/proposal")
	}
}
func TestReceptionistResetRequiresConfirmedStoppedRole(t *testing.T) {
	b, w := controlsRig(t)
	old := receptionistLookPath
	t.Cleanup(func() { receptionistLookPath = old })
	receptionistLookPath = func(string) (string, error) { return "/mock/codex", nil }
	b.Service.Config.ReceptionistCLI = "codex"
	b.Service.Config.ReceptionistModel = "luna"
	b.Service.Now = func() time.Time { return testNow }
	input := sampleRequest()
	input.Target.ID = "routing"
	r := mustAdmit(t, b.Service.Store, input)
	if _, e := b.receptionist(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	row := Worker{Target: Target{ID: "local/new", Instance: "role-instance", Agent: "codex"}, State: "lost"}
	if e := b.resetReceptionist(context.Background(), row); e == nil {
		t.Fatal("lost role blindly replaced")
	}
	row.State = "exited"
	if e := b.resetReceptionist(context.Background(), row); e != nil {
		t.Fatal(e)
	}
	input.EventID = "after-reset"
	next := mustAdmit(t, b.Service.Store, input)
	if _, e := b.receptionist(context.Background(), next); e != nil || len(w.launches) != 2 {
		t.Fatal("explicit generation not recreated", e, len(w.launches))
	}
}

func TestContextDirectoryDoesNotWaitForReceptionistLaunch(t *testing.T) {
	b, w := controlsRig(t)
	old := receptionistLookPath
	t.Cleanup(func() { receptionistLookPath = old })
	receptionistLookPath = func(string) (string, error) { return "/mock/codex", nil }
	b.Service.Config.ReceptionistCLI = "codex"
	b.Service.Config.ReceptionistModel = "luna"
	w.rows = append(w.rows, Worker{Target: Target{ID: "local/two", Instance: "two", Agent: "codex"}, State: "running"})
	gate := make(chan struct{})
	w.launchGate = gate
	t.Cleanup(func() { close(gate); b.Wait() })
	i := ownerInteraction("directory-now", "2")
	i.Data = discordgo.ApplicationCommandInteractionData{Name: "Ask Swarm", TargetID: "source", Resolved: &discordgo.ApplicationCommandInteractionDataResolved{Messages: map[string]*discordgo.Message{"source": {ID: "source", ChannelID: "20", Content: "Who should investigate?", Author: &discordgo.User{ID: "2"}}}}}
	done := make(chan error, 1)
	go func() {
		_, e := b.contextCommand(context.Background(), i, Actor{ID: "2"}, Route{GuildID: "10", ChannelID: "20"})
		done <- e
	}()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("directory waited on model launch")
	}
	if len(b.Service.Messenger.(*fakeMessenger).posts) != 1 {
		t.Fatal("directory unavailable while model blocked")
	}
}
