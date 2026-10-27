package discord

import (
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func TestDiscordLabelsRespectUTF16Limits(t *testing.T) {
	text := strings.Repeat("🙂", 2000)
	for _, item := range []struct {
		text  string
		limit int
	}{{shortLabel(text), 100}, {nativeLabel(text), 80}, {shortMessage(text), 2000}} {
		if !utf8.ValidString(item.text) || len(utf16.Encode([]rune(item.text))) > item.limit {
			t.Fatal("Discord text exceeds native limit")
		}
	}
}
