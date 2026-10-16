package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func liveOptions() Options {
	return Options{MaxBytes: 1024, MaxFiles: 4, MaxTotalBytes: 4096, LiveBytes: 4096}
}

func openLiveTest(t *testing.T, dir string, opts Options) *Journal {
	t.Helper()
	j, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

func liveMessage(id, state, text string) Record {
	raw, _ := json.Marshal(map[string]any{"v": 1, "item_id": id, "kind": "agent_message", "status": state, "text": text})
	return Record{SessionID: "session", Type: TypeInteraction, Payload: raw}
}

func TestLive_FailedControlNeverExposesAnUnpersistedCursor(t *testing.T) {
	dir := t.TempDir()
	j := openLiveTest(t, dir, liveOptions())
	mustAppend(t, j, liveMessage("message", "completed", "live"))
	ceiling := j.reserved
	if err := j.activeF.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(Record{SessionID: "session", Type: TypeDeleted}); err == nil {
		t.Fatal("closed control writer accepted a deletion")
	}
	res, err := j.ReadFrom(0)
	if err != nil || res.Cursor != ceiling || j.Cursor() != ceiling || j.lostThrough > ceiling {
		t.Fatalf("failed control exposed future custody: cursor=%d read=%+v lost=%d err=%v", j.Cursor(), res, j.lostThrough, err)
	}
	_ = j.Close()
	j = openLiveTest(t, dir, liveOptions())
	next := mustAppend(t, j, Record{SessionID: "session", Type: TypeDeleted})
	if next.Cursor <= res.Cursor {
		t.Fatalf("restart reused an exposed boundary: next=%d prior=%d", next.Cursor, res.Cursor)
	}
}

func TestLive_RejectedControlDoesNotPublishItsCandidateCursor(t *testing.T) {
	j := openLiveTest(t, t.TempDir(), liveOptions())
	mustAppend(t, j, liveMessage("message", "completed", "live"))
	ceiling := j.reserved
	if _, err := j.Append(Record{Type: TypePresence, Payload: json.RawMessage(`"` + strings.Repeat("x", int(j.opts.MaxTotalBytes)) + `"`)}); err == nil {
		t.Fatal("oversized control record was accepted")
	}
	if j.Cursor() != ceiling || j.lostThrough > ceiling {
		t.Fatalf("unissued candidate was published: cursor=%d ceiling=%d lost=%d", j.Cursor(), ceiling, j.lostThrough)
	}
	if next := mustAppend(t, j, Record{Type: TypePresence}); next.Cursor <= ceiling {
		t.Fatal("subsequent valid control did not advance the exposed boundary")
	}
}

func TestLive_ControlCursorExhaustionFailsClosed(t *testing.T) {
	j := openLiveTest(t, t.TempDir(), liveOptions())
	j.cursor, j.reserved = math.MaxUint64, math.MaxUint64
	if _, err := j.Append(Record{Type: TypeDeleted}); err == nil || j.Cursor() != math.MaxUint64 {
		t.Fatalf("control cursor wrapped: cursor=%d err=%v", j.Cursor(), err)
	}
}

func TestLive_DeletionErasesTheSessionsCachedDiscussion(t *testing.T) {
	j := openLiveTest(t, t.TempDir(), liveOptions())
	mustAppend(t, j, liveMessage("deleted-message", "completed", "deleted body"))
	other := liveMessage("other-message", "completed", "unrelated body")
	other.SessionID = "other"
	mustAppend(t, j, other)
	mustAppend(t, j, Record{SessionID: "session", Type: TypeDeleted})
	res, err := j.ReadFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	foundOther := false
	for _, record := range res.Events {
		if record.Type == TypeInteraction {
			if record.SessionID == "session" {
				t.Fatal("deleted discussion remained in the live RAM history")
			}
			foundOther = foundOther || record.SessionID == "other"
		}
	}
	if !foundOther {
		t.Fatal("deletion discarded unrelated live history")
	}
}

func TestLive_ConversationNeverReachesDiskAndRestartHasIndependentFloor(t *testing.T) {
	dir := t.TempDir()
	j := openLiveTest(t, dir, liveOptions())
	control := mustAppend(t, j, Record{SessionID: "session", Type: TypeLaunched})
	message := mustAppend(t, j, liveMessage("message", "completed", "private conversation body"))
	res, _ := j.ReadFrom(control.Cursor)
	if res.FullResync || len(res.Events) != 1 {
		t.Fatalf("live replay = %+v", res)
	}
	_ = j.Close()
	for _, path := range segFiles(t, dir) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("private conversation body")) {
			t.Fatal("conversation was persisted")
		}
	}
	j = openLiveTest(t, dir, liveOptions())
	res, _ = j.ReadFrom(control.Cursor)
	if !res.FullResync {
		t.Fatal("old control records masked the lost transient interval")
	}
	for _, record := range res.Events {
		if record.Type == TypeInteraction {
			t.Fatal("conversation survived restart")
		}
	}
	next := mustAppend(t, j, liveMessage("next", "completed", "new body"))
	if next.Cursor <= message.Cursor {
		t.Fatal("restart reused a published cursor")
	}
}

