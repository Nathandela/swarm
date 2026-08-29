package hermes

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

const realVersionBanner = "Hermes Agent v0.20.6 (2026.8.27) · upstream aff5125f\n"

const fixtureConversationID = "20260829_103232_1a7c23"

func newAdapter() adapter.Adapter { return New() }

func TestDescriptorsAndSupportedVersions(t *testing.T) {
	a := newAdapter()
	if got := a.Name(); got != "hermes" {
		t.Errorf("Name() = %q; want hermes", got)
	}
	if got := a.Binary(); got != "hermes" {
		t.Errorf("Binary() = %q; want hermes", got)
	}
	if got := a.VersionArgs(); !reflect.DeepEqual(got, []string{"--version"}) {
		t.Errorf("VersionArgs() = %v; want [--version]", got)
	}
	if got := a.SupportedVersions(); got != (adapter.VersionConstraint{Min: "0.20.6", Max: "9999.0.0"}) {
		t.Errorf("SupportedVersions() = %+v; want 0.20.6..9999.0.0", got)
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "characterized banner", in: realVersionBanner, want: "0.20.6", ok: true},
		{name: "without v", in: "Hermes Agent 12.34.56\n", want: "12.34.56", ok: true},
		{name: "parenthesized", in: "Hermes Agent (v1.2.3)\n", want: "1.2.3", ok: true},
		{name: "first semantic token", in: "Hermes Agent 3.4.5 and 9.9.9", want: "3.4.5", ok: true},
		{name: "garbage", in: "Hermes Agent development build", ok: false},
		{name: "date is not a version", in: "Hermes Agent development build (2026.8.27)", ok: false},
		{name: "unrelated dotted binary", in: "Other Hermes v0.20.6", ok: false},
		{name: "two components", in: "v1.2", ok: false},
		{name: "empty component", in: "v1..2", ok: false},
		{name: "embedded prefix", in: "buildv1.2.3", ok: false},
		{name: "embedded suffix", in: "v1.2.3beta", ok: false},
		{name: "empty", in: "", ok: false},
		{name: "invalid utf8", in: "\x00\xff\x1b[garbage", ok: false},
		{name: "oversized components", in: strings.Repeat("9", 4096) + ".2.3", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := newAdapter().ParseVersion(tt.in)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("ParseVersion(%q) = (%q,%v); want (%q,%v)", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

type stubProber struct {
	path string
	out  string
}

func (s stubProber) LookPath(string) (string, error)      { return s.path, nil }
func (s stubProber) Run(string, []string) (string, error) { return s.out, nil }

func TestDetectCharacterizedVersionAndFloor(t *testing.T) {
	a := newAdapter()
	current := adapter.Detect(a, stubProber{path: "/usr/local/bin/hermes", out: realVersionBanner})
	if !current.Found || current.Version != "0.20.6" || !current.InRange {
		t.Fatalf("Detect(characterized) = %+v; want found/in-range 0.20.6", current)
	}
	old := adapter.Detect(a, stubProber{path: "/usr/local/bin/hermes", out: "Hermes Agent v0.20.5\n"})
	if !old.Found || old.InRange {
		t.Fatalf("Detect(old) = %+v; want found but out of range", old)
	}
}

func TestCommandDeterministicArgv(t *testing.T) {
	tests := []struct {
		name string
		spec adapter.LaunchSpec
		want []string
	}{
		{
			name: "minimum always forces classic cli",
			spec: adapter.LaunchSpec{},
			want: []string{"hermes", "chat", "--cli"},
		},
		{
			name: "all options in fixed order",
			spec: adapter.LaunchSpec{
				Options: map[string]string{
					"skills": "review,debug", "model": "swarm-test", "profile": "coder",
					"yolo": "true", "provider": "swarm-mock", "toolsets": "terminal,web",
					"reasoning": "high", "unknown": "ignored",
				},
				InitialPrompt: "Reply exactly pong",
			},
			want: []string{
				"hermes", "--profile", "coder", "chat", "--cli",
				"--provider", "swarm-mock", "--model", "swarm-test", "--reasoning", "high",
				"--toolsets", "terminal,web", "--skills", "review,debug", "--yolo",
				"-q", "Reply exactly pong",
			},
		},
		{
			name: "false yolo omitted and prompt remains atomic",
			spec: adapter.LaunchSpec{
				Options:       map[string]string{"yolo": "false"},
				InitialPrompt: "quote ' $(touch nope)\nsecond line",
			},
			want: []string{"hermes", "chat", "--cli", "-q", "quote ' $(touch nope)\nsecond line"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := newAdapter().Command(tt.spec)
			if err != nil {
				t.Fatalf("Command: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Command() = %#v; want %#v", got, tt.want)
			}
		})
	}
}

func TestResumeCarriesProfileIDAndRuntimeOptions(t *testing.T) {
	a := newAdapter()
	none, err := a.Resume(adapter.ResumeSpec{Options: map[string]string{"profile": "coder"}})
	if err != nil {
		t.Fatalf("Resume(empty): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("Resume(empty) = %v; want no command", none)
	}

	got, err := a.Resume(adapter.ResumeSpec{
		ConversationID: fixtureConversationID,
		Options: map[string]string{
			"profile": "coder", "provider": "swarm-mock", "model": "swarm-test",
			"reasoning": "ultra", "toolsets": "terminal,web", "skills": "review", "yolo": "true",
		},
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	want := []string{
		"hermes", "--profile", "coder", "chat", "--cli",
		"--resume", fixtureConversationID, "--no-restore-cwd",
		"--provider", "swarm-mock", "--model", "swarm-test", "--reasoning", "ultra",
		"--toolsets", "terminal,web", "--skills", "review", "--yolo",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Resume() = %#v; want %#v", got, want)
	}
}

func TestConversationIDValidatorUsesClassicSessionParser(t *testing.T) {
	validator, ok := adapter.AsConversationIDValidator(newAdapter())
	if !ok {
		t.Fatal("Hermes adapter does not expose its optional conversation-ID validator")
	}
	for _, id := range []string{
		fixtureConversationID,
		secondConversationID,
	} {
		if !validator.IsValidConversationID(id) {
			t.Errorf("IsValidConversationID(%q) = false; want true", id)
		}
	}
	for _, id := range []string{
		"",
		"20261340_296199_1a7c23",
		"20260829_103232_1A7C23",
		"20260829_103232_1a7c23ff",
		fixtureConversationID + "\n",
	} {
		if validator.IsValidConversationID(id) {
			t.Errorf("IsValidConversationID(%q) = true; want false", id)
		}
	}
}

func TestOptionsSchema(t *testing.T) {
	got := newAdapter().Options()
	want := []adapter.OptionSpec{
		{Key: "profile", Label: "Profile", Type: "string"},
		{Key: "provider", Label: "Provider", Type: "string"},
		{Key: "model", Label: "Model", Type: "string"},
		{Key: "reasoning", Label: "Reasoning", Type: "choice", Choices: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}},
		{Key: "toolsets", Label: "Toolsets (comma-separated)", Type: "string"},
		{Key: "skills", Label: "Skills (comma-separated)", Type: "string"},
		{Key: "yolo", Label: "YOLO — auto-approve all tool calls (dangerous)", Type: "bool", Default: "false"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Options() = %#v; want %#v", got, want)
	}

	// Returned schemas must not expose shared mutable slice storage.
	got[0].Key = "corrupted"
	got[3].Choices[0] = "corrupted"
	again := newAdapter().Options()
	if again[0].Key != "profile" || again[3].Choices[0] != "none" {
		t.Fatalf("Options() leaked caller mutation: %#v", again)
	}
}

func TestSignalSources(t *testing.T) {
	got := newAdapter().SignalSources()
	want := []adapter.SignalSource{{Kind: "heuristic", Descriptor: map[string]string{"grid": "hermes"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SignalSources() = %#v; want %#v", got, want)
	}
	got[0].Descriptor["grid"] = "corrupted"
	if again := newAdapter().SignalSources(); again[0].Descriptor["grid"] != "hermes" {
		t.Fatalf("SignalSources() leaked caller mutation: %#v", again)
	}
}

func TestConformance(t *testing.T) {
	a := newAdapter()
	if errs := adapter.CheckConformance(a); len(errs) != 0 {
		t.Fatalf("Hermes adapter is not conformant: %v", errs)
	}
	adapter.Conformance(t, a)
}
