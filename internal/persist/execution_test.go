package persist

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

func executionFixture(id string) ExecutionRecord {
	return ExecutionRecord{Version: ExecutionVersion, SessionID: id, Incarnation: "native-1", Incomplete: "unqualified_native_turn", MainTurn: status.TurnIdle, Interaction: status.InteractionNone, Actors: map[string]ExecutionActor{"child": {Sequence: 2, StartSequence: 2, Active: true}}}
}

func TestExecutionAtomicDiskFailure(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := executionFixture("s1")
	if err = s.SaveExecution(r); err != nil {
		t.Fatal(err)
	}
	original := writeTemp
	defer func() { writeTemp = original }()
	writeTemp = func(f *os.File, data []byte) (int, error) { return 0, syscall.ENOSPC }
	r.Actors["child"] = ExecutionActor{Sequence: 3, StartSequence: 2}
	if err = s.SaveExecution(r); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("want ENOSPC: %v", err)
	}
	got, err := s.LoadExecution("s1")
	if err != nil || !got.Actors["child"].Active {
		t.Fatalf("atomic custody lost: %+v %v", got, err)
	}
	files, err := filepath.Glob(filepath.Join(s.root, "s1", "execution.json.tmp*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("scratch leaked: %v %v", files, err)
	}
}

