package voice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

var ErrDiscussionActivationUnavailable = errors.New("voice: discussion manager could not become ready")

func discussionActivationError(err error) error {
	if errors.Is(err, protocol.ErrManagerLaunchPrerequisite) || errors.Is(err, ErrControllerUnsupported) || errors.Is(err, ErrControllerNativeUnavailable) {
		return errors.Join(ErrDiscussionActivationUnavailable, err)
	}
	return err
}

const DiscussionIdleGrace = 2 * time.Minute

type discussionRuntime interface {
	managerServiceRuntime
	ManagerRetire(context.Context, protocol.ManagerRetireReq, bool) (protocol.ManagerRetireReceipt, error)
}

type DiscussionInput struct {
	Origin                                             schema.MessageOrigin
	Text, Answer, QuestionID, QuestionHash, Supersedes string
	Project                                            func(string) (string, bool)
}

type DiscussionOutputSource struct {
	AdmissionID string `json:"admission_id,omitempty"`
	JobID       string `json:"job_id,omitempty"`
	AttemptID   string `json:"attempt_id,omitempty"`
	SystemCause string `json:"system_cause,omitempty"`
}

type DiscussionOutput struct {
	DiscussionOutputSource
	ID, Text, QuestionID, QuestionHash string
	Sequence                           uint64
}

type DiscussionController struct {
	mu            sync.Mutex
	ctx           context.Context
	dir, endpoint string
	cfg           Config
	runtime       discussionRuntime
	store         *Store
	scope         DiscussionScope
	bridge        *privateHTTP
	handler       *managerHandler
	controller    *Controller
	followCancel  context.CancelFunc
	followDone    chan struct{}
	journalReady  atomic.Bool
	now           func() time.Time
	idleSince     time.Time
}

func OpenDiscussion(ctx context.Context, dir, socketDir, endpoint string, cfg Config, scope DiscussionScope) (*DiscussionController, error) {
	runtime, err := NewProtocolControllerClient(ctx, cfg.DaemonSocket, endpoint)
	if err != nil {
		return nil, err
	}
	return openDiscussion(ctx, dir, socketDir, endpoint, cfg, scope, runtime)
}

func openDiscussion(ctx context.Context, dir, socketDir, endpoint string, cfg Config, scope DiscussionScope, runtime discussionRuntime) (*DiscussionController, error) {
	cfg.VMProjectAccess = scope.VMProjectAccess
	validAudience := (configText(scope.GuildID, 128) && scope.DMOwnerID == "") || (scope.GuildID == "" && configText(scope.DMOwnerID, 128))
	if cfg.Validate() != nil || !strings.HasPrefix(cfg.ConversationID, DiscussionConversationPrefix) || !configText(scope.InstallationID, 128) || !validAudience || !configText(scope.ThreadID, 128) {
		return nil, ErrInvalid
	}
	store, err := OpenStore(dir)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = store.Close()
		}
	}()
	state, err := store.Snapshot()
	if err != nil {
		return nil, err
	}
	if state.CurrentConversation != "" && state.CurrentConversation != cfg.ConversationID {
		return nil, ErrStale
	}
	_, intentErr := os.Lstat(filepath.Join(dir, managerIntentName(cfg.ConversationID)))
	if intentErr != nil && !errors.Is(intentErr, os.ErrNotExist) {
		return nil, intentErr
	}
	credential, err := managerBridgeCredential(dir, managerBridgeAuth, state.CurrentConversation == "" && errors.Is(intentErr, os.ErrNotExist))
	if err != nil {
		return nil, err
	}
	handler := newManagerHandler(cfg.ConversationID, credential)
	bridge, err := startPrivateHTTP(ctx, socketDir, handler)
	if err != nil {
		return nil, err
	}
	d := &DiscussionController{ctx: ctx, dir: dir, endpoint: endpoint, cfg: cfg, scope: scope, runtime: runtime, store: store, bridge: bridge, handler: handler, now: time.Now}
	if state.CurrentConversation != "" {
		b := state.Conversations[cfg.ConversationID].Binding
		b.Generation = state.Generation
		if err = d.bind(b); err != nil {
			_ = bridge.Close()
			return nil, err
		}
	}
	failed = false
	return d, nil
}

