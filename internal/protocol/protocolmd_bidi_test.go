package protocol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GG-7 drift, with teeth (F9): the frozen protocolmd_test only checks that every
// struct json tag APPEARS SOMEWHERE in protocol.md (a substring presence check,
// vulnerable to common-substring false positives and blind to doc rows with no
// backing struct field). This bidirectional check parses the FIELD TABLES and
// asserts a two-way set equality between the documented field rows and the frozen
// wire structs' json tags: every struct tag has a field-table row, AND every
// field-table row names a real struct tag.

// TestProtocolMDBidi_FieldSetMatchesStructs is the bidirectional GG-7 drift check.
func TestProtocolMDBidi_FieldSetMatchesStructs(t *testing.T) {
	doc := docFieldRows(t)
	if len(doc) == 0 {
		t.Fatalf("no field-table rows parsed from protocol.md — the field tables must use a `JSON key` header")
	}

	tagSet := map[string]bool{}
	for _, tag := range wireJSONTags() {
		tagSet[tag] = true
	}

	// Forward: every struct json tag has a documented field-table row (matched as a
	// TABLE ROW, not any prose occurrence).
	for tag := range tagSet {
		if !doc[tag] {
			t.Errorf("struct json tag %q has no field-table row in protocol.md (GG-7 forward drift)", tag)
		}
	}
	// Reverse: no documented field-table row lacks a backing struct field.
	for name := range doc {
		if !tagSet[name] {
			t.Errorf("protocol.md field-table row %q has no matching struct json tag (GG-7 reverse drift)", name)
		}
	}
}

// docFieldRows parses protocol.md and returns the set of field names taken from
// the FIRST column of every field-table row. A field table is one whose header
// row contains "JSON key"; each field row's first column carries the name in
// backticks. This matches a field TABLE row specifically, so a stray prose
// mention of a field name (or a non-field table like the frame-type table) never
// counts (F9).
func docFieldRows(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join(repoRoot(t), "docs", "specifications", "protocol.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read protocol.md: %v", err)
	}
	fields := map[string]bool{}
	inTable := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "|") && strings.Contains(trimmed, "JSON key") {
			inTable = true // header row of a field table
			continue
		}
		if !inTable {
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			inTable = false // table ended
			continue
		}
		if strings.HasPrefix(trimmed, "| ---") || strings.HasPrefix(trimmed, "|---") {
			continue // separator row
		}
		cols := strings.SplitN(trimmed, "|", 3) // ["", " `name` ", " ... "]
		if len(cols) < 2 {
			continue
		}
		if name := backticked(cols[1]); name != "" {
			fields[name] = true
		}
	}
	return fields
}

// backticked returns the first backtick-delimited token in s, or "".
func backticked(s string) string {
	i := strings.IndexByte(s, '`')
	if i < 0 {
		return ""
	}
	rest := s[i+1:]
	j := strings.IndexByte(rest, '`')
	if j < 0 {
		return ""
	}
	return rest[:j]
}
