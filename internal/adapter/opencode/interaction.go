package opencode

import (
	"encoding/json"
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
)

// NativeMessageRowEvent identifies a captured native HTTP message row, not an SSE event.
const NativeMessageRowEvent = "native.message.row"

type nativeInteractions struct{}

// NativeInteractions shapes managed native reads without advertising generic adapter capability.
func NativeInteractions() adapter.InteractionSource { return nativeInteractions{} }
func (nativeInteractions) Decision(string, string) (adapter.DecisionAction, bool) {
	return adapter.DecisionAction{}, false
}
func (nativeInteractions) Interactions(p adapter.HookPayload) []adapter.Interaction {
	if p.Event != NativeMessageRowEvent {
		return nil
	}
	info, parts, err := DecodeMessageRow(p.Raw)
	if err != nil {
		return nil
	}
	if info.Role == "user" {
		genuine := false
		for _, part := range parts {
			if part.Type == "compaction" {
				return nil
			}
			if (part.Type == "text" || part.Type == "file") && !part.Synthetic && !part.Ignored {
				genuine = true
			}
		}
		var text strings.Builder
		for _, part := range parts {
			if part.Type == "text" && (!genuine || !part.Synthetic && !part.Ignored) {
				text.WriteString(part.Text)
			}
		}
		if text.Len() == 0 {
			return nil
		}
		source := adapter.SourceNative
		if !genuine {
			source = adapter.SourceDerived
		}
		return []adapter.Interaction{{Kind: adapter.KindMessageSnapshot, MessageRole: "user", Ref: info.ID, TurnRef: info.ID, Text: text.String(), Source: source}}
	}
	var out []adapter.Interaction
	for _, part := range parts {
		out = append(out, NativePartInteractions(info, part)...)
	}
	return out
}

// NativePartInteractions uses previously verified message context for a full native part update.
func NativePartInteractions(info Message, part Part) []adapter.Interaction {
	if info.Role != "assistant" || info.ID != part.MessageID || info.SessionID != part.SessionID || info.IsAssistantSummary() {
		return nil
	}
	switch part.Type {
	case "text":
		if part.Text == "" || part.Ignored {
			return nil
		}
		state := adapter.StatusInProgress
		if info.Time.Completed > 0 {
			state = adapter.StatusCompleted
		}
		return []adapter.Interaction{{Kind: adapter.KindMessageSnapshot, MessageRole: "assistant", Ref: part.ID, TurnRef: info.ParentID, Text: part.Text, Status: state}}
	case "tool":
		return nativeToolInteractions(info, part)
	}
	return nil
}

func nativeToolInteractions(info Message, part Part) []adapter.Interaction {
	if part.State == nil || part.Tool == "" {
		return nil
	}
	var state string
	switch part.State.Status {
	case "pending", "running":
		state = adapter.StatusInProgress
	case "completed":
		state = adapter.StatusCompleted
	case "error":
		state = adapter.StatusFailed
	default:
		return nil
	}
	var raw struct {
		State struct {
			Input struct {
				FilePath  string  `json:"filePath"`
				Path      string  `json:"path"`
				Command   string  `json:"command"`
				Pattern   string  `json:"pattern"`
				Query     string  `json:"query"`
				URL       string  `json:"url"`
				OldString *string `json:"oldString"`
			} `json:"input"`
			Metadata struct {
				Exit     *int   `json:"exit"`
				FilePath string `json:"filepath"`
				Exists   *bool  `json:"exists"`
				Diff     string `json:"diff"`
				FileDiff struct {
					File      string `json:"file"`
					Patch     string `json:"patch"`
					Additions int    `json:"additions"`
					Deletions int    `json:"deletions"`
				} `json:"filediff"`
				Todos []struct {
					Content string `json:"content"`
					Status  string `json:"status"`
				} `json:"todos"`
			} `json:"metadata"`
		} `json:"state"`
	}
	if json.Unmarshal(part.Raw, &raw) != nil {
		return nil
	}
	input, meta := raw.State.Input, raw.State.Metadata
	action := adapter.ToolAction{Type: "other"}
	switch part.Tool {
	case "bash":
		action.Type = "execute"
		action.Command = input.Command
	case "read":
		action.Type = "read"
		action.Path = input.FilePath
	case "edit":
		action.Type = "edit"
		action.Path = input.FilePath
	case "write":
		action.Type = "write"
		action.Path = input.FilePath
	case "glob", "grep":
		action.Type = "search"
		action.Query = input.Pattern
	case "websearch", "codesearch":
		action.Type = "search"
		action.Query = input.Query
	case "webfetch":
		action.Type = "fetch"
		action.Query = input.URL
	case "task":
		action.Type = "agent"
	}
	item := adapter.Interaction{Kind: adapter.KindToolRun, Ref: part.ID, TurnRef: info.ParentID, Tool: part.Tool, Status: state, Action: action, OutputExcerpt: part.State.Output}
	if state == adapter.StatusFailed {
		item.OutputExcerpt = part.State.Error
	}
	if meta.Exit != nil {
		item.ExitCode = *meta.Exit
	}
	out := []adapter.Interaction{item}
	if state != adapter.StatusCompleted {
		return out
	}
	file := adapter.Interaction{Kind: adapter.KindFileChange, Ref: part.ID + ":file", TurnRef: info.ParentID, Status: adapter.StatusCompleted}
	switch part.Tool {
	case "write":
		if meta.Exists != nil && meta.FilePath != "" {
			file.Path = meta.FilePath
			file.Change = "modify"
			if !*meta.Exists {
				file.Change = "create"
			}
		}
	case "edit":
		if meta.FileDiff.File != "" && input.OldString != nil {
			file.Path = meta.FileDiff.File
			file.Change = "modify"
			if *input.OldString == "" {
				file.Change = "create"
			}
			file.DiffExcerpt = meta.FileDiff.Patch
			file.Added = meta.FileDiff.Additions
			file.Removed = meta.FileDiff.Deletions
		}
	case "todowrite":
		if meta.Todos != nil {
			plan := adapter.Interaction{Kind: adapter.KindPlanUpdate, Ref: "plan:" + info.SessionID, TurnRef: info.ParentID}
			for _, todo := range meta.Todos {
				switch todo.Status {
				case "pending", "in_progress", "completed", "cancelled":
				default:
					return out
				}
				plan.Steps = append(plan.Steps, adapter.PlanStep{Text: todo.Content, State: todo.Status})
			}
			out = append(out, plan)
		}
	}
	if file.Path != "" {
		out = append(out, file)
	}
	return out
}