func TestLive_EmptyWindowStillReportsLostInterval(t *testing.T) {
	dir := t.TempDir()
	j := openLiveTest(t, dir, liveOptions())
	first := mustAppend(t, j, liveMessage("first", "completed", "one"))
	mustAppend(t, j, liveMessage("second", "completed", "two"))
	_ = j.Close()
	j = openLiveTest(t, dir, liveOptions())
	res, _ := j.ReadFrom(first.Cursor)
	if len(res.Events) != 0 || !res.FullResync {
		t.Fatalf("empty restart replay = %+v", res)
	}
}

func TestLive_MigrationStreamsOversizedSegmentAndKeepsControlHighWater(t *testing.T) {
	dir := t.TempDir()
	j := openLiveTest(t, dir, Options{})
	first := mustAppend(t, j, Record{SessionID: "session", Type: TypeLaunched})
	for i := 0; i < 128; i++ {
		mustAppend(t, j, liveMessage("legacy", "completed", strings.Repeat("legacy-body", 1500)))
	}
	last := mustAppend(t, j, Record{SessionID: "session", Type: TypeDeleted})
	_ = j.Close()
	j = openLiveTest(t, dir, liveOptions())
	res, _ := j.ReadFrom(0)
	if len(res.Events) != 2 || res.Events[0].Cursor != first.Cursor || res.Events[1].Cursor != last.Cursor {
		t.Fatalf("migrated controls = %+v", res.Events)
	}
	if j.Cursor() != last.Cursor {
		t.Fatalf("cursor = %d, want %d", j.Cursor(), last.Cursor)
	}
	var size int64
	for _, path := range segFiles(t, dir) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		size += int64(len(raw))
		if bytes.Contains(raw, []byte("legacy-body")) {
			t.Fatal("legacy conversation retained")
		}
	}
	if size > liveOptions().MaxTotalBytes {
		t.Fatalf("migrated bytes = %d", size)
	}
}

func TestLive_MigrationFailureDoesNotRemoveLegacyEvidence(t *testing.T) {
	for _, fail := range []string{"checkpoint", "huge_record", "future_record"} {
		t.Run(fail, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, segName(1))
			raw := append(EncodeRecord(Record{SchemaVersion: SchemaVersion, Cursor: 19, Type: TypeLaunched}), '\n')
			switch fail {
			case "checkpoint":
				original := writeLiveSegment
				writeLiveSegment = func(string, string, []byte) error { return errors.New("no durable checkpoint") }
				t.Cleanup(func() { writeLiveSegment = original })
			case "huge_record":
				raw = append(raw, bytes.Repeat([]byte("x"), maxMigrationRecordBytes+1)...)
			case "future_record":
				raw = append(raw, EncodeRecord(Record{SchemaVersion: SchemaVersion + 1, Cursor: 20, Type: TypeDeleted})...)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if j, err := Open(dir, liveOptions()); err == nil {
				_ = j.Close()
				t.Fatal("unsafe migration succeeded")
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatalf("failed migration changed legacy evidence: %v", err)
			}
		})
	}
}

