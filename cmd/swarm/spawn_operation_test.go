package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/workermail"
)

type spawnOperationFixture struct {
	*fakeSpawnClient
	idSeen string
}

func (f *spawnOperationFixture) LaunchOperation(id string, req protocol.LaunchReq) (string, string, error) {
	f.idSeen = id
	return f.Launch(req)
}

func (f *spawnOperationFixture) LaunchOperationStatus(id string) (schema.OperationOutcomeView, error) {
	f.idSeen = id
	return schema.OperationOutcomeView{State: schema.OutcomeApplied, SessionID: f.id}, nil
}

func TestSpawnOperationReusesHandoffSnapshotAndReadsReceipt(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("UNRELATED_SECRET", "must not enter the launch receipt")
	var out bytes.Buffer
	if code := runSpawn([]string{"--new-operation-id"}, nil, &out, io.Discard); code != 0 {
		t.Fatal(code)
	}
	id := strings.TrimSpace(out.String())
	if !protocol.LaunchOperationReplayable(id, time.Now()) {
		t.Fatal("CLI generated an unusable operation ID")
	}
	src := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(src, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &spawnOperationFixture{fakeSpawnClient: newFakeSpawnClient()}
	args := []string{"--operation-id", id, "--cli", "codex", "--handoff", src}
	f.err = io.ErrUnexpectedEOF
	if code := runSpawn(args, f, io.Discard, io.Discard); code != 1 || f.idSeen != id {
		t.Fatal("lost launch response", code, f.idSeen)
	}
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	f.err = nil
	out.Reset()
	if code := runSpawn(args, f, &out, io.Discard); code != 0 || strings.TrimSpace(out.String()) != f.id {
		t.Fatal("could not replay launch", code, out.String())
	}
	if len(f.launches) != 2 || !reflect.DeepEqual(f.launches[0], f.launches[1]) || strings.Contains(strings.Join(f.launches[0].Env, "\n"), "UNRELATED_SECRET") {
		t.Fatal("operation replay changed the request or retained ambient secrets")
	}
	pointer := strings.TrimSuffix(strings.TrimPrefix(f.launches[0].InitialPrompt, "Read and follow the instructions in "), ".")
	if body, err := os.ReadFile(pointer); err != nil || string(body) != "original" {
		t.Fatal("handoff snapshot lost on ambiguous outcome", err)
	}
	if err := os.WriteFile(src, []byte("different instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := runSpawn(args, f, io.Discard, io.Discard); code != 1 || len(f.launches) != 2 {
		t.Fatal("changed document reached the reliable daemon receipt")
	}
	out.Reset()
	var got schema.OperationOutcomeView
	if code := runSpawn([]string{"--status", id}, f, &out, io.Discard); code != 0 || json.Unmarshal(out.Bytes(), &got) != nil || got.SessionID != f.id || got.State != schema.OutcomeApplied || len(f.launches) != 2 {
		t.Fatal("read-only launch status", code, out.String())
	}
	if code := runSpawn([]string{"--status", id, "--prompt", "change"}, f, io.Discard, io.Discard); code != 2 {
		t.Fatal("mixed status and launch accepted")
	}
	if code := runSpawn([]string{"--operation-id=", "--cli", "codex", "--prompt", "change"}, f, io.Discard, io.Discard); code != 2 {
		t.Fatal("empty operation ID silently downgraded a reliable launch")
	}
}

func TestOperationHandoffConcurrentSnapshotAndUnsafeDestination(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	source := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(source, []byte("one complete snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	results := make(chan string, 8)
	for range cap(results) {
		go func() {
			file, err := operationHandoff("same-operation", source)
			if err != nil {
				t.Error(err)
			}
			results <- file
		}()
	}
	first := <-results
	for i := 1; i < cap(results); i++ {
		if file := <-results; file != first {
			t.Fatalf("racing retries selected different documents %q and %q", first, file)
		}
	}
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, first); err != nil {
		t.Fatal(err)
	}
	if _, err := operationHandoff("same-operation", source); err == nil {
		t.Fatal("accepted a replaced snapshot")
	}
}

func TestHandoffRecoversInterruptedCopyAndBindsDigestInDaemonRequest(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	source := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(source, []byte("complete original"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := operationHandoff("operation", source)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(first)
	// On process death halfway through a write, only the hidden staging
	// name survives. A retry must rebuild from the original before dispatch.
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".partial"), []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := operationHandoff("operation", source)
	body, readErr := os.ReadFile(second)
	if err != nil || readErr != nil || first != second || string(body) != "complete original" {
		t.Fatalf("interrupted snapshot: %q, %v, %v", body, err, readErr)
	}
	// An OS temporary-file cleaner can remove the entire snapshot. A changed
	// source must then produce a different owner request hash, even at this ID.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new payload"), 0600); err != nil {
		t.Fatal(err)
	}
	third, err := operationHandoff("operation", source)
	if err != nil || third == first {
		t.Fatal("changed payload reused the daemon request pointer", err)
	}
	if err := os.WriteFile(third, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := operationHandoff("operation", source); err == nil {
		t.Fatal("corrupted snapshot was accepted")
	}
}

func TestDispatcherShellUsesOrdinaryReliableSpawnForEveryProvider(t *testing.T) {
	t.Setenv(schema.EnvManagerVMCLI, "1")
	t.Setenv("SWARM_SESSION_ID", "manager")
	t.Setenv(workermail.ProofEnv, strings.Repeat("a", 64))
	for _, agent := range []string{"codex", "claude", "agy", "hermes", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			f := &spawnOperationFixture{fakeSpawnClient: newFakeSpawnClient()}
			f.err = io.ErrUnexpectedEOF
			var stdout, stderr bytes.Buffer
			args := []string{"--cli", agent, "--dir", t.TempDir(), "--prompt", "exact job"}
			if code := runSpawn(args, f, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "operation_id="+f.idSeen+"\n") || !protocol.LaunchOperationReplayable(f.idSeen, time.Now()) || stdout.Len() != 0 {
				t.Fatalf("lost response: %d, %q, %q", code, stdout.String(), stderr.String())
			}
			original := f.launches[0]
			if original.Agent != agent || original.InitialPrompt != "exact job" || original.SpawnedFrom != "manager" || original.SpawnIntent != schema.SpawnIntentDelegate {
				t.Fatal("ordinary spawn lost lineage or changed the instructions")
			}
			f.err = nil
			if code := runSpawn(append(args, "--operation-id", f.idSeen), f, &stdout, &stderr); code != 0 || strings.TrimSpace(stdout.String()) != f.id || len(f.launches) != 2 || !reflect.DeepEqual(original, f.launches[1]) {
				t.Fatal("explicit retry could not reuse generated operation")
			}
		})
	}
}

func TestRetainedDispatcherSpawnUsesHookProofOrStopsBeforeLaunch(t *testing.T) {
	t.Setenv(schema.EnvManagerVMCLI, "1")
	t.Setenv(hookclient.EnvSessionID, "retained")
	t.Setenv(workermail.ProofEnv, "")
	args := []string{"--cli", "codex", "--prompt", "inspect", "--dir", t.TempDir()}
	client := &spawnOperationFixture{fakeSpawnClient: newFakeSpawnClient()}
	t.Setenv(hookclient.EnvToken, "")
	var stderr bytes.Buffer
	if code := runSpawn(args, client, io.Discard, &stderr); code != 1 || len(client.launches) != 0 || !strings.Contains(stderr.String(), "resume the same discussion") || strings.Contains(stderr.String(), "operation_id=") {
		t.Fatalf("unqualified launch: %d %q", code, stderr.String())
	}
	t.Setenv(hookclient.EnvToken, "retained-token")
	if code := runSpawn(args, client, io.Discard, io.Discard); code != 0 || len(client.launches) != 1 || client.launches[0].DispatcherProof != workermail.Proof("retained-token", "spawn") {
		t.Fatalf("retained hook did not authenticate launch: %d", code)
	}
}
