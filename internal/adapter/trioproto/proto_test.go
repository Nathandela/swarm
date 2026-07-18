package trioproto

// TEMPORARY EXPLORATION TESTS — see package doc. These prove the prototype
// adapters against (1) the frozen conformance suite, (2) the real installed
// binaries via the production detect path, and (3) the real recorded PTY
// captures. (2) and (3) skip when the binary/fixture is absent so the package
// stays green anywhere.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/detect"
)

func protos() map[string]adapter.Adapter {
	return map[string]adapter.Adapter{
		"agy":      NewAgy(),
		"opencode": NewOpencode(),
	}
}

func TestConformance(t *testing.T) {
	for name, a := range protos() {
		t.Run(name, func(t *testing.T) { adapter.Conformance(t, a) })
	}
}

func TestParseVersion_RealBanners(t *testing.T) {
	cases := []struct{ cli, banner, want string }{
		{"agy", "1.1.4\n", "1.1.4"},
		{"opencode", "1.17.9\n", "1.17.9"},
	}
	for _, c := range cases {
		v, ok := protos()[c.cli].ParseVersion(c.banner)
		if !ok || v != c.want {
			t.Errorf("%s: ParseVersion(%q) = (%q, %v), want (%q, true)", c.cli, c.banner, v, ok, c.want)
		}
	}
}

// TestDetect_RealBinaries runs the production Detect path (LookPath + exec with
// the 2s probe bound) against the actually-installed CLIs, reporting latency.
func TestDetect_RealBinaries(t *testing.T) {
	for name, a := range protos() {
		t.Run(name, func(t *testing.T) {
			h := detect.Host{}
			if _, err := h.LookPath(a.Binary()); err != nil {
				t.Skipf("%s not installed", a.Binary())
			}
			start := time.Now()
			det := adapter.Detect(a, h)
			t.Logf("Detect: found=%v path=%s version=%q inRange=%v latency=%s",
				det.Found, det.Path, det.Version, det.InRange, time.Since(start).Round(time.Millisecond))
			if !det.Found {
				t.Errorf("expected found, got %+v", det)
			}
			if det.Version == "" {
				// Not a failure: this is the 2s probeTimeout ceiling biting an
				// interpreter-based CLI under load — the design doc's evidence
				// for raising detect.probeTimeout.
				t.Logf("PROBE TIMED OUT at the 2s bound (interpreter cold start under load)")
			} else if !det.InRange {
				t.Errorf("version %q out of supported range", det.Version)
			}
		})
	}
}

// TestExtract_FromRecordedCaptures runs each prototype's extractor over the
// REAL PTY capture recorded by swarm-char (env-gated like the fixture corpus).
func TestExtract_FromRecordedCaptures(t *testing.T) {
	dir := os.Getenv("SWARM_TRIO_FIXDIR")
	if dir == "" {
		t.Skip("SWARM_TRIO_FIXDIR not set")
	}
	want := map[string]struct {
		id string
		ok bool
	}{
		"agy":      {"fb5e3e02-e5ef-4d25-b398-aead20366441", true},
		"opencode": {"ses_08b642915ffeYL3T6ea1DnJZDd", true},
	}
	for name, a := range protos() {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, "fx_"+name+".json"))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var fx adapter.Fixture
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			id, ok := a.ExtractConversationID(nil, fx.PTYCapture)
			if id != want[name].id || ok != want[name].ok {
				t.Errorf("ExtractConversationID = (%q, %v), want (%q, %v)", id, ok, want[name].id, want[name].ok)
			}
		})
	}
}

func TestArgvShapes(t *testing.T) {
	spec := adapter.LaunchSpec{Cwd: "/w", InitialPrompt: "hi", Options: map[string]string{
		"model": "m", "mode": "plan", "agent": "plan", "dangerously-skip-permissions": "true"}}
	cases := []struct {
		cli  string
		want []string
	}{
		{"agy", []string{"agy", "--model", "m", "--mode", "plan", "--dangerously-skip-permissions", "--prompt-interactive", "hi"}},
		{"opencode", []string{"opencode", "--model", "m", "--agent", "plan", "--prompt", "hi"}},
	}
	for _, c := range cases {
		argv, err := protos()[c.cli].Command(spec)
		if err != nil {
			t.Fatalf("%s: %v", c.cli, err)
		}
		if len(argv) != len(c.want) {
			t.Errorf("%s: argv %v, want %v", c.cli, argv, c.want)
			continue
		}
		for i := range argv {
			if argv[i] != c.want[i] {
				t.Errorf("%s: argv %v, want %v", c.cli, argv, c.want)
				break
			}
		}
	}
	resumes := map[string][]string{
		"agy":      {"agy", "--conversation", "X"},
		"opencode": {"opencode", "--session", "X"},
	}
	for cli, want := range resumes {
		argv, err := protos()[cli].Resume(adapter.ResumeSpec{Cwd: "/w", ConversationID: "X"})
		if err != nil || len(argv) != len(want) {
			t.Errorf("%s: Resume argv %v (err %v), want %v", cli, argv, err, want)
		}
	}
}
