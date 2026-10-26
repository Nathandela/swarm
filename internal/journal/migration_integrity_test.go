package journal

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrationNeverRecoversAControlFromInsideATornPayload(t *testing.T) {
	for _, terminated := range []bool{false, true} {
		t.Run(map[bool]string{false: "torn EOF", true: "corrupt complete line"}[terminated], func(t *testing.T) {
			dir := t.TempDir()
			raw := append(EncodeRecord(Record{SchemaVersion: SchemaVersion, Cursor: 1, SessionID: "s", Type: TypeLaunched}), '\n')
			raw = append(raw, []byte(`{"schema_version":1,"cursor":2,"session_id":"s","type":"interaction","payload":{"v":1,"item_id":"tool","ts":"2026-10-26T00:00:00Z","kind":"tool_run","nested":`)...)
			raw = append(raw, EncodeRecord(Record{SchemaVersion: SchemaVersion, Cursor: 999, SessionID: "s", Type: TypeDeleted})...)
			if terminated {
				raw = append(raw, '\n')
			}
			path := filepath.Join(dir, segName(1))
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			j, err := Open(dir, liveOptions())
			if terminated {
				if err == nil {
					_ = j.Close()
					t.Fatal("ambiguous complete line was accepted")
				}
				if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, raw) {
					t.Fatalf("rejected migration changed its source: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = j.Close() }()
			res, err := j.ReadFrom(0)
			if err != nil || j.Cursor() != 1 || len(res.Events) != 1 || res.Events[0].Type != TypeLaunched {
				t.Fatalf("torn payload manufactured cursor or control: %+v, %v", res, err)
			}
		})
	}
}
