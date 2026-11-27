package voice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

type activationHoldRuntime struct {
	*discussionTestRuntime
	rows    []protocol.SessionView
	launche int
}

func (r *activationHoldRuntime) List() ([]protocol.SessionView, error) {
	return append([]protocol.SessionView(nil), r.rows...), nil
}

func (r *activationHoldRuntime) LaunchOperation(operationID string, req protocol.LaunchReq) (string, string, error) {
	r.launche++
	return "", "", errors.New("unexpected replacement launch while activation is held by direct input")
}

func (r *activationHoldRuntime) LaunchOperationStatus(operationID string) (schema.OperationOutcomeView, error) {
	return schema.OperationOutcomeView{}, errors.New("unexpected launch status poll for applied wake")
}

func (r *activationHoldRuntime) ManagerRuntime(session, instance string) (protocol.ManagerRuntimeStatus, error) {
	if r.managerErr != nil {
		return protocol.ManagerRuntimeStatus{}, r.managerErr
	}
	return r.discussionTestRuntime.ManagerRuntime(session, instance)
}

func TestDiscussionParkedWakeDirectInputHoldPreservesQueueAndStatus(t *testing.T) {
	_, store, fixture, oldBinding := newControllerFixture(t)
	dir := filepath.Dir(store.db.Path())
	workDir := t.TempDir()
	cfg := Config{
		Version:        2,
		PlaceID:        "place",
		VoiceID:        "marin",
		Language:       "en",
		Mode:           "conversation",
		DaemonSocket:   filepath.Join(dir, "daemon.sock"),
		ConversationID: oldBinding.ConversationID,
		Provider:       oldBinding.Provider,
		WorkDir:        workDir,
		Name:           "Office-Swarm",
	}
	retireReq := protocol.ManagerRetireReq{
		Binding: runtimeBinding(oldBinding),
		ExpectedTurn: protocol.ManagerNativeTurn{
			State: "idle",
		},
	}
	retirementID := protocol.ManagerRetireOperationID(retireReq)
	if err := store.update(func(st *State) error {
		c := st.Conversations[oldBinding.ConversationID]
		c.Parked = true
		c.Retirement = &retireReq
		c.Project = cfg.WorkDir
		putRecord(st, "conversations", &st.Conversations, oldBinding.ConversationID, c)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	childSession := "ep/child-session"
	childInstance := "child-instance-1"
	refs := ManagerBridgeRefs{Socket: fixture.status.BridgeSocket, AuthFile: filepath.Join(dir, "bridge.auth")}
	wakeReq := managerLaunchRequest(cfg, refs)
	wakeReq.Options[protocol.OptionResumeFrom] = oldBinding.SessionID
	wakeReq.ResumeSourceInstance = oldBinding.SessionInstance
	wakeReq.Manager.RetireOperationID = retirementID
	wakeFile := "resume-" + hashBytes([]byte(retirementID)) + ".json"
	if err := saveManagerIntent(dir, wakeFile, managerLaunchIntent{
		Version:        1,
		ConversationID: cfg.ConversationID,
		OperationID:    "launch:1795769590460:5abbdb23774b40d1498fef3e83509a98",
		Request:        wakeReq,
		Phase:          "applied",
		SessionID:      childSession,
	}); err != nil {
		t.Fatal(err)
	}

	runtime := &activationHoldRuntime{
		discussionTestRuntime: &discussionTestRuntime{
			controllerRuntimeFixture: fixture,
			managerErr:               managerproof.ErrDirectInputUnresolved,
		},
		rows: []protocol.SessionView{
			{
				ID:                    childSession,
				EndpointID:            "ep",
				Role:                  "manager",
				ManagerConversationID: cfg.ConversationID,
				Agent:                 cfg.Provider,
				Cwd:                   cfg.WorkDir,
				Status:                status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone},
				Capabilities:          &protocol.SessionCapabilities{SessionInstance: childInstance},
			},
		},
	}
	scope := DiscussionScope{
		InstallationID: "install",
		DiscussionID:   "discord-discussion",
		ThreadID:       "thread",
		DMOwnerID:      "owner",
	}
	newController := func(s *Store) *DiscussionController {
		d := &DiscussionController{
			store:    s,
			runtime:  runtime,
			scope:    scope,
			cfg:      cfg,
			endpoint: "ep",
			now:      time.Now,
			ctx:      t.Context(),
			dir:      dir,
			bridge:   &privateHTTP{socket: fixture.status.BridgeSocket},
			handler:  newManagerHandler(cfg.ConversationID, "fixture"),
		}
		t.Cleanup(d.stopFollowing)
		return d
	}

	d := newController(store)
	for _, id := range []string{"first", "second", "third"} {
		origin := schema.MessageOrigin{
			Kind:           "owner_direct",
			InstallationID: "install",
			DiscussionID:   "discord-discussion",
			ChannelID:      "thread",
			RequestID:      id,
			MessageID:      id,
			ActorID:        "owner",
			RequesterID:    "owner",
			Revision:       "current",
		}
		if err := d.Admit(t.Context(), DiscussionInput{Origin: origin, Text: "message " + id}); err != nil {
			t.Fatalf("admit %s: %v", id, err)
		}
	}

	checkHold := func(dc *DiscussionController) {
		t.Helper()
		err := dc.Sync(t.Context(), nil)
		var hold *DiscussionInputHold
		if !errors.As(err, &hold) {
			t.Fatalf("expected *DiscussionInputHold, got %T: %v", err, err)
		}
		if !hold.Activation {
			t.Fatalf("expected Activation=true on hold: %+v", hold)
		}
		if !errors.Is(err, ErrDiscussionDirectInputHeld) {
			t.Fatalf("expected ErrDiscussionDirectInputHeld, got %v", err)
		}
		if hold.AdmissionID != "first" {
			t.Fatalf("expected hold on first queued admission, got %q", hold.AdmissionID)
		}
	}

	checkHold(d)
	checkHold(d)

	// Verify diagnostic status exposes both parked binding and attempted child coordinates.
	stView, err := d.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if stView.State != "parked" || stView.ManagerSession != oldBinding.SessionID || stView.ManagerInstance != oldBinding.SessionInstance {
		t.Fatalf("parked binding changed unexpectedly: %+v", stView)
	}
	if stView.PendingManagerSession != childSession || stView.PendingManagerInstance != childInstance {
		t.Fatalf("expected pending child %s/%s, got %q/%q", childSession, childInstance, stView.PendingManagerSession, stView.PendingManagerInstance)
	}

	// Simulate connector restart by closing and reopening voice.db.
	storeDir := filepath.Dir(store.db.Path())
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	d2 := newController(reopened)
	checkHold(d2)

	snap, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture.sent) != 0 || runtime.launche != 0 || len(runtime.retirements) != 0 {
		t.Fatalf("expected zero sends/launches/retirements while held: sent=%d launches=%d retire=%d", len(fixture.sent), runtime.launche, len(runtime.retirements))
	}
	var ids []string
	for _, a := range snap.Admissions {
		if a.ConversationID == cfg.ConversationID && a.SettledAt.IsZero() && a.Origin != nil {
			ids = append(ids, a.Origin.RequestID)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 unsettled admissions preserved, got %v", ids)
	}
}

func TestDiscussionFirstLaunchDirectInputReturnsStartupNotice(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	convID := DiscussionConversationPrefix + "first-launch"
	workDir := t.TempDir()
	cfg := Config{
		Version:        2,
		PlaceID:        "place",
		VoiceID:        "marin",
		Language:       "en",
		Mode:           "conversation",
		DaemonSocket:   filepath.Join(dir, "daemon.sock"),
		ConversationID: convID,
		Provider:       "codex",
		WorkDir:        workDir,
		Name:           "Office-Swarm",
	}
	refs := ManagerBridgeRefs{
		Socket:   filepath.Join(dir, "bridge.sock"),
		AuthFile: filepath.Join(dir, "bridge.auth"),
	}
	req := managerLaunchRequest(cfg, refs)
	childSession := "ep/first-child"
	childInstance := "first-inst"
	if err := saveManagerIntent(dir, managerIntentName(convID), managerLaunchIntent{
		Version:        1,
		ConversationID: convID,
		OperationID:    "launch:1795769590460:firstlaunch",
		Request:        req,
		Phase:          "applied",
		SessionID:      childSession,
	}); err != nil {
		t.Fatal(err)
	}

	runtime := &activationHoldRuntime{
		discussionTestRuntime: &discussionTestRuntime{
			controllerRuntimeFixture: &controllerRuntimeFixture{},
			managerErr:               managerproof.ErrDirectInputUnresolved,
		},
		rows: []protocol.SessionView{
			{
				ID:                    childSession,
				EndpointID:            "ep",
				Role:                  "manager",
				ManagerConversationID: convID,
				Agent:                 cfg.Provider,
				Cwd:                   cfg.WorkDir,
				Status:                status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone},
				Capabilities:          &protocol.SessionCapabilities{SessionInstance: childInstance},
			},
		},
	}
	d := &DiscussionController{
		store:    store,
		runtime:  runtime,
		scope:    DiscussionScope{InstallationID: "install", DiscussionID: "discord-discussion", ThreadID: "thread", DMOwnerID: "owner"},
		cfg:      cfg,
		endpoint: "ep",
		now:      time.Now,
		ctx:      t.Context(),
		dir:      dir,
		bridge:   &privateHTTP{socket: refs.Socket},
		handler:  newManagerHandler(convID, "fixture"),
	}
	t.Cleanup(d.stopFollowing)

	origin := schema.MessageOrigin{
		Kind:           "owner_direct",
		InstallationID: "install",
		DiscussionID:   "discord-discussion",
		ChannelID:      "thread",
		RequestID:      "first",
		MessageID:      "first",
		ActorID:        "owner",
		RequesterID:    "owner",
		Revision:       "current",
	}
	err = d.Admit(context.Background(), DiscussionInput{Origin: origin, Text: "hello"})
	if !errors.Is(err, ErrDiscussionActivationUnavailable) || !errors.Is(err, ErrDiscussionDirectInputHeld) {
		t.Fatalf("expected activation unavailable + direct-input hold on first launch, got %v", err)
	}
	msg := DiscussionStartupMessage(err)
	if !strings.Contains(strings.ToLower(msg), "direct terminal input") {
		t.Fatalf("expected direct-input startup wording, got %q", msg)
	}
	if strings.Contains(strings.ToLower(msg), "exit normally") {
		t.Fatalf("startup wording must not advise normal exit: %q", msg)
	}

	stView, err := d.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if stView.State != "starting" || stView.PendingManagerSession != childSession || stView.PendingManagerInstance != childInstance {
		raw, _ := json.Marshal(stView)
		t.Fatalf("expected starting state with pending child coordinates, got %s", raw)
	}
}

