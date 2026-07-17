package skeleton

// FIX 5 unit tests for composeLaunchSpec: the resume-as-new-session flow validates
// the source and composes the adapter's REAL resume argv (carrying the source's
// conversation id + linking ResumedFrom), and a fresh launch composes the adapter's
// Command argv with the bare binary resolved to an absolute path. Pure — no live
// daemon: getSource and lookPath are stubs.

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

const testEndpoint = "ep-test01"

// stubLookPath resolves any bare name to /abs/<name> (a fake absolute path) and
// passes an already-absolute path through.
func stubLookPath(name string, _ []string) (string, error) {
	if strings.HasPrefix(name, "/") {
		return name, nil
	}
	return "/abs/" + name, nil
}

// srcGetter returns a getSource closure over a single seeded source meta.
func srcGetter(local string, m persist.Meta) func(string) (persist.Meta, bool) {
	return func(q string) (persist.Meta, bool) {
		if q == local {
			return m, true
		}
		return persist.Meta{}, false
	}
}

func endedClaudeSource(local string) persist.Meta {
	return persist.Meta{
		ID:             local,
		AgentType:      "claude",
		ConversationID: "conv-XYZ-789",
		Status:         status.Status{Process: status.ProcessExited},
	}
}

func TestComposeLaunchSpec_ValidClaudeResume(t *testing.T) {
	const local = "srclocal"
	src := endedClaudeSource(local)
	spec := daemon.LaunchSpec{
		AgentType: "claude",
		Cwd:       "/work",
		Options:   map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, local)},
	}

	got, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter(local, src), stubLookPath)
	if err != nil {
		t.Fatalf("valid resume rejected: %v", err)
	}
	if got.ResumedFrom != local {
		t.Errorf("ResumedFrom = %q; want %q (the resume link)", got.ResumedFrom, local)
	}
	if len(got.Argv) == 0 || got.Argv[0] != "/abs/claude" {
		t.Fatalf("resume argv[0] = %v; want the resolved claude binary", got.Argv)
	}
	if !argvContains(got.Argv, "--resume") || !argvContains(got.Argv, src.ConversationID) {
		t.Errorf("resume argv %v does not carry --resume + the source conversation id %q", got.Argv, src.ConversationID)
	}
}

func TestComposeLaunchSpec_InvalidResumeRejected(t *testing.T) {
	const local = "srclocal"
	claudeSrc := endedClaudeSource(local)
	runningSrc := claudeSrc
	runningSrc.Status.Process = status.ProcessRunning
	codexSrc := claudeSrc
	codexSrc.AgentType = "codex"

	cases := []struct {
		name      string
		agentType string
		resumeID  string
		src       persist.Meta
		found     bool
	}{
		{"foreign endpoint", "claude", protocol.NamespacedID("ep-other", local), claudeSrc, true},
		{"malformed id", "claude", "not-namespaced", claudeSrc, true},
		{"source not found", "claude", protocol.NamespacedID(testEndpoint, local), claudeSrc, false},
		{"source still running", "claude", protocol.NamespacedID(testEndpoint, local), runningSrc, true},
		{"agent type mismatch", "claude", protocol.NamespacedID(testEndpoint, local), codexSrc, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			get := func(q string) (persist.Meta, bool) {
				if tc.found && q == local {
					return tc.src, true
				}
				return persist.Meta{}, false
			}
			spec := daemon.LaunchSpec{
				AgentType: tc.agentType,
				Cwd:       "/work",
				Options:   map[string]string{protocol.OptionResumeFrom: tc.resumeID},
			}
			if _, err := composeLaunchSpec(spec, testEndpoint, "", get, stubLookPath); err == nil {
				t.Fatalf("invalid resume (%s) was accepted; want a clear rejection", tc.name)
			}
		})
	}
}

func TestComposeLaunchSpec_FreshClaudeLaunchComposesArgv(t *testing.T) {
	spec := daemon.LaunchSpec{AgentType: "claude", Cwd: "/work", Options: map[string]string{}}
	got, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter("", persist.Meta{}), stubLookPath)
	if err != nil {
		t.Fatalf("fresh claude launch: %v", err)
	}
	if len(got.Argv) == 0 || got.Argv[0] != "/abs/claude" {
		t.Fatalf("fresh argv[0] = %v; want the resolved claude binary", got.Argv)
	}
	if !argvContains(got.Argv, "--settings") {
		t.Errorf("fresh claude argv %v omits the --settings hook injection", got.Argv)
	}
}

// TestComposeLaunchSpec_NoConversationIDRejected: a real adapter asked to resume a
// source that never captured a conversation id must be REJECTED — never silently
// downgraded to a fresh launch falsely stamped ResumedFrom (B1).
func TestComposeLaunchSpec_NoConversationIDRejected(t *testing.T) {
	const local = "srclocal"
	src := endedClaudeSource(local)
	src.ConversationID = "" // never captured
	spec := daemon.LaunchSpec{
		AgentType: "claude",
		Cwd:       "/work",
		Options:   map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, local)},
	}
	got, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter(local, src), stubLookPath)
	if err == nil {
		t.Fatalf("resume with no captured conversation id was accepted (got %+v); want a clear rejection", got)
	}
	if got.ResumedFrom != "" || len(got.Argv) != 0 {
		t.Errorf("a rejected resume must not stamp ResumedFrom or compose argv; got ResumedFrom=%q argv=%v", got.ResumedFrom, got.Argv)
	}
}

// TestComposeLaunchSpec_FakeResumeRejected: the reserved fake agent has no registry
// adapter that can resume, so a resume request is rejected rather than relaunching
// fresh with a misleading ResumedFrom (B1).
func TestComposeLaunchSpec_FakeResumeRejected(t *testing.T) {
	const local = "fakesrc"
	src := persist.Meta{ID: local, AgentType: "fake", ConversationID: "c", Status: status.Status{Process: status.ProcessExited}}
	spec := daemon.LaunchSpec{
		AgentType: "fake",
		Cwd:       "/work",
		Options:   map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, local), "script": "/s.txt"},
	}
	if _, err := composeLaunchSpec(spec, testEndpoint, "/bin/fake-agent", srcGetter(local, src), stubLookPath); err == nil {
		t.Fatalf("fake resume (no resuming adapter) was accepted; want a clear rejection")
	}
}

func argvContains(argv []string, s string) bool {
	for _, a := range argv {
		if a == s {
			return true
		}
	}
	return false
}
