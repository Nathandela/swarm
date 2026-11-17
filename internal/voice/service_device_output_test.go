package voice

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/voice/openailive"
	"github.com/Nathandela/swarm/internal/voiceclient"
	"github.com/Nathandela/swarm/internal/voicewire"
)

type blockingDevicePreview struct {
	*previewFixture
	entered  chan struct{}
	canceled chan struct{}
}

func (p *blockingDevicePreview) Next(ctx context.Context) (openailive.Event, error) {
	close(p.entered)
	<-ctx.Done()
	close(p.canceled)
	return openailive.Event{}, ctx.Err()
}

func TestSignedDevicePreviewOverPinnedSSH(t *testing.T) {
	dir := privateServerDir(t)
	cfg := testConfig()
	cfg.DeviceListen = "127.0.0.1:0"
	refs := ManagerBridgeRefs{Socket: filepath.Join(dir, "bridge.sock"), AuthFile: filepath.Join(dir, managerBridgeAuth)}
	native := bootstrapNativeFixture(cfg, refs)
	native.runtime.Turn.State = "idle"
	controller := &controllerRuntimeFixture{status: native.runtime, receipts: map[string]protocol.SemanticMessageReceipt{}}
	nativeSends := make(chan protocol.ComposerSendReq, 2)
	controller.beforeSend = func(_ string, req protocol.ComposerSendReq) { nativeSends <- req }
	runtime := &serviceRuntimeFixture{bootstrapFixture: native, controllerRuntimeFixture: controller}

	const statement = "The verified publication is ready."
	blocks := make(chan *blockingDevicePreview, 4)
	var openerCalls atomic.Int32
	openPreview := func(_ context.Context, greeting string) (previewSession, error) {
		if greeting != previewGreeting(statement, cfg.Language) {
			return nil, errors.New("provider received material outside the verified publication")
		}
		order := openerCalls.Add(1)
		basic := &previewFixture{spoken: make(chan string, 1), events: []openailive.Event{
			{Type: "session.commentary.appended", ClientEventID: "fixture-greeting"},
			{Type: "session.output_audio.delta", Audio: []byte{0, 4, 0, 7}},
			{Type: "session.closed"},
		}}
		if order == 2 || order >= 4 {
			block := &blockingDevicePreview{previewFixture: basic, entered: make(chan struct{}), canceled: make(chan struct{})}
			blocks <- block
			return block, nil
		}
		return basic, nil
	}

	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	done := make(chan error, 1)
	go func() { done <- serveManager(ctx, dir, "endpoint", cfg, runtime, nil, openPreview) }()
	defer func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	owner := NewManagerClient(dir)
	defer owner.Close()
	var addr string
	for ctx.Err() == nil {
		var err error
		if addr, err = owner.DeviceEndpoint(ctx); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if addr == "" {
		t.Fatal("device endpoint never bound")
	}
	sshCfg, sshKey := deviceSSHFixture(t, addr)
	connected := func(key ed25519.PrivateKey) *voiceclient.DeviceClient {
		t.Helper()
		client, err := voiceclient.NewDeviceClient(ctx, sshCfg, sshKey, key)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(client.Close)
		return client
	}
	enroll := func() (*voiceclient.DeviceClient, voicewire.DeviceCredential, ed25519.PrivateKey) {
		t.Helper()
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		invitation, err := owner.IssueDeviceEnrollment(ctx, public)
		if err != nil {
			t.Fatal(err)
		}
		client := connected(private)
		grant, err := client.Enroll(ctx, invitation)
		if err != nil {
			t.Fatal(err)
		}
		return client, grant, private
	}
	client, grant, key := enroll()
	other, otherGrant, _ := enroll()
	if _, err := client.SubmitText(ctx, grant, "utterance", "short input"); err != nil {
		t.Fatal(err)
	}
	select {
	case send := <-nativeSends:
		for {
			receipt, err := client.Receipt(ctx, grant, "utterance")
			if err != nil {
				t.Fatal(err)
			}
			if receipt.Phase == "submitted" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		nonce := send.ManagerAdmission.Nonce
		controller.mu.Lock()
		controller.status.ReadyForInput = false
		controller.status.Turn = protocol.ManagerNativeTurn{State: "running", NativeInputID: nonce, NativeTurnID: "preview-turn"}
		controller.proof = protocol.ManagerCallProof{Main: true, NativeInputID: nonce, NativeTurnID: "preview-turn", NativeCallID: "preview-call"}
		controller.mu.Unlock()
	case <-ctx.Done():
		t.Fatal("device input was not sent")
	}
	bridge, err := NewManagerBridgeClient(refs.Socket, refs.AuthFile, cfg.ConversationID, "manager")
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	args, err := json.Marshal(PublishInput{OutputID: "item", Sequence: 1, Text: statement, Final: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.Call(ctx, ManagerToolCall{Name: "publish", Arguments: args}); err != nil {
		t.Fatal(err)
	}
	results, err := client.Publications(ctx, grant, "utterance")
	if err != nil || len(results) != 1 {
		t.Fatalf("publication count = %d, %v", len(results), err)
	}
	pub := results[0].ID
	assertDenied := func(pcm []byte, err error, want int) {
		t.Helper()
		var status *voiceclient.DeviceHTTPError
		if len(pcm) != 0 || !errors.As(err, &status) || status.StatusCode != want {
			t.Fatalf("denied preview returned %d bytes, %v, want HTTP %d", len(pcm), err, want)
		}
	}
	pcm, err := client.PreviewPublication(ctx, grant, "utterance", pub)
	assertDenied(pcm, err, http.StatusPreconditionFailed)
	controller.mu.Lock()
	controller.status.Turn.State = "completed"
	controller.mu.Unlock()
	for {
		receipt, err := client.Receipt(ctx, grant, "utterance")
		if err != nil {
			t.Fatal(err)
		}
		if receipt.Phase == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	pcm, err = other.PreviewPublication(ctx, otherGrant, "utterance", pub)
	assertDenied(pcm, err, http.StatusNotFound)
	if openerCalls.Load() != 0 {
		t.Fatal("unauthorized previews reached the provider")
	}
	pcm, err = client.PreviewPublication(ctx, grant, "utterance", pub)
	if err != nil || !bytes.Equal(pcm, []byte{0, 4, 0, 7}) {
		t.Fatalf("valid signed preview = %v, %v", pcm, err)
	}

	type result struct {
		pcm []byte
		err error
	}
	begin := func(previewCtx context.Context) (*blockingDevicePreview, <-chan result) {
		t.Helper()
		results := make(chan result, 1)
		go func() {
			pcm, err := client.PreviewPublication(previewCtx, grant, "utterance", pub)
			results <- result{pcm, err}
		}()
		select {
		case block := <-blocks:
			select {
			case <-block.entered:
				return block, results
			case <-ctx.Done():
			}
		case <-ctx.Done():
		}
		t.Fatal("preview provider was not entered")
		return nil, nil
	}
	ended := func(block *blockingDevicePreview, results <-chan result) result {
		t.Helper()
		select {
		case <-block.canceled:
		case <-ctx.Done():
			t.Fatal("provider was not interrupted")
		}
		select {
		case res := <-results:
			return res
		case <-ctx.Done():
			t.Fatal("preview did not stop")
		}
		return result{}
	}
	inFlight, response := begin(ctx)
	superseding := connected(key)
	if pcm, err := superseding.PreviewPublication(ctx, grant, "utterance", pub); err != nil || len(pcm) == 0 {
		t.Fatalf("superseding preview: %v", err)
	}
	res := ended(inFlight, response)
	assertDenied(res.pcm, res.err, http.StatusPreconditionFailed)

	clientCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	inFlight, response = begin(clientCtx)
	cancel()
	res = ended(inFlight, response)
	if len(res.pcm) != 0 || !errors.Is(res.err, context.Canceled) {
		t.Fatalf("client Stop leaked PCM or lost cancellation: %v", res.err)
	}

	inFlight, response = begin(ctx)
	revokeCtx, deadline := context.WithTimeout(ctx, time.Second)
	defer deadline()
	if err := owner.RevokeDevice(revokeCtx, grant.DeviceID); err != nil {
		t.Fatalf("revocation was blocked by provider: %v", err)
	}
	res = ended(inFlight, response)
	assertDenied(res.pcm, res.err, http.StatusForbidden)
	pcm, err = client.PreviewPublication(ctx, grant, "utterance", pub)
	assertDenied(pcm, err, http.StatusForbidden)
	if openerCalls.Load() != 5 {
		t.Fatalf("revoked preview opened additional provider session: %d", openerCalls.Load())
	}
}
