package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/voice/openailive"
)

type serviceRuntimeFixture struct {
	*bootstrapFixture
	*controllerRuntimeFixture
	proofErr      error
	proofPending  bool
	rosterPending bool
}

func (s *serviceRuntimeFixture) List() ([]protocol.SessionView, error) {
	rows, err := s.bootstrapFixture.List()
	if len(rows) != 0 && s.rosterPending {
		s.rosterPending = false
		rows[0].Capabilities = nil
	}
	return rows, err
}

func (s *serviceRuntimeFixture) ManagerRuntime(session, instance string) (protocol.ManagerRuntimeStatus, error) {
	s.mu.Lock()
	if s.proofPending {
		s.proofPending = false
		s.mu.Unlock()
		return protocol.ManagerRuntimeStatus{}, managerproof.ErrUnavailable
	}
	s.mu.Unlock()
	if s.proofErr != nil {
		return protocol.ManagerRuntimeStatus{}, s.proofErr
	}
	return s.controllerRuntimeFixture.ManagerRuntime(session, instance)
}

func (s *serviceRuntimeFixture) ResolveManagerCall(context.Context, protocol.ManagerCallReq) (protocol.ManagerCallProof, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proof, nil
}

func TestManagerServiceAdmissionAndSingleton(t *testing.T) {
	dir := privateServerDir(t)
	cfg := testConfig()
	refs := ManagerBridgeRefs{Socket: filepath.Join(dir, "bridge.sock"), AuthFile: filepath.Join(dir, "bridge.auth")}
	native := bootstrapNativeFixture(cfg, refs)
	native.runtime.Turn.State = "idle"
	entered := make(chan protocol.ComposerSendReq, 2)
	controller := &controllerRuntimeFixture{status: native.runtime, receipts: map[string]protocol.SemanticMessageReceipt{}}
	controller.beforeSend = func(_ string, req protocol.ComposerSendReq) { entered <- req }
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	runtime := &serviceRuntimeFixture{bootstrapFixture: native, controllerRuntimeFixture: controller, proofPending: true, rosterPending: true}
	spoken := make(chan string, 2)
	openPreview := func(context.Context, string) (previewSession, error) {
		return &previewFixture{spoken: spoken, events: []openailive.Event{
			{Type: "session.commentary.appended", ClientEventID: "unrelated-greeting"},
			{Type: "session.output_audio.delta", Audio: []byte{8, 9}},
			{Type: "session.commentary.appended", ClientEventID: "fixture-greeting"},
			{Type: "session.output_transcript.delta", Text: "Never turn caption into a second request"},
			{Type: "session.output_audio.delta", Audio: []byte{1, 2, 3, 4}},
			{Type: "session.closed"},
		}}, nil
	}
	start := func() chan error {
		done := make(chan error, 1)
		go func() { defer close(done); done <- serveManager(ctx, dir, "endpoint", cfg, runtime, nil, openPreview) }()
		return done
	}
	done := start()
	defer func() { stop(); <-done }()
	client := NewManagerClient(dir)
	defer client.Close()
	wait := func() {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			select {
			case err := <-done:
				t.Fatalf("service exited: %v", err)
			default:
			}
			if state, err := client.Status(ctx); err == nil && state.Ready {
				return
			}
		}
		t.Fatal("controller never became ready")
	}
	wait()
	for _, path := range []string{"bridge.sock", "control.sock"} {
		if err := validateManagerSocket(filepath.Join(dir, path)); err != nil {
			t.Fatalf("invalid private socket %s: %v", path, err)
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://voice-control/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "https://outside.invalid")
	response, err := client.http.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("browser origin was admitted")
	}
	if err := serveManager(ctx, dir, "endpoint", cfg, runtime, nil, nil); err == nil {
		t.Fatal("second service acquired the singleton")
	}
	input, err := client.SubmitText(ctx, "same-id", "exact text")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := client.SubmitText(ctx, "same-id", "exact text")
	if err != nil || retry.Sequence != input.Sequence {
		t.Fatalf("repeat = %v, %v", retry, err)
	}
	if _, err := client.SubmitText(ctx, "same-id", "revised text"); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict = %v", err)
	}
	receipt, err := client.Receipt(ctx, "same-id")
	if err != nil || receipt.Sequence != input.Sequence {
		t.Fatalf("receipt = %v, %v", receipt, err)
	}
	var sent protocol.ComposerSendReq
	select {
	case sent = <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("native admission never dispatched")
	}
	if sent.Text != "exact text" || sent.ManagerAdmission.Binding.Session != "endpoint/manager" {
		t.Fatalf("wrong native target or text: %+v", sent)
	}
	submitted := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if receipt, err = client.Receipt(ctx, "same-id"); err == nil && receipt.Phase == "submitted" {
			submitted = true
			break
		}
	}
	if !submitted {
		t.Fatalf("missing native submitted receipt: %+v, %v", receipt, err)
	}
	nonce := sent.ManagerAdmission.Nonce
	controller.mu.Lock()
	controller.status.ReadyForInput = false
	controller.status.Turn = protocol.ManagerNativeTurn{State: "running", NativeInputID: nonce, NativeTurnID: "native-turn"}
	controller.proof = protocol.ManagerCallProof{Main: true, NativeInputID: nonce, NativeTurnID: "native-turn", NativeCallID: "native-call"}
	controller.mu.Unlock()
	bridge, err := NewManagerBridgeClient(refs.Socket, refs.AuthFile, cfg.ConversationID, "manager")
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	for _, attempt := range []struct {
		client *http.Client
		url    string
		auth   string
		want   int
	}{
		{bridge.http, "http://voice/v1/status", "", http.StatusNotFound},
		{client.http, "http://voice-control/manager/call", "", http.StatusNotFound},
		{client.http, "http://voice-control/v1/status", "Bearer fixture", http.StatusForbidden},
	} {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, attempt.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if attempt.auth != "" {
			request.Header.Set("Authorization", attempt.auth)
		}
		response, err := attempt.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != attempt.want {
			t.Fatalf("%s HTTP status = %d, want %d", attempt.url, response.StatusCode, attempt.want)
		}
	}
	const phrases = 34
	const shortPhrase = "Canonical manager text."
	for sequence := 1; sequence <= phrases; sequence++ {
		text := strings.Repeat("x", 4096)
		if sequence == 1 {
			text = shortPhrase
		}
		args, err := json.Marshal(map[string]any{"output_id": fmt.Sprintf("answer-%d", sequence), "sequence": sequence, "text": text, "final": sequence == phrases})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bridge.Call(ctx, ManagerToolCall{Name: "publish", Arguments: json.RawMessage(args)}); err != nil {
			t.Fatal(err)
		}
	}
	output, err := client.Publications(ctx, "same-id")
	if err != nil || len(output) != phrases {
		t.Fatalf("manager output has %d publications: %v", len(output), err)
	}
	for i, phrase := range output {
		wantSize := 4096
		if i == 0 {
			wantSize = len(shortPhrase)
		}
		if phrase.Sequence != uint64(i+1) || len(phrase.Text) != wantSize || phrase.Final != (i == phrases-1) {
			t.Fatalf("invalid publication %d", i)
		}
	}
	if _, err := client.PreviewPublication(ctx, "same-id", output[0].ID); !errors.Is(err, ErrStale) {
		t.Fatalf("preview before native completion = %v", err)
	}
	controller.mu.Lock()
	controller.status.Turn.State = "completed"
	controller.mu.Unlock()
	completed := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if receipt, err = client.Receipt(ctx, "same-id"); err == nil && receipt.Phase == "completed" {
			completed = receipt.OperationID != "" && receipt.NativeTurnID == "native-turn"
			break
		}
	}
	if !completed {
		t.Fatalf("missing native completion receipt: %+v, %v", receipt, err)
	}
	if _, err := client.PreviewPublication(ctx, "same-id", "unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unverified publication = %v", err)
	}
	if _, err := client.PreviewPublication(ctx, "same-id", output[1].ID); !errors.Is(err, ErrCapacity) {
		t.Fatalf("oversized publication = %v", err)
	}
	pcm, err := client.PreviewPublication(ctx, "same-id", output[0].ID)
	if err != nil || !bytes.Equal(pcm, []byte{1, 2, 3, 4}) {
		t.Fatalf("confirmed preview audio = %v, %v", pcm, err)
	}
	select {
	case text := <-spoken:
		if text != previewGreeting(shortPhrase, cfg.Language) {
			t.Fatalf("provider commentary = %q", text)
		}
	default:
		t.Fatal("owner preview did not supply canonical text")
	}
	retry, err = client.SubmitText(ctx, "same-id", "exact text")
	if err != nil || retry.Phase != "completed" || retry.OperationID != receipt.OperationID {
		t.Fatalf("completed repeat = %+v, %v", retry, err)
	}
	if _, err := client.SubmitText(ctx, "same-id", "revised text"); !errors.Is(err, ErrConflict) {
		t.Fatalf("completed conflict = %v", err)
	}
	select {
	case other := <-entered:
		t.Fatalf("retry dispatched again: %+v", other)
	default:
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not stop")
	}
	if native.launches != 1 {
		t.Fatalf("transient roster/observer delay executed %d launch calls", native.launches)
	}
	for _, path := range []string{"bridge.sock", "control.sock"} {
		if _, err := os.Lstat(filepath.Join(dir, path)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("socket %s remains: %v", path, err)
		}
	}
}

