package accountconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeContextSettingsRepresentationsReuseWithoutWrites(t *testing.T) {
	const original = `{"model":"sonnet","cleanupPeriodDays":1,"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"echo <safe>"}]}]},"permissions":{"allow":["Read","Edit"]}}`
	for name, candidate := range map[string]string{
		"indent-order-newline": "{\n  \"permissions\": {\"allow\": [\"Read\", \"Edit\"]},\n  \"model\": \"sonnet\", \"hooks\": {\"UserPromptSubmit\": [{\"hooks\": [{\"command\": \"echo <safe>\", \"type\": \"command\"}]}]},\n  \"cleanupPeriodDays\": 1\n}\n",
		"escaped-string":       `{"permissions":{"allow":["Read","Edit"]},"hooks":{"UserPromptSubmit":[{"hooks":[{"command":"echo \u003csafe\u003e","type":"command"}]}]},"cleanupPeriodDays":1,"model":"sonnet"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := fixture(t, "claude")
			put(t, filepath.Join(f.source, "settings.json"), original)
			lease := func(_ string, install func() error) error { return install() }
			c, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease)
			if err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(f.candidate, "settings.json"), candidate)
			// These are the existing marker's canonical hashes, not new-format metadata.
			if c.SettingsSHA256 == claudeHashBytes([]byte(candidate)) {
				t.Fatal("fixture does not differ from installed canonical bytes")
			}
			before := snapshotClaudeProfileFiles(t, f.candidate)
			if err := RevalidateClaudeContext(c, f.candidate); err != nil {
				t.Error("equivalent private settings failed revalidation:", err)
			}
			reused, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease)
			if err != nil || reused.GlobalGeneration != c.GlobalGeneration {
				t.Error("equivalent private settings failed reuse:", err)
			}
			assertClaudeProfileFilesUnchanged(t, f.candidate, before)
		})
	}
}

func TestClaudeContextSettingsRejectChangedOrAmbiguousJSONWithoutWrites(t *testing.T) {
	const original = `{"model":"sonnet","cleanupPeriodDays":9007199254740992,"permissions":{"allow":["Read","Edit"]},"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"exit 0"}]}]}}`
	for name, candidate := range map[string]string{
		"model":            `{"model":"opus","cleanupPeriodDays":9007199254740992,"permissions":{"allow":["Read","Edit"]},"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"exit 0"}]}]}}`,
		"nested-hook":      `{"model":"sonnet","cleanupPeriodDays":9007199254740992,"permissions":{"allow":["Read","Edit"]},"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"exit 1"}]}]}}`,
		"permission-order": `{"model":"sonnet","cleanupPeriodDays":9007199254740992,"permissions":{"allow":["Edit","Read"]},"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"exit 0"}]}]}}`,
		"auth-selector":    `{"model":"sonnet","cleanupPeriodDays":9007199254740992,"permissions":{"allow":["Read","Edit"]},"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"exit 0"}]}]},"env":{"ANTHROPIC_API_KEY":"synthetic"}}`,
		"rounded-number":   `{"model":"sonnet","cleanupPeriodDays":9007199254740993,"permissions":{"allow":["Read","Edit"]},"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"exit 0"}]}]}}`,
		"duplicate-model":  `{"model":"opus","model":"sonnet","cleanupPeriodDays":9007199254740992,"permissions":{"allow":["Read","Edit"]},"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"exit 0"}]}]}}`,
		"duplicate-nested": `{"model":"sonnet","cleanupPeriodDays":9007199254740992,"permissions":{"allow":["Write"],"allow":["Read","Edit"]},"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"exit 0"}]}]}}`,
		"null":             "null", "array": "[]", "scalar": "true", "empty": "", "malformed": "{", "trailing": original + " {}",
	} {
		t.Run(name, func(t *testing.T) {
			f := fixture(t, "claude")
			put(t, filepath.Join(f.source, "settings.json"), original)
			lease := func(_ string, install func() error) error { return install() }
			c, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease)
			if err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(f.candidate, "settings.json"), candidate)
			before := snapshotClaudeProfileFiles(t, f.candidate)
			if err := RevalidateClaudeContext(c, f.candidate); err == nil {
				t.Error("changed or ambiguous settings admitted")
			}
			if _, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease); err == nil {
				t.Error("reuse admitted changed or ambiguous settings")
			}
			assertClaudeProfileFilesUnchanged(t, f.candidate, before)
		})
	}
}

