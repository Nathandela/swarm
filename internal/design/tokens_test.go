// Package design pins the shared design-token contract for the Phase B
// Android theme (PB-TOK-1..3, docs/specifications/remote-phaseB-requirements.md
// section 6.13, scoped by the section 5 decision: one skin, fixed dark theme,
// light mode deferred to Phase C).
//
// Contract these tests pin: internal/design/tokens.json is the single
// machine-readable origin of truth for the theme. Its schema is:
//
//	{
//	  "schema": 1,
//	  "source": "docs/research/remote-control-design-directions.html",
//	  "skin": "substrate",                  // pinned; switching to void needs a spec/ADR change
//	  "mode": "dark",                       // light mode is deferred to Phase C
//	  "tokens": {
//	    "--p-bg": "#08090a",
//	    ...                                 // every --p-* token the chosen skin
//	  },                                    // defines in the design HTML, verbatim
//	  "terminal_peek": {
//	    "fg": "--p-hero",                   // token refs, not duplicated values:
//	    "font": "--p-mono"                  // fg must be --p-hero (the phosphor
//	  }                                     // green), font a monospace stack
//	}
//
// Token names keep the full CSS custom-property name so the drift check
// against the design HTML is an exact string comparison (values compared
// whitespace-normalized). Unknown JSON fields and trailing data are rejected.
package design

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

const (
	tokenSourcePath = "tokens.json"
	designHTMLPath  = "../../docs/research/remote-control-design-directions.html"
	designHTMLRef   = "docs/research/remote-control-design-directions.html"
	// htmlTokenCount is the verified count from requirements section 6.13:
	// each direction block in the design HTML defines 31 distinct --p-* tokens.
	htmlTokenCount = 31
)

// skinClass maps the recorded skin name to its CSS class in the design HTML.
// Only the two retained directions are valid: d3 Signal (purple) and
// d4 Instrument are marked "Not retained" in the artifact.
var skinClass = map[string]string{
	"substrate": "d1",
	"void":      "d2",
}

type tokenSource struct {
	Schema       int               `json:"schema"`
	Source       string            `json:"source"`
	Skin         string            `json:"skin"`
	Mode         string            `json:"mode"`
	Tokens       map[string]string `json:"tokens"`
	TerminalPeek terminalPeek      `json:"terminal_peek"`
}

type terminalPeek struct {
	Fg   string `json:"fg"`
	Font string `json:"font"`
}