func TestManagerServiceDoesNotRepeatUncertainLaunch(t *testing.T) {
	dir := privateServerDir(t)
	cfg := testConfig()
	refs := ManagerBridgeRefs{Socket: filepath.Join(dir, "bridge.sock"), AuthFile: filepath.Join(dir, "bridge.auth")}
	native := bootstrapNativeFixture(cfg, refs)
	native.lost = true
	controller := &controllerRuntimeFixture{status: native.runtime}
	runtime := &serviceRuntimeFixture{bootstrapFixture: native, controllerRuntimeFixture: controller}
	if err := serveManager(context.Background(), dir, "endpoint", cfg, runtime, nil, nil); !errors.Is(err, ErrControllerUncertain) {
		t.Fatalf("lost launch reply = %v; want uncertain", err)
	}
	native.rows = nil
	native.lost = false
	if err := serveManager(context.Background(), dir, "endpoint", cfg, runtime, nil, nil); !errors.Is(err, ErrControllerUncertain) {
		t.Fatalf("unresolved launch = %v; want uncertain", err)
	}
	if native.launches != 1 {
		t.Fatalf("executed %d launch calls for an unresolved intent", native.launches)
	}
}

func TestManagerServiceQuarantineOffersStatusWithoutAuthority(t *testing.T) {
	for _, offlineLoss := range []bool{false, true} {
		t.Run(fmt.Sprintf("offline_loss=%t", offlineLoss), func(t *testing.T) {
			testManagerServiceQuarantine(t, offlineLoss)
		})
	}
}

