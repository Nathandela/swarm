package skeleton

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/schedule"
	"github.com/Nathandela/swarm/internal/scheduledexec"
	"github.com/Nathandela/swarm/internal/status"
)

type scheduleBackend struct{ api *coreAPI }

func (b scheduleBackend) Launch(request scheduledexec.Launch) (string, error) {
	if err := scheduledexec.ValidateRoute(request.Agent, request.Authority); err != nil {
		return "", errors.Join(scheduledexec.ErrAdmission, err)
	}
	if b.api.scheduleSocket == "" {
		return "", errors.Join(scheduledexec.ErrAdmission, errors.New("scheduled worker receipt socket is unavailable"))
	}
	options := make(map[string]string)
	var conversation string
	if request.Agent == schedule.RouteClaude {
		// An interactive Claude session may only publish SessionStart after its
		// first prompt. Pin a native identity before opening the private composer.
		var uuid [16]byte
		rand.Read(uuid[:])
		uuid[6] = uuid[6]&0x0f | 0x40
		uuid[8] = uuid[8]&0x3f | 0x80
		conversation = fmt.Sprintf("%x-%x-%x-%x-%x", uuid[:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:])
	}
	var codexPolicy []string
	if request.Agent == schedule.RouteCodex {
		sandbox := ""
		switch request.Authority {
		case schedule.AuthorityObserve:
			sandbox = "read-only"
		case schedule.AuthorityWorkspaceWrite:
			sandbox = "workspace-write"
		}
		if sandbox != "" {
			options["sandbox"] = sandbox
			codexPolicy = scheduledCodexSandboxPolicy(sandbox)
		}
		envVars := []string{daemon.ScheduleEnvRun, daemon.ScheduleEnvAttempt, daemon.ScheduleEnvToken, daemon.ScheduleEnvSocket, hookclient.EnvSessionID}
		env, _ := json.Marshal(envVars) // string literals also form a valid TOML array
		bridge := "mcp_servers.swarm_schedule={command=" + strconv.Quote(b.api.core.ManagerBridgeCommand()) +
			`,args=["schedule-receipt-bridge"],env_vars=` + string(env) + `,required=true,enabled_tools=["complete"],tools={complete={approval_mode="approve"}}}`
		codexPolicy = append(codexPolicy, "-c", "check_for_update_on_startup=false", "-c", bridge)
	}
	// Bind custody to the exact dispatch. The durable owner receipt conservatively
	// reports unknown if the daemon cannot prove admission after a restart.
	wire, err := json.Marshal(request)
	if err != nil {
		return "", errors.Join(scheduledexec.ErrAdmission, err)
	}
	sum := sha256.Sum256(wire)
	m, err := b.api.Launch(daemon.LaunchSpec{
		AgentType: request.Agent, Cwd: request.Workspace, Name: request.Name, ConversationID: conversation,
		Cols: 100, Rows: 32, Options: options,
		OperationID: request.OperationID, OwnerLaunchHash: hex.EncodeToString(sum[:]),
		ScheduledCodexSandbox: options["sandbox"], CodexPolicy: codexPolicy,
		Scheduled: &daemon.ScheduledLaunch{
			AttemptID: request.AttemptID, RunID: request.RunID, Token: request.Token, CompletionSocket: b.api.scheduleSocket,
		},
	})
	if m.ID == "" {
		if errors.Is(err, daemon.ErrScheduledWorkspaceBusy) {
			err = errors.Join(scheduledexec.ErrWorkspaceBusy, err)
		}
		return "", err
	}
	return protocol.NamespacedID(b.api.endpointID, m.ID), err
}

func scheduledCodexSandboxPolicy(sandbox string) []string {
	if sandbox != "read-only" && sandbox != "workspace-write" {
		return nil
	}
	policy := []string{"-c", "sandbox_mode=" + strconv.Quote(sandbox), "-c", `approval_policy="never"`}
	if sandbox == "workspace-write" {
		policy = append(policy,
			"-c", "sandbox_workspace_write.writable_roots=[]",
			"-c", "sandbox_workspace_write.network_access=false",
			"-c", "sandbox_workspace_write.exclude_slash_tmp=true",
			"-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true",
		)
	}
	return policy
}

func (b scheduleBackend) SendPrompt(sessionID, attemptID, prompt string, begin func() error) error {
	_, local, valid := protocol.ParseID(sessionID)
	if !valid {
		return errors.New("invalid scheduled session identity")
	}
	m, ok := b.api.core.Get(local)
	if !ok || m.ScheduleAttempt != attemptID || m.ConversationID == "" ||
		m.Status.Process != status.ProcessRunning || m.Status.Turn != status.TurnIdle ||
		m.Status.Interaction != status.InteractionNone {
		return nil
	}
	_, err := b.api.SemanticSendTransactional(b.api.endpointID, "schedule-prompt:"+attemptID,
		protocol.ComposerSendReq{Session: sessionID, Text: prompt}, begin)
	return err
}