func loadTokenSource(t *testing.T) tokenSource {
	t.Helper()
	data, err := os.ReadFile(tokenSourcePath)
	if err != nil {
		t.Fatalf("PB-TOK-1: token source internal/design/%s must exist and be checked in: %v", tokenSourcePath, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var src tokenSource
	if err := dec.Decode(&src); err != nil {
		t.Fatalf("PB-TOK-1: token source does not match the pinned schema: %v", err)
	}
	if dec.More() {
		t.Fatalf("PB-TOK-1: token source carries data after the JSON document")
	}
	return src
}

var declRe = regexp.MustCompile(`(--p-[a-z0-9-]+)\s*:\s*([^;]+);`)

// extractSkinTokens reads the design HTML and returns every --p-* declaration
// of the given direction block (".dN { ... }"), values whitespace-normalized.
func extractSkinTokens(t *testing.T, class string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(designHTMLPath)
	if err != nil {
		t.Fatalf("design HTML %s not readable: %v", designHTMLRef, err)
	}
	blockRe := regexp.MustCompile(`(?s)\.` + class + `\s*\{(.*?)\}`)
	m := blockRe.FindSubmatch(data)
	if m == nil {
		t.Fatalf("design HTML defines no .%s token block", class)
	}
	tokens := make(map[string]string)
	for _, d := range declRe.FindAllStringSubmatch(string(m[1]), -1) {
		tokens[d[1]] = normalize(d[2])
	}
	if len(tokens) != htmlTokenCount {
		t.Fatalf("extractor sanity: .%s defines %d --p-* tokens, requirements section 6.13 verified %d", class, len(tokens), htmlTokenCount)
	}
	return tokens
}

func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// PB-TOK-1: a single machine-readable token source exists and matches the schema.
func TestTokenSourceExistsAndMatchesSchema(t *testing.T) {
	src := loadTokenSource(t)
	if src.Schema != 1 {
		t.Errorf("PB-TOK-1: schema must be 1, got %d", src.Schema)
	}
	if src.Source != designHTMLRef {
		t.Errorf("PB-TOK-1: source must reference %q, got %q", designHTMLRef, src.Source)
	}
	if len(src.Tokens) == 0 {
		t.Error("PB-TOK-1: tokens must not be empty")
	}
	for name := range src.Tokens {
		if !strings.HasPrefix(name, "--p-") {
			t.Errorf("PB-TOK-1: token %q is not a --p-* product token", name)
		}
	}
}

// PB-TOK-1 (no drift) and PB-TOK-2 (completeness): the JSON carries exactly
// the tokens the chosen skin defines in the design HTML, with equal values.
func TestTokenSourceMatchesChosenSkinInDesignHTML(t *testing.T) {
	// Exercise the extractor against both retained skins first, so any
	// failure below can only mean the JSON is wrong, not the extractor.
	for _, class := range skinClass {
		extractSkinTokens(t, class)
	}

	src := loadTokenSource(t)
	class, ok := skinClass[src.Skin]
	if !ok {
		t.Fatalf("PB-TOK-2: recorded skin %q is not a retained direction (want substrate or void)", src.Skin)
	}
	want := extractSkinTokens(t, class)

	for name, wantVal := range want {
		gotVal, ok := src.Tokens[name]
		if !ok {
			t.Errorf("PB-TOK-2: token %s defined by skin %q in the design HTML is missing from tokens.json", name, src.Skin)
			continue
		}
		if normalize(gotVal) != wantVal {
			t.Errorf("PB-TOK-1: token %s drifted: json %q, html %q", name, gotVal, wantVal)
		}
	}
	for name := range src.Tokens {
		if _, ok := want[name]; !ok {
			t.Errorf("PB-TOK-1: token %s in tokens.json is not defined by skin %q in the design HTML", name, src.Skin)
		}
	}
}

// PB-TOK-2: the chosen skin is Substrate (d1), and the theme is pinned dark
// (light mode is deferred to Phase C per requirements section 5). Void (d2)
// remains a legal future choice, but switching to it requires a spec/ADR
// change, not a silent edit here.
func TestChosenSkinIsSubstrateAndPinnedDark(t *testing.T) {
	src := loadTokenSource(t)
	if src.Skin != "substrate" {
		t.Errorf("PB-TOK-2: skin must be \"substrate\", got %q", src.Skin)
	}
	if src.Mode != "dark" {
		t.Errorf("PB-TOK-2: mode must be pinned to \"dark\", got %q", src.Mode)
	}
}

// PB-TOK-3: the terminal peek keeps the phosphor-green monospace treatment.
// The foreground must be --p-hero itself: in both retained skins --p-hero IS
// the phosphor green, and pinning the ref forbids every mis-wiring a hue
// classifier would let through (near-black inks like --p-hero-ink, off-greens
// like --p-ok). Purple needs no separate check: the drift test enforces exact
// HTML<->JSON equality and skinClass admits only d1/d2, so the retired purple
// direction (d3) cannot enter the token set without failing those tests --
// do not reintroduce an HSV classifier for it.
func TestTerminalPeekIsPhosphorGreenMonospace(t *testing.T) {
	src := loadTokenSource(t)

	if src.TerminalPeek.Fg != "--p-hero" {
		t.Errorf("PB-TOK-3: terminal_peek.fg must be \"--p-hero\", got %q", src.TerminalPeek.Fg)
	}
	if _, ok := src.Tokens[src.TerminalPeek.Fg]; !ok {
		t.Fatalf("PB-TOK-3: terminal_peek.fg %q does not name a token in the set", src.TerminalPeek.Fg)
	}

	fontVal, ok := src.Tokens[src.TerminalPeek.Font]
	if !ok {
		t.Fatalf("PB-TOK-3: terminal_peek.font %q does not name a token in the set", src.TerminalPeek.Font)
	}
	if !strings.Contains(strings.ToLower(fontVal), "monospace") {
		t.Errorf("PB-TOK-3: terminal peek font %s=%q must be a monospace stack", src.TerminalPeek.Font, fontVal)
	}
}
