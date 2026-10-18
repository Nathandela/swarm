package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestLiveMigrationRecoversLegacyPartialInteractions(t *testing.T) {
	// Exercise cuts inside a string, an escape, an object key, and nested data.
	payload := json.RawMessage(`{"text":"command with \"quotes\"","action":{"command":"echo hello"}}`)
	broken := EncodeRecord(Record{SchemaVersion: SchemaVersion, Cursor: 2, SessionID: "session", Type: TypeInteraction, Payload: payload})
	payloadAt := bytes.Index(broken, []byte(`"payload":`)) + len(`"payload":`)
	for cut := payloadAt + 1; cut < len(broken)-1; cut++ {
		t.Run(strconv.Itoa(cut), func(t *testing.T) {
			dir := t.TempDir()
			first := Record{SchemaVersion: SchemaVersion, Cursor: 1, SessionID: "session", Type: TypeLaunched}
			following := Record{SchemaVersion: SchemaVersion, Cursor: 9, SessionID: "session", Type: TypeDeleted}
			raw := append(EncodeRecord(first), '\n')
			raw = append(raw, broken[:cut]...)
			raw = append(raw, EncodeRecord(following)...)
			if cut%2 == 0 {
				raw = append(raw, '\n')
			}
			if err := os.WriteFile(filepath.Join(dir, segName(1)), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			j := openLiveTest(t, dir, liveOptions())
			res, err := j.ReadFrom(0)
			if err != nil || len(res.Events) != 2 || res.Events[0].Cursor != 1 || res.Events[1].Cursor != 9 || j.Cursor() != 9 {
				t.Fatalf("recovered migration = %+v, err=%v", res, err)
			}
			next := mustAppend(t, j, liveMessage("new", "completed", "new body"))
			if next.Cursor <= 9 {
				t.Fatal("migration reused a recovered cursor")
			}
			_ = j.Close()
			j = openLiveTest(t, dir, liveOptions())
			if j.Cursor() < next.Cursor {
				t.Fatal("restart lost cursor custody")
			}
		})
	}
}

func TestLiveMigrationRejectsUnrecognizedCorruption(t *testing.T) {
	interaction := `{"schema_version":1,"cursor":2,"session_id":"s","type":"interaction","payload":{"text":"torn`
	suffix := EncodeRecord(Record{SchemaVersion: SchemaVersion, Cursor: 3, SessionID: "s", Type: TypeDeleted})
	future := EncodeRecord(Record{SchemaVersion: SchemaVersion + 1, Cursor: 3, SessionID: "s", Type: TypeDeleted})
	for name, raw := range map[string][]byte{
		"garbage":        append([]byte("garbage"), suffix...),
		"torn control":   append([]byte(`{"schema_version":1,"cursor":2,"session_id":"s","type":"deleted","payload":{"text":"torn`), suffix...),
		"future suffix":  append([]byte(interaction), future...),
		"future prefix":  append([]byte(`{"schema_version":999,"cursor":2,"session_id":"s","type":"interaction","payload":{"text":"torn`), suffix...),
		"invalid prefix": append([]byte(`{"schema_version":1,"cursor":2,"session_id":"s","type":"interaction","payload":{broken`), suffix...),
		"reused cursor":  append([]byte(`{"schema_version":1,"cursor":3,"session_id":"s","type":"interaction","payload":{"text":"torn`), suffix...),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, segName(1))
			raw = append(raw, '\n')
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if j, err := Open(dir, liveOptions()); err == nil {
				_ = j.Close()
				t.Fatal("unsafe migration succeeded")
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatalf("failed migration changed evidence: %v", err)
			}
		})
	}
}

type failingSegment struct {
	*os.File
	short   bool
	syncErr bool
	writes  int
}

func (f *failingSegment) Write(p []byte) (int, error) {
	f.writes++
	if f.syncErr {
		return f.File.Write(p)
	}
	n, err := f.File.Write(p[:len(p)/2])
	if err != nil {
		return n, err
	}
	if f.short {
		return n, nil
	}
	return n, syscall.ENOSPC
}
func (f *failingSegment) Sync() error { return syscall.EIO }