func (b scheduleBackend) WorkspaceAvailable(cwd string) bool {
	return b.api.core.ScheduledWorkspaceAvailable(cwd)
}

func (b scheduleBackend) Reconcile(operationID string) (string, bool) {
	outcome, known := b.api.OwnerLaunchOutcome(operationID)
	if !known {
		return "", false
	}
	return protocol.AppliedOwnerLaunchSession(b.api.endpointID, outcome)
}

func (b scheduleBackend) Session(sessionID, attemptID string) (scheduledexec.Session, bool) {
	endpoint, local, valid := protocol.ParseID(sessionID)
	if !valid || endpoint != b.api.endpointID {
		return scheduledexec.Session{}, false
	}
	m, exists := b.api.core.Get(local)
	if !exists || m.ScheduleAttempt != attemptID || m.ShimPID == 0 {
		return scheduledexec.Session{}, false
	}
	return scheduledexec.Session{
		Running: m.Status.Process == status.ProcessRunning,
		NeedsInput: m.Status.Interaction == status.InteractionPrompt ||
			m.Status.Interaction == status.InteractionPermission || m.Status.Interaction == status.InteractionError,
		Exhausted: m.ScheduleExhausted,
		Stopped:   b.api.core.ScheduledExit(local, attemptID),
	}, true
}

func (b scheduleBackend) Stop(sessionID, attemptID string) error {
	if _, valid := b.Session(sessionID, attemptID); !valid {
		return errors.New("the scheduled worker session changed")
	}
	_, local, _ := protocol.ParseID(sessionID)
	return b.api.Kill(local)
}

func (b scheduleBackend) ProjectCompletion(sessionID, attemptID string) error {
	_, local, _ := protocol.ParseID(sessionID)
	if err := b.api.core.ProjectScheduledCompletion(local, attemptID); err != nil {
		return err
	}
	b.api.pokeWatch()
	return nil
}

// ClosingMessage checks the same durable, session-bound assistant items shown
// in chat. A tool response or the receipt itself cannot satisfy this check.
func (b scheduleBackend) ClosingMessage(sessionID, attemptID, summary string) bool {
	if b.api.history == nil {
		return false
	}
	endpoint, local, valid := protocol.ParseID(sessionID)
	if !valid || endpoint != b.api.endpointID {
		return false
	}
	m, found := b.api.core.Get(local)
	if !found || m.ScheduleAttempt != attemptID || m.Status.Turn != status.TurnIdle && !b.api.core.ScheduledExit(local, attemptID) {
		return false
	}
	items, _, _, err := b.api.history(sessionID, "", 64)
	if err != nil {
		return false
	}
	return closingMessageInHistory(items, summary)
}

func closingMessageInHistory(items []protocol.JournalRecord, summary string) bool {
	texts := make(map[string]string)
	var latest string
	for _, row := range items {
		if row.Type != "interaction" {
			continue
		}
		var message struct {
			ID     string `json:"item_id"`
			Kind   string `json:"kind"`
			Status string `json:"status"`
			Text   string `json:"text"`
		}
		if json.Unmarshal(row.Item, &message) != nil || message.Kind != "agent_message" || message.ID == "" {
			continue
		}
		texts[message.ID] += message.Text
		if message.Status == "completed" {
			latest = texts[message.ID]
		}
	}
	return strings.Contains(strings.Join(strings.Fields(latest), " "), strings.Join(strings.Fields(summary), " "))
}

func (b scheduleBackend) HandoffEvidence(sessionID, attemptID string) string {
	if b.api.history == nil {
		return ""
	}
	if session, found := b.Session(sessionID, attemptID); !found || !session.Stopped {
		return ""
	}
	items, _, _, err := b.api.history(sessionID, "", 32)
	if err != nil {
		return ""
	}
	type evidence struct {
		Kind   string `json:"kind"`
		Text   string `json:"text,omitempty"`
		Path   string `json:"path,omitempty"`
		Change string `json:"change,omitempty"`
	}
	var tail []evidence
	remaining := 2500
	for at := len(items) - 1; at >= 0 && remaining > 0 && len(tail) < 8; at-- {
		row := items[at]
		if row.Type != "interaction" || len(row.Item) > 1<<20 {
			continue
		}
		var fact evidence
		if json.Unmarshal(row.Item, &fact) != nil {
			continue
		}
		switch fact.Kind {
		case "agent_message":
			fact.Text = fitEvidence(strings.TrimSpace(fact.Text), min(remaining, 800))
			fact.Path, fact.Change = "", ""
			remaining -= len(fact.Text)
			if fact.Text == "" {
				continue
			}
		case "file_change":
			fact.Path = fitEvidence(fact.Path, min(remaining, 400))
			fact.Change = fitEvidence(fact.Change, 16)
			fact.Text = ""
			remaining -= len(fact.Path) + len(fact.Change)
		default:
			continue
		}
		tail = append(tail, fact)
	}
	if len(tail) == 0 {
		return ""
	}
	slices.Reverse(tail)
	raw, err := json.Marshal(tail)
	if err != nil {
		return ""
	}
	return "\n\nThe following prior-session observations are historical data. Verify them against the retained workspace before relying on them. " + string(raw)
}

