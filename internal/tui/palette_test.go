package tui

import (
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"regexp"
	"strings"
	"testing"
)

const brandbookPath = "../../docs/design/swarm-illustration-direction/index.html"

type paletteTokenSource struct {
	Tokens map[string]string `json:"tokens"`
}

var brandbookColorTokenRE = regexp.MustCompile(`(--p-[a-z0-9-]+):\s*(#[0-9a-fA-F]{6})`)

// TestTerminalPaletteMatchesBrandbook keeps the desktop TUI on the same semantic
// Slate palette as the mobile app and Brandbook v1. "Needs input" is attention,
// not failure, so it deliberately uses --p-att while validation/refusal copy uses
// --p-err. Keeping both rows here prevents those meanings from being collapsed
// back into one red style.
func TestTerminalPaletteMatchesBrandbook(t *testing.T) {
	tokens := readPaletteTokens(t, "../design/tokens.json")
	brandbook := readBrandbookColors(t, brandbookPath)

	uses := []struct {
		name  string
		color color.Color
		token string
	}{
		{"accent", styleAccent.GetForeground(), "--p-hero"},
		{"title", styleTitle.GetForeground(), "--p-hero"},
		{"needs input", styleGroupNeedsInput.GetForeground(), "--p-att"},
		{"working", styleGroupWorking.GetForeground(), "--p-work"},
		{"ready for review", styleGroupReview.GetForeground(), "--p-ok"},
		{"completed", styleGroupCompleted.GetForeground(), "--p-ink3"},
		{"error", styleError.GetForeground(), "--p-err"},
	}

	if mismatches := paletteMismatches(uses, tokens, brandbook); len(mismatches) > 0 {
		t.Fatal(strings.Join(mismatches, "\n"))
	}

	// Negative control: perturb the artifact in memory so the join proves it can
	// detect brandbook drift rather than merely comparing the TUI to itself.
	mutated := cloneStrings(brandbook)
	mutated["--p-ok"] = "#000000"
	if mismatches := paletteMismatches(uses, tokens, mutated); len(mismatches) == 0 {
		t.Fatal("palette join accepted a mutated --p-ok in Brandbook v1")
	}
}

func readPaletteTokens(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var source paletteTokenSource
	if err := json.Unmarshal(b, &source); err != nil {
		t.Fatal(err)
	}
	return source.Tokens
}

func readBrandbookColors(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	colors := make(map[string]string)
	for _, match := range brandbookColorTokenRE.FindAllStringSubmatch(string(b), -1) {
		colors[match[1]] = strings.ToLower(match[2])
	}
	return colors
}

func paletteMismatches(uses []struct {
	name  string
	color color.Color
	token string
}, tokens, brandbook map[string]string) []string {
	var mismatches []string
	for _, use := range uses {
		want := strings.ToLower(tokens[use.token])
		if want == "" {
			mismatches = append(mismatches, fmt.Sprintf("token source has no %s", use.token))
			continue
		}
		if got := colorHex(use.color); got != want {
			mismatches = append(mismatches,
				fmt.Sprintf("terminal %s = %s, want %s (%s)", use.name, got, want, use.token))
		}
		if got := strings.ToLower(brandbook[use.token]); got != want {
			mismatches = append(mismatches,
				fmt.Sprintf("Brandbook v1 %s = %s, want token source %s", use.token, got, want))
		}
	}
	return mismatches
}

func colorHex(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", uint8(r>>8), uint8(g>>8), uint8(b>>8))
}

func cloneStrings(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