func TestDiscussionParkedWakePreservesManualInterventionUntilResumeManual(t *testing.T) {
	_, store, fixture, oldBinding := newControllerFixture(t)
	dir := filepath.Dir(store.db.Path())
	workDir := t.TempDir()
	cfg := Config{
		Version:        2,
		PlaceID:        "place",
		VoiceID:        "marin",
		Language:       "en",
		Mode:           "conversation",
		DaemonSocket:   filepath.Join(dir, "daemon.sock"),
		ConversationID: oldBinding.ConversationID,
		Provider:       oldBinding.Provider,
		WorkDir:        workDir,
		Name:           "Office-Swarm",
	}
	retireReq := protocol.ManagerRetireReq{
		Binding:      runtimeBinding(oldBinding),
		ExpectedTurn: protocol.ManagerNativeTurn{State: "idle"},
	}
	retirementID := protocol.ManagerRetireOperationID(retireReq)
	if err := store.update(func(st *State) error {
		c := st.Conversations[oldBinding.ConversationID]
		c.Parked = true
		c.Retirement = &retireReq
		c.Project = workDir
		putRecord(st, "conversations", &st.Conversations, oldBinding.ConversationID, c)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	childSession := "ep/child-manual"
	childInstance := "child-manual-inst"
	refs := ManagerBridgeRefs{Socket: fixture.status.BridgeSocket, AuthFile: filepath.Join(dir, "bridge.auth")}
	wakeReq := managerLaunchRequest(cfg, refs)
	wakeReq.Options[protocol.OptionResumeFrom] = oldBinding.SessionID
	wakeReq.ResumeSourceInstance = oldBinding.SessionInstance
	wakeReq.Manager.RetireOperationID = retirementID
	wakeFile := "resume-" + hashBytes([]byte(retirementID)) + ".json"
	if err := saveManagerIntent(dir, wakeFile, managerLaunchIntent{
		Version:        1,
		ConversationID: cfg.ConversationID,
		OperationID:    "launch:1795769590460:manualwake",
		Request:        wakeReq,
		Phase:          "applied",
		SessionID:      childSession,
	}); err != nil {
		t.Fatal(err)
	}

	fixture.status.Binding.Session = childSession
	fixture.status.Binding.SessionInstance = childInstance
	fixture.status.Binding.ObservationGeneration = "obs-child"
	fixture.status.AuthFile = refs.AuthFile
	fixture.status.ReadyForInput = true
	fixture.status.Turn = protocol.ManagerNativeTurn{
		State:             "completed",
		ExternalInput:     1,
		ExternallySteered: true,
	}

	runtime := &activationHoldRuntime{
		discussionTestRuntime: &discussionTestRuntime{controllerRuntimeFixture: fixture},
		rows: []protocol.SessionView{
			{
				ID:                    childSession,
				EndpointID:            "ep",
				Role:                  "manager",
				ManagerConversationID: cfg.ConversationID,
				Agent:                 cfg.Provider,
				Cwd:                   cfg.WorkDir,
				Status:                status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone},
				Capabilities:          &protocol.SessionCapabilities{SessionInstance: childInstance},
			},
		},
	}
	d := &DiscussionController{
		store:    store,
		runtime:  runtime,
		scope:    DiscussionScope{InstallationID: "install", DiscussionID: "discord-discussion", ThreadID: "thread", DMOwnerID: "owner"},
		cfg:      cfg,
		endpoint: "ep",
		now:      time.Now,
		ctx:      t.Context(),
		dir:      dir,
		bridge:   &privateHTTP{socket: refs.Socket},
		handler:  newManagerHandler(cfg.ConversationID, "fixture"),
	}
	t.Cleanup(d.stopFollowing)

	origin := schema.MessageOrigin{
		Kind: "owner_direct", InstallationID: "install", DiscussionID: "discord-discussion",
		ChannelID: "thread", RequestID: "first", MessageID: "first", ActorID: "owner", RequesterID: "owner", Revision: "current",
	}
	if err := d.Admit(t.Context(), DiscussionInput{Origin: origin, Text: "queued msg"}); err != nil {
		t.Fatal(err)
	}
	err := d.Sync(t.Context(), nil)
	var hold *DiscussionInputHold
	if !errors.As(err, &hold) || !errors.Is(err, ErrDiscussionDirectInputHeld) || hold.Activation {
		t.Fatalf("expected post-bind manual pause hold, got %T: %v", err, err)
	}
	if len(fixture.sent) != 0 {
		t.Fatalf("queued message was sent before resume-manual: %d sends", len(fixture.sent))
	}
	if err := d.ResumeManual(t.Context(), childSession, childInstance, 1); err != nil {
		t.Fatalf("ResumeManual on newly bound child: %v", err)
	}
}

func TestDiscussionReplacementChildDirectInputDoesNotTriggerCooldown(t *testing.T) {
	_, store, fixture, oldBinding := newControllerFixture(t)
	dir := filepath.Dir(store.db.Path())
	workDir := t.TempDir()
	cfg := Config{
		Version: 2, PlaceID: "place", VoiceID: "marin", Language: "en", Mode: "conversation",
		DaemonSocket: filepath.Join(dir, "daemon.sock"), ConversationID: oldBinding.ConversationID,
		Provider: oldBinding.Provider, WorkDir: workDir, Name: "Office-Swarm",
	}
	retireReq := protocol.ManagerRetireReq{
		Binding:      runtimeBinding(oldBinding),
		Replacement:  schema.ManagerReplaceCustody,
		ExpectedTurn: protocol.ManagerNativeTurn{State: "unknown"},
	}
	retirementID := protocol.ManagerRetireOperationID(retireReq)
	if err := store.update(func(st *State) error {
		c := st.Conversations[oldBinding.ConversationID]
		c.Parked = true
		c.Retirement = &retireReq
		c.Project = workDir
		putRecord(st, "conversations", &st.Conversations, oldBinding.ConversationID, c)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	childSession := "ep/child-repl"
	refs := ManagerBridgeRefs{Socket: fixture.status.BridgeSocket, AuthFile: filepath.Join(dir, "bridge.auth")}
	wakeReq := managerLaunchRequest(cfg, refs)
	wakeFile := "reconstruct-" + hashBytes([]byte(retirementID)) + ".json"
	if err := saveManagerIntent(dir, wakeFile, managerLaunchIntent{
		Version: 1, ConversationID: cfg.ConversationID, OperationID: "launch:1795769590460:repl",
		Request: wakeReq, Phase: "applied", SessionID: childSession,
	}); err != nil {
		t.Fatal(err)
	}
	runtime := &activationHoldRuntime{
		discussionTestRuntime: &discussionTestRuntime{controllerRuntimeFixture: fixture, managerErr: managerproof.ErrDirectInputUnresolved},
		rows: []protocol.SessionView{{
			ID: childSession, EndpointID: "ep", Role: "manager", ManagerConversationID: cfg.ConversationID,
			Agent: cfg.Provider, Cwd: workDir, Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone},
			Capabilities: &protocol.SessionCapabilities{SessionInstance: "repl-inst"},
		}},
	}
	d := &DiscussionController{
		store: store, runtime: runtime,
		scope: DiscussionScope{InstallationID: "install", DiscussionID: "discord-discussion", ThreadID: "thread", DMOwnerID: "owner"},
		cfg: cfg, endpoint: "ep", now: time.Now, ctx: t.Context(), dir: dir,
		bridge: &privateHTTP{socket: refs.Socket}, handler: newManagerHandler(cfg.ConversationID, "fixture"),
	}
	t.Cleanup(d.stopFollowing)
	origin := schema.MessageOrigin{
		Kind: "owner_direct", InstallationID: "install", DiscussionID: "discord-discussion",
		ChannelID: "thread", RequestID: "first", MessageID: "first", ActorID: "owner", RequesterID: "owner", Revision: "current",
	}
	if err := d.Admit(t.Context(), DiscussionInput{Origin: origin, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		err := d.Sync(t.Context(), nil)
		var hold *DiscussionInputHold
		if !errors.As(err, &hold) || !hold.Activation || !errors.Is(err, ErrDiscussionDirectInputHeld) {
			t.Fatalf("sync %d: expected activation hold without cooldown, got %v", i, err)
		}
	}
	snap, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Conversations[cfg.ConversationID].ReplacementAfter != 0 {
		t.Fatalf("expected ReplacementAfter=0, got %d", snap.Conversations[cfg.ConversationID].ReplacementAfter)
	}
}

func TestDiscussionActivationHoldPreservesUncertainAndRejectsCorruptStatus(t *testing.T) {
	_, store, fixture, oldBinding := newControllerFixture(t)
	dir := filepath.Dir(store.db.Path())
	workDir := t.TempDir()
	cfg := Config{
		Version: 2, PlaceID: "place", VoiceID: "marin", Language: "en", Mode: "conversation",
		DaemonSocket: filepath.Join(dir, "daemon.sock"), ConversationID: oldBinding.ConversationID,
		Provider: oldBinding.Provider, WorkDir: workDir, Name: "Office-Swarm",
	}
	retireReq := protocol.ManagerRetireReq{Binding: runtimeBinding(oldBinding), ExpectedTurn: protocol.ManagerNativeTurn{State: "idle"}}
	retirementID := protocol.ManagerRetireOperationID(retireReq)
	if err := store.update(func(st *State) error {
		c := st.Conversations[oldBinding.ConversationID]
		c.Parked = true
		c.Retirement = &retireReq
		c.Project = workDir
		putRecord(st, "conversations", &st.Conversations, oldBinding.ConversationID, c)
		a, err := allocateAdmission(st, oldBinding, "owner")
		if err != nil {
			return err
		}
		a.Origin = &schema.MessageOrigin{
			Kind: "owner_direct", InstallationID: "install", DiscussionID: "discord-discussion",
			ChannelID: "thread", RequestID: "uncertain-req", MessageID: "uncertain-req", ActorID: "owner", RequesterID: "owner", Revision: "current",
		}
		a.Phase = "uncertain"
		putRecord(st, "admissions", &st.Admissions, admissionKey(oldBinding.ConversationID, a.Sequence), a)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wakeFile := "resume-" + hashBytes([]byte(retirementID)) + ".json"
	// Corrupt intent (empty operation_id) must NOT be corroborated by Status.
	if err := saveManagerIntent(dir, wakeFile, managerLaunchIntent{
		Version: 1, ConversationID: cfg.ConversationID, OperationID: "",
		Phase: "applied", SessionID: "ep/bad-child",
	}); err != nil {
		t.Fatal(err)
	}
	runtime := &activationHoldRuntime{
		discussionTestRuntime: &discussionTestRuntime{controllerRuntimeFixture: fixture, managerErr: managerproof.ErrDirectInputUnresolved},
		rows: []protocol.SessionView{{
			ID: "ep/bad-child", EndpointID: "ep", Role: "manager", ManagerConversationID: cfg.ConversationID,
			Agent: cfg.Provider, Cwd: workDir, Status: status.Status{Process: status.ProcessRunning},
			Capabilities: &protocol.SessionCapabilities{SessionInstance: "bad-inst"},
		}},
	}
	d := &DiscussionController{
		store: store, runtime: runtime,
		scope: DiscussionScope{InstallationID: "install", DiscussionID: "discord-discussion", ThreadID: "thread", DMOwnerID: "owner"},
		cfg: cfg, endpoint: "ep", now: time.Now, ctx: t.Context(), dir: dir,
		bridge: &privateHTTP{socket: fixture.status.BridgeSocket}, handler: newManagerHandler(cfg.ConversationID, "fixture"),
	}
	t.Cleanup(d.stopFollowing)

	stView, err := d.Status()
	if err != nil {
		t.Fatal(err)
	}
	if stView.PendingManagerSession != "" || stView.PendingManagerInstance != "" {
		t.Fatalf("status corroborated invalid intent: %+v", stView)
	}

	// Verify activationDirectInputHold preserves BOTH ErrDiscussionDirectInputHeld and ErrDiscussionEffectUncertain.
	holdErr := d.activationDirectInputHold(managerproof.ErrDirectInputUnresolved)
	var hold *DiscussionInputHold
	if !errors.As(holdErr, &hold) || !hold.Activation || !errors.Is(holdErr, ErrDiscussionDirectInputHeld) || !errors.Is(holdErr, ErrDiscussionEffectUncertain) {
		t.Fatalf("expected activation + direct-input + effect-uncertain hold, got %v", holdErr)
	}
}

type blockingListRuntime struct {
	*activationHoldRuntime
	block <-chan struct{}
}

func (b *blockingListRuntime) List() ([]protocol.SessionView, error) {
	<-b.block
	return nil, errors.New("cancelled")
}

func TestDiscussionStatusBoundsStalledRosterLookup(t *testing.T) {
	_, store, fixture, oldBinding := newControllerFixture(t)
	dir := filepath.Dir(store.db.Path())
	workDir := t.TempDir()
	cfg := Config{
		Version: 2, PlaceID: "place", VoiceID: "marin", Language: "en", Mode: "conversation",
		DaemonSocket: filepath.Join(dir, "daemon.sock"), ConversationID: oldBinding.ConversationID,
		Provider: oldBinding.Provider, WorkDir: workDir, Name: "Office-Swarm",
	}
	retireReq := protocol.ManagerRetireReq{Binding: runtimeBinding(oldBinding), ExpectedTurn: protocol.ManagerNativeTurn{State: "idle"}}
	retirementID := protocol.ManagerRetireOperationID(retireReq)
	if err := store.update(func(st *State) error {
		c := st.Conversations[oldBinding.ConversationID]
		c.Parked = true
		c.Retirement = &retireReq
		c.Project = workDir
		putRecord(st, "conversations", &st.Conversations, oldBinding.ConversationID, c)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	refs := ManagerBridgeRefs{Socket: fixture.status.BridgeSocket, AuthFile: filepath.Join(dir, "bridge.auth")}
	wakeReq := managerLaunchRequest(cfg, refs)
	wakeReq.Options[protocol.OptionResumeFrom] = oldBinding.SessionID
	wakeReq.ResumeSourceInstance = oldBinding.SessionInstance
	wakeReq.Manager.RetireOperationID = retirementID
	wakeFile := "resume-" + hashBytes([]byte(retirementID)) + ".json"
	if err := saveManagerIntent(dir, wakeFile, managerLaunchIntent{
		Version: 1, ConversationID: cfg.ConversationID, OperationID: "launch:1795769590460:stalled",
		Request: wakeReq, Phase: "applied", SessionID: "ep/stalled-child",
	}); err != nil {
		t.Fatal(err)
	}
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	d := &DiscussionController{
		store: store,
		runtime: &blockingListRuntime{
			activationHoldRuntime: &activationHoldRuntime{discussionTestRuntime: &discussionTestRuntime{controllerRuntimeFixture: fixture}},
			block:                 unblock,
		},
		cfg: cfg, endpoint: "ep", now: time.Now, ctx: t.Context(), dir: dir,
		bridge: &privateHTTP{socket: refs.Socket}, handler: newManagerHandler(cfg.ConversationID, "fixture"),
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	stView, err := d.StatusWithContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("StatusWithContext was not bounded: took %v", time.Since(start))
	}
	if stView.PendingManagerSession != "ep/stalled-child" || stView.PendingManagerInstance != "" {
		t.Fatalf("expected durable session ID without instance on timeout, got %+v", stView)
	}
	// CheckoutStatus must skip roster corroboration completely and return immediately.
	chkStart := time.Now()
	chkView, err := d.CheckoutStatus()
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(chkStart) > 200*time.Millisecond || chkView.PendingManagerSession != "ep/stalled-child" || chkView.PendingManagerInstance != "" {
		t.Fatalf("CheckoutStatus blocked or returned wrong pending coordinates (%v): %+v", time.Since(chkStart), chkView)
	}
}

func TestDiscussionGuildActivationHoldBehindWorkerWake(t *testing.T) {
	_, store, fixture, b := newControllerFixture(t)
	if err := store.update(func(st *State) error {
		a, err := allocateAdmission(st, b, "wake:worker")
		if err != nil {
			return err
		}
		w := Wake{ID: "worker", ConversationID: b.ConversationID, State: "admitted", Admission: a.Sequence}
		putRecord(st, "wakes", &st.Wakes, key(b.ConversationID, w.ID), w)
		human, err := allocateAdmission(st, b, "human")
		if err != nil {
			return err
		}
		human.Origin = &schema.MessageOrigin{
			Kind: "external", InstallationID: "install", DiscussionID: "discord-discussion",
			GuildID: "10", ChannelID: "201", RequestID: "guild-msg", MessageID: "guild-msg",
			ActorID: "user-1", RequesterID: "user-1", Revision: "current",
		}
		putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, human.Sequence), human)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	d := &DiscussionController{
		store: store,
		scope: DiscussionScope{InstallationID: "install", DiscussionID: "discord-discussion", GuildID: "10", ThreadID: "201"},
		cfg:   Config{ConversationID: b.ConversationID},
		now:   time.Now,
		ctx:   t.Context(),
		dir:   filepath.Dir(fixture.status.AuthFile),
	}
	err := d.activationDirectInputHold(managerproof.ErrDirectInputUnresolved)
	var hold *DiscussionInputHold
	if !errors.As(err, &hold) || !hold.Activation || !errors.Is(err, ErrDiscussionDirectInputHeld) || hold.AdmissionID != "guild-msg" {
		t.Fatalf("expected guild human activation hold behind worker wake, got %v", err)
	}
}
