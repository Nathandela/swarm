package claude

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
)

// Claude Code keeps one file per RUNNING process under ~/.claude/sessions/<pid>.json
// carrying the session's current display name and when it was set. This is the exact
// file the CLI wrote for a swarm-launched session on 2026-08-28 (version 2.1.250),
// secrets and socket paths aside.
const liveRegistrySample = `{"pid":2246,"sessionId":"fff31caf-df8b-416f-8990-ecc58eb0dcaf","cwd":"/Users/Nathan/Code/swarm","startedAt":1787894219288,"version":"2.1.250","kind":"interactive","entrypoint":"cli","name":"WIP correction on chat","nameSource":"user","nameSince":1787894219289,"updatedAt":1787894222551,"status":"busy"}`

const liveRegistrySession = "fff31caf-df8b-416f-8990-ecc58eb0dcaf"

func liveNameSource(t *testing.T) adapter.LiveNameSource {
	t.Helper()
	src, ok := adapter.AsLiveNameSource(New())
	if !ok {
		t.Fatal("the claude adapter does not implement adapter.LiveNameSource")
	}
	return src
}

func TestLiveNameDir_IsClaudesSessionsRegistry(t *testing.T) {
	if got := liveNameSource(t).LiveNameDir(); got != ".claude/sessions" {
		t.Fatalf("LiveNameDir = %q, want .claude/sessions", got)
	}
}

func TestLiveNameFromFile_ReadsNameAndSince(t *testing.T) {
	ln, ok := liveNameSource(t).LiveNameFromFile([]byte(liveRegistrySample), liveRegistrySession)
	if !ok {
		t.Fatal("the real registry file was not recognised")
	}
	if ln.Name != "WIP correction on chat" {
		t.Errorf("Name = %q", ln.Name)
	}
	if want := time.UnixMilli(1787894219289); !ln.Since.Equal(want) {
		t.Errorf("Since = %v, want %v", ln.Since, want)
	}
}

func TestLiveNameFromFile_AnotherSessionsFileIsNotOurs(t *testing.T) {
	if _, ok := liveNameSource(t).LiveNameFromFile([]byte(liveRegistrySample), "00000000-0000-4000-8000-000000000000"); ok {
		t.Fatal("a file naming a different sessionId was accepted")
	}
}

func TestLiveNameFromFile_RejectsWhatItCannotUse(t *testing.T) {
	src := liveNameSource(t)
	cases := map[string]string{
		"no name":       `{"sessionId":"` + liveRegistrySession + `","nameSince":1787894219289}`,
		"empty name":    `{"sessionId":"` + liveRegistrySession + `","name":"","nameSince":1787894219289}`,
		"no nameSince":  `{"sessionId":"` + liveRegistrySession + `","name":"x"}`,
		"garbage":       `not json`,
		"empty":         ``,
		"array":         `[]`,
		"oversized":     `{"sessionId":"` + liveRegistrySession + `","name":"` + strings.Repeat("x", 1<<20) + `","nameSince":1}`,
		"wrong id type": `{"sessionId":7,"name":"x","nameSince":1}`,
	}
	for label, raw := range cases {
		if _, ok := src.LiveNameFromFile([]byte(raw), liveRegistrySession); ok {
			t.Errorf("%s: accepted", label)
		}
	}
	// Total: nil input never panics.
	if _, ok := src.LiveNameFromFile(nil, liveRegistrySession); ok {
		t.Error("nil input accepted")
	}
}
