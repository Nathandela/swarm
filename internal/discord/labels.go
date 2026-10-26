package discord

import "unicode/utf16"

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
