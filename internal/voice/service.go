package voice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/voice/openailive"
)

const (
	managerControlSocket = "control.sock"
	managerBridgeAuth    = schema.DiscussionBridgeAuthFile
	managerTickInterval  = 500 * time.Millisecond
)

type managerServiceRuntime interface {
	managerBootstrapClient
	ControllerRuntime
}

// ServeManager runs the selected manager's foreground controller until canceled.
// Closing the service releases its sockets and ledger without stopping native sessions.
func ServeManager(ctx context.Context, dir, endpoint string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cfg, err := LoadConfig(dir)
	if err != nil {
		return err
	}
	runtime, err := NewProtocolControllerClient(ctx, cfg.DaemonSocket, endpoint)
	if err != nil {
		return err
	}
	preparePTT := func() (pttTranscriber, error) {
		key, err := LoadOpenAIKey(dir)
		if err != nil {
			return nil, ErrPTTUnavailable
		}
		return func(ctx context.Context, pcm []byte) (string, error) {
			return openailive.Transcribe(ctx, key, pcm, cfg.Language)
		}, nil
	}
	openPreview := func(ctx context.Context, greeting string) (previewSession, error) {
		key, err := LoadOpenAIKey(dir)
		if err != nil {
			return nil, ErrPreviewUnavailable
		}
		return openailive.OpenPreview(ctx, key, cfg.VoiceID, greeting)
	}
	return serveManager(ctx, dir, endpoint, cfg, runtime, preparePTT, openPreview)
}

func serveManager(ctx context.Context, dir, endpoint string, cfg Config, runtime managerServiceRuntime, preparePTT func() (pttTranscriber, error), openPreview func(context.Context, string) (previewSession, error)) error {
	for {
		err := runManagerController(ctx, dir, endpoint, cfg, runtime, preparePTT, openPreview)
		if !errors.Is(err, errManagerReplacementAdopted) {
			return err
		}
	}
}

