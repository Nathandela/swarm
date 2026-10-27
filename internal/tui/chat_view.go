package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/rivo/uniseg"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/charmbracelet/x/ansi"
)

type chatSendIntent struct{ Text string }
type chatBackIntent struct{}

type chatView struct {
	items   []json.RawMessage
	status  string
	err     string
	draft   lineEditor
	allowed bool
	top     int // physical history rows above the newest row
	width   int
	height  int
}

func newChatView(history []schema.JournalRecord, native *schema.NativeHistoryPage, status, errText string, allowed bool) chatView {
	v := chatView{status: status, err: errText, allowed: allowed}
	for _, record := range history {
		if record.Item != nil {
			v.items = append(v.items, record.Item)
		}
	}
	if native != nil {
		v.items = append(v.items, native.Items...)
	}
	return v
}

func (v chatView) resize(width, height int) chatView {
	v.width, v.height = max(0, width), max(0, height)
	return v.clampTop()
}

func (v chatView) clampTop() chatView {
	if v.width == 0 || v.height == 0 {
		v.top = 0
		return v
	}
	rows := v.historyRows(v.width)
	budget := v.historyBudget()
	v.top = min(max(0, v.top), max(0, len(rows)-budget))
	return v
}

func (v chatView) historyBudget() int {
	draftRows, _ := chatDraftRows(v.draft, max(1, v.width-lipgloss.Width("> ")))
	composerBudget := min(len(draftRows), max(1, v.height-min(v.height, 2)))
	status := v.err
	if status == "" {
		status = v.status
	}
	reserved := composerBudget
	if status != "" && v.height-reserved >= 1 {
		reserved++
	}
	if v.height >= 3 && v.height-reserved >= 2 {
		reserved++
	}
	return max(0, v.height-reserved)
}

func (v chatView) update(msg tea.Msg) (chatView, tea.Cmd) {
	switch m := msg.(type) {
	case tea.PasteMsg:
		v.draft.insert(m.Content) // lineEditor.paste is intentionally single-line.
		return v, nil
	case tea.KeyPressMsg:
		switch {
		case m.Code == tea.KeyEsc && m.Mod == 0:
			return v, func() tea.Msg { return chatBackIntent{} }
		case m.Code == tea.KeyEnter && m.Mod == 0:
			if v.allowed && strings.TrimSpace(v.draft.text) != "" {
				text := v.draft.text
				return v, func() tea.Msg { return chatSendIntent{Text: text} }
			}
			return v, nil
		case m.Code == tea.KeyUp && m.Mod == 0:
			if v.width == 0 || v.height == 0 {
				v.top = 0
			} else {
				maxTop := max(0, len(v.historyRows(v.width))-v.historyBudget())
				v.top = min(v.top+1, maxTop)
			}
			return v, nil
		case m.Code == tea.KeyDown && m.Mod == 0:
			v.top = max(0, v.top-1)
			return v, nil
		default:
			v.draft.update(m)
		}
	}
	return v, nil
}

func (v chatView) view(width, height int) string {
	if width < 1 || height < 1 {
		return ""
	}
	// Reserve the bottom row for the caret. At two rows the status remains visible;
	// at three or more rows keep a title too.
	prompt := "> "
	if width == 1 {
		prompt = ""
	} else if width <= lipgloss.Width(prompt) {
		prompt = ">"
	}
	draftRows, cursorRow := chatDraftRows(v.draft, max(1, width-lipgloss.Width(prompt)))
	composerBudget := min(len(draftRows), max(1, height-min(height, 2)))
	draftRows = visibleAround(draftRows, cursorRow, composerBudget)
	status := v.err
	if status == "" {
		status = v.status
	}
	status = ansi.Truncate(strings.ReplaceAll(displayText(status), "\n", " "), width, "…")

	reserved := len(draftRows)
	if status != "" && height-reserved >= 1 {
		reserved++
	}
	title := height >= 3
	if title && height-reserved >= 2 {
		reserved++
	} else {
		title = false
	}
	historyBudget := max(0, height-reserved)
	history := v.historyRows(max(1, width))
	maxTop := max(0, len(history)-historyBudget)
	top := min(max(0, v.top), maxTop)
	end := len(history) - top
	start := max(0, end-historyBudget)
	rows := append([]string(nil), history[start:end]...)
	if title {
		rows = append([]string{styleTitle.Render(ansi.Truncate("Chat", width, ""))}, rows...)
	}
	if status != "" && len(rows)+len(draftRows) < height {
		if v.err != "" {
			rows = append(rows, styleError.Render(status))
		} else {
			rows = append(rows, styleDim.Render(status))
		}
	}
	for _, line := range draftRows {
		rows = append(rows, prompt+line)
	}
	if len(rows) > height {
		rows = rows[len(rows)-height:]
	}
	return strings.Join(rows, "\n")
}

func (v chatView) historyRows(width int) []string {
	var rows []string
	for _, raw := range v.items {
		var item struct {
			Kind   string `json:"kind"`
			Role   string `json:"role"`
			Status string `json:"status"`
			Text   string `json:"text"`
		}
		if json.Unmarshal(raw, &item) != nil || item.Text == "" {
			continue
		}
		label := item.Role
		if label == "" {
			label = item.Kind
		}
		if label == "" {
			label = "message"
		}
		if item.Status != "" {
			label += " · " + item.Status
		}
		text := displayText(item.Text)
		rows = append(rows, wrapDisplay(fmt.Sprintf("%s: %s", displayText(label), text), width)...)
	}
	return rows
}

func chatDraftRows(draft lineEditor, width int) ([]string, int) {
	runes := []rune(draft.text)
	cursor := min(max(0, draft.cursor), len(runes))
	text := []rune(displayText(draft.text))
	visibleCursor := min(len([]rune(displayText(string(runes[:cursor])))), len(text))
	before := string(text[:visibleCursor])
	marked := before + "█" + string(text[visibleCursor:])
	lines := strings.Split(marked, "\n")
	cursorLine := strings.Count(before, "\n")
	cursorColumn := len([]rune(before[strings.LastIndex(before, "\n")+1:]))
	var rows []string
	cursorRow := 0
	for i, line := range lines {
		parts := wrapDisplay(line, width)
		if i == cursorLine {
			prefix := string([]rune(line)[:cursorColumn+1])
			cursorRow = len(rows) + len(wrapDisplay(prefix, width)) - 1
		}
		rows = append(rows, parts...)
	}
	return rows, cursorRow
}

func wrapDisplay(text string, width int) []string {
	width = max(1, width)
	var rows []string
	for _, line := range strings.Split(text, "\n") {
		var row strings.Builder
		cols := 0
		graphemes := uniseg.NewGraphemes(line)
		for graphemes.Next() {
			g := graphemes.Str()
			w := uniseg.StringWidth(g)
			if w > width {
				g, w = "�", 1
			}
			if cols > 0 && cols+w > width {
				rows = append(rows, row.String())
				row.Reset()
				cols = 0
			}
			row.WriteString(g)
			cols += w
		}
		rows = append(rows, row.String())
	}
	return rows
}

func visibleAround(rows []string, cursor, budget int) []string {
	if len(rows) <= budget {
		return rows
	}
	start := min(max(0, cursor-budget+1), len(rows)-budget)
	return rows[start : start+budget]
}

// displayText removes terminal controls and formatting directives while retaining newlines.
func displayText(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || !unicode.IsPrint(r) {
			return ' '
		}
		return r
	}, s)
}
