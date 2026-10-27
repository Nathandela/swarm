package hermesinput

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSharedNativeConfigurationUsesProviderEnsureAndExistingProfile(t *testing.T) {
	cfg := SessionConfig{Program: "hermes", ProviderVersion: "0.22.5", Profile: "my-agent", WorkerID: "worker", Cwd: "/project", InitialPrompt: "initial"}
	if got, want := cfg.EnsureArgv(), []string{"hermes", "--profile", "my-agent", "gateway", "ensure"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ensure argv=%v", got)
	}
	for _, value := range cfg.EnsureArgv() {
		if value == "--force" || value == "--replace" {
			t.Fatal("shared owner is replaced")
		}
	}
	for _, version := range []string{"0.20.6", "0.22.4", "unknown", "0.22.5-beta.1"} {
		if SupportedVersion(version) {
			t.Fatalf("unqualified version %q", version)
		}
	}
	if !SupportedVersion("0.22.5") {
		t.Fatal("qualified stable floor refused")
	}
}

func TestPrepareUsesExistingOwnerAndDoesNotSubmitOrClone(t *testing.T) {
	b, methods, mu := fixture(t, false)
	result := map[string]any{"state": "ready", "client_code_sha": strings.Repeat("c", 40), "endpoint": map[string]any{"profile_id": b.Home, "control_home": b.Home, "instance_id": b.InstanceID, "authority_epoch": 1, "runtime_protocol": 1, "code_sha": strings.Repeat("c", 40)}}
	raw, _ := json.Marshal(result)
	program := filepath.Join(t.TempDir(), "hermes")
	if err := os.WriteFile(program, []byte("#!/bin/sh\ncat <<'NATIVE_ENSURE_FIXTURE'\n"+string(raw)+"\nNATIVE_ENSURE_FIXTURE\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d, err := Prepare(ctx, SessionConfig{Program: program, ProviderVersion: "0.22.5", WorkerID: "worker", Cwd: t.TempDir(), InitialPrompt: "never submitted here"}, []string{"PATH=" + os.Getenv("PATH")}, t.TempDir(), 123, 456)
	if err != nil {
		t.Fatal(err)
	}
	if d.Binding.OwnerPID != b.OwnerPID || d.Binding.SessionID != b.SessionID || d.Binding.AuthorityEpoch != 1 {
		t.Fatalf("shared owner binding=%+v", d)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(*methods, []string{"setup.status", "session.create"}) {
		t.Fatalf("native effects=%v", *methods)
	}
}

func TestSharedStopAndViewerCloseNeverStopGateway(t *testing.T) {
	detached := make(chan map[string]any, 1)
	b, methods, mu := fixture(t, false, func(method string, result map[string]any) {
		if method == "session.detach" {
			detached <- result
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := Stop(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := ViewerEnded(ctx, t.TempDir(), Descriptor{Binding: b}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"session.resume", "process.stop", "session.resume", "process.list", "subagent.list", "session.resume", "session.detach"}
	if !reflect.DeepEqual(*methods, want) {
		t.Fatalf("native stop/close calls=%v", *methods)
	}
	detach := <-detached
	reason, _ := detach["fixture_end_reason"].(string)
	if detach["session_id"] != b.SessionID || detach["subscription_id"] != "sub-1" || reason != "" {
		t.Fatalf("unexpected viewer detach: %v", detach)
	}
}

func TestPrepareCannotRebindCanonicalResumeToAnotherSession(t *testing.T) {
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := fixture(t, false, func(method string, result map[string]any) {
		if method == "session.resume" {
			result["info"] = map[string]string{"cwd": cwd}
		}
	})
	result := map[string]any{"state": "ready", "client_code_sha": strings.Repeat("c", 40), "endpoint": map[string]any{"profile_id": b.Home, "control_home": b.Home, "instance_id": b.InstanceID, "authority_epoch": 1, "runtime_protocol": 1, "code_sha": strings.Repeat("c", 40)}}
	raw, _ := json.Marshal(result)
	program := filepath.Join(t.TempDir(), "hermes")
	if err := os.WriteFile(program, []byte("#!/bin/sh\ncat <<'NATIVE_ENSURE_FIXTURE'\n"+string(raw)+"\nNATIVE_ENSURE_FIXTURE\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	dir := t.TempDir()
	_, err = Prepare(ctx, SessionConfig{Program: program, ProviderVersion: "0.22.5", WorkerID: "worker", Cwd: cwd, ResumeID: "local-" + strings.Repeat("b", 64)}, []string{"PATH=" + os.Getenv("PATH")}, dir, 123, 456)
	if err == nil {
		t.Fatal("canonical resume rebound to another session")
	}
	if _, err := ReadDescriptor(dir); !os.IsNotExist(err) {
		t.Fatalf("unproven resume persisted: %v", err)
	}
}

func TestViewerDetachAttemptedAfterScopedStopRefusal(t *testing.T) {
	b, methods, mu := fixture(t, false)
	b.SessionID = "local-" + strings.Repeat("b", 64)
	d := Descriptor{WorkerID: "worker", Generation: strings.Repeat("a", 32), Binding: b}
	dir := t.TempDir()
	if err := WriteStopIntent(dir, d); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := ViewerEnded(ctx, dir, d); err == nil {
		t.Fatal("native scope refusal lost")
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(*methods, []string{"session.resume", "session.resume"}) {
		t.Fatalf("viewer detach was not attempted after stop refusal: %v", *methods)
	}
}

func TestUnknownAdmissionDoesNotSkipQueuedCancellationOrBackgroundStop(t *testing.T) {
	detaches := make(chan string, 1)
	b, methods, mu := fixture(t, false, func(method string, result map[string]any) {
		if method == "session.resume" {
			result["execution_state"] = "unknown"
			result["pending"] = []any{map[string]any{"admission_id": "uncertain", "status": "unknown"}, map[string]any{"admission_id": "queued", "status": "queued"}}
		}
		if method == "session.detach" {
			reason, _ := result["fixture_end_reason"].(string)
			detaches <- reason
		}
	})
	// The fixture records detach parameters separately to catch an implicit
	// last-viewer close that can discard unknown work and drain a successor.
	d := Descriptor{WorkerID: "worker", Generation: strings.Repeat("a", 32), Binding: b}
	dir := t.TempDir()
	if err := WriteStopIntent(dir, d); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := ViewerEnded(ctx, dir, d); err == nil {
		t.Fatal("unknown admission lost")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"session.resume", "prompt.cancel", "process.stop", "session.resume", "session.detach"}
	if !reflect.DeepEqual(*methods, want) {
		t.Fatalf("unknown skipped scoped controls: %v", *methods)
	}
	if reason := <-detaches; reason != "" {
		t.Fatalf("unconfirmed stop ended unknown native scope: %q", reason)
	}
}

func TestOrdinaryViewerExitDoesNotEndAcceptedNativeWork(t *testing.T) {
	detaches := make(chan string, 1)
	b, methods, mu := fixture(t, false, func(method string, result map[string]any) {
		if method == "session.detach" {
			reason, _ := result["fixture_end_reason"].(string)
			detaches <- reason
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := ViewerEnded(ctx, t.TempDir(), Descriptor{Binding: b}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"session.resume", "session.detach"}; !reflect.DeepEqual(*methods, want) {
		t.Fatalf("ordinary exit controlled native work: %v", *methods)
	}
	if reason := <-detaches; reason != "" {
		t.Fatalf("ordinary exit ended native scope: %q", reason)
	}
}

func TestDescriptorBindsWorkerAndShimWithoutCredentials(t *testing.T) {
	dir := t.TempDir()
	d := Descriptor{WorkerID: "worker", Generation: strings.Repeat("a", 32), ProviderVersion: "0.22.5", ShimPID: 123, ShimStartTime: 456, Binding: Binding{Home: "/native", ControlHome: "/native-owner", OwnerPID: 42, OwnerStartTime: 99, InstanceID: "owner-instance", SessionID: "local-" + strings.Repeat("b", 64), AuthorityEpoch: 1, CodeSHA: strings.Repeat("c", 40)}}
	if err := WriteDescriptor(dir, d); err != nil {
		t.Fatal(err)
	}
	got, err := ReadDescriptor(dir)
	if err != nil || got != d {
		t.Fatalf("descriptor=%+v %v", got, err)
	}
	info, err := os.Stat(filepath.Join(dir, DescriptorFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("descriptor mode=%v %v", info, err)
	}
}
