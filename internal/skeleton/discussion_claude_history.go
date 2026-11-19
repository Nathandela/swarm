package skeleton

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func readDiscussionClaudeEvidence(reader *bufio.Reader, session, targetTurn, promptHash string) (schema.WorkerTurnEvidence, error) {
	scan := discussionTranscriptScan{windowEnd: discussionTranscriptMaxBytes}
	proof, err := scan.readClaude(reader, session, targetTurn, promptHash)
	if err == io.EOF {
		err = nil
	}
	return proof, err
}

func (scan *discussionTranscriptScan) readClaude(reader *bufio.Reader, session, targetTurn, promptHash string) (schema.WorkerTurnEvidence, error) {
	var none schema.WorkerTurnEvidence
	for {
		line, err := scan.readLine(reader)
		if err != nil {
			return none, err
		}
		var row struct {
			Type       string `json:"type"`
			SessionID  string `json:"sessionId"`
			UUID       string `json:"uuid"`
			ParentUUID string `json:"parentUuid"`
			PromptID   string `json:"promptId"`
			Sidechain  bool   `json:"isSidechain"`
			Meta       bool   `json:"isMeta"`
			APIError   bool   `json:"isApiErrorMessage"`
			Message    struct {
				Role    string          `json:"role"`
				Stop    string          `json:"stop_reason"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if rejectDuplicateJSONKeys(line) != nil || json.Unmarshal(line, &row) != nil {
			return none, errLoginObservationPending
		}
		if row.Sidechain || (row.Type != "user" && row.Type != "assistant" && row.Type != "attachment") {
			continue
		}
		if row.SessionID != session || !adapter.IsCanonicalConversationID(row.UUID) {
			return none, errLoginObservationPending
		}
		if scan.turn == "" {
			if row.Type != "user" || row.ParentUUID != "" || row.Meta || row.Message.Role != "user" {
				return none, nil
			}
			var prompt string
			if json.Unmarshal(row.Message.Content, &prompt) != nil {
				var blocks []struct{ Type, Text string }
				if json.Unmarshal(row.Message.Content, &blocks) != nil || len(blocks) != 1 || blocks[0].Type != "text" {
					return none, nil
				}
				prompt = blocks[0].Text
			}
			scan.turn = row.PromptID
			if scan.turn == "" {
				scan.turn = row.UUID
			}
			if !adapter.IsCanonicalConversationID(scan.turn) || targetTurn != "" && scan.turn != targetTurn || discussionInputHash(prompt) != promptHash {
				return none, nil
			}
			scan.previous = row.UUID
			continue
		}
		if row.ParentUUID != scan.previous || row.UUID == scan.previous {
			return none, nil
		}
		scan.previous = row.UUID
		var blocks []struct{ Type, Text string }
		if row.Type == "attachment" {
			continue
		}
		if json.Unmarshal(row.Message.Content, &blocks) != nil {
			return none, nil
		}
		if row.Type == "user" {
			if row.PromptID != "" || len(blocks) == 0 {
				return none, nil
			}
			for _, block := range blocks {
				if block.Type != "tool_result" {
					return none, nil
				}
			}
			continue
		}
		if row.PromptID != "" && row.PromptID != scan.turn {
			return none, nil
		}
		if row.APIError {
			return schema.WorkerTurnEvidence{TurnID: scan.turn, State: "failed"}, nil
		}
		if row.Message.Role == "assistant" && row.Message.Stop == "end_turn" {
			var text []string
			for _, block := range blocks {
				if block.Type == "text" {
					text = append(text, block.Text)
				}
			}
			result := schema.WorkerTurnEvidence{TurnID: scan.turn, State: "completed"}
			result.Text, result.TextTruncated = boundedDiscussionWorkerText(strings.Join(text, "\n"))
			return result, nil
		}
	}
}