func TestExecutionRejectsCorruptionBoundsAndWrongOwnership(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := executionFixture("s1")
	r.Incarnation = ""
	if err = s.SaveExecution(r); err == nil {
		t.Fatal("accepted missing incarnation")
	}
	r = executionFixture("s1")
	for i := 0; i < MaxExecutionActors; i++ {
		r.Actors[fmt.Sprintf("actor-%d", i)] = ExecutionActor{Sequence: 1, StartSequence: 1, Active: true}
	}
	if err = s.SaveExecution(r); err == nil {
		t.Fatal("accepted actor overflow")
	}
	r = executionFixture("s1")
	if err = s.SaveExecution(r); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(s.root, "s1", "execution.json")
	if err = os.WriteFile(p, []byte(`{"version":1`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LoadExecution("s1"); err == nil {
		t.Fatal("accepted truncated crash record")
	}
}

func TestExecutionAggregateAdmission(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxExecutionRecords; i++ {
		dir := filepath.Join(s.root, fmt.Sprintf("s%d", i))
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "execution.json"), []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.SaveExecution(executionFixture("additional")); err == nil {
		t.Fatal("aggregate admission exceeded record bound")
	}
	if err = s.SaveExecution(executionFixture("s0")); err != nil {
		t.Fatalf("existing custody cannot be replaced at count limit: %v", err)
	}
}

func TestExecutionPrivateAuthorityFiles(t *testing.T) {
	for _, kind := range []string{"permissions", "hardlink", "symlink", "duplicate", "unknown", "alias", "trailing"} {
		t.Run(kind, func(t *testing.T) {
			s, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err = s.SaveExecution(executionFixture("s1")); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.root, "s1", "execution.json")
			switch kind {
			case "permissions":
				err = os.Chmod(path, 0644)
			case "hardlink":
				err = os.Link(path, filepath.Join(s.root, "alias"))
			case "symlink":
				backup := filepath.Join(s.root, "private")
				if err = os.Rename(path, backup); err == nil {
					err = os.Symlink(backup, path)
				}
			default:
				raw, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				switch kind {
				case "duplicate":
					raw = append([]byte(`{"version":1,`), raw[1:]...)
				case "alias":
					raw = append([]byte(`{"Version":1,`), raw[1:]...)
				case "unknown":
					raw = append([]byte(`{"extra":true,`), raw[1:]...)
				case "trailing":
					raw = append(raw, []byte(`{}`)...)
				}
				err = os.WriteFile(path, raw, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.LoadExecution("s1"); err == nil {
				t.Fatal("accepted unsafe authority file")
			}
		})
	}
}

func TestExecutionActualStoreWritesDoNotSerializeSessions(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PrepareExecutionStore(); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	original := writeTemp
	defer func() { writeTemp = original }()
	writeTemp = func(f *os.File, data []byte) (int, error) {
		if strings.Contains(f.Name(), string(filepath.Separator)+"s1"+string(filepath.Separator)) {
			close(entered)
			<-release
		}
		return f.Write(data)
	}
	done := make(chan error, 1)
	go func() { done <- s.SaveExecution(executionFixture("s1")) }()
	<-entered
	other := make(chan error, 1)
	go func() { other <- s.SaveExecution(executionFixture("s2")) }()
	select {
	case err := <-other:
		if err != nil {
			close(release)
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("store held admission mutex during another session's disk write")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestExecutionCrashScratchIsBounded(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.root, "s1")
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(dir, "execution.pending")
	if err = os.WriteFile(scratch, []byte("crash residue"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err = s.SaveExecution(executionFixture("s1")); err != nil {
			t.Fatal(err)
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 || files[0].Name() != "execution.json" {
		t.Fatalf("unbounded crash scratch: %v %v", files, err)
	}
}

func TestExecutionDirectorySyncFailureIsNotAcknowledged(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveExecution(executionFixture("s1")); err != nil {
		t.Fatal(err)
	}
	original := executionSyncDir
	defer func() { executionSyncDir = original }()
	executionSyncDir = func(*os.Root) error { return syscall.ENOSPC }
	r := executionFixture("s1")
	r.Actors["child"] = ExecutionActor{Sequence: 3, StartSequence: 2}
	if err = s.SaveExecution(r); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("directory durability failure acknowledged: %v", err)
	}
	got, err := s.LoadExecution("s1")
	if err != nil {
		t.Fatalf("post-rename record torn: %v", err)
	}
	if got.Actors["child"].Sequence != 3 {
		t.Fatal("expected visible but explicitly unacknowledged record")
	}
}

func TestExecutionRetirementRequiresExactResolvedCustody(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := executionFixture("s1")
	if err = s.SaveExecution(r); err != nil {
		t.Fatal(err)
	}
	if err = s.RetireExecution("s1", "other"); err == nil {
		t.Fatal("foreign incarnation retired evidence")
	}
	if err = s.RetireExecution("s1", r.Incarnation); err == nil {
		t.Fatal("active actor custody retired")
	}
	r.Actors["child"] = ExecutionActor{Sequence: 3, StartSequence: 2}
	r.PendingCallbacks = map[string]string{"3": strings.Repeat("a", 64)}
	if err = s.SaveExecution(r); err != nil {
		t.Fatal(err)
	}
	if err = s.RetireExecution("s1", r.Incarnation); err == nil {
		t.Fatal("pending outer-fold receipt retired")
	}
	r.PendingCallbacks = nil
	if err = s.SaveExecution(r); err != nil {
		t.Fatal(err)
	}
	if err = s.RetireExecution("s1", r.Incarnation); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LoadExecution("s1"); !os.IsNotExist(err) {
		t.Fatalf("retired record remains: %v", err)
	}
	capacity, err := s.executionCapacity()
	if err != nil {
		t.Fatal(err)
	}
	capacity.mu.Lock()
	count, total := len(capacity.entries), capacity.bytes
	capacity.mu.Unlock()
	if count != 0 || total != 0 {
		t.Fatalf("retirement leaked admission: %d %d", count, total)
	}
}

func TestExecutionAggregateBytesReserveReplacementHeadroom(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxExecutionRecords-1; i++ {
		dir := filepath.Join(s.root, fmt.Sprintf("s%d", i))
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		f, e := os.OpenFile(filepath.Join(dir, "execution.json"), os.O_CREATE|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		e = f.Truncate(MaxExecutionBytes)
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
	}
	if err = s.SaveExecution(executionFixture("additional")); err == nil {
		t.Fatal("new record consumed atomic replacement reserve")
	}
	if err = s.SaveExecution(executionFixture("s0")); err != nil {
		t.Fatalf("reserved headroom unavailable to existing custody: %v", err)
	}
}

func TestExecutionRetirementRetriesUnlinkDurability(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := executionFixture("s1")
	r.Actors = nil
	if err = s.SaveExecution(r); err != nil {
		t.Fatal(err)
	}
	original := executionSyncDir
	defer func() { executionSyncDir = original }()
	executionSyncDir = func(*os.Root) error { return syscall.ENOSPC }
	if err = s.RetireExecution("s1", r.Incarnation); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("unlink durability uncertainty hidden: %v", err)
	}
	if _, err = s.LoadExecution("s1"); !os.IsNotExist(err) {
		t.Fatal("test did not reach post-unlink failure")
	}
	if err = s.RetireExecution("s1", "foreign"); err == nil {
		t.Fatal("foreign incarnation completed uncertain retirement")
	}
	if err = s.SaveExecution(r); err == nil {
		t.Fatal("new save bypassed uncertain retirement")
	}
	executionSyncDir = original
	if err = s.RetireExecution("s1", r.Incarnation); err != nil {
		t.Fatalf("exact retirement cannot retry directory durability: %v", err)
	}
	capacity, err := s.executionCapacity()
	if err != nil {
		t.Fatal(err)
	}
	capacity.mu.Lock()
	count, total := len(capacity.entries), capacity.bytes
	capacity.mu.Unlock()
	if count != 0 || total != 0 {
		t.Fatalf("retirement leaked admission: %d %d", count, total)
	}
}

func TestExecutionDeleteReleasesCapacityAcrossStoreHandles(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxExecutionRecords; i++ {
		dir := filepath.Join(s.root, fmt.Sprintf("s%d", i))
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "execution.json"), []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.PrepareExecutionStore(); err != nil {
		t.Fatal(err)
	}
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	alias, err := filepath.Rel(working, s.root)
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewStore(alias)
	if err != nil {
		t.Fatal(err)
	}
	if err = other.Delete("s0"); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveExecution(executionFixture("replacement")); err != nil {
		t.Fatalf("deleted custody still consumes admission: %v", err)
	}
	capacity, err := s.executionCapacity()
	if err != nil {
		t.Fatal(err)
	}
	capacity.mu.Lock()
	_, stale := capacity.entries["s0"]
	capacity.mu.Unlock()
	if stale {
		t.Fatal("deleted execution slot retained")
	}
}

func TestExecutionSaveDeleteAdmissionRace(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxExecutionRecords-1; i++ {
		dir := filepath.Join(s.root, fmt.Sprintf("other%d", i))
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "execution.json"), []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.SaveExecution(executionFixture("s1")); err != nil {
		t.Fatal(err)
	}
	other, err := NewStore(s.root)
	if err != nil {
		t.Fatal(err)
	}
	original := writeTemp
	defer func() { writeTemp = original }()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	writeTemp = func(f *os.File, data []byte) (int, error) {
		if strings.Contains(f.Name(), string(filepath.Separator)+"s1"+string(filepath.Separator)) {
			once.Do(func() { close(entered); <-release })
		}
		return f.Write(data)
	}
	writing := make(chan error, 1)
	go func() { writing <- s.SaveExecution(executionFixture("s1")) }()
	<-entered
	capacity, err := s.executionCapacity()
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	waitRefs := func(want int) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			capacity.mu.Lock()
			refs := capacity.entries["s1"].refs
			capacity.mu.Unlock()
			if refs >= want {
				return
			}
			if time.Now().After(deadline) {
				close(release)
				t.Fatalf("concurrent operation did not reach per-session custody: %d want %d", refs, want)
			}
			time.Sleep(time.Millisecond)
		}
	}
	deleting := make(chan error, 1)
	go func() { deleting <- other.Delete("s1") }()
	waitRefs(2)
	queued := make(chan error, 1)
	go func() { queued <- other.SaveExecution(executionFixture("s1")) }()
	waitRefs(3)
	if err = s.SaveExecution(executionFixture("replacement")); err == nil {
		close(release)
		t.Fatal("admission used custody before deletion finished")
	}
	close(release)
	if err = <-writing; err != nil {
		t.Fatal(err)
	}
	if err = <-deleting; err != nil {
		t.Fatal(err)
	}
	_ = <-queued
	if _, err = s.LoadExecution("s1"); !os.IsNotExist(err) {
		t.Fatalf("queued stale save resurrected deleted session: %v", err)
	}
	if err = s.SaveExecution(executionFixture("replacement")); err != nil {
		t.Fatalf("completed deletion did not release admission: %v", err)
	}
}

func codexExecutionFixture() ExecutionRecord {
	r := executionFixture("s1")
	r.ConversationID = "thread-1"
	r.TurnID = "turn-1"
	r.OwnerRevision = 1
	r.Codex = &CodexExecution{NativeIncarnation: "backend-1", FeedEpoch: "dial-1", ThreadID: r.ConversationID, TurnID: r.TurnID, OwnerRevision: 1, Sequence: 1, LastEventHash: strings.Repeat("a", 64), State: "active", Items: map[string]CodexExecutionItem{"tool-1": {NativeIncarnation: "backend-1", FeedEpoch: "dial-1", TurnID: "turn-1", Kind: "commandExecution", StartSequence: 1}}, Children: map[string]CodexExecutionChild{"child-1": {NativeIncarnation: "backend-1", FeedEpoch: "dial-1", TurnID: "turn-1", ChildTurnID: "child-turn-1", Active: true}}, Decisions: map[string]CodexExecutionDecision{"request-1": {NativeIncarnation: "backend-1", FeedEpoch: "dial-1", TurnID: "turn-1", Kind: "question"}}}
	return r
}

func TestExecutionCodexSchemaRoundTripAndBounds(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := codexExecutionFixture()
	if err = s.SaveExecution(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadExecution(r.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Codex.NativeIncarnation != r.Codex.NativeIncarnation || !got.Codex.Children["child-1"].Active || got.Codex.Decisions["request-1"].Kind != "question" || got.Codex.Items["tool-1"].Complete {
		t.Fatal("Codex custody did not round-trip")
	}
	r.Codex.ThreadID = "foreign"
	if err = s.SaveExecution(r); err == nil {
		t.Fatal("contradictory conversation identity persisted")
	}
	r = codexExecutionFixture()
	for i := 0; i < MaxExecutionActors; i++ {
		r.Codex.Items[fmt.Sprintf("extra%d", i)] = CodexExecutionItem{NativeIncarnation: "backend-1", FeedEpoch: "dial-1", TurnID: "turn-1", Kind: "reasoning", StartSequence: 1}
	}
	if err = s.SaveExecution(r); err == nil {
		t.Fatal("combined actor/tool/request cap exceeded")
	}
}

func TestExecutionCodexSchemaRejectsAmbiguousNestedAuthority(t *testing.T) {
	for _, scenario := range []string{"alias", "duplicate", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			s, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err = s.SaveExecution(codexExecutionFixture()); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.root, "s1", "execution.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			replacement := `"state":"active","State":"terminal"`
			if scenario == "duplicate" {
				replacement = `"state":"active","state":"terminal"`
			}
			if scenario == "unknown" {
				replacement = `"state":"active","future_authority":true`
			}
			raw = []byte(strings.Replace(string(raw), `"state":"active"`, replacement, 1))
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = s.LoadExecution("s1"); err == nil {
				t.Fatal("ambiguous Codex authority accepted")
			}
		})
	}
}

func TestExecutionLegacyVersionRetainsHookCustody(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := executionFixture("s1")
	r.Version = 1
	if err = s.SaveExecution(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadExecution("s1")
	if err != nil || !got.Actors["child"].Active {
		t.Fatalf("legacy hook custody lost during schema extension: %+v %v", got, err)
	}
	r = codexExecutionFixture()
	r.Version = 1
	if err = s.SaveExecution(r); err == nil {
		t.Fatal("Codex authority smuggled into old schema")
	}
}