func (d *DiscussionController) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	err := d.bridge.Close()
	d.stopFollowing()
	return errors.Join(err, d.store.Close())
}

func (d *DiscussionController) refs() ManagerBridgeRefs {
	return ManagerBridgeRefs{Socket: d.bridge.socket, AuthFile: filepath.Join(d.dir, managerBridgeAuth)}
}

func (d *DiscussionController) bind(b ManagerBinding) error {
	controller, err := NewController(d.store, d.runtime, b, d.refs())
	if err != nil {
		return err
	}
	_, local, ok := protocol.ParseID(b.SessionID)
	if !ok {
		return ErrStale
	}
	scope := d.scope
	scope.VMProjectAccess = scope.VMProjectAccess && b.NativeCLI
	scope.journalReady = d.journalReady.Load
	controller.discussion = &scope
	controller.managerSettingsDir = scope.ManagerSettingsDir
	d.stopFollowing()
	d.controller = controller
	d.handler.bind(local, controller.ManagerCall)
	d.handler.bindWorker(controller.acceptDiscussionWorkerMessage)
	ctx, cancel := context.WithCancel(d.ctx)
	done := make(chan struct{})
	d.followCancel, d.followDone = cancel, done
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			_ = controller.followDaemonJournal(ctx, d.cfg.DaemonSocket, func() { d.journalReady.Store(true) })
			d.journalReady.Store(false)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}()
	return nil
}

func (d *DiscussionController) stopFollowing() {
	if d.followCancel != nil {
		d.followCancel()
		<-d.followDone
		d.followCancel = nil
	}
}

func (d *DiscussionController) Admit(ctx context.Context, input DiscussionInput) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.scope.validateOrigin(input.Origin) {
		return ErrInvalid
	}
	st, err := d.store.Snapshot()
	if err != nil {
		return err
	}
	a, err := findDiscussionAdmission(st, input.Origin)
	if err != nil {
		return err
	}
	if a.Origin != nil && *a.Origin == input.Origin {
		return nil
	}
	if a.Origin != nil && (a.Phase != "superseded" || a.Origin.Revision != input.Supersedes) {
		return ErrConflict
	}
	if err := d.activate(ctx, st); err != nil {
		return err
	}
	project, explicit := "", false
	if input.Project != nil {
		project, explicit = input.Project(st.Conversations[d.cfg.ConversationID].Project)
	}
	if input.QuestionID == "" {
		input.QuestionID, input.QuestionHash = matchingDiscussionQuestion(st, input.Answer, d.now())
	}
	_, err = d.controller.submitDiscussionInProject(input.Origin, input.Text, input.Answer, input.QuestionID, input.QuestionHash, project, explicit)
	if err == nil {
		d.idleSince = time.Time{}
	}
	return err
}

func (d *DiscussionController) AdmissionKnown(origin schema.MessageOrigin) (bool, error) {
	state, err := d.AdmissionState(origin)
	return state != "missing", err
}

func (d *DiscussionController) AdmissionState(origin schema.MessageOrigin) (string, error) {
	if !d.scope.validateOrigin(origin) {
		return "missing", ErrInvalid
	}
	st, err := d.store.readView()
	if err != nil {
		return "missing", err
	}
	a, err := findDiscussionAdmission(st, origin)
	if err != nil || a.Origin == nil {
		return "missing", err
	}
	if *a.Origin != origin {
		if a.Phase != "superseded" {
			return "missing", ErrConflict
		}
		return "missing", nil
	}
	switch {
	case a.SourceCutover:
		return "cutover", nil
	case a.Phase == "superseded":
		return "superseded", nil
	case a.Phase == "rejected" || !a.SettledAt.IsZero():
		return "cancelled", nil
	default:
		return "accepted", nil
	}
}

func (d *DiscussionController) CancelAdmission(origin schema.MessageOrigin) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.controller == nil || !d.scope.validateOrigin(origin) {
		return ErrInvalid
	}
	st, err := d.store.readView()
	if err != nil {
		return err
	}
	a, err := findDiscussionAdmission(st, origin)
	if err != nil {
		return err
	}
	if a.Origin == nil || *a.Origin != origin || a.SourceCutover {
		return ErrStale
	}
	if a.Phase == "rejected" && !a.SettledAt.IsZero() {
		return nil
	}
	if a.Phase != "superseded" {
		return ErrConflict
	}
	return d.store.rejectQueuedControllerInput(d.controller.binding, a, "Discord source withdrawn before native submission")
}

