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

// TestComposeLaunchSpec_ReferenceAgentRejected asserts GG-6 scope: in a real install
// (no fake-agent binary configured — the production condition), the fixture-only
// "reference" adapter, though it stays registered for the E9.5 characterization
// harness, is NOT launchable through the assembled daemon — a launch RPC naming it is
// refused at the compose boundary. The real production providers (claude/codex) still
// resolve to a concrete argv even in production mode.
func TestComposeLaunchSpec_ReferenceAgentRejected(t *testing.T) {
	// Production mode: fakeAgentBin == "" (SWARM_FAKE_AGENT_BIN unset in a real install).
	refSpec := daemon.LaunchSpec{AgentType: "reference", Cwd: "/work", Options: map[string]string{}}
	got, err := composeLaunchSpec(refSpec, testEndpoint, "", srcGetter("", persist.Meta{}), stubLookPath)
	if err == nil {
		t.Fatalf("production launch of the fixture-only reference adapter was accepted (got %+v); GG-6 requires it be refused", got)
	}
	if len(got.Argv) != 0 {
		t.Errorf("a refused launch must compose no argv; got %v", got.Argv)
	}

	// The real production providers still resolve to their bare binary (absolute path).
	for _, agent := range []string{"claude", "codex"} {
		spec := daemon.LaunchSpec{AgentType: agent, Cwd: "/work", Options: map[string]string{}}
		got, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter("", persist.Meta{}), stubLookPath)
		if err != nil {
			t.Fatalf("production agent %q rejected: %v", agent, err)
		}
		if len(got.Argv) == 0 || got.Argv[0] != "/abs/"+agent {
			t.Fatalf("%s launch argv[0] = %v; want the resolved %s binary", agent, got.Argv, agent)
		}
	}

	// The reserved fake agent still resolves to the fake-agent binary (dev/test mode).
	fakeSpec := daemon.LaunchSpec{AgentType: "fake", Cwd: "/work", Options: map[string]string{"script": "/s.txt"}}
	fakeGot, err := composeLaunchSpec(fakeSpec, testEndpoint, "/bin/fake-agent", srcGetter("", persist.Meta{}), stubLookPath)
	if err != nil {
		t.Fatalf("fake launch rejected: %v", err)
	}
	if len(fakeGot.Argv) == 0 || fakeGot.Argv[0] != "/bin/fake-agent" {
		t.Fatalf("fake launch argv = %v; want the fake-agent binary", fakeGot.Argv)
	}
}

// TestComposeLaunchSpec_ReferenceAllowedInDevTest pins the other half of the GG-6
// gate: in DEV/TEST mode (a fake-agent binary configured — the same signal that
// enables the reserved "fake" agent), the reference adapter IS launchable, so it can
// remain the non-billable e2e vehicle for the conversation-capture/resume flows
// (C1/R2) without reopening any production launch surface.
func TestComposeLaunchSpec_ReferenceAllowedInDevTest(t *testing.T) {
	spec := daemon.LaunchSpec{AgentType: "reference", Cwd: "/work", Options: map[string]string{}}
	got, err := composeLaunchSpec(spec, testEndpoint, "/bin/fake-agent", srcGetter("", persist.Meta{}), stubLookPath)
	if err != nil {
		t.Fatalf("dev/test launch of reference rejected: %v", err)
	}
	if len(got.Argv) == 0 || got.Argv[0] != "/abs/reference-cli" {
		t.Fatalf("reference launch argv[0] = %v; want the resolved reference-cli binary", got.Argv)
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
