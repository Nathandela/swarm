package skeleton

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/persist"
)

const claudeDiagnosticsFile = "claude-model-diagnostics.jsonl"

var errClaudeFailureModelPending = errors.New("account recovery: waiting for native failed-request model evidence")

// Prompt identity comes from the authenticated native StopFailure hook. The
// transcript's current primary ancestry and request ID then identify one
// diagnostic, without relying on timestamps or a stale assistant model.
func readClaudeTerminalFailure(reader io.Reader, conversation, prompt, class string) (string, bool, error) {
	if !adapter.IsCanonicalConversationID(prompt) {
		return "", false, errClaudeFailureModelPending
	}
	scanner := bufio.NewScanner(io.LimitReader(reader, accountHistoryMaxBytes+1))
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	parents := map[string]string{}
	root, latestPrompt := "", ""
	var terminal struct {
		Type, UUID, ParentUUID, SessionID, PromptID, RequestID, Error string
		IsAPIError, Sidechain                                         bool
		Status                                                        int
	}
	for scanner.Scan() {
		var row struct {
			Type       string `json:"type"`
			UUID       string `json:"uuid"`
			ParentUUID string `json:"parentUuid"`
			SessionID  string `json:"sessionId"`
			PromptID   string `json:"promptId"`
			RequestID  string `json:"requestId"`
			Error      string `json:"error"`
			IsAPIError bool   `json:"isApiErrorMessage"`
			Sidechain  bool   `json:"isSidechain"`
			Status     int    `json:"apiErrorStatus"`
		}
		if !accountconfig.ValidClaudeNativeProofJSON(scanner.Bytes()) || json.Unmarshal(scanner.Bytes(), &row) != nil {
			return "", false, errClaudeFailureModelPending
		}
		if row.Sidechain || (row.Type != "user" && row.Type != "assistant") {
			continue
		}
		if row.SessionID != conversation || !adapter.IsCanonicalConversationID(row.UUID) {
			return "", false, errClaudeFailureModelPending
		}
		if _, duplicate := parents[row.UUID]; duplicate || len(parents) >= 65536 {
			return "", false, errClaudeFailureModelPending
		}
		parents[row.UUID] = row.ParentUUID
		if row.Type == "user" && row.PromptID != "" {
			latestPrompt = row.PromptID
			if row.PromptID == prompt {
				if root != "" {
					return "", false, errClaudeFailureModelPending
				}
				root = row.UUID
			}
		}
		terminal.Type, terminal.UUID, terminal.ParentUUID, terminal.SessionID, terminal.PromptID, terminal.RequestID, terminal.Error = row.Type, row.UUID, row.ParentUUID, row.SessionID, row.PromptID, row.RequestID, row.Error
		terminal.IsAPIError, terminal.Sidechain, terminal.Status = row.IsAPIError, row.Sidechain, row.Status
	}
	if scanner.Err() != nil {
		return "", false, errClaudeFailureModelPending
	}
	if latestPrompt != "" && latestPrompt != prompt {
		return "", true, nil
	}
	if root == "" || terminal.Type != "assistant" || !terminal.IsAPIError || terminal.Error != class || terminal.RequestID == "" {
		return "", false, errClaudeFailureModelPending
	}
	if class == "rate_limit" && terminal.Status != 429 || class == "authentication_failed" && terminal.Status != 401 {
		return "", false, errClaudeFailureModelPending
	}
	for ancestor, steps := terminal.ParentUUID, 0; ancestor != "" && steps < len(parents); steps++ {
		if ancestor == root {
			return terminal.RequestID, false, nil
		}
		parent, exists := parents[ancestor]
		if !exists {
			break
		}
		ancestor = parent
	}
	return "", false, errClaudeFailureModelPending
}

func (m *accountRotationManager) claudeFailedRequestModel(meta persist.Meta, input accountInboxRecord) (string, bool, error) {
	if meta.AccountBinding == nil || meta.CLIIdentity == nil || meta.CLIIdentity.Version != "2.1.289" || !adapter.IsCanonicalConversationID(input.TurnID) {
		return "", false, errClaudeFailureModelPending
	}
	profile, err := accountconfig.NativeHistoryAuthority(m.w.stateDir, meta.AccountProjectionRef, meta.AgentType, meta.ProviderCwd(), meta.AccountBinding.ConfigurationGeneration)
	if err != nil || profile == "" {
		return "", false, errClaudeFailureModelPending
	}
	resolver := newAccountResumeHistoryResolver(m.w.stateDir, nil)
	path, outcome := resolver.LocateTranscript(meta, input.Conversation)
	if outcome != resumeHistoryFound {
		return "", false, errClaudeFailureModelPending
	}
	root, err := historyOpenRoot(profile)
	if err != nil {
		return "", false, errClaudeFailureModelPending
	}
	defer func() { _ = root.Close() }()
	rel, err := filepath.Rel(profile, path)
	if err != nil {
		return "", false, errClaudeFailureModelPending
	}
	file, err := historyOpenFile(root, rel)
	if err != nil {
		return "", false, errClaudeFailureModelPending
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || info.Size() > accountHistoryMaxBytes {
		return "", false, errClaudeFailureModelPending
	}
	class := "rate_limit"
	if input.Class == "auth-invalid" {
		class = "authentication_failed"
	}
	var transcript io.Reader = file
	if info.Size() > 16<<20 {
		tail := bufio.NewReaderSize(io.NewSectionReader(file, info.Size()-(16<<20), 16<<20), 64<<10)
		for {
			_, err := tail.ReadSlice('\n')
			if err == nil {
				break
			}
			if !errors.Is(err, bufio.ErrBufferFull) {
				return "", false, errClaudeFailureModelPending
			}
		}
		transcript = tail
	}
	request, stale, err := readClaudeTerminalFailure(transcript, input.Conversation, input.TurnID, class)
	if err != nil || stale {
		return "", stale, err
	}
	state, err := openAccountRecoveryRoot(m.w.stateDir)
	if err != nil {
		return "", false, errClaudeFailureModelPending
	}
	defer func() { _ = state.Close() }()
	diagnostic, err := historyOpenFile(state, filepath.Join(meta.ID, claudeDiagnosticsFile))
	if err != nil {
		return "", false, errClaudeFailureModelPending
	}
	defer func() { _ = diagnostic.Close() }()
	proof, err := accountconfig.ReadClaudeFailureModel(diagnostic, request, class)
	if err != nil || !exactAccountModel(proof.Model) {
		return "", false, errClaudeFailureModelPending
	}
	return proof.Model, false, nil
}