func findDiscussionAdmission(st State, origin schema.MessageOrigin) (Admission, error) {
	for _, a := range st.Admissions {
		if a.Origin == nil || a.Origin.RequestID != origin.RequestID {
			continue
		}
		previous := *a.Origin
		previous.Revision, previous.ActorName = origin.Revision, origin.ActorName
		if previous != origin {
			return Admission{}, ErrConflict
		}
		return a, nil
	}
	return Admission{}, nil
}

func (d *DiscussionController) activate(ctx context.Context, st State) error {
	conv := st.Conversations[d.cfg.ConversationID]
	if conv.Retirement != nil && !conv.Parked {
		if err := d.finishRetirement(ctx, *conv.Retirement, true); err != nil {
			return err
		}
		st, err := d.store.Snapshot()
		if err != nil {
			return err
		}
		conv = st.Conversations[d.cfg.ConversationID]
	}
	if d.controller != nil && !conv.Parked {
		return nil
	}
	if runtime, ok := d.runtime.(*ProtocolControllerClient); ok {
		if err := runtime.discussionSupported(ctx); err != nil {
			return discussionActivationError(err)
		}
	}
	var binding ManagerBinding
	var err error
	if conv.Parked {
		binding, err = d.wakeParked(ctx, st.Generation, conv)
	} else {
		req := managerLaunchRequest(d.cfg, d.refs())
		var selected managerSelection
		selected, err = bootstrapManagerRequest(ctx, d.dir, d.cfg, d.refs(), d.endpoint, nil, d.runtime, req, managerIntentName(d.cfg.ConversationID), "")
		if err == nil {
			binding, err = d.store.BindManager(selected.Binding)
		}
	}
	if err != nil {
		return discussionActivationError(err)
	}
	d.idleSince = time.Time{}
	return d.bind(binding)
}

func (d *DiscussionController) wakeParked(ctx context.Context, generation uint64, conv Conversation) (ManagerBinding, error) {
	if conv.Retirement == nil {
		return ManagerBinding{}, ErrCorrupt
	}
	retirement := protocol.ManagerRetireOperationID(*conv.Retirement)
	suffix := hashBytes([]byte(retirement)) + ".json"
	for {
		req := managerLaunchRequest(d.cfg, d.refs())
		source := conv.Binding.SessionID
		instance := conv.Binding.SessionInstance
		stage := 0
		if prev := conv.FailedResume; prev != nil {
			stage = prev.Attempt
			if stage < 1 || stage > 2 || prev.Session == "" || prev.Instance == "" {
				return ManagerBinding{}, ErrCorrupt
			}
			if d.scope.VerifyAudience != nil {
				if err := d.scope.VerifyAudience(ctx); err != nil {
					return ManagerBinding{}, err
				}
			}
			source, instance = prev.Session, prev.Instance
		}
		file := "resume-" + suffix
		if stage == 2 {
			file = "reconstruct-" + suffix
		} else {
			req.Options[protocol.OptionResumeFrom] = source
			req.ResumeSourceInstance = instance
			req.Manager.RetireOperationID = retirement
			if stage == 1 {
				file = "retry-" + suffix
			}
		}
		// A legacy native thread can restore its old unrestricted policy on resume.
		// Start a new native history only after confirming retirement and ruling out
		// any in-flight legacy resume. Preserve the thread's tracked work and outbox.
		migrate := false
		if d.cfg.VMProjectAccess && !conv.Binding.NativeCLI && stage != 2 {
			var err error
			migrate, err = d.canMigrateParked(ctx, conv, file, req)
			if err != nil {
				return ManagerBinding{}, errors.Join(ErrDiscussionActivationUnavailable, err)
			}
			if migrate {
				req = managerLaunchRequest(d.cfg, d.refs())
				file = "native-cli-" + suffix
			}
		}
		selected, err := bootstrapManagerRequest(ctx, d.dir, d.cfg, d.refs(), d.endpoint, nil, d.runtime, req, file, source)
		if err != nil {
			var failed *failedResume
			if migrate || stage == 2 || !errors.As(err, &failed) {
				if errors.Is(err, ErrControllerUncertain) || errors.Is(err, ErrStale) || errors.Is(err, ErrConflict) {
					return ManagerBinding{}, errors.Join(ErrDiscussionActivationUnavailable, err)
				}
				return ManagerBinding{}, err
			}
			failed.Attempt = stage + 1
			if err = d.store.update(func(st *State) error {
				c := st.Conversations[d.cfg.ConversationID]
				if !sameParkedManager(st, generation, c, conv) {
					return ErrStale
				}
				c.FailedResume = failed
				putRecord(st, "conversations", &st.Conversations, d.cfg.ConversationID, c)
				return nil
			}); err != nil {
				return ManagerBinding{}, err
			}
			conv.FailedResume = failed
			continue
		}
		next := selected.Binding
		if next.LoginEpoch != conv.Binding.LoginEpoch || next.SessionInstance == conv.Binding.SessionInstance || next.SessionID == conv.Binding.SessionID {
			return ManagerBinding{}, errors.Join(ErrDiscussionActivationUnavailable, ErrStale)
		}
		if migrate || stage == 2 {
			if next.NativeSessionID == conv.Binding.NativeSessionID {
				return ManagerBinding{}, errors.Join(ErrDiscussionActivationUnavailable, ErrStale)
			}
			old := conv.Binding
			old.Generation = generation
			return d.recoverBinding(old, next, selected.Runtime.Turn, true, &conv)
		}
		if next.NativeSessionID != conv.Binding.NativeSessionID {
			return ManagerBinding{}, errors.Join(ErrDiscussionActivationUnavailable, ErrStale)
		}
		return d.resume(generation, conv, next, selected.Runtime.Turn.ExternalInput)
	}
}

