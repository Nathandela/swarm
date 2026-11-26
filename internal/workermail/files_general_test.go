package workermail

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// syntheticZip builds a minimal archive with the given entries.
func syntheticZip(t *testing.T, entries ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, name := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte("<synthetic/>")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestCaptureWorkerFilesAcceptsGeneralFilesWithHonestTypes(t *testing.T) {
	checkout := canonicalTestCheckout(t)
	files := []struct {
		name, contentType string
		data              []byte
	}{
		{"Quarterly deck.pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation", syntheticZip(t, "[Content_Types].xml", "ppt/presentation.xml")},
		{"memo.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", syntheticZip(t, "[Content_Types].xml", "word/document.xml")},
		{"model.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", syntheticZip(t, "[Content_Types].xml", "xl/workbook.xml")},
		{"bundle.zip", "application/zip", syntheticZip(t, "a.txt")},
		{"notes.md", "text/plain; charset=utf-8", []byte("# Synthetic notes\n")},
		{"data.json", "text/plain; charset=utf-8", []byte(`{"synthetic":true}`)},
		{"blob.bin", "application/octet-stream", []byte{0, 1, 2, 3, 0xff, 0xfe}},
		{"report.pdf", "application/pdf", []byte("%PDF-1.4\n%synthetic\n")},
	}
	var paths []string
	for _, f := range files {
		path := filepath.Join(checkout, f.name)
		if err := os.WriteFile(path, f.data, 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	got, err := CaptureFiles(checkout, paths)
	if err != nil || len(got) != len(files) {
		t.Fatalf("capture: %d files, %v", len(got), err)
	}
	for i, f := range files {
		sum := sha256.Sum256(f.data)
		pf := got[i].file
		if pf.Name != f.name || pf.ContentType != f.contentType || pf.SHA256 != hex.EncodeToString(sum[:]) || !bytes.Equal(got[i].data, f.data) {
			t.Errorf("%s: got %+v", f.name, pf)
		}
	}
}

func TestCaptureWorkerFilesRejectsDisguisedAndRunnableFiles(t *testing.T) {
	checkout := canonicalTestCheckout(t)
	for name, data := range map[string][]byte{
		"deck.pptx":  []byte("not a zip archive"),      // extension claims Office, bytes disagree
		"photo.png":  []byte("%PDF-1.4\n"),             // known type with the wrong content
		"bundle.zip": []byte("plain text"),             // claims an archive
		"setup.exe":  []byte("MZ synthetic"),           // runnable on the reader's machine
		"run.sh":     []byte("#!/bin/sh\necho hi\n"),   // runnable script
		"page.html":  []byte("<html>synthetic</html>"), // renders active content in browsers
	} {
		path := filepath.Join(checkout, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := CaptureFiles(checkout, []string{path}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
