package skeleton

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func usageLaunchFixture(t *testing.T) (string, string, []string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, cwd := filepath.Join(root, "native"), filepath.Join(root, "project")
	for _, dir := range []string{home, cwd} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return home, cwd, []string{"claude", "--settings", `{"hooks":{"Stop":[]}}`, "--model", "opus"}
}

func TestLoginClaudeUsageLaunchPreservesCommonSettings(t *testing.T) {
	home, cwd, argv := usageLaunchFixture(t)
	const raw = `{"statusLine":{"type":"command","command":"printf 'custom; $0'","padding":3,"refreshInterval":10,"hideVimModeIndicator":true},"model":"sonnet"}`
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loginClaudeUsageArgs(argv, home, cwd, nil)
	var settings map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got[2]), &settings); err != nil {
		t.Fatal(err)
	}
	var line map[string]any
	if err := json.Unmarshal(settings["statusLine"], &line); err != nil {
		t.Fatal(err)
	}
	if line["padding"] != float64(3) || line["refreshInterval"] != float64(10) || line["hideVimModeIndicator"] != true || !strings.Contains(line["command"].(string), " hook SwarmUsage '") || string(settings["hooks"]) != `{"Stop":[]}` {
		t.Fatalf("native common settings changed: %s", got[2])
	}
	_, quoted, _ := strings.Cut(line["command"].(string), " hook SwarmUsage ")
	decoded, err := exec.Command("/bin/sh", "-c", "set -- "+quoted+`; printf '%s' "$1"`).Output()
	if err != nil || string(decoded) != "printf 'custom; $0'" {
		t.Fatalf("original shell command changed by quoting: %q, %v", decoded, err)
	}
	if argv[2] != `{"hooks":{"Stop":[]}}` {
		t.Fatal("caller argv mutated")
	}
	after, _ := os.ReadFile(filepath.Join(home, "settings.json"))
	if string(after) != raw {
		t.Fatal("native settings file mutated")
	}
}

func TestLoginClaudeUsageLaunchSkipsUnsupportedOverrides(t *testing.T) {
	for _, test := range []struct{ name, file, body string }{
		{"project", ".claude/settings.json", `{"statusLine":{"type":"command","command":"printf project"}}`},
		{"local", ".claude/settings.local.json", `{"statusLine":null}`},
		{"invalid", ".claude/settings.json", `{`},
		{"duplicate", ".claude/settings.json", `{"statusLine":null,"statusLine":null}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			home, cwd, argv := usageLaunchFixture(t)
			path := filepath.Join(cwd, test.file)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := loginClaudeUsageArgs(argv, home, cwd, nil); !reflect.DeepEqual(got, argv) {
				t.Fatalf("unsupported native override replaced: %v", got)
			}
		})
	}
	home, cwd, argv := usageLaunchFixture(t)
	argv[2] = `{"hooks":{},"statusLine":{"type":"command","command":"printf explicit"}}`
	if got := loginClaudeUsageArgs(argv, home, cwd, nil); !reflect.DeepEqual(got, argv) {
		t.Fatalf("explicit native override replaced: %v", got)
	}
}

func TestLoginClaudeUsageLaunchAddsQuietObserver(t *testing.T) {
	home, cwd, argv := usageLaunchFixture(t)
	got := loginClaudeUsageArgs(argv, home, cwd, nil)
	if !strings.Contains(got[2], ` hook SwarmUsage ''`) {
		t.Fatalf("quiet observer missing: %v", got)
	}
}

func TestLoginClaudeUsageLaunchRecognizesNativeHomeAlias(t *testing.T) {
	home, cwd, argv := usageLaunchFixture(t)
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"statusLine":{"type":"command","command":"printf custom"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, filepath.Join(filepath.Dir(cwd), ".claude")); err != nil {
		t.Fatal(err)
	}
	got := loginClaudeUsageArgs(argv, home, cwd, nil)
	if !strings.Contains(got[2], " hook SwarmUsage") {
		t.Fatalf("native home alias mistaken for project override: %v", got)
	}
}

func TestLoginClaudeUsageLaunchSkipsOrdinaryOwnerHomeSettings(t *testing.T) {
	home, cwd, argv := usageLaunchFixture(t)
	owner := filepath.Dir(cwd)
	if err := os.MkdirAll(filepath.Join(owner, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner, ".claude", "settings.json"), []byte(`{"statusLine":{"type":"command","command":"printf external"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loginClaudeUsageArgs(argv, home, cwd, []string{"HOME=" + owner})
	if !strings.Contains(got[2], " hook SwarmUsage") {
		t.Fatalf("ordinary native settings mistaken for project override: %v", got)
	}
}
