package skeleton

// ADR-024 side-fix: a resume-as-new-session KEEPS the source's launch options.
// The TUI's resume request carries only resume_from, so before the merge the
// composed argv silently dropped --model/--sandbox (observed live 2026-09-01: a
// resumed codex fell from its Workspace sandbox to the thread default). Pure
// composeLaunchSpec tests -- no live daemon.

import (
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func endedCodexSource(local string, options map[string]string) persist.Meta {
	return persist.Meta{
		ID:             local,
		AgentType:      "codex",
		ConversationID: "01a056ec-f192-7961-84cc-b4fa60e47aee",
		LaunchOptions:  options,
		Status:         status.Status{Process: status.ProcessExited},
	}
}

func TestResumeCarriesTheSourceLaunchOptions(t *testing.T) {
	const local = "srclocal"
	src := endedCodexSource(local, map[string]string{"model": "gpt-5.6-sol", "sandbox": "workspace-write"})
	spec := daemon.LaunchSpec{
		AgentType: "codex",
		Cwd:       "/work",
		Options:   map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, local)},
	}
	got, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter(local, src), stubLookPath)
	if err != nil {
		t.Fatalf("resume rejected: %v", err)
	}
	for _, want := range []string{"resume", src.ConversationID, "--model", "gpt-5.6-sol", "--sandbox", "workspace-write",
		"check_for_update_on_startup=false" /* a recycled session comes back ready, never parked on the update nag */} {
		if !argvContains(got.Argv, want) {
			t.Errorf("resume argv %v is missing %q (the source's launch option)", got.Argv, want)
		}
	}
	if got.Options["model"] != "gpt-5.6-sol" || got.Options["sandbox"] != "workspace-write" {
		t.Errorf("resolved Options %v did not persist the merged launch options; a chained resume would drop them", got.Options)
	}
}

func TestResumeRequestOptionsBeatTheSources(t *testing.T) {
	const local = "srclocal"
	src := endedCodexSource(local, map[string]string{"model": "gpt-5.6-sol"})
	spec := daemon.LaunchSpec{
		AgentType: "codex",
		Cwd:       "/work",
		Options: map[string]string{
			protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, local),
			"model":                   "gpt-5.6-terra",
		},
	}
	got, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter(local, src), stubLookPath)
	if err != nil {
		t.Fatalf("resume rejected: %v", err)
	}
	if !argvContains(got.Argv, "gpt-5.6-terra") || argvContains(got.Argv, "gpt-5.6-sol") {
		t.Errorf("argv %v: the request's explicit model must beat the source's", got.Argv)
	}
}

// TestResumeNeverChainsReservedKeys: a source that was ITSELF resumed persisted
// its own resume_from; inheriting it (or handoff_from, or the fake-agent script)
// would re-orchestrate a past generation.
func TestResumeNeverChainsReservedKeys(t *testing.T) {
	const local = "srclocal"
	src := endedCodexSource(local, map[string]string{
		protocol.OptionResumeFrom:           protocol.NamespacedID(testEndpoint, "greatgrandparent"),
		protocol.OptionHandoffFrom:          protocol.NamespacedID(testEndpoint, "elsewhere"),
		protocol.OptionResumeConversationID: "01a056ec-0000-7961-84cc-b4fa60e47aee",
		"script":                            "/tmp/fake-script",
		"model":                             "gpt-5.6-sol",
	})
	spec := daemon.LaunchSpec{
		AgentType: "codex",
		Cwd:       "/work",
		Options:   map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, local)},
	}
	got, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter(local, src), stubLookPath)
	if err != nil {
		t.Fatalf("resume rejected: %v", err)
	}
	for _, reserved := range []string{protocol.OptionHandoffFrom, protocol.OptionResumeConversationID, "script"} {
		if _, present := got.Options[reserved]; present {
			t.Errorf("reserved key %q chained through the resume merge", reserved)
		}
	}
	if got.Options[protocol.OptionResumeFrom] != protocol.NamespacedID(testEndpoint, local) {
		t.Errorf("resume_from = %q; want the REQUEST's source, never the inherited one", got.Options[protocol.OptionResumeFrom])
	}
	if got.ResumedFrom != local {
		t.Errorf("ResumedFrom = %q; want %q", got.ResumedFrom, local)
	}
}

// TestResumeMergeNeverMutatesTheRequestMap: composeLaunchSpec receives the
// caller's map by reference; the merge must build a fresh one.
func TestResumeMergeNeverMutatesTheRequestMap(t *testing.T) {
	const local = "srclocal"
	src := endedCodexSource(local, map[string]string{"model": "gpt-5.6-sol"})
	reqOptions := map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, local)}
	want := map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, local)}
	spec := daemon.LaunchSpec{AgentType: "codex", Cwd: "/work", Options: reqOptions}
	if _, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter(local, src), stubLookPath); err != nil {
		t.Fatalf("resume rejected: %v", err)
	}
	if !reflect.DeepEqual(reqOptions, want) {
		t.Errorf("the caller's Options map was mutated: %v", reqOptions)
	}
}
