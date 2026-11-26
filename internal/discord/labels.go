package discord

import (
	"regexp"
	"strings"
	"unicode/utf16"
)

func clipUTF16(text string, limit int) string {
	units := 0
	for index, char := range text {
		units += utf16.RuneLen(char)
		if units > limit {
			return text[:index]
		}
	}
	return text
}
func lengthUTF16(text string) int {
	units := 0
	for _, char := range text {
		units += utf16.RuneLen(char)
	}
	return units
}

var discordMention = regexp.MustCompile(`<(@[!&]?|#)\d+>`)

// teammateThreadName names a new thread after its opener: mentions removed,
// first eight words, Discord's 100-character limit respected.
func teammateThreadName(name, content string) string {
	words := strings.Fields(discordMention.ReplaceAllString(content, " "))
	if len(words) == 0 {
		return clipUTF16(name+" discussion", 95)
	}
	return clipUTF16(strings.Join(words[:min(len(words), 8)], " "), 95)
}
