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

func discussionInputHash(text string) string {
	hash := sha256.Sum256([]byte(text))
	return hex.EncodeToString(hash[:])
}

func (d *Daemon) discussionWorkerHistory(meta persist.Meta, req schema.InteractionHistoryReq) (schema.NativeHistoryPage, protocol.ErrorCode, error) {
	scope := req.Worker
	unavailable := errors.New("worker turn evidence unavailable")
	empty := schema.NativeHistoryPage{}
	parent, ok := d.core.Get(scope.Parent)
	instance, exact := d.sessionInstance(meta.ID)
	if req.Limit != 1 || req.NativeCursor != "" || req.BeforeItem != "" ||
		len(scope.PromptHash) != sha256.Size*2 || scope.TurnID != "" && !adapter.IsCanonicalConversationID(scope.TurnID) ||
		!ok || parent.Role != "manager" || !strings.HasPrefix(parent.ManagerConversationID, "discord-teammate-") ||
		meta.Role == "manager" || meta.AgentType != "codex" || meta.SpawnedFrom != parent.ID || meta.SpawnIntent != schema.SpawnIntentDelegate ||
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
	reader := bufio.NewReaderSize(io.LimitReader(file, loginTranscriptTailBytes), 64<<10)
	if scope.TurnID != "" {
		reader, err = nativeTranscriptTail(file)
		if err != nil {
			return empty, protocol.CodeUnavailable, unavailable
		}
	}
	evidence, err := readDiscussionCodexEvidence(reader, scope.TurnID, scope.PromptHash)
	current, exists := d.core.Get(meta.ID)
	currentInstance, bound := d.sessionInstance(meta.ID)
	if err != nil || !exists || !bound || currentInstance != instance || current.ConversationID != meta.ConversationID || current.SpawnedFrom != meta.SpawnedFrom {
		return empty, protocol.CodeUnavailable, unavailable
	}
	page := schema.NativeHistoryPage{Version: 1, Scope: "authorized_worker_turn", Floor: true, Items: []json.RawMessage{}}
	if evidence.TurnID != "" {
		page.Worker = &evidence
	}
	return page, "", nil
}

func readDiscussionCodexEvidence(reader *bufio.Reader, targetTurn, promptHash string) (schema.WorkerTurnEvidence, error) {
	var result schema.WorkerTurnEvidence
	var turn string
	var matched bool
	for {
		line, err := readNativeLine(reader)
		if err == io.EOF {
			return result, nil
		}
		if err != nil {
			return result, err
		}
		var row struct {
			Type    string `json:"type"`
			Payload struct {
				Type             string            `json:"type"`
				TurnID           string            `json:"turn_id"`
				Message          string            `json:"message"`
				LastAgentMessage string            `json:"last_agent_message"`
				Error            json.RawMessage   `json:"error"`
				Images           []json.RawMessage `json:"images"`
				FileIDs          []string          `json:"file_ids"`
				LocalImages      []string          `json:"local_images"`
				Audio            []json.RawMessage `json:"audio"`
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
			if turn != "" && (targetTurn == "" || turn == targetTurn) {
				return result, nil
			}
			if !adapter.IsCanonicalConversationID(p.TurnID) {
				return result, errLoginObservationPending
			}
			turn = p.TurnID
		case "user_message":
			if turn == "" || targetTurn != "" && turn != targetTurn {
				continue
			}
			if matched || discussionInputHash(p.Message) != promptHash || len(p.Images)+len(p.FileIDs)+len(p.LocalImages)+len(p.Audio) != 0 {
				return result, nil
			}
			matched = true
		case "task_complete", "turn_aborted":
			if turn == "" || targetTurn != "" && turn != targetTurn {
				continue
			}
			if p.TurnID != turn || !matched {
				return result, nil
			}
			result.TurnID = turn
			result.State = "interrupted"
			if p.Type == "task_complete" {
				result.State = "completed"
				text := strings.TrimSpace(p.LastAgentMessage)
				end := min(len(text), 4096)
				for end < len(text) && !utf8.RuneStart(text[end]) {
					end--
				}
				result.Text = text[:end]
				if len(p.Error) > 0 && string(p.Error) != "null" {
					result.State = "failed"
				}
			}
			return result, nil
		}
	}
}
