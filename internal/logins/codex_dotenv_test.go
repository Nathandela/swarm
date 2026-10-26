package logins

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func codexDotenvBirthFixture(t *testing.T) (*Store, Runtime) {
	t.Helper()
	s, _, b, op := committedTransferFixture(t)
	if _, _, err := s.TransferCodexMigration(snapshot(t, s).Revision, op.ID, b.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	rt, err := s.Runtime(ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	return s, rt
}

func TestCodexDotenvRejectsAuthSelectors(t *testing.T) {
	s, rt := codexDotenvBirthFixture(t)
	for _, key := range []string{"OPENAI_FEDERATION_RULE_ID", "OPENAI_IDENTITY_TOKEN_FILE", "OPENAI_WORKLOAD_IDENTITY_CONTEXT", "OPENAI_API_KEY", "OPENAI_ORGANIZATION", "OPENAI_PROJECT"} {
		for _, value := range []string{"synthetic-private-value", ""} {
			t.Run(key+"/"+value, func(t *testing.T) {
				if err := os.WriteFile(filepath.Join(rt.Home, ".env"), []byte("export "+key+"="+value+"\r\n"), 0600); err != nil {
					t.Fatal(err)
				}
				_, err := s.PrepareCodexExternalBirth(rt, nil)
				if !errors.Is(err, ErrIneligible) {
					t.Fatalf("auth selector assignment accepted: %v", err)
				}
				if !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "synthetic-private-value") {
					t.Fatalf("diagnostic must identify only the selector: %v", err)
				}
			})
		}
	}
}

func TestCodexAuthSelectorsNeverInherited(t *testing.T) {
	rt := Runtime{Provider: ProviderCodex, Epoch: 1, Identity: identityDigest(ProviderCodex, "a"), Home: "/private/native"}
	env, err := prepareEnvironment(rt, "", []string{
		"PATH=/bin", "TOOL_SETTING=keep",
		"OPENAI_FEDERATION_RULE_ID=synthetic-rule",
		"OPENAI_IDENTITY_TOKEN_FILE=/private/synthetic-assertion",
		"OPENAI_WORKLOAD_IDENTITY_CONTEXT=",
		"OPENAI_API_KEY=synthetic-api-key",
		"OPENAI_ORGANIZATION=synthetic-organization",
		"OPENAI_PROJECT=synthetic-project",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/bin", "TOOL_SETTING=keep", "CODEX_HOME=/private/native"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("auth selectors retained: %v", env)
	}
}

func TestCodexDotenvCheckedOnEnvironmentPreparation(t *testing.T) {
	for _, mode := range []string{"normal", "switch"} {
		t.Run(mode, func(t *testing.T) {
			var s *Store
			var rt Runtime
			var prepare func() error
			if mode == "normal" {
				s = testStore(t)
				rt = activate(t, s, add(t, s, ProviderCodex, "A", codexCredential("a", "a")))
				prepare = func() error { _, err := s.PrepareEnvironment(rt, nil); return err }
			} else {
				var op Switch
				s, _, _, op = committedTransferFixture(t)
				var err error
				rt, err = s.RuntimeForSwitch(op.ID, "unlaunched")
				if err != nil {
					t.Fatal(err)
				}
				prepare = func() error { _, err := s.PrepareSwitchEnvironment(op.ID, "unlaunched", rt, nil); return err }
			}
			if err := prepare(); err != nil {
				t.Fatalf("clean home refused: %v", err)
			}
			for _, key := range []string{"OPENAI_FEDERATION_RULE_ID", "OPENAI_API_KEY", "OPENAI_ORGANIZATION", "OPENAI_PROJECT"} {
				if err := os.WriteFile(filepath.Join(rt.Home, ".env"), []byte("export "+key+"=synthetic-private-value\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := prepare(); !errors.Is(err, ErrIneligible) {
					t.Fatalf("late dotenv selector %s accepted: %v", key, err)
				}
			}
		})
	}
}

func TestCodexDotenvGrammar(t *testing.T) {
	// Codex's locked dotenvy 0.15.7 groups quoted records before parsing values.
	// Its crate SHA-256 is 1aaf95b3e5c8f23aa320147307562d361db0ae0d51242340f558153b4eb2439b.
	largeMultiline := strings.Repeat(" ", maxCredentialBytes/2) + "TOOL_SETTING=\"first\n" + strings.Repeat("continuation\n", 4096) + "last\"\n"
	if len(largeMultiline) >= maxCredentialBytes {
		t.Fatal("multiline fixture exceeds the bounded reader")
	}
	cases := []struct {
		name string
		body string
		bad  bool
	}{
		{"empty", "", false},
		{"comments", "# OPENAI_FEDERATION_RULE_ID=ignored\n# OPENAI_API_KEY=ignored\n# OPENAI_ORGANIZATION=ignored\n# OPENAI_PROJECT=ignored\n  # unmatched '\" comment\r\n", false},
		{"ordinary", "TOOL_SETTING=keep\nTool.Path=value\nexport=value\n", false},
		{"exports_crlf", " export TOOL_SETTING = 'keep me' # '\" comment\r\n", false},
		{"unicode_whitespace", "\u2003export\u2003TOOL_SETTING\u2003=\u2003value\u2003\n", false},
		{"quoted_multiline", "TOOL_SETTING=\"first\nOPENAI_FEDERATION_RULE_ID=only-value-text\nOPENAI_API_KEY=only-value-text\nOPENAI_ORGANIZATION=only-value-text\nOPENAI_PROJECT=only-value-text\n# still a value\nlast\" # '\" comment\n", false},
		{"single_multiline", "TOOL_SETTING='first\nOPENAI_IDENTITY_TOKEN_FILE=only-value-text\nlast'\n", false},
		{"large_leading_whitespace_multiline", largeMultiline, false},
		{"selector_after_large_multiline", largeMultiline + "OPENAI_FEDERATION_RULE_ID=synthetic-private-value\n", true},
		{"quote_fragments", "TOOL_SETTING='hello '\\''world'\"\ngood ' \\'morning\"\n", false},
		{"escapes", "TOOL_SETTING=my\\ cool\\ value\nOTHER=\"quote\\\" slash\\\\ newline\\n dollar\\$\"\n", false},
		{"hash", "TOOL_SETTING=keep#literal # comment '\"\n", false},
		{"substitutions", "TOOL_SETTING=\"${OTHER}/$OTHER-keep\"\nOTHER='${LITERAL}'\n", false},
		{"native_filtered_keys", "CODEX_ACCESS_TOKEN=ignored\nCoDeX_API_KEY=ignored\n", false},
		{"api_key_assignment", "OPENAI_API_KEY=synthetic-private-value\n", true},
		{"organization_assignment", "OPENAI_ORGANIZATION=synthetic-private-value\n", true},
		{"project_assignment", "OPENAI_PROJECT=synthetic-private-value\n", true},
		{"selector_after_multiline", "TOOL_SETTING='first\nlast'\nOPENAI_IDENTITY_TOKEN_FILE=synthetic-private-value\n", true},
		{"duplicate_empty_selector", "OPENAI_FEDERATION_RULE_ID=synthetic-private-value\nOPENAI_FEDERATION_RULE_ID=\n", true},
		{"missing_equals", "TOOL_SETTING synthetic-private-value\n", true},
		{"invalid_key", ".TOOL_SETTING=synthetic-private-value\n", true},
		{"unclosed_double", "TOOL_SETTING=\"synthetic-private-value\n", true},
		{"unclosed_single", "TOOL_SETTING='synthetic-private-value\n", true},
		{"native_handles_unclosed_substitution", "TOOL_SETTING=${synthetic-private-value\n", false},
		{"native_handles_invalid_escape", "TOOL_SETTING=synthetic-private-value\\q\n", false},
		{"native_handles_trailing_junk", "TOOL_SETTING=synthetic-private-value junk\n", false},
		{"selector_after_unclosed_substitution", "TOOL_SETTING=${synthetic-private-value\nOPENAI_FEDERATION_RULE_ID=synthetic-private-value\n", true},
		{"selector_after_invalid_escape", "TOOL_SETTING=synthetic-private-value\\q\nOPENAI_FEDERATION_RULE_ID=synthetic-private-value\n", true},
		{"selector_after_trailing_junk", "TOOL_SETTING=synthetic-private-value junk\nOPENAI_FEDERATION_RULE_ID=synthetic-private-value\n", true},
		{"bom_not_removed_by_native_iterator", "\ufeffTOOL_SETTING=synthetic-private-value\n", true},
		{"invalid_utf8", "TOOL_SETTING=synthetic-private-value\xff\n", true},
		{"nul", "TOOL_SETTING=synthetic-private-value\x00\n", true},
	}
	s, rt := codexDotenvBirthFixture(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(rt.Home, ".env"), []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := s.PrepareCodexExternalBirth(rt, []string{"TOOL_SETTING=keep"})
			if tc.bad && !errors.Is(err, ErrIneligible) || !tc.bad && err != nil {
				t.Fatalf("bad=%v: %v", tc.bad, err)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-private-value") {
				t.Fatalf("dotenv diagnostic exposed a value: %v", err)
			}
		})
	}
}

func TestCodexDotenvKeepsTransportAndToolSettings(t *testing.T) {
	s, rt := codexDotenvBirthFixture(t)
	body := "HTTPS_PROXY=http://127.0.0.1:43127\n" +
		"HTTP_PROXY=http://127.0.0.1:43127\n" +
		"ALL_PROXY=http://127.0.0.1:43127\n" +
		"NO_PROXY=127.0.0.1,localhost,github.com,api.github.com,codeload.github.com\n" +
		"SSL_CERT_FILE=" + filepath.Join(rt.Home, "transport-trust.pem") + "\n"
	path := filepath.Join(rt.Home, ".env")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	env, err := s.PrepareCodexExternalBirth(rt, []string{"PATH=/bin", "TOOL_SETTING=keep"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(env, []string{"PATH=/bin", "TOOL_SETTING=keep", "CODEX_HOME=" + rt.Home}) {
		t.Fatalf("launch environment changed: %v", env)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != body {
		t.Fatalf("dotenv changed: %v", err)
	}
}

func TestCodexDotenvUsesPrivateBoundedReader(t *testing.T) {
	s, rt := codexDotenvBirthFixture(t)
	path := filepath.Join(rt.Home, ".env")
	target := filepath.Join(rt.Home, "tool.env")
	if err := os.WriteFile(target, []byte("TOOL_SETTING=keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareCodexExternalBirth(rt, nil); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink dotenv accepted: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("#", maxCredentialBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareCodexExternalBirth(rt, nil); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("oversized dotenv accepted: %v", err)
	}
}