func (d *DiscussionController) canMigrateParked(ctx context.Context, conv Conversation, file string, expected protocol.LaunchReq) (bool, error) {
	raw, err := readVoiceFile(d.dir, file, MaxControlFrameBytes)
	if err == nil {
		var intent managerLaunchIntent
		// An executing/applied resume must reconcile its existing owner receipt.
		if validateObjectMembers(raw, "version", "conversation_id", "operation_id", "request", "phase", "session_id") != nil || json.Unmarshal(raw, &intent) != nil {
			return false, ErrCorrupt
		}
		if intent.Phase != "prepared" {
			return false, nil
		}
		if intent.Version != 1 || intent.ConversationID != d.cfg.ConversationID || intent.OperationID == "" || intent.SessionID != "" || !matchesLegacyManagerRequest(intent.Request, expected) {
			return false, ErrConflict
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if d.scope.VerifyAudience != nil {
		if err := d.scope.VerifyAudience(ctx); err != nil {
			return false, err
		}
	}
	receipt, err := d.runtime.ManagerRetire(ctx, *conv.Retirement, true)
	if err != nil {
		return false, err
	}
	if receipt.State != "retired" || receipt.OperationID != protocol.ManagerRetireOperationID(*conv.Retirement) {
		return false, ErrControllerUncertain
	}
	return true, nil
}

func sameParkedManager(st *State, generation uint64, c, previous Conversation) bool {
	if st.CurrentConversation != previous.Binding.ConversationID || st.Generation != generation || !c.Parked || c.Binding != previous.Binding ||
		c.Retirement == nil || previous.Retirement == nil || *c.Retirement != *previous.Retirement || c.ManagerGeneration == ^uint64(0) {
		return false
	}
	if c.FailedResume == nil || previous.FailedResume == nil {
		return c.FailedResume == previous.FailedResume
	}
	return *c.FailedResume == *previous.FailedResume
}

func (d *DiscussionController) resume(generation uint64, previous Conversation, next ManagerBinding, external uint64) (ManagerBinding, error) {
	err := d.store.update(func(st *State) error {
		c := st.Conversations[next.ConversationID]
		if !sameParkedManager(st, generation, c, previous) {
			return ErrStale
		}
		c.ManagerGeneration = max(c.ManagerGeneration, 1) + 1
		next.Generation = st.Generation
		c.Binding = identity(next)
		c.Parked = false
		c.Retirement = nil
		c.FailedResume = nil
		c.ExternalInput = external
		c.ExternalObserved = true
		c.SchedulerPaused = false
		putRecord(st, "conversations", &st.Conversations, next.ConversationID, c)
		return nil
	})
	return next, err
}

// Dormant reports that no native process or future worker event needs this open controller.
func (d *DiscussionController) Dormant() (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.controller == nil {
		return true, nil
	}
	st, err := d.store.readView()
	if err != nil {
		return false, err
	}
	if !st.Conversations[d.cfg.ConversationID].Parked || !discussionQuiescent(st, d.now()) {
		return false, nil
	}
	for _, action := range st.Actions {
		if action.ConversationID == d.cfg.ConversationID && action.State == discussionQueuedSend {
			return false, nil
		}
	}
	for _, worker := range st.Workers {
		if !worker.Ended {
			return false, nil
		}
		if _, verified := st.Events[key(d.cfg.ConversationID, discussionEvidenceID(worker))]; !verified && !worker.EvidenceLimited && !worker.VMChild {
			return false, nil
		}
	}
	return true, nil
}

func (d *DiscussionController) Sync(ctx context.Context, emit func(DiscussionOutput) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.controller == nil {
		return nil
	}
	st, err := d.store.Snapshot()
	if err != nil {
		return err
	}
	if err = d.export(st, emit); err != nil {
		return err
	}
	if d.controller.discussion != nil && d.controller.discussion.VMProjectAccess && d.controller.workers != nil {
		changed, err := d.controller.observeVMWorkers(st)
		if err != nil {
			return err
		}
		if changed {
			st, err = d.store.Snapshot()
			if err != nil {
				return err
			}
		}
	}
	if len(st.Workers) > 0 {
		if err := d.controller.reconcileDiscussionWorkerEvidence(ctx); err != nil {
			return err
		}
		if err := d.controller.reconcileDiscussionSends(ctx); err != nil {
			return err
		}
		st, err = d.store.Snapshot()
		if err != nil {
			return err
		}
	}
	conversation := st.Conversations[d.cfg.ConversationID]
	if conversation.Retirement != nil && !conversation.Parked {
		return d.finishRetirement(ctx, *conversation.Retirement, true)
	}
	if conversation.Parked {
		if !discussionPending(st) {
			return nil
		}
		if d.scope.VerifyAudience != nil {
			if err := d.scope.VerifyAudience(ctx); err != nil {
				return err
			}
		}
		if err := d.activate(ctx, st); err != nil {
			return err
		}
	}
	if err := d.controller.Tick(ctx); err != nil {
		d.idleSince = time.Time{}
		if discussionRecoverableBindingError(err) {
			return d.recoverActive(ctx, err)
		}
		return err
	}
	return d.maybeRetire(ctx)
}

func discussionPending(st State) bool {
	for _, a := range st.Admissions {
		if a.SettledAt.IsZero() {
			return true
		}
	}
	for _, w := range st.Wakes {
		if w.State == "prepared" || w.State == "admitted" || w.State == "native_input" {
			return true
		}
	}
	return false
}

func discussionQuiescent(st State, now time.Time) bool {
	if discussionPending(st) || st.Conversations[st.CurrentConversation].SchedulerPaused || st.Journal[st.CurrentConversation].Binding == "" {
		return false
	}
	for _, a := range st.Actions {
		if a.State != "settled" && a.State != discussionQueuedSend {
			return false
		}
	}
	for _, p := range st.Publications {
		if !p.Exported {
			return false
		}
	}
	for _, q := range st.Requests {
		if q.Kind == "manager" && q.State == "pending" && !q.Exported && now.Before(q.ExpiresAt) {
			return false
		}
	}
	return true
}

func (d *DiscussionController) maybeRetire(ctx context.Context) error {
	c := d.controller
	c.scheduler.Lock()
	defer c.scheduler.Unlock()
	st, err := d.store.Snapshot()
	if err != nil {
		return err
	}
	now := d.now()
	if !discussionQuiescent(st, now) {
		d.idleSince = time.Time{}
		return nil
	}
	if d.idleSince.IsZero() {
		d.idleSince = now
		return nil
	}
	if now.Sub(d.idleSince) < DiscussionIdleGrace {
		return nil
	}
	native, err := c.nativeStatus(ctx)
	if err != nil {
		return err
	}
	if !native.ReadyForInput || (native.Turn.State != "completed" && native.Turn.State != "idle") || native.Turn.ExternallySteered {
		d.idleSince = time.Time{}
		return nil
	}
	req := protocol.ManagerRetireReq{Binding: runtimeBinding(c.binding), Generation: max(st.Conversations[c.binding.ConversationID].ManagerGeneration, 1), ExpectedTurn: native.Turn}
	err = d.store.update(func(current *State) error {
		if err := checkBinding(current, c.binding); err != nil {
			return err
		}
		if !discussionQuiescent(*current, now) || !d.journalReady.Load() {
			return ErrControllerUncertain
		}
		conversation := current.Conversations[c.binding.ConversationID]
		if conversation.Retirement != nil {
			return ErrConflict
		}
		req.Checkpoint = hashValue(struct {
			Journal   JournalCursor
			Admission uint64
			Speech    uint64
			Claim     time.Time
		}{current.Journal[c.binding.ConversationID], conversation.LastAdmission, conversation.LastSpeechEpoch, now})
		conversation.Retirement = &req
		putRecord(current, "conversations", &current.Conversations, c.binding.ConversationID, conversation)
		return nil
	})
	if err != nil {
		return err
	}
	return d.finishRetirement(ctx, req, false)
}

func (d *DiscussionController) finishRetirement(ctx context.Context, req protocol.ManagerRetireReq, statusOnly bool) error {
	receipt, err := d.runtime.ManagerRetire(ctx, req, statusOnly)
	if err != nil {
		return err
	}
	if receipt.OperationID != protocol.ManagerRetireOperationID(req) {
		return ErrStale
	}
	switch receipt.State {
	case "unknown", "prepared":
		if !statusOnly {
			return ErrControllerUncertain
		}
		native, err := d.controller.nativeStatus(ctx)
		if err != nil {
			if !discussionRecoverableBindingError(err) {
				return err
			}
			if receipt.State == "unknown" {
				rows, listErr := d.runtime.List()
				if listErr != nil {
					return listErr
				}
				if discussionManagerExited(rows, req.Binding) {
					d.idleSince = time.Time{}
					return d.recordRetirement(req, false)
				}
			}
			binding := d.controller.binding
			observed, probeErr := d.runtime.ManagerRuntime(binding.SessionID, binding.SessionInstance)
			if probeErr != nil || !discussionSameNative(binding, discussionBindingFromNative(observed)) ||
				observed.AuthFile != d.controller.refs.AuthFile || observed.BridgeSocket != d.controller.refs.Socket {
				return err
			}
			// No daemon claim exists, and the original physical manager is still
			// provable under a new observation epoch. Reconcile it on the next tick.
			d.idleSince = time.Time{}
			return d.recordRetirement(req, false)
		}
		if !native.ReadyForInput || native.Turn != req.ExpectedTurn {
			d.idleSince = time.Time{}
			return d.recordRetirement(req, false)
		}
		_, err = d.runtime.ManagerRetire(ctx, req, false)
		if errors.Is(err, managerproof.ErrStale) {
			verified, readErr := d.runtime.ManagerRetire(ctx, req, true)
			if readErr != nil {
				return readErr
			}
			if verified.State == "unknown" {
				d.idleSince = time.Time{}
				return d.recordRetirement(req, false)
			}
		}
		if err != nil {
			return err
		}
	case "uncertain":
		if statusOnly {
			_, err = d.runtime.ManagerRetire(ctx, req, false)
			if err != nil && !errors.Is(err, managerproof.ErrStale) {
				return err
			}
		}
	case "retired":
		rows, err := d.runtime.List()
		if err != nil {
			return err
		}
		if discussionManagerExited(rows, req.Binding) {
			return d.recordRetirement(req, true)
		}
	}
	return ErrControllerUncertain
}

func (d *DiscussionController) recordRetirement(req protocol.ManagerRetireReq, parked bool) error {
	return d.store.update(func(st *State) error {
		b := d.controller.binding
		if err := checkBinding(st, b); err != nil {
			return err
		}
		c := st.Conversations[b.ConversationID]
		if c.Retirement == nil || *c.Retirement != req {
			return ErrConflict
		}
		c.Parked = parked
		if !parked {
			c.Retirement = nil
		}
		putRecord(st, "conversations", &st.Conversations, b.ConversationID, c)
		return nil
	})
}

func matchingDiscussionQuestion(st State, response string, now time.Time) (string, string) {
	var pending Request
	for _, question := range st.Requests {
		if question.Kind != "manager" || question.State != "pending" || !question.Exported || !now.Before(question.ExpiresAt) {
			continue
		}
		if pending.ID != "" {
			return "", ""
		}
		pending = question
	}
	if pending.ID == "" {
		return "", ""
	}
	var question struct{ Choices []string }
	if json.Unmarshal(pending.Payload, &question) != nil {
		return "", ""
	}
	for _, choice := range question.Choices {
		if strings.EqualFold(strings.TrimSpace(response), choice) {
			return pending.ID, pending.Hash
		}
	}
	return "", ""
}

func (d *DiscussionController) export(st State, emit func(DiscussionOutput) error) error {
	var outputs []DiscussionOutput
	for _, p := range st.Publications {
		if p.Exported {
			continue
		}
		output := DiscussionOutput{DiscussionOutputSource: p.DiscussionSource}
		output.ID, output.Text, output.Sequence = p.ID, p.Text, p.OutputSequence
		outputs = append(outputs, output)
	}
	for _, r := range st.Requests {
		if r.Exported || r.Kind != "manager" || r.State != "pending" || !d.now().Before(r.ExpiresAt) {
			continue
		}
		question, err := discussionQuestionText(r)
		if err != nil {
			return err
		}
		output := DiscussionOutput{DiscussionOutputSource: r.DiscussionSource}
		output.ID, output.QuestionID, output.QuestionHash, output.Text = r.ID, r.ID, r.Hash, question
		output.Sequence = r.OutputSequence
		outputs = append(outputs, output)
	}
	sort.Slice(outputs, func(i, j int) bool {
		if outputs[i].Sequence != outputs[j].Sequence {
			return outputs[i].Sequence < outputs[j].Sequence
		}
		return outputs[i].ID < outputs[j].ID
	})
	for _, output := range outputs {
		if err := emit(output); err != nil {
			return err
		}
		if err := d.store.update(func(current *State) error {
			k := key(d.cfg.ConversationID, output.ID)
			if output.QuestionID != "" {
				r, ok := current.Requests[k]
				if !ok || r.Hash != output.QuestionHash {
					return ErrStale
				}
				r.Exported = true
				putRecord(current, "requests", &current.Requests, k, r)
			} else {
				p, ok := current.Publications[k]
				if !ok || p.Text != output.Text {
					return ErrStale
				}
				p.Exported = true
				putRecord(current, "publications", &current.Publications, k, p)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (d *DiscussionController) PublicationExported(id string, question bool) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st, err := d.store.readView()
	if err != nil {
		return false, err
	}
	k := key(d.cfg.ConversationID, id)
	if question {
		return st.Requests[k].Exported, nil
	}
	return st.Publications[k].Exported, nil
}

func discussionOutputSource(st *State, conversation string, a Admission) DiscussionOutputSource {
	if a.Origin != nil {
		output := DiscussionOutputSource{AdmissionID: a.Origin.RequestID}
		for _, worker := range st.Workers {
			if worker.Admission != a.Sequence || worker.JobID == "" || worker.Reserved {
				continue
			}
			if output.JobID != "" && output.JobID != worker.JobID {
				return DiscussionOutputSource{AdmissionID: a.Origin.RequestID}
			}
			output.JobID, output.AttemptID = worker.JobID, worker.AttemptID
		}
		return output
	}
	if wakeID, ok := strings.CutPrefix(a.SourceID, "wake:"); ok {
		wake := st.Wakes[key(conversation, wakeID)]
		event := st.Events[key(conversation, wake.EventID)]
		return DiscussionOutputSource{AdmissionID: wakeID, JobID: event.JobID, AttemptID: event.AttemptID}
	}
	return DiscussionOutputSource{}
}
