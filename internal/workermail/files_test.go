package workermail

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestWorkerFilesRetainExactBytesAndScopeAfterAcknowledgement(t *testing.T) {
	dir := t.TempDir()
	checkout := t.TempDir()
	png := filepath.Join(checkout, "screenshot.png")
	csv := filepath.Join(checkout, "report.csv")
	pngBytes, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+cy7sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	csvBytes := []byte("column,value\nrelease,unique-42\n")
	for name, data := range map[string][]byte{png: pngBytes, csv: csvBytes} {
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	uploads, err := CaptureFiles(checkout, []string{png, csv})
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{Discussion: "discussion", Parent: "parent", Session: "worker", Instance: "instance", Attempt: "attempt"}
	n, err := Submit(dir, b, "result", "complete", "", true, uploads...)
	if err != nil || n.State != "queued" || len(n.Files) != 2 || n.Files[0].Name != "screenshot.png" || n.Files[1].ContentType != "text/csv" {
		t.Fatalf("immutable file receipt: %+v, %v", n, err)
	}
	if bytes.Contains([]byte(n.Text), []byte(checkout)) {
		t.Fatal("source path leaked in note")
	}
	retry, err := Submit(dir, b, n.ID, "complete", "", false, uploads...)
	if err != nil || retry.Sequence != n.Sequence || !slices.Equal(n.Files, retry.Files) {
		t.Fatalf("retry changed receipt: %+v, %v", retry, err)
	}
	if _, err := Submit(dir, b, n.ID, "complete", n.Text, false); !errors.Is(err, ErrConflict) {
		t.Fatal("same ID should not replace files with text-only receipt", err)
	}
	if err := os.WriteFile(csv, []byte("changed,after capture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := CaptureFiles(checkout, []string{png, csv})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Submit(dir, b, n.ID, "complete", "", false, changed...); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed immutable bytes accepted as retry: %v", err)
	}
	if err := Acknowledge(dir, n, ""); err != nil {
		t.Fatal(err)
	}
	got, err := Lookup(dir, b, n.ID)
	if err != nil || got.State != "ingested" || !slices.Equal(got.Files, n.Files) {
		t.Fatalf("ordinary note reserialization discarded sidecar refs: %+v, %v", got, err)
	}
	for i, expected := range [][]byte{pngBytes, csvBytes} {
		raw, err := ReadFile(dir, b, n.ID, n.Files[i].ID)
		if err != nil || !bytes.Equal(raw, expected) {
			t.Fatalf("original bytes not preserved for file %d: %v", i, err)
		}
	}
	for _, wrong := range []Binding{
		{Discussion: "other", Parent: b.Parent, Session: b.Session, Instance: b.Instance, Attempt: b.Attempt},
		{Discussion: b.Discussion, Parent: "other", Session: b.Session, Instance: b.Instance, Attempt: b.Attempt},
		{Discussion: b.Discussion, Parent: b.Parent, Session: b.Session, Instance: "other", Attempt: b.Attempt},
	} {
		if _, err := ReadFile(dir, wrong, n.ID, n.Files[0].ID); !errors.Is(err, ErrAbsent) {
			t.Fatalf("unscoped read %+v: %v", wrong, err)
		}
	}
	ids := []string{n.Files[0].ID, n.Files[1].ID}
	if err := PinFiles(dir, b, n.ID, "publication", ids); err != nil {
		t.Fatal(err)
	}
	if err := PinFiles(dir, b, n.ID, "publication", ids); err != nil {
		t.Fatal("pin replay failed", err)
	}
	if err := PinFiles(dir, b, n.ID, "publication", []string{ids[1], ids[0]}); !errors.Is(err, ErrConflict) {
		t.Fatal("ordered pin conflict not rejected", err)
	}
	if err := PinFiles(dir, b, n.ID, "orphan", ids); err != nil {
		t.Fatal(err)
	}
	if err := SettleFiles(dir, b, n.ID, []string{"publication"}); err != nil {
		t.Fatal(err)
	}
	prune := func() {
		t.Helper()
		if err := transact(dir, true, func(tx *bolt.Tx) error {
			return pruneSettled(tx, time.Now().Add(ReceiptRetention+time.Hour))
		}); err != nil {
			t.Fatal(err)
		}
	}
	prune()
	if raw, err := ReadFile(dir, b, n.ID, ids[1]); err != nil || !bytes.Equal(raw, csvBytes) {
		t.Fatal("unresolved delivery pin lost raw bytes", err)
	}
	if err := ReleaseFiles(dir, b, n.ID, "publication"); err != nil {
		t.Fatal(err)
	}
	prune()
	if _, err := ReadFile(dir, b, n.ID, ids[1]); !errors.Is(err, ErrAbsent) {
		t.Fatal("expired released file remained", err)
	}
}

func TestCaptureWorkerFilesRejectsUnsafeFilesystemInputs(t *testing.T) {
	checkout := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.csv")
	if err := os.WriteFile(outside, []byte("private,data\n"), 0600); err != nil {
		t.Fatal(err)
	}
	names := []string{"symlink.csv", "hardlink.csv", "pipe.csv", "fake.png"}
	internal := make([]string, 0, len(names)+1)
	for _, name := range names {
		internal = append(internal, filepath.Join(checkout, name))
	}
	if err := os.Symlink(outside, internal[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, internal[1]); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(internal[2], 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(internal[3], []byte("plain text"), 0600); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(checkout, "huge.csv")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(f.Truncate(9<<20), f.Close()); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(checkout, "outside-directory")
	if err := os.Symlink(filepath.Dir(outside), nested); err != nil {
		t.Fatal(err)
	}
	for _, path := range append(internal, outside, large, filepath.Join(nested, filepath.Base(outside))) {
		if _, err := CaptureFiles(checkout, []string{path}); err == nil {
			t.Errorf("unsafe file accepted: %s", path)
		}
	}
}

func TestCaptureWorkerFilesRejectsClashingDiscordNames(t *testing.T) {
	checkout := t.TempDir()
	nested := filepath.Join(checkout, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(checkout, "report.csv"), filepath.Join(nested, "Report.csv"), filepath.Join(checkout, " report.csv")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("column,value\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := CaptureFiles(checkout, paths[:2]); err == nil {
		t.Fatal("case-colliding output names accepted")
	}
	if _, err := CaptureFiles(checkout, paths[2:]); err == nil {
		t.Fatal("filename with surrounding whitespace accepted")
	}
}