func testManagerServiceQuarantine(t *testing.T, offlineLoss bool) {
	dir := privateServerDir(t)
	cfg := testConfig()
	refs := ManagerBridgeRefs{Socket: filepath.Join(dir, "bridge.sock"), AuthFile: filepath.Join(dir, managerBridgeAuth)}
	native := bootstrapNativeFixture(cfg, refs)
	proof := native.runtime.Binding
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := store.BindManager(ManagerBinding{
		ConversationID: proof.ConversationID, Provider: proof.Provider, SessionID: proof.Session,
		SessionInstance: proof.SessionInstance, NativeSessionID: proof.NativeSessionID,
		NativeGeneration: proof.NativeGeneration, ObservationGeneration: proof.ObservationGeneration, LoginEpoch: proof.LoginEpoch,
	})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if !offlineLoss {
		if err := store.noteControllerCustodyLost(bound); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
	} else {
		if _, _, err := native.LaunchOperation("", protocol.LaunchReq{
			Agent: cfg.Provider, Cwd: cfg.WorkDir, Manager: &protocol.ManagerLaunchReq{ConversationID: cfg.ConversationID},
		}); err != nil {
			t.Fatal(err)
		}
		native.launches = 0
		if err := writeVoiceFile(dir, managerBridgeAuth, []byte(strings.Repeat("a", 64))); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	defer func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	runtime := &serviceRuntimeFixture{bootstrapFixture: native, controllerRuntimeFixture: &controllerRuntimeFixture{}}
	if offlineLoss {
		runtime.proofErr = managerproof.ErrQuarantined
	}
	go func() { done <- serveManager(ctx, dir, "endpoint", cfg, runtime, nil, nil) }()
	client := NewManagerClient(dir)
	defer client.Close()
	var state ManagerStatus
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if state, err = client.Status(ctx); err == nil {
			break
		}
	}
	if err != nil || state.Ready || !state.CustodyLost || identity(state.Binding) != identity(bound) {
		t.Fatalf("quarantine status = %+v, %v", state, err)
	}
	if _, err := client.SubmitText(ctx, "dead-id", "new instruction"); !errors.Is(err, ErrStale) {
		t.Fatalf("quarantined admission = %v", err)
	}
	if _, err := os.Lstat(refs.AuthFile); !offlineLoss && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quarantine created bridge auth: %v", err)
	}
	if native.launches != 0 {
		t.Fatal("quarantine relaunched the manager")
	}
}
