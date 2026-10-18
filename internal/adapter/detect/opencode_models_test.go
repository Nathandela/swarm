package detect

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
)

const openCodeCatalogFixture = `openrouter/~anthropic/claude-fable-latest
{"id":"~anthropic/claude-fable-latest","providerID":"openrouter","name":"Claude Fable Latest","status":"active","api":{"id":"anthropic/claude-fable"},"cost":{"input":1}}
opencode/glm-5.3-flash-free
{"id":"glm-5.3-flash-free","providerID":"opencode","name":"GLM 5.3 Flash Free","status":"active","limit":{"context":1048576}}
opencode/cat-jam-free
{"id":"cat-jam-free","providerID":"opencode","name":"Cat Jam Free","status":"deprecated"}
openrouter/wrong-provider
{"id":"wrong-provider","providerID":"opencode","name":"Wrong Provider"}
opencode/deepseek-v4.1-flash-free
{"id":"deepseek-v4.1-flash-free","providerID":"opencode","name":"DeepSeek V4.1 Flash Free"}
opencode/glm-5.3-flash-free
{"id":"glm-5.3-flash-free","providerID":"opencode","name":"Duplicate"}
`

func TestParseOpenCodeModels(t *testing.T) {
	want := []adapter.ModelChoice{
		{ID: "opencode/deepseek-v4.1-flash-free", Display: "DeepSeek V4.1 Flash Free"},
		{ID: "opencode/glm-5.3-flash-free", Display: "GLM 5.3 Flash Free"},
		{ID: "openrouter/~anthropic/claude-fable-latest", Display: "Claude Fable Latest"},
	}
	if got := parseOpenCodeModels([]byte(openCodeCatalogFixture)); !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %+v, want %+v", got, want)
	}
	for _, data := range []string{
		strings.TrimSuffix(openCodeCatalogFixture, "\n") + "\nmalformed\n{",
		"opencode/valid\n{\"id\":\"valid\",\"providerID\":\"opencode\",\"name\":\"Valid\"}\nopencode/broken\n{",
		"opencode/valid\n{\"id\":\"valid\",\"providerID\":\"opencode\",\"name\":\"Valid\"}\nstray",
	} {
		if got := parseOpenCodeModels([]byte(data)); got != nil {
			t.Errorf("malformed stream returned partial choices: %+v", got)
		}
	}
}

func TestParseOpenCodeDefault(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"model":"openrouter/~anthropic/claude-fable-latest","provider":{"openrouter":{"apiKey":"synthetic-secret"}}}`, "openrouter/~anthropic/claude-fable-latest"},
		{`{"model":"opencode/glm-5.3-flash-free"}`, "opencode/glm-5.3-flash-free"},
		{`{"small_model":"opencode/big-pickle"}`, ""},
		{`{"model":"bad\nvalue"}`, ""},
		{`{"model":"no-provider"}`, ""},
		{`{`, ""},
	} {
		if got := parseOpenCodeDefault([]byte(tc.raw)); got != tc.want {
			t.Errorf("default from %q = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestProbeModelsOpenCodeUsesNativeCLIAndFallsBack(t *testing.T) {
	bin := t.TempDir()
	path := filepath.Join(bin, "opencode")
	script := "#!/bin/sh\ncase \"$1 $2\" in\n" +
		"  'models --verbose') cat <<'MODELS'\n" + openCodeCatalogFixture + "MODELS\n;;\n" +
		"  'debug config') printf '%s\\n' '{\"model\":\"opencode/glm-5.3-flash-free\"}' ;;\n" +
		"  *) exit 3 ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	configured, models := ProbeModels("opencode")
	if configured != "opencode/glm-5.3-flash-free" || len(models) != 3 {
		t.Fatalf("probe = (%q, %+v), want configured GLM and three selectable IDs", configured, models)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if configured, models := ProbeModels("opencode"); configured != "" || models != nil {
		t.Fatalf("failed native CLI probe = (%q, %+v), want editable fallback", configured, models)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if configured, models := ProbeModels("opencode"); configured != "" || models != nil {
		t.Fatalf("missing binary probe = (%q, %+v), want editable fallback", configured, models)
	}
}

func TestRunOpenCodeProbeIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, ok := runOpenCodeProbe(ctx, cancel, path, "models", "--verbose"); ok || time.Since(start) > 1500*time.Millisecond {
		t.Fatal("hanging native CLI did not fail within the timeout")
	}

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	out := &openCodeProbeOutput{limit: 4, cancel: cancel}
	if _, err := out.Write([]byte("12345")); err == nil || ctx.Err() == nil {
		t.Fatal("oversized native output did not cancel the probe")
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nhead -c 4194305 /dev/zero\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if data, ok := runOpenCodeProbe(ctx, cancel, path, "models", "--verbose"); ok || len(data) != 0 {
		t.Fatalf("oversized native output was accepted: ok=%v bytes=%d", ok, len(data))
	}
}
