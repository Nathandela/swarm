package persist

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

const OpenCodeSchemaVersion = 3
const RequiredRuntimeOpenCode = "opencode-tui"

const (
	OpenCodeModeCreate       = "create"
	OpenCodeModeResume       = "resume"
	OpenCodePhasePlanned     = "planned"
	OpenCodePhaseBound       = "bound"
	OpenCodePhaseReady       = "ready"
	OpenCodeInitialPrepared  = "prepared"
	OpenCodeInitialStarted   = "started"
	OpenCodeInitialAdmitted  = "admitted"
	OpenCodeInitialUncertain = "uncertain"
)

// OpenCodeBinding holds identity custody; credentials and prompt text stay in private launch files.
type OpenCodeBinding struct {
	Revision          uint64           `json:"revision"`
	Mode              string           `json:"mode"`
	Phase             string           `json:"phase"`
	Generation        string           `json:"generation"`
	ProfileEnv        []string         `json:"profile_env,omitempty"`
	NativeCreatedAtMS int64            `json:"native_created_at_ms,omitempty"`
	Initial           *OpenCodeInitial `json:"initial,omitempty"`
}

type OpenCodeInitial struct {
	OperationID string `json:"operation_id"`
	RequestHash string `json:"request_hash"`
	Phase       string `json:"phase"`
	MessageID   string `json:"message_id,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Model       string `json:"model,omitempty"`
	Variant     string `json:"variant,omitempty"`
}

var (
	openCodeNativeID   = regexp.MustCompile(`^ses_(?:[A-Za-z0-9]{26}|[0-9a-f]{32})$`)
	openCodeMessageID  = regexp.MustCompile(`^msg_(?:[A-Za-z0-9]{26}|[0-9a-f]{32})$`)
	openCodeGeneration = regexp.MustCompile(`^[0-9a-f]{32}$`)
	openCodeHash       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func IsNativeOpenCodeConversationID(id string) bool { return openCodeNativeID.MatchString(id) }

// Only native configuration paths belong in durable public metadata.
func OpenCodeProfileKey(key string) bool {
	switch key {
	case "HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_TUI_CONFIG":
		return true
	default:
		return false
	}
}

func cleanOpenCodeText(s string, max int) bool {
	return len(s) <= max && !strings.ContainsFunc(s, unicode.IsControl)
}

func ValidOpenCodeBinding(b OpenCodeBinding) bool {
	if b.Revision == 0 || !openCodeGeneration.MatchString(b.Generation) || b.NativeCreatedAtMS < 0 {
		return false
	}
	if b.Mode != OpenCodeModeCreate && b.Mode != OpenCodeModeResume {
		return false
	}
	switch b.Phase {
	case OpenCodePhasePlanned:
	case OpenCodePhaseBound, OpenCodePhaseReady:
		if b.NativeCreatedAtMS == 0 {
			return false
		}
	default:
		return false
	}
	if b.Mode == OpenCodeModeResume && b.NativeCreatedAtMS == 0 {
		return false
	}
	if len(b.ProfileEnv) > 8 {
		return false
	}
	seen := map[string]bool{}
	for _, entry := range b.ProfileEnv {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !OpenCodeProfileKey(key) || seen[key] || !cleanOpenCodeText(value, 4096) || !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return false
		}
		seen[key] = true
	}
	if b.Initial == nil {
		return true
	}
	i := b.Initial
	if !openCodeGeneration.MatchString(i.OperationID) || !openCodeHash.MatchString(i.RequestHash) {
		return false
	}
	if i.MessageID != "" && !openCodeMessageID.MatchString(i.MessageID) {
		return false
	}
	if !cleanOpenCodeText(i.Agent, 128) || !cleanOpenCodeText(i.Model, 256) || !cleanOpenCodeText(i.Variant, 128) {
		return false
	}
	switch i.Phase {
	case OpenCodeInitialPrepared:
		return i.MessageID == "" && i.Agent == "" && i.Model == "" && i.Variant == ""
	case OpenCodeInitialStarted:
		return b.Phase == OpenCodePhaseReady && i.MessageID == "" && i.Agent == "" && i.Model == "" && i.Variant == ""
	case OpenCodeInitialAdmitted:
		provider, model, ok := strings.Cut(i.Model, "/")
		return b.Phase == OpenCodePhaseReady && i.MessageID != "" && i.Agent != "" && ok && provider != "" && model != ""
	case OpenCodeInitialUncertain:
		return b.Phase == OpenCodePhaseReady
	default:
		return false
	}
}

func CloneOpenCodeBinding(b *OpenCodeBinding) *OpenCodeBinding {
	if b == nil {
		return nil
	}
	out := *b
	out.ProfileEnv = append([]string(nil), b.ProfileEnv...)
	if b.Initial != nil {
		initial := *b.Initial
		out.Initial = &initial
	}
	return &out
}

func validOpenCodeMeta(m Meta) bool {
	return m.RequiredRuntime == RequiredRuntimeOpenCode && m.AgentType == "opencode" && IsNativeOpenCodeConversationID(m.ConversationID) &&
		m.OpenCodeBinding != nil && ValidOpenCodeBinding(*m.OpenCodeBinding) && m.AccountBinding == nil && m.AccountProjectionRef == "" && m.InputEmbargo == "" && m.AccountClaudeFallback == nil
}
