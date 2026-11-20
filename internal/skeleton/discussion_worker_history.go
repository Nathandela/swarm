package skeleton

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

type discussionCodexReceipt struct {
	InputID string `json:"input_id"`
	TurnID  string `json:"turn_id"`
}

func discussionInputHash(text string) string {
	hash := sha256.Sum256([]byte(text))
	return hex.EncodeToString(hash[:])
}

func (d *Daemon) discussionWorkerHistory(meta persist.Meta, req schema.InteractionHistoryReq) (schema.NativeHistoryPage, protocol.ErrorCode, error) {
	scope := *req.Worker
	if scope.PromptHash == "" && scope.TurnID == "" && scope.InputID == "" && meta.WorkerDiscussion == scope.ManagerConversation && meta.WorkerInstance == scope.SessionInstance && meta.WorkerAttempt != "" {
		scope.PromptHash = meta.WorkerPromptHash
	}
	unavailable := errors.New("worker turn evidence unavailable")
	empty := schema.NativeHistoryPage{}
	parent, ok := d.core.Get(scope.Parent)
	instance, exact := d.sessionInstance(meta.ID)
	if req.Limit != 1 || req.NativeCursor != "" || req.BeforeItem != "" ||
		len(scope.PromptHash) != sha256.Size*2 || len(scope.InputID) > 128 || scope.TurnID != "" && (!adapter.IsCanonicalConversationID(scope.TurnID) || scope.InputID == "") || scope.TurnID == "" && scope.InputID != "" ||
		!ok || parent.Role != "manager" || parent.ManagerConversationID != scope.ManagerConversation || !strings.HasPrefix(parent.ManagerConversationID, "discord-teammate-") ||
		meta.Role == "manager" || (meta.AgentType != "codex" && (meta.AgentType != "claude" || scope.TurnID != "")) || meta.SpawnedFrom != parent.ID || meta.SpawnIntent != schema.SpawnIntentDelegate ||
		!exact || instance != scope.SessionInstance || !adapter.IsCanonicalConversationID(meta.ConversationID) {
		return empty, protocol.CodeUnavailable, unavailable
	}

	var file *os.File
	var err error
	if meta.LoginRuntime != nil {
		file, err = openLoginTranscript(meta)
	} else if home, homeErr := os.UserHomeDir(); homeErr == nil && d.api.historyResolver != nil {
		file, err = openVerifiedNativeTranscript(meta, home, d.api.historyResolver)
	}
	if err != nil || file == nil {
		return empty, protocol.CodeUnavailable, unavailable
	}
	defer func() { _ = file.Close() }()
	evidence, limited, err := d.readDiscussionTranscript(file, meta, scope)
	current, exists := d.core.Get(meta.ID)
	currentInstance, bound := d.sessionInstance(meta.ID)
	if err != nil || !exists || !bound || currentInstance != instance || current.ConversationID != meta.ConversationID || current.SpawnedFrom != meta.SpawnedFrom {
		return empty, protocol.CodeUnavailable, unavailable
	}
	page := schema.NativeHistoryPage{Version: 1, Scope: "authorized_worker_turn", Floor: true, Items: []json.RawMessage{}, WorkerEvidenceLimited: limited}
	if evidence.TurnID != "" {
		page.Worker = &evidence
	}
	return page, "", nil
}

func readDiscussionCodexEvidence(reader *bufio.Reader, targetTurn, promptHash string) (schema.WorkerTurnEvidence, error) {
	scan := discussionTranscriptScan{windowEnd: discussionTranscriptMaxBytes}
	proof, err := scan.readCodex(reader, targetTurn, "", promptHash)
	if err == io.EOF {
		err = nil
	}
	return proof, err
}

func (scan *discussionTranscriptScan) readCodex(reader *bufio.Reader, targetTurn, inputID, promptHash string) (schema.WorkerTurnEvidence, error) {
	var result schema.WorkerTurnEvidence
	for {
		line, err := scan.readLine(reader)
		if err != nil {
			return result, err
		}
		var row struct {
			Type    string `json:"type"`
			Payload struct {
				Type             string            `json:"type"`
				TurnID           string            `json:"turn_id"`
				Message          string            `json:"message"`
				ClientID         string            `json:"client_id"`
				LastAgentMessage string            `json:"last_agent_message"`
				Error            json.RawMessage   `json:"error"`
				Images           []json.RawMessage `json:"images"`
				FileIDs          []string          `json:"file_ids"`
				LocalImages      []string          `json:"local_images"`
				Audio            []json.RawMessage `json:"audio"`
				Item             struct {
					Type     string `json:"type"`
					ID       string `json:"id"`
					ClientID string `json:"client_id"`
					Content  []struct {
						Type         string            `json:"type"`
						Text         string            `json:"text"`
						TextElements []json.RawMessage `json:"text_elements"`
					} `json:"content"`
				} `json:"item"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &row) != nil || rejectDuplicateJSONKeys(line) != nil {
			return result, errLoginObservationPending
		}
		if row.Type != "event_msg" {
			continue
		}
		p := row.Payload
		switch p.Type {
		case "task_started":
			if scan.turn != "" && (targetTurn == "" || scan.turn == targetTurn) {
				return result, nil
			}
			if !adapter.IsCanonicalConversationID(p.TurnID) {
				return result, errLoginObservationPending
			}
			scan.turn = p.TurnID
		case "item_completed":
			if p.Item.Type != "UserMessage" || scan.turn == "" || targetTurn != "" && scan.turn != targetTurn {
				continue
			}
			if p.TurnID != scan.turn || p.Item.ID == "" || inputID != "" && p.Item.ClientID != inputID || len(p.Item.Content) != 1 || p.Item.Content[0].Type != "text" ||
				len(p.Item.Content[0].TextElements) != 0 || scan.matched || discussionInputHash(p.Item.Content[0].Text) != promptHash {
				return result, nil
			}
			scan.matched = true
		case "user_message":
			if scan.turn == "" || targetTurn != "" && scan.turn != targetTurn {
				continue
			}
			if scan.matched || inputID != "" && p.ClientID != inputID || discussionInputHash(p.Message) != promptHash || len(p.Images)+len(p.FileIDs)+len(p.LocalImages)+len(p.Audio) != 0 {
				return result, nil
			}
			scan.matched = true
		case "task_complete", "turn_aborted":
			if scan.turn == "" || targetTurn != "" && scan.turn != targetTurn {
				continue
			}
			if p.TurnID != scan.turn || !scan.matched {
				return result, nil
			}
			result.TurnID = scan.turn
			result.State = "interrupted"
			if p.Type == "task_complete" {
				result.State = "completed"
				result.Text, result.TextTruncated = boundedDiscussionWorkerText(p.LastAgentMessage)
				if len(p.Error) > 0 && string(p.Error) != "null" {
					result.State = "failed"
				}
			}
			return result, nil
		}
	}
}

func boundedDiscussionWorkerText(text string) (string, bool) {
	text = strings.TrimSpace(text)
	end := min(len(text), 4096)
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end], end != len(text)
}
