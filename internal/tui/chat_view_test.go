package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/charmbracelet/x/ansi"
)

func TestChatViewRendersJournalAndNativeMessagesStatusErrorAndDraft(t *testing.T) {
	history := []schema.JournalRecord{{Item: json.RawMessage(`{"v":1,"item_id":"01JBQ4Z0X9M6T7NPKV2RQF8SJD","kind":"user_message","text":"hello"}`)}}
	native := &schema.NativeHistoryPage{Version: 2, Items: []json.RawMessage{json.RawMessage(`{"v":2,"kind":"message_snapshot","item_id":"native-1","role":"assistant","text":"native reply","provenance":"imported"}`)}}
	v := newChatView(history, native, "Waiting", "", true)
	v.draft = newLineEditor("draft")
	rendered := v.view(40, 9)
	for _, want := range []string{"user_message: hello", "assistant: native reply", "Waiting", "draft"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q in %q", want, rendered)
		}
	}
	v.err = "send refused"
	if !strings.Contains(v.view(40, 9), "send refused") {
		t.Fatal("error not rendered")
	}
}

func TestChatViewBlockedSendAndAllowedChangeDoNotEmitOrClear(t *testing.T) {
	v := chatView{allowed: false, draft: newLineEditor("keep me")}
	next, cmd := v.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || next.draft.text != "keep me" {
		t.Fatal("blocked send emitted or cleared draft")
	}
	next.allowed = true // eligibility becoming available must not itself send.
	if next.draft.text != "keep me" {
		t.Fatal("eligibility change cleared draft")
	}
}

func TestChatViewUnicodeMultilinePasteAndSendIntent(t *testing.T) {
	v := chatView{allowed: true}
	v, _ = v.update(tea.PasteMsg{Content: "é🐝\nsecond line"})
	if v.draft.text != "é🐝\nsecond line" {
		t.Fatalf("paste lost content: %q", v.draft.text)
	}
	_, cmd := v.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("allowed nonempty draft emitted no intent")
	}
	intent, ok := cmd().(chatSendIntent)
	if !ok || intent.Text != "é🐝\nsecond line" {
		t.Fatalf("wrong intent: %#v", intent)
	}
	if v.draft.text != intent.Text {
		t.Fatal("intent cleared view draft")
	}
	if !strings.Contains(v.view(20, 7), "second line") {
		t.Fatal("multiline draft was not rendered")
	}
}

func TestChatViewEscapeReturnsToParentAndPreservesDraft(t *testing.T) {
	v, cmd := (chatView{draft: newLineEditor("preserve")}).update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("Escape emitted no parent intent")
	}
	if _, ok := cmd().(chatBackIntent); !ok {
		t.Fatalf("wrong intent %T", cmd())
	}
	if v.draft.text != "preserve" {
		t.Fatal("Escape discarded draft")
	}
}

