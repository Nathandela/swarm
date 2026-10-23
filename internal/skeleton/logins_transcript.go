package skeleton

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

const loginTranscriptTailBytes = 8 << 20

var errLoginObservationPending = errors.New("login: native terminal failure evidence is not available yet")

func openLoginTranscript(meta persist.Meta) (*os.File, error) {
	if meta.LoginRuntime == nil {
		return nil, logins.ErrIneligible
	}
	resolver := newFilesystemResumeHistoryResolver(meta.LoginRuntime.Home, defaultResumeHistoryLimits)
	resolver.privateProvider = meta.AgentType
	path, outcome := resolver.LocateTranscript(meta, meta.ConversationID)
	if outcome != resumeHistoryFound {
		return nil, errLoginObservationPending
	}
	rel, err := filepath.Rel(meta.LoginRuntime.Home, path)
	if err != nil || !filepath.IsLocal(rel) {
		return nil, logins.ErrUnsafePath
	}
	root, err := os.OpenRoot(meta.LoginRuntime.Home)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	parent, err := openRecoverySubdir(root, filepath.Dir(rel))
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()
	f, err := parent.OpenFile(filepath.Base(rel), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	stat, ok := infoSysNative(info)
	if err != nil || !ok || !info.Mode().IsRegular() || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		_ = f.Close()
		return nil, logins.ErrUnsafePath
	}
	return f, nil
}
func openRecoverySubdir(root *os.Root, path string) (*os.Root, error) {
	if !filepath.IsLocal(path) {
		return nil, logins.ErrUnsafePath
	}
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	if path == "." {
		return current, nil
	}
	for _, component := range strings.Split(path, string(filepath.Separator)) {
		next, err := openRecoveryDir(current, component)
		_ = current.Close()
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}
func nativeTranscriptTail(file *os.File) (*bufio.Reader, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	start := max(int64(0), info.Size()-loginTranscriptTailBytes)
	if _, err = file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(io.LimitReader(file, loginTranscriptTailBytes), 64<<10)
	if start > 0 {
		for {
			_, err := reader.ReadSlice('\n')
			if err == bufio.ErrBufferFull {
				continue
			}
			if err != nil {
				return nil, err
			}
			break
		}
	}
	return reader, nil
}
func readNativeLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(line)+len(chunk) > 1<<20 {
			return nil, errLoginObservationPending
		}
		line = append(line, chunk...)
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
}