func runManagerController(ctx context.Context, dir, endpoint string, cfg Config, runtime managerServiceRuntime, preparePTT func() (pttTranscriber, error), openPreview func(context.Context, string) (previewSession, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || cfg.Validate() != nil {
		return ErrInvalid
	}
	mux := http.NewServeMux()
	bridge, err := startPrivateHTTP(ctx, dir, mux)
	if err != nil {
		return err
	}
	defer func() { _ = bridge.Close() }()
	if _, production := runtime.(*ProtocolControllerClient); production {
		persisted, err := LoadConfig(dir)
		if err != nil {
			return err
		}
		if persisted != cfg {
			return errors.New("voice: configuration changed during startup; start the service again")
		}
	}
	controlMux := http.NewServeMux()
	listener, err := startPrivateHTTPOn(ctx, dir, managerControlSocket, "control.lock", controlMux)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	store, err := OpenStore(dir)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	state, err := store.Snapshot()
	if err != nil {
		return err
	}
	var devices *deviceRegistry
	if cfg.DeviceListen != "" {
		devices, err = openDeviceRegistry(dir, deviceRegistryName(cfg), cfg.PlaceID, time.Now)
		if err != nil {
			return err
		}
	}
	refs := ManagerBridgeRefs{Socket: bridge.socket, AuthFile: filepath.Join(dir, managerBridgeAuthName(cfg))}
	recovery := &managerReplacement{dir: dir, endpoint: endpoint, config: cfg, refs: refs, runtime: runtime, wake: make(chan struct{}, 1)}
	readOnly := func(binding ManagerBinding) error {
		boundEndpoint, _, valid := protocol.ParseID(binding.SessionID)
		if !valid || boundEndpoint != endpoint || binding.Provider != cfg.Provider || state.CurrentConversation != cfg.ConversationID {
			return ErrStale
		}
		control := &managerControl{store: store, binding: binding, replacement: recovery, devices: devices, tickAttention: "manager custody lost; explicit replacement required"}
		if devices != nil {
			stopDevices, err := control.startDeviceEndpoint(ctx, cfg.DeviceListen)
			if err != nil {
				return err
			}
			defer stopDevices()
		}
		recovery.signal()
		controlMux.Handle("/v1/", control)
		return control.run(ctx, bridge, listener)
	}
	var existing *ManagerBinding
	if conversation, ok := state.Conversations[cfg.ConversationID]; ok {
		binding := conversation.Binding
		binding.Generation = state.Generation
		if conversation.CustodyLost {
			return readOnly(binding)
		}
		existing = &binding
	}
	intentExists := false
	if _, err := os.Lstat(filepath.Join(dir, managerIntentName(cfg.ConversationID))); err == nil {
		intentExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	credential, err := managerBridgeCredential(dir, filepath.Base(refs.AuthFile), existing == nil && !intentExists)
	if err != nil {
		return err
	}
	handler := newManagerHandler(cfg.ConversationID, credential)
	selected, err := bootstrapManager(ctx, dir, cfg, refs, endpoint, existing, runtime)
	if errors.Is(err, managerproof.ErrQuarantined) && existing != nil {
		if err := store.noteControllerCustodyLost(*existing); err != nil {
			return err
		}
		return readOnly(*existing)
	}
	if err != nil {
		return err
	}
	binding, err := store.BindManager(selected.Binding)
	if err != nil {
		return err
	}
	controller, err := NewController(store, runtime, binding, refs)
	if err != nil {
		return err
	}
	controller.managerSettingsDir = filepath.Dir(dir)
	handler.bind(selected.LocalID, controller.ManagerCall)
	mux.Handle("/manager/call", handler)
	control := &managerControl{store: store, binding: binding, controller: controller, replacement: recovery, wake: make(chan struct{}, 1), preparePTT: preparePTT, pttReads: make(chan struct{}, 4), preview: ownerPreview{open: openPreview, language: cfg.Language}, devices: devices}
	if devices != nil {
		stopDevices, err := control.startDeviceEndpoint(ctx, cfg.DeviceListen)
		if err != nil {
			return err
		}
		defer stopDevices()
	}
	controlMux.Handle("/v1/", control)

	if _, ok := runtime.(*ProtocolControllerClient); ok {
		journalCtx, cancelJournal := context.WithCancel(ctx)
		journalDone := make(chan struct{})
		go func() {
			defer close(journalDone)
			control.followJournal(journalCtx, cfg.DaemonSocket)
		}()
		defer func() {
			cancelJournal()
			<-journalDone
		}()
	}
	return control.run(ctx, bridge, listener)
}

func managerBridgeCredential(dir, file string, create bool) (string, error) {
	raw, err := readVoiceFile(dir, file, 64)
	if errors.Is(err, os.ErrNotExist) && create {
		var token [32]byte
		if _, err := rand.Read(token[:]); err != nil {
			return "", err
		}
		credential := hex.EncodeToString(token[:])
		if err := writeVoiceFile(dir, file, []byte(credential)); err != nil {
			return "", err
		}
		return credential, nil
	}
	if err != nil {
		return "", err
	}
	if _, err := hex.DecodeString(string(raw)); err != nil || len(raw) != 64 {
		return "", ErrCorrupt
	}
	return string(raw), nil
}

type managerControl struct {
	store          *Store
	binding        ManagerBinding
	controller     *Controller
	replacement    *managerReplacement
	wake           chan struct{}
	preparePTT     func() (pttTranscriber, error)
	pttReads       chan struct{}
	preview        ownerPreview
	devices        *deviceRegistry
	deviceEndpoint string
	statusStreams  deviceStatusStreams
	capture        captureSlot

	mu               sync.Mutex
	tickAttention    string
	journalAttention string
}

func (c *managerControl) run(ctx context.Context, bridge, listener *privateHTTP) error {
	var ticks <-chan time.Time
	var replace <-chan struct{}
	if c.replacement != nil {
		replace = c.replacement.wake
	}
	if c.controller != nil {
		ticker := time.NewTicker(managerTickInterval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	nextCleanup := time.Now().Add(time.Minute)
	for {
		if c.controller != nil {
			tickCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
			err := c.controller.Tick(tickCtx)
			if now := time.Now(); !now.Before(nextCleanup) {
				cleanupErr := c.store.compactReceipts(c.binding, now, 64)
				if err == nil {
					err = cleanupErr
				}
				nextCleanup = now.Add(5 * time.Minute)
			}
			if streamErr := c.syncDeviceStatuses(); err == nil {
				err = streamErr
			}
			cancel()
			c.mu.Lock()
			c.tickAttention = serviceAttention(err)
			c.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-bridge.done:
			if ctx.Err() != nil {
				return nil
			}
			return bridge.Close()
		case <-listener.done:
			if ctx.Err() != nil {
				return nil
			}
			return listener.Close()
		case <-replace:
			adopted, err := c.progressReplacement(ctx)
			if adopted {
				return errManagerReplacementAdopted
			}
			c.replacement.mu.Lock()
			c.replacement.attention = serviceAttention(err)
			c.replacement.mu.Unlock()
		case <-c.wake:
		case <-ticks:
		}
	}
}

func (c *managerControl) followJournal(ctx context.Context, socket string) {
	for ctx.Err() == nil {
		c.mu.Lock()
		c.journalAttention = ""
		c.mu.Unlock()
		err := c.controller.FollowDaemonJournal(ctx, socket)
		c.mu.Lock()
		c.journalAttention = "daemon journal reconnecting"
		unavailable := errors.Is(err, ErrControllerUnsupported) || errors.Is(err, ErrStale)
		if unavailable {
			c.journalAttention = "daemon journal unavailable"
		}
		c.mu.Unlock()
		if unavailable {
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

func serviceAttention(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, protocol.ErrManagerLaunchPrerequisite):
		return protocol.ErrManagerLaunchPrerequisite.Error()
	case errors.Is(err, ErrControllerPaused):
		return "direct terminal activity needs reconciliation"
	case errors.Is(err, ErrControllerUncertain):
		return "native outcome uncertain"
	case errors.Is(err, ErrStale):
		return "manager binding unavailable"
	case errors.Is(err, ErrControllerUnsupported):
		return "native operation unsupported"
	case errors.Is(err, ErrExpired):
		return "operation expired before dispatch"
	default:
		return "controller reconciliation pending"
	}
}