func TestLive_MigrationToleratesTornTailAndInterruptedRewrite(t *testing.T) {
	dir := t.TempDir()
	first := Record{SchemaVersion: SchemaVersion, Cursor: 1, Type: TypeLaunched}
	last := Record{SchemaVersion: SchemaVersion, Cursor: 3, Type: TypeDeleted}
	legacy := append(EncodeRecord(first), '\n')
	legacy = append(legacy, EncodeRecord(Record{SchemaVersion: SchemaVersion, Cursor: 2, Type: TypeInteraction, Payload: json.RawMessage(`{"item_id":"old","text":"legacy"}`)})...)
	legacy = append(legacy, '\n')
	legacy = append(legacy, EncodeRecord(last)...)
	legacy = append(legacy, []byte("\n{\"cursor\":4,")...)
	if err := os.WriteFile(filepath.Join(dir, segName(1)), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	compact := append(EncodeRecord(first), '\n')
	compact = append(compact, EncodeRecord(last)...)
	compact = append(compact, '\n')
	if err := os.WriteFile(filepath.Join(dir, segName(2)), compact, 0o600); err != nil {
		t.Fatal(err)
	}
	checkpoint := append(EncodeRecord(Record{SchemaVersion: SchemaVersion, Cursor: 64, Type: typeCursorReservation}), '\n')
	if err := os.WriteFile(filepath.Join(dir, segName(3)), checkpoint, 0o600); err != nil {
		t.Fatal(err)
	}
	j := openLiveTest(t, dir, liveOptions())
	res, _ := j.ReadFrom(0)
	if len(res.Events) != 2 || j.Cursor() != 64 {
		t.Fatalf("interrupted migration = %+v", res)
	}
}

func TestLive_CursorReservationFailurePublishesNothing(t *testing.T) {
	j := openLiveTest(t, t.TempDir(), liveOptions())
	feed, cancel := j.Subscribe()
	defer cancel()
	original := writeLiveReservation
	writeLiveReservation = func(*Journal, uint64) error { return errors.New("disk full") }
	t.Cleanup(func() { writeLiveReservation = original })
	if _, err := j.Append(liveMessage("first", "completed", "body")); err == nil {
		t.Fatal("append accepted without durable cursor custody")
	}
	if j.Cursor() != 0 || len(j.records) != 0 {
		t.Fatal("failed reservation advanced state")
	}
	select {
	case <-feed:
		t.Fatal("failed reservation published a record")
	default:
	}
}

func TestLive_PressureEvictsWholeItemsAndSuppressesTheirLaterDeltas(t *testing.T) {
	opts := liveOptions()
	opts.LiveBytes = 700
	j := openLiveTest(t, t.TempDir(), opts)
	first := mustAppend(t, j, liveMessage("long", "in_progress", strings.Repeat("a", 180)))
	mustAppend(t, j, liveMessage("long", "in_progress", strings.Repeat("b", 180)))
	mustAppend(t, j, liveMessage("new", "completed", strings.Repeat("c", 180)))
	mustAppend(t, j, liveMessage("long", "completed", "tail"))
	res, _ := j.ReadFrom(first.Cursor)
	if !res.FullResync {
		t.Fatal("eviction did not report the lost interval")
	}
	for _, r := range j.records {
		_, item := liveIdentity(r)
		if item.ID == "long" {
			t.Fatal("partial item survived eviction")
		}
	}
	if j.liveBytes > opts.LiveBytes || len(j.suppressed) != 0 {
		t.Fatalf("live state bytes=%d suppressed=%d", j.liveBytes, len(j.suppressed))
	}
}

func TestLive_PinsOnlyLatestUnexpiredApprovalUntilResolution(t *testing.T) {
	now := time.Now().UTC()
	opts := liveOptions()
	opts.LiveBytes = 100
	opts.Clock = func() time.Time { return now }
	j := openLiveTest(t, t.TempDir(), opts)
	request := func() Record {
		raw, _ := json.Marshal(map[string]any{"item_id": "request", "kind": "approval_request", "status": "in_progress", "expires_at": now.Add(time.Minute)})
		return Record{SessionID: "session", Type: TypeInteraction, Payload: raw}
	}
	for i := 0; i < 8; i++ {
		mustAppend(t, j, request())
	}
	if len(j.records) != 1 || len(j.pins) != 1 {
		t.Fatalf("repeated approval retained %d records, %d pins", len(j.records), len(j.pins))
	}
	mustAppend(t, j, liveMessage("noise", "completed", strings.Repeat("x", 500)))
	if len(j.records) != 1 || len(j.pins) != 1 {
		t.Fatal("pressure removed pending approval")
	}
	mustAppend(t, j, Record{SessionID: "session", Type: TypeInteraction, Payload: json.RawMessage(`{"item_id":"resolution","kind":"approval_resolved","interaction_id":"request"}`)})
	if len(j.pins) != 0 || len(j.suppressed) != 0 || j.liveBytes > opts.LiveBytes {
		t.Fatal("resolved approval stayed pinned")
	}
	mustAppend(t, j, request())
	now = now.Add(2 * time.Minute)
	mustAppend(t, j, liveMessage("after-expiry", "completed", "body"))
	if len(j.pins) != 0 || j.liveBytes > opts.LiveBytes {
		t.Fatal("expired approval stayed pinned")
	}
}

func TestLive_ExitDoesNotReintroduceSuppressedTerminalTail(t *testing.T) {
	opts := liveOptions()
	opts.LiveBytes = 100
	j := openLiveTest(t, t.TempDir(), opts)
	mustAppend(t, j, liveMessage("evicted", "in_progress", strings.Repeat("x", 200)))
	mustAppend(t, j, Record{SessionID: "session", Type: TypeExited})
	mustAppend(t, j, liveMessage("evicted", "failed", "orphan terminal tail"))
	for _, record := range j.records {
		_, item := liveIdentity(record)
		if item.ID == "evicted" {
			t.Fatal("session exit admitted a tail without its head")
		}
	}
	mustAppend(t, j, Record{SessionID: "session", Type: TypeInteraction, Payload: json.RawMessage(`{"item_id":"terminal","kind":"session_status","process":"exited"}`)})
	if len(j.suppressed) != 0 || len(j.cacheDisabled) != 0 {
		t.Fatal("terminal sweep retained cache bookkeeping")
	}
}

func TestLive_ControlRotationDoesNotDiscardUnrelatedRAMRecords(t *testing.T) {
	opts := liveOptions()
	opts.MaxBytes, opts.MaxFiles, opts.MaxTotalBytes = 200, 2, 400
	j := openLiveTest(t, t.TempDir(), opts)
	message := mustAppend(t, j, liveMessage("keep", "completed", "RAM body"))
	for i := 0; i < 8; i++ {
		mustAppend(t, j, Record{SessionID: "session", Type: TypePresence})
	}
	found := false
	for _, record := range j.records {
		found = found || record.Cursor == message.Cursor
	}
	if !found {
		t.Fatal("control rotation removed unrelated RAM content")
	}
	var size int64
	for _, path := range segFiles(t, j.dir) {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		size += info.Size()
	}
	if size > opts.MaxTotalBytes {
		t.Fatalf("control disk exceeds budget: %d", size)
	}
}

func TestLive_CompactionKeepsNormalControlRetentionGranularity(t *testing.T) {
	dir := t.TempDir()
	j := openLiveTest(t, dir, Options{})
	var latest Record
	for i := 0; i < 40; i++ {
		latest = mustAppend(t, j, Record{SessionID: strings.Repeat("s", 200), Type: TypeGroupTransition})
	}
	_ = j.Close()
	opts := liveOptions()
	j = openLiveTest(t, dir, opts)
	mustAppend(t, j, Record{SessionID: strings.Repeat("s", 200), Type: TypePresence})
	res, _ := j.ReadFrom(0)
	found := false
	for _, record := range res.Events {
		found = found || record.Cursor == latest.Cursor
	}
	if !found {
		t.Fatal("first post-migration append discarded the newest prior control with a large compacted segment")
	}
	if len(segFiles(t, dir)) > opts.MaxFiles {
		t.Fatal("compaction exceeded control segment count")
	}
}

func TestLive_GenericRollbackAndReupgradeNeverReusePublishedCursor(t *testing.T) {
	dir := t.TempDir()
	opts := liveOptions()
	opts.MaxBytes, opts.MaxFiles, opts.MaxTotalBytes = 400, 2, 800
	j := openLiveTest(t, dir, opts)
	var published uint64
	for i := 0; i < 75; i++ {
		published = mustAppend(t, j, liveMessage("message", "completed", "body")).Cursor
		if i%10 == 0 {
			mustAppend(t, j, Record{SessionID: "session", Type: TypePresence})
		}
	}
	_ = j.Close()
	legacy := openLiveTest(t, dir, Options{})
	replay, _ := legacy.ReadFrom(0)
	var previous uint64
	for _, record := range replay.Events {
		if record.Cursor <= previous {
			t.Fatalf("rollback replay is not increasing: %d after %d", record.Cursor, previous)
		}
		previous = record.Cursor
	}
	rolledBack := mustAppend(t, legacy, Record{SessionID: "session", Type: TypeDeleted})
	if rolledBack.Cursor <= published {
		t.Fatalf("rollback reused cursor: %d <= %d", rolledBack.Cursor, published)
	}
	_ = legacy.Close()
	j = openLiveTest(t, dir, opts)
	next := mustAppend(t, j, liveMessage("next", "completed", "next body"))
	if next.Cursor <= rolledBack.Cursor {
		t.Fatal("re-upgrade reused rollback cursor")
	}
	res, _ := j.ReadFrom(0)
	for _, r := range res.Events {
		if r.Type == typeCursorReservation {
			t.Fatal("reservation leaked into the live stream")
		}
	}
}

func TestLive_RotationFailureLatchesBeforeFurtherTransientPublication(t *testing.T) {
	dir := t.TempDir()
	opts := liveOptions()
	opts.MaxBytes = 200
	j := openLiveTest(t, dir, opts)
	mustAppend(t, j, Record{SessionID: "session", Type: TypeLaunched})
	message := mustAppend(t, j, liveMessage("before", "completed", "body"))
	original := syncLiveDir
	storageErr := errors.New("directory sync failed")
	syncLiveDir = func(string) error { return storageErr }
	t.Cleanup(func() { syncLiveDir = original })
	if _, err := j.Append(Record{SessionID: "session", Type: TypePresence}); !errors.Is(err, storageErr) {
		t.Fatalf("rotation error = %v", err)
	}
	syncLiveDir = original
	if _, err := j.Append(liveMessage("after", "completed", "body")); !errors.Is(err, storageErr) {
		t.Fatalf("transient bypassed failed rotation: %v", err)
	}
	_ = j.Close()
	j = openLiveTest(t, dir, opts)
	next := mustAppend(t, j, liveMessage("reopened", "completed", "body"))
	if next.Cursor <= message.Cursor {
		t.Fatal("recovery reused a published cursor")
	}
}

func TestLive_SaturatedSuppressionDoesNotBlockApprovalOrTerminalTraffic(t *testing.T) {
	opts := liveOptions()
	opts.LiveBytes = 100
	j := openLiveTest(t, t.TempDir(), opts)
	j.suppressed = make(map[string]struct{}, maxSuppressedItems)
	for i := 0; i < maxSuppressedItems; i++ {
		j.suppressed["session\x00old-"+strconv.Itoa(i)] = struct{}{}
	}
	feed, cancel := j.Subscribe()
	defer cancel()
	mustAppend(t, j, liveMessage("new-open", "in_progress", strings.Repeat("x", 200)))
	if !j.cacheDisabled["session"] || len(j.suppressed) != 0 {
		t.Fatal("saturated markers did not shed the session cache")
	}
	raw, _ := json.Marshal(map[string]any{"item_id": "request", "kind": "approval_request", "status": "in_progress", "expires_at": time.Now().Add(time.Minute)})
	mustAppend(t, j, Record{SessionID: "session", Type: TypeInteraction, Payload: raw})
	if len(j.pins) != 1 {
		t.Fatal("cache saturation lost current approval")
	}
	mustAppend(t, j, Record{SessionID: "session", Type: TypeInteraction, Payload: json.RawMessage(`{"item_id":"resolved","kind":"approval_resolved","interaction_id":"request"}`)})
	mustAppend(t, j, liveMessage("old-0", "completed", "tail"))
	for i := 0; i < 4; i++ {
		select {
		case <-feed:
		default:
			t.Fatal("cache saturation blocked live fanout")
		}
	}
	if len(j.pins) != 0 {
		t.Fatal("cache saturation blocked approval resolution")
	}
}