func TestChatViewTinySizeAndScrollClamp(t *testing.T) {
	v := chatView{status: "ready", draft: newLineEditor("draft")}
	for _, size := range [][2]int{{0, 0}, {1, 1}, {1, 3}, {2, 3}, {8, 3}} {
		got := v.view(size[0], size[1])
		if size[0] == 0 || size[1] == 0 {
			if got != "" {
				t.Fatalf("zero size rendered %q", got)
			}
			continue
		}
		rows := strings.Split(got, "\n")
		if len(rows) > size[1] {
			t.Fatalf("%dx%d rendered %d rows: %q", size[0], size[1], len(rows), got)
		}
		for _, row := range rows {
			if ansi.StringWidth(row) > size[0] {
				t.Fatalf("row exceeds width %d: %q", size[0], row)
			}
		}
	}
	items := make([]json.RawMessage, 10)
	for i := range items {
		items[i] = json.RawMessage(`{"v":1,"item_id":"id","kind":"user_message","text":"row"}`)
	}
	v.items = items
	v = v.resize(20, 4)
	for i := 0; i < 50; i++ {
		v, _ = v.update(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	maxTop := max(0, len(v.historyRows(v.width))-v.historyBudget())
	if v.top != maxTop {
		t.Fatalf("Up was not clamped: top=%d max=%d", v.top, maxTop)
	}
	v, _ = v.update(tea.KeyPressMsg{Code: tea.KeyDown})
	if v.top != maxTop-1 {
		t.Fatalf("Down stuck after excess Up: top=%d max=%d", v.top, maxTop)
	}
}

func TestChatViewWrappedDraftKeepsCaretAndStatusVisible(t *testing.T) {
	v := chatView{status: "ready", allowed: true, draft: newLineEditor("one very long line\nsecond very long line")}
	got := v.view(8, 5)
	if !strings.Contains(got, "ready") || !strings.Contains(got, "█") {
		t.Fatalf("status/caret clipped: %q", got)
	}
	if len(strings.Split(got, "\n")) > 5 {
		t.Fatalf("render exceeded height: %q", got)
	}
}

func TestChatViewSanitizesDisplayWithoutChangingSentDraft(t *testing.T) {
	const draft = "safe\x1b]52;c;leak\a\u202e"
	displayed := displayText(draft)
	if strings.ContainsAny(displayed, "\x1b\a") || strings.Contains(displayed, "leak") || strings.ContainsRune(displayed, '\u202e') {
		t.Fatalf("unsafe terminal text survived display sanitization: %q", displayed)
	}
	v := chatView{allowed: true, draft: newLineEditor(draft)}
	rendered := v.view(30, 4)
	if strings.Contains(rendered, "\x1b]") || strings.ContainsRune(rendered, '\a') || strings.Contains(rendered, "leak") {
		t.Fatalf("untrusted OSC payload rendered: %q", rendered)
	}
	_, cmd := v.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	intent := cmd().(chatSendIntent)
	if intent.Text != draft {
		t.Fatalf("display sanitizer changed submitted text: %q", intent.Text)
	}
}

func TestChatViewCaretUsesSanitizedPrefix(t *testing.T) {
	draft := newLineEditor("a\x1b]52;c;hidden\a🐝")
	draft.cursor = 1
	rows, cursor := chatDraftRows(draft, 10)
	if cursor != 0 || !strings.Contains(rows[0], "a█🐝") || strings.Contains(rows[0], "hidden") {
		t.Fatalf("caret did not follow sanitized prefix: rows=%q cursor=%d", rows, cursor)
	}
}

func TestChatViewNarrowUnicodeRowsFitAndKeepCaret(t *testing.T) {
	for _, width := range []int{1, 2} {
		v := chatView{allowed: true, draft: newLineEditor("🐝e\u0301")}.resize(width, 5)
		got := v.view(width, 5)
		for _, row := range strings.Split(got, "\n") {
			if cells := ansi.StringWidth(row); cells > width {
				t.Fatalf("width %d row is %d cells: %q", width, cells, row)
			}
		}
		if !strings.Contains(got, "█") {
			t.Fatalf("width %d lost caret: %q", width, got)
		}
		if width == 1 && !strings.Contains(got, "�") {
			t.Fatalf("width 1 did not safely replace overwide emoji: %q", got)
		}
	}
	if got := wrapDisplay("🐝e\u0301", 2); len(got) != 2 || ansi.StringWidth(got[0]) != 2 || got[1] != "e\u0301" {
		t.Fatalf("width-2 grapheme wrapping = %#v", got)
	}
}

func TestChatViewSanitizesWholeDraftAtEveryRawCursor(t *testing.T) {
	const raw = "a\x1b]52;c;hidden\a🐝"
	for cursor := 0; cursor <= len([]rune(raw)); cursor++ {
		draft := newLineEditor(raw)
		draft.cursor = cursor
		rows, _ := chatDraftRows(draft, 40)
		rendered := strings.Join(rows, "\n")
		if strings.Contains(rendered, "hidden") || strings.ContainsAny(rendered, "\x1b\a") || !strings.Contains(rendered, "█") {
			t.Fatalf("raw cursor %d: %q", cursor, rendered)
		}
		if draft.text != raw {
			t.Fatal("display changed raw draft")
		}
	}
}
