package tui

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/Nathandela/swarm/internal/protocol"
)

//go:embed templates/handoff-source.md.tmpl
var handoffSourcePrompt string

var handoffSourceTemplate = template.Must(template.New("handoff-source").Funcs(template.FuncMap{
	"shellQuote": shellQuote,
}).Parse(handoffSourcePrompt))

type handoffPromptData struct {
	Target      string
	Model       string
	Supervision string
}

// renderHandoffPrompt produces the complete instruction sent to the selected source
// session. Target, model and supervision mode are the only form-controlled values; the
// mode selects the supervision tail (ADR-010 Amendment 3 C1) and is checked against the
// closed vocabulary here so the template never renders an unknown one. The fixed
// template is kept below the daemon's one-message input bound, and the rendered result
// is checked again because a free-form model value can expand it.
func renderHandoffPrompt(target, model, supervision string) (string, error) {
	switch supervision {
	case protocol.SupervisionPassive, protocol.SupervisionManual, protocol.SupervisionNone:
	default:
		return "", fmt.Errorf("handoff prompt: unknown supervision mode %q", supervision)
	}
	var out strings.Builder
	data := handoffPromptData{Target: target, Model: model, Supervision: supervision}
	if err := handoffSourceTemplate.Execute(&out, data); err != nil {
		return "", fmt.Errorf("render handoff prompt: %w", err)
	}
	prompt := out.String()
	if len(prompt) > protocol.MaxSendInputText {
		return "", fmt.Errorf("handoff prompt is %d bytes, over the %d-byte send bound", len(prompt), protocol.MaxSendInputText)
	}
	return prompt, nil
}

// shellQuote returns one POSIX-shell single-quoted word. The handoff prompt contains a
// copyable command, so editable model text must remain data even when it contains spaces,
// quotes, or shell metacharacters.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