func TestAppendStorageFailurePreventsFurtherWrites(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, failure := range []string{"partial", "short", "sync"} {
			t.Run(map[bool]string{false: "legacy", true: "live"}[live]+"/"+failure, func(t *testing.T) {
				dir := t.TempDir()
				opts := Options{}
				if live {
					opts = liveOptions()
				}
				j := openLiveTest(t, dir, opts)
				first := mustAppend(t, j, Record{SessionID: "s", Type: TypeLaunched})
				original := j.activeF
				f := &failingSegment{File: original.(*os.File), short: failure == "short", syncErr: failure == "sync"}
				j.activeF = f
				feed, cancel := j.Subscribe()
				defer cancel()
				_, err := j.Append(Record{SessionID: "s", Type: TypePresence})
				want := error(syscall.ENOSPC)
				if failure == "short" {
					want = io.ErrShortWrite
				}
				if failure == "sync" {
					want = syscall.EIO
				}
				if !errors.Is(err, want) {
					t.Fatalf("failure=%v, want %v", err, want)
				}
				// Even after disk recovery, this handle must not append onto the torn line.
				j.activeF = original
				for _, r := range []Record{{SessionID: "s", Type: TypePresence}, liveMessage("later", "completed", "body")} {
					if _, err := j.Append(r); !errors.Is(err, want) {
						t.Fatalf("storage failure was not latched: %v", err)
					}
				}
				if f.writes != 1 || len(j.records) != 1 {
					t.Fatal("failed append mutated journal records")
				}
				select {
				case <-feed:
					t.Fatal("failed append published a record")
				default:
				}
				_ = j.Close()
				j = openLiveTest(t, dir, opts)
				next := mustAppend(t, j, Record{SessionID: "s", Type: TypePresence})
				if next.Cursor <= first.Cursor {
					t.Fatal("restart reused committed cursor")
				}
			})
		}
	}
}

func TestReservationStorageFailureStopsPublication(t *testing.T) {
	for _, failure := range []string{"partial", "short", "sync"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			j := openLiveTest(t, dir, liveOptions())
			first := mustAppend(t, j, Record{SessionID: "s", Type: TypeLaunched})
			original := j.activeF
			j.activeF = &failingSegment{File: original.(*os.File), short: failure == "short", syncErr: failure == "sync"}
			feed, cancel := j.Subscribe()
			defer cancel()
			_, failureErr := j.Append(liveMessage("first", "completed", "private body"))
			if failureErr == nil {
				t.Fatal("failed reservation accepted a message")
			}
			j.activeF = original
			if _, err := j.Append(liveMessage("second", "completed", "private body")); err != failureErr {
				t.Fatalf("reservation failure not latched: %v", err)
			}
			if j.Cursor() != first.Cursor || len(j.records) != 1 {
				t.Fatal("failed reservation published cursor custody")
			}
			select {
			case <-feed:
				t.Fatal("failed reservation published an event")
			default:
			}
			_ = j.Close()
			j = openLiveTest(t, dir, liveOptions())
			if next := mustAppend(t, j, liveMessage("recovered", "completed", "body")); next.Cursor <= first.Cursor {
				t.Fatal("reopen reused a cursor")
			}
		})
	}
}

func TestMigrationRejectsFutureSuffixWithoutNewline(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`{"schema_version":1,"cursor":2,"session_id":"s","type":"interaction","payload":{"text":"torn`)
	raw = append(raw, EncodeRecord(Record{SchemaVersion: SchemaVersion + 1, Cursor: 3, SessionID: "s", Type: TypeDeleted})...)
	if err := os.WriteFile(filepath.Join(dir, segName(1)), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if j, err := Open(dir, liveOptions()); err == nil {
		_ = j.Close()
		t.Fatal("future suffix treated as a torn EOF tail")
	}
}

func TestMigrationTornTailMayContainRecordLikePayload(t *testing.T) {
	dir := t.TempDir()
	raw := append(EncodeRecord(Record{SchemaVersion: SchemaVersion, Cursor: 1, SessionID: "s", Type: TypeLaunched}), '\n')
	raw = append(raw, []byte(`{"schema_version":1,"cursor":2,"session_id":"s","type":"interaction","payload":{"nested":{"schema_version":1,"cursor":999,"text":"torn`)...)
	if err := os.WriteFile(filepath.Join(dir, segName(1)), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	j := openLiveTest(t, dir, liveOptions())
	if j.Cursor() != 1 {
		t.Fatal("torn payload manufactured cursor custody")
	}
}
