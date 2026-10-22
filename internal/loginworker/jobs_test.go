package loginworker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/procstart"
)

func metadataJob(t *testing.T) (Config, *jobFiles) {
	t.Helper()
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := Config{SchemaVersion: 1, StateRoot: state, JobID: "0123456789abcdef0123456789abcdef", Generation: 1, Provider: "codex", Label: "Saved", Deadline: time.Now().Add(-time.Hour)}
	if err := prepareHome(c); err != nil {
		t.Fatal(err)
	}
	f, err := openJob(state, c.JobID, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	if err := f.write(ConfigFile, c); err != nil {
		t.Fatal(err)
	}
	return c, f
}

func TestClaudeEnrollmentUsesTheCanonicalKeychainUser(t *testing.T) {
	for _, inherited := range [][]string{{"HOME=/ordinary"}, {"USER=override", "USER=unsafe@name", "HOME=/ordinary"}} {
		env := NativeEnvironment(Config{Provider: "claude", StateRoot: "/private", JobID: "fixture"}, inherited)
		count := 0
		for _, entry := range env {
			if strings.HasPrefix(entry, "USER=") {
				count++
				if entry != "USER="+logins.NativeUsername() {
					t.Fatal("native enrollment and Keychain reader disagree")
				}
			}
		}
		if count != 1 {
			t.Fatal("native enrollment has ambiguous Keychain user")
		}
	}
}

func TestCancelBeforeProgressPublicationLeavesTerminalProof(t *testing.T) {
	c, _ := metadataJob(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := CancelJob(ctx, c); err != nil {
		t.Fatal(err)
	}
	p, err := ReadProgress(c.StateRoot, c.JobID)
	if err != nil || p.Phase != PhaseCanceled || !p.WritersStopped || !p.NativeWritersStopped {
		t.Fatalf("missing cancellation proof: %+v %v", p, err)
	}
}

func TestCompletedMetadataSurvivesDeadlineAndNativeUpgrade(t *testing.T) {
	c, _ := metadataJob(t)
	read, err := ReadConfig(c.StateRoot, c.JobID)
	if err != nil || read.JobID != c.JobID {
		t.Fatalf("read terminal metadata: %v", err)
	}
	if err := validateConfig(read); err == nil {
		t.Fatal("expired incomplete config accepted for execution")
	}
	jobs, err := Jobs(c.StateRoot)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("inventory: %v, count %d", err, len(jobs))
	}
}

func TestReadyFlagCannotConsumeWhileRunnerAlive(t *testing.T) {
	c, f := metadataJob(t)
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	p := Progress{SchemaVersion: 1, JobID: c.JobID, Generation: 1, Phase: PhaseReady, WritersStopped: true, NativeWritersStopped: true, Runner: ProcessIdentity{PID: os.Getpid(), StartTime: start}}
	if err := f.write(ProgressFile, p); err != nil {
		t.Fatal(err)
	}
	if err := MarkCompleted(c); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("live runner accepted: %v", err)
	}
	if done, err := Completed(c); err != nil || done {
		t.Fatalf("completion receipt published: %v %v", done, err)
	}
}

func TestJobMetadataRejectsRedirectedConfigAndSymlink(t *testing.T) {
	c, f := metadataJob(t)
	other := c
	other.StateRoot = filepath.Dir(c.StateRoot)
	if err := f.write(ConfigFile, other); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(c.StateRoot, c.JobID); err == nil {
		t.Fatal("redirected state root accepted")
	}
	if err := f.root.Remove(ConfigFile); err != nil {
		t.Fatal(err)
	}
	if err := f.root.Symlink("/etc/passwd", ConfigFile); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(c.StateRoot, c.JobID); err == nil {
		t.Fatal("symlink config accepted")
	}
}

func TestConfigPublicationCrashDoesNotHideOtherJobs(t *testing.T) {
	c, _ := metadataJob(t)
	orphan := c
	orphan.JobID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := prepareHome(orphan); err != nil {
		t.Fatal(err)
	}
	jobs, err := Jobs(c.StateRoot)
	if err != nil || len(jobs) != 1 || jobs[0].JobID != c.JobID {
		t.Fatalf("partial job poisoned inventory: %v", err)
	}
}

func TestPruneNeedsTerminalCleanupAndNeverFollowsLinks(t *testing.T) {
	c, f := metadataJob(t)
	c.Deadline = time.Now().Add(-25 * time.Hour)
	p := Progress{SchemaVersion: 1, JobID: c.JobID, Generation: 1, Phase: PhaseReady, WritersStopped: true, NativeWritersStopped: true}
	if err := f.write(ProgressFile, p); err != nil {
		t.Fatal(err)
	}
	if err := Prune(c); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(c.StateRoot, c.JobID); err != nil {
		t.Fatal("unconsumed job removed")
	}
	if err := MarkCompleted(c); err != nil {
		t.Fatal(err)
	}
	if err := MarkCleaned(c); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("owner data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.root.Symlink(outside, "outside"); err != nil {
		t.Fatal(err)
	}
	if err := Prune(c); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(c.StateRoot, c.JobID); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("old terminal job retained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal("prune followed link outside job")
	}
}