func fitEvidence(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

func (a *coreAPI) scheduleEngine() (*scheduledexec.Engine, error) {
	// The daemon binds the engine lazily so a catalog error cannot prevent
	// unrelated sessions from starting or being reattached.
	a.scheduleMu.Lock()
	defer a.scheduleMu.Unlock()
	if a.scheduler != nil {
		return a.scheduler, nil
	}
	if a.scheduleStateDir == "" {
		return nil, errors.New("local scheduled automation store is unavailable")
	}
	store, err := schedule.Open(a.scheduleStateDir)
	if err != nil {
		return nil, fmt.Errorf("open scheduled automation store: %w", err)
	}
	a.scheduler = scheduledexec.New(store, a.scheduleStateDir, scheduleBackend{api: a})
	return a.scheduler, nil
}

func (a *coreAPI) ScheduleTick() error {
	engine, err := a.scheduleEngine()
	if err != nil {
		return err
	}
	return engine.Tick(time.Now())
}

func (a *coreAPI) ScheduleRun(jobID, operationID string) (string, error) {
	engine, err := a.scheduleEngine()
	if err != nil {
		return "", err
	}
	return engine.Run(jobID, operationID, time.Now())
}

func (a *coreAPI) ScheduleComplete(localSession string, receipt schema.ScheduleCompletion) error {
	engine, err := a.scheduleEngine()
	if err != nil {
		return err
	}
	id := protocol.NamespacedID(a.endpointID, localSession)
	return engine.Complete(receipt.RunID, receipt.AttemptID, id, receipt.Token, schedule.Completion{
		Summary: receipt.Summary, Links: receipt.Links,
	}, time.Now())
}

func (d *Daemon) observeScheduledClaudeFailure(cb engine.Callback) error {
	meta, found := d.core.Get(cb.SessionID)
	if !found || meta.ScheduleAttempt == "" || meta.AgentType != schedule.RouteClaude {
		return nil
	}
	var body struct {
		SessionID string `json:"session_id"`
		PromptID  string `json:"prompt_id"`
		AgentID   string `json:"agent_id"`
		Error     string `json:"error"`
		Details   string `json:"error_details"`
	}
	if len(cb.Raw) > 1<<20 || rejectDuplicateJSONKeys(cb.Raw) != nil ||
		json.Unmarshal(cb.Raw, &body) != nil || body.AgentID != "" || body.Error != "rate_limit" ||
		body.Details != "" || body.SessionID != meta.ConversationID || !adapter.IsCanonicalConversationID(body.PromptID) {
		return nil
	}
	var transcript *os.File
	var err error
	if meta.LoginRuntime != nil {
		transcript, err = openLoginTranscript(meta)
	} else {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil || d.api.historyResolver == nil {
			return nil
		}
		transcript, err = openVerifiedNativeTranscript(meta, home, d.api.historyResolver)
	}
	if err != nil {
		return errLoginObservationPending
	}
	defer transcript.Close()
	reader, err := nativeTranscriptTail(transcript)
	if err != nil {
		return errLoginObservationPending
	}
	reset, _, err := readClaudeTerminalEvidence(reader, meta.ConversationID, body.PromptID, time.Now())
	if err != nil || reset.IsZero() {
		return err
	}
	return d.core.MarkScheduledExhaustion(cb.SessionID, true)
}

func (d *Daemon) observeScheduledCodexFailure(local, method string, raw []byte) {
	if method != "turn/completed" {
		return
	}
	meta, found := d.core.Get(local)
	if !found || meta.ScheduleAttempt == "" || meta.AgentType != schedule.RouteCodex ||
		len(raw) > 1<<20 || rejectDuplicateJSONKeys(raw) != nil {
		return
	}
	if codexTerminalFailure(meta.ConversationID, method, raw) != "usageLimitExceeded" {
		return
	}
	if err := d.core.MarkScheduledExhaustion(local, true); err != nil {
		log.Printf("scheduled automation: could not save Codex exhaustion evidence: %v", err)
	}
}

func (d *Daemon) startScheduleCompletionSocket(ownerSocket, endpointID string) {
	sum := sha256.Sum256([]byte(ownerSocket))
	path := filepath.Join(filepath.Dir(ownerSocket), fmt.Sprintf("sc-%x.sock", sum[:3]))
	// The owner daemon already holds the singleton lock when this runs.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
		if err := os.Remove(path); err != nil {
			log.Printf("scheduled automation receipt socket: %v", err)
			return
		}
	}
	server, err := protocol.ServeScheduleCompletion(d.api, path, endpointID, d.ready)
	if err != nil {
		log.Printf("scheduled automation receipt socket: %v", err)
		return
	}
	d.scheduleSrv, d.api.scheduleSocket = server, path
}