func TestClaudeContextPartialUpdateSettingsRepresentations(t *testing.T) {
	for _, stage := range []string{"old", "new", "committed", "changed"} {
		t.Run(stage, func(t *testing.T) {
			f := fixture(t, "claude")
			lease := func(_ string, install func() error) error { return install() }
			put(t, filepath.Join(f.source, "settings.json"), `{"model":"sonnet","cleanupPeriodDays":1}`)
			previous, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease)
			if err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(f.source, "settings.json"), `{"model":"opus","cleanupPeriodDays":2}`)
			other := filepath.Join(f.root, "next")
			if err := os.Mkdir(other, 0700); err != nil {
				t.Fatal(err)
			}
			next, err := PrepareClaudeContext(other, f.cwd, f.env, lease)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeClaudeContextUpdate(f.candidate, claudeContextUpdate{1, previous, next}); err != nil {
				t.Fatal(err)
			}
			candidate := "{\n  \"model\": \"sonnet\", \"cleanupPeriodDays\": 1\n}\n"
			if stage != "old" {
				candidate = "{\n  \"model\": \"opus\", \"cleanupPeriodDays\": 2\n}\n"
			}
			if stage == "changed" {
				candidate = `{"model":"haiku","cleanupPeriodDays":2}`
			}
			put(t, filepath.Join(f.candidate, "settings.json"), candidate)
			if stage == "committed" {
				raw, err := os.ReadFile(filepath.Join(other, ClaudeContextMarker))
				if err != nil {
					t.Fatal(err)
				}
				put(t, filepath.Join(f.candidate, ClaudeContextMarker), string(raw))
			}
			before := snapshotClaudeProfileFiles(t, f.candidate)
			refreshed, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease, true)
			if stage == "changed" {
				if err == nil {
					t.Fatal("unrelated partial-update value change admitted")
				}
				assertClaudeProfileFilesUnchanged(t, f.candidate, before)
				return
			}
			if err != nil || refreshed.GlobalGeneration != next.GlobalGeneration {
				t.Fatal("equivalent old/new partial representation failed recovery:", err)
			}
			if _, err := os.Stat(filepath.Join(f.candidate, ClaudeContextUpdateMarker)); !os.IsNotExist(err) {
				t.Fatal("completed partial-update intent remains")
			}
			if stage == "committed" {
				assertClaudeProfileFilesUnchanged(t, f.candidate, before)
			}
		})
	}
}

func TestClaudeSettingsDigestPreservesExactKeysAndNumbers(t *testing.T) {
	canonical := []byte(`{"permissions":{"Case":"first","case":"second"},"cleanupPeriodDays":1}`)
	digest, err := claudeSettingsDigest(canonical)
	if err != nil || digest == "" {
		t.Fatal("case-distinct map keys refused:", err)
	}
	if ValidClaudeNativeProofJSON(canonical) {
		t.Fatal("existing folded-duplicate native proof contract changed")
	}
	for _, raw := range []string{
		`{"permissions":{"Case":"first","case":"second"},"cleanupPeriodDays":1.0}`,
		`{"permissions":{"Case":"first","case":"second"},"cleanupPeriodDays":1e0}`,
	} {
		other, err := claudeSettingsDigest([]byte(raw))
		if err != nil || other == digest {
			t.Fatal("noncanonical numeric spelling silently normalized:", err)
		}
	}
	for _, raw := range []string{`{"model":"opus","\u006dodel":"sonnet"}`, `{"permissions":{"Case":"one","Case":"two"}}`} {
		if _, err := claudeSettingsDigest([]byte(raw)); err == nil {
			t.Fatal("duplicate map key admitted")
		}
	}
}

func TestClaudeSettingsDigestRejectsLossyUnicode(t *testing.T) {
	for _, raw := range []string{
		"{\"hooks\":{\"command\":\"\xff\"}}",
		`{"hooks":{"command":"\ud800"}}`,
		`{"hooks":{"command":"\udfff"}}`,
		`{"hooks":{"command":"\ud800x"}}`,
		`{"hooks":{"command":"\ud800\u0041"}}`,
		`{"\ud800":"value"}`,
	} {
		if _, err := claudeSettingsDigest([]byte(raw)); err == nil {
			t.Errorf("lossy Unicode settings admitted: %q", raw)
		}
	}
	for _, pair := range [][2]string{
		{`{"hooks":{"command":"\ud83d\ude00"}}`, `{"hooks":{"command":"😀"}}`},
		{`{"hooks":{"command":"\ufffd"}}`, `{"hooks":{"command":"�"}}`},
		{`{"hooks":{"command":"\\ud800"}}`, `{"hooks":{"command":"\u005cud800"}}`},
	} {
		first, err := claudeSettingsDigest([]byte(pair[0]))
		if err != nil {
			t.Fatal("valid Unicode refused:", err)
		}
		second, err := claudeSettingsDigest([]byte(pair[1]))
		if err != nil || first != second {
			t.Fatal("equivalent valid Unicode differs:", err)
		}
	}
}

type claudeProfileFileSnapshot struct {
	info os.FileInfo
	raw  string
}

func snapshotClaudeProfileFiles(t *testing.T, profile string) map[string]claudeProfileFileSnapshot {
	t.Helper()
	out := map[string]claudeProfileFileSnapshot{}
	for _, name := range []string{"settings.json", ".claude.json", ClaudeContextMarker} {
		path := filepath.Join(profile, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = claudeProfileFileSnapshot{info, string(raw)}
	}
	return out
}

func assertClaudeProfileFilesUnchanged(t *testing.T, profile string, before map[string]claudeProfileFileSnapshot) {
	t.Helper()
	for name, previous := range before {
		path := filepath.Join(profile, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(previous.info, info) || !previous.info.ModTime().Equal(info.ModTime()) || string(raw) != previous.raw {
			t.Errorf("%s rewritten during reuse/refusal", name)
		}
	}
}