// Source sample: anthropics/claude-code#97975. Generic StopFailure rate_limit
// also means credits_required (#98012), so only structured rejected windows act.
func readClaudeTerminalEvidence(reader *bufio.Reader, conversation, prompt string, now time.Time) (time.Time, bool, error) {
	if !adapter.IsCanonicalConversationID(prompt) {
		return time.Time{}, false, nil
	}
	parents := map[string]string{}
	root, latestPrompt := "", ""
	terminalID, terminalParent := "", ""
	var reset time.Time
	authFailed := false
	for {
		line, err := readNativeLine(reader)
		if len(bytes.TrimSpace(line)) > 0 {
			if rejectDuplicateJSONKeys(line) != nil {
				return time.Time{}, false, errLoginObservationPending
			}
			var row struct {
				Type       string `json:"type"`
				UUID       string `json:"uuid"`
				ParentUUID string `json:"parentUuid"`
				SessionID  string `json:"sessionId"`
				PromptID   string `json:"promptId"`
				Sidechain  bool   `json:"isSidechain"`
				APIError   bool   `json:"isApiErrorMessage"`
				Error      string `json:"error"`
				Quota      struct {
					Status string `json:"status"`
					Type   string `json:"rateLimitType"`
					Reset  int64  `json:"resetsAt"`
				} `json:"quotaLimits"`
			}
			if json.Unmarshal(line, &row) != nil {
				return time.Time{}, false, errLoginObservationPending
			}
			if !row.Sidechain && (row.Type == "user" || row.Type == "assistant" || row.Type == "attachment") {
				if row.SessionID != conversation || !adapter.IsCanonicalConversationID(row.UUID) || len(parents) >= 65536 {
					return time.Time{}, false, errLoginObservationPending
				}
				if row.Type == "attachment" && !adapter.IsCanonicalConversationID(row.ParentUUID) {
					return time.Time{}, false, errLoginObservationPending
				}
				if _, duplicate := parents[row.UUID]; duplicate {
					return time.Time{}, false, errLoginObservationPending
				}
				parents[row.UUID] = row.ParentUUID
				// Native attachments connect the prompt ancestry without ending a turn.
				if row.Type != "attachment" {
					if row.Type == "user" && row.PromptID != "" {
						latestPrompt = row.PromptID
						root = row.UUID
					}
					terminalID, terminalParent = row.UUID, row.ParentUUID
					reset = time.Time{}
					authFailed = false
					if row.Type == "assistant" && row.APIError && row.Error == "authentication_failed" {
						authFailed = true
					}
					if row.Type == "assistant" && row.APIError && row.Error == "rate_limit" && row.Quota.Status == "rejected" {
						switch row.Quota.Type {
						case "five_hour", "seven_day", "seven_day_opus", "seven_day_sonnet", "seven_day_overage_included":
							candidate := time.Unix(row.Quota.Reset, 0)
							if candidate.After(now) && candidate.Before(now.Add(30*24*time.Hour)) {
								reset = candidate
							}
						}
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return time.Time{}, false, errLoginObservationPending
		}
	}
	if latestPrompt != prompt || root == "" {
		return time.Time{}, false, nil
	}
	if terminalID == root || (reset.IsZero() && !authFailed) {
		return time.Time{}, false, nil
	}
	for parent, steps := terminalParent, 0; parent != "" && steps < len(parents); steps++ {
		if parent == root {
			return reset, authFailed, nil
		}
		next, ok := parents[parent]
		if !ok {
			break
		}
		parent = next
	}
	return time.Time{}, false, nil
}
func (m *loginManager) observeClaudeFailure(cb engine.Callback) error {
	meta, ok := m.get(cb.SessionID)
	if !ok || meta.LoginRuntime == nil || meta.AgentType != logins.ProviderClaude {
		return nil
	}
	var body struct {
		SessionID string `json:"session_id"`
		PromptID  string `json:"prompt_id"`
		AgentID   string `json:"agent_id"`
		Error     string `json:"error"`
		Details   string `json:"error_details"`
	}
	if len(cb.Raw) > 1<<20 || rejectDuplicateJSONKeys(cb.Raw) != nil || json.Unmarshal(cb.Raw, &body) != nil || body.AgentID != "" || (body.Error != "rate_limit" && body.Error != "authentication_failed") || body.SessionID != meta.ConversationID || !adapter.IsCanonicalConversationID(body.PromptID) || (body.Error == "rate_limit" && body.Details != "") {
		return nil
	}
	file, err := openLoginTranscript(meta)
	if err != nil {
		return errLoginObservationPending
	}
	defer func() { _ = file.Close() }()
	reader, err := nativeTranscriptTail(file)
	if err != nil {
		return errLoginObservationPending
	}
	reset, authFailed, err := readClaudeTerminalEvidence(reader, meta.ConversationID, body.PromptID, time.Now())
	if err != nil {
		return err
	}
	if authFailed && body.Error == "authentication_failed" {
		return m.recordAuthFailure(meta)
	}
	if reset.IsZero() || body.Error != "rate_limit" {
		return nil
	}
	return m.recordExhaustion(meta, reset)
}
func (m *loginManager) recordExhaustion(meta persist.Meta, reset time.Time) error {
	return m.recordExhaustionFact(meta, reset, true, false)
}

// Sparse quota metadata may refine an existing terminal fact; it cannot declare
// a new rejection after the prior window has reset.
func (m *loginManager) recordExhaustionFact(meta persist.Meta, reset time.Time, terminal, nonRolling bool) error {
	for attempt := 0; attempt < 3; attempt++ {
		r, err := m.store.Snapshot()
		if err != nil {
			return err
		}
		p := r.Providers[meta.AgentType]
		if meta.LoginRuntime == nil || p.Epoch != meta.LoginRuntime.Epoch {
			return nil
		}
		for _, login := range r.Logins {
			if login.ID != p.ActiveID {
				continue
			}
			if !terminal && (!login.LimitKnown || !login.ExhaustedUntil.IsZero() && !time.Now().Before(login.ExhaustedUntil)) {
				return nil
			}
			if login.LimitNonRolling && !nonRolling {
				return nil
			}
			if login.LimitKnown && login.LimitNonRolling == nonRolling && (reset.Equal(login.ExhaustedUntil) || !reset.IsZero() && !login.ExhaustedUntil.IsZero() && !reset.After(login.ExhaustedUntil)) {
				return nil
			}
			_, err = m.store.UpdateExhaustion(r.Revision, login.ID, reset, nonRolling)
			if errors.Is(err, logins.ErrConflict) {
				break
			}
			return err
		}
	}
	return errLoginObservationPending
}

// Exact native resume owns the current model selection. Codex v0.163.0's
// tui/app_server_session.rs ResumeModelSettings::RestoreFromThread deliberately
// omits model overrides, allowing app-server to restore the saved thread settings.
// Passing a launch flag here would override a later in-conversation model change.
func switchResumeOptions(source persist.Meta, _ logins.Runtime, endpoint string) (map[string]string, error) {
	options := map[string]string{protocol.OptionResumeFrom: endpoint + "/" + source.ID}
	if source.AgentType == logins.ProviderClaude || source.AgentType == logins.ProviderCodex {
		options["model"] = ""
	}
	return options, nil
}

func readClaudeRejectedQuota(reader *bufio.Reader, conversation, prompt string, now time.Time) (time.Time, error) {
	reset, _, err := readClaudeTerminalEvidence(reader, conversation, prompt, now)
	return reset, err
}
func (m *loginManager) recordAuthFailure(meta persist.Meta) error {
	for attempt := 0; attempt < 3; attempt++ {
		r, err := m.store.Snapshot()
		if err != nil {
			return err
		}
		p := r.Providers[meta.AgentType]
		if meta.LoginRuntime == nil || p.Epoch != meta.LoginRuntime.Epoch {
			return nil
		}
		for _, login := range r.Logins {
			if login.ID != p.ActiveID {
				continue
			}
			if login.AuthState == logins.AuthNeedsLogin {
				return nil
			}
			_, err = m.store.UpdateAvailability(r.Revision, login.ID, logins.AuthNeedsLogin, login.LimitKnown, login.ExhaustedUntil)
			if errors.Is(err, logins.ErrConflict) {
				break
			}
			return err
		}
	}
	return errLoginObservationPending
}
