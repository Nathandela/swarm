package shim

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestHookCleanupPreviewAndApplyShareExactPrefix(t *testing.T) {
	s, path, cert := producerFixture(t)
	for _, body := range []string{"first", "second"} {
		if _, err := s.Append([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	id := s.IncarnationID()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("torn")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	before, _ := os.ReadFile(path)
	proofBefore, _ := os.ReadFile(path + ".producer")
	entriesBefore, _ := os.ReadDir(filepath.Dir(path))
	preview, err := CleanStoppedHookSpool(path, id, cert, 1, 0, 0, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantCut := int64(hookRecordHeaderLen + len("first"))
	if preview.EligibleBytes != wantCut || preview.Changed || preview.Before == nil || preview.After == nil {
		t.Fatalf("preview=%+v", preview)
	}
	unchanged, _ := os.ReadFile(path)
	proofAfter, _ := os.ReadFile(path + ".producer")
	entriesAfter, _ := os.ReadDir(filepath.Dir(path))
	if !bytes.Equal(before, unchanged) || !bytes.Equal(proofBefore, proofAfter) || len(entriesBefore) != len(entriesAfter) {
		t.Fatal("preview mutated spool, proof, or directory")
	}
	applied, err := CleanStoppedHookSpool(path, id, cert, 1, 0, 0, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !applied.Changed || applied.EligibleBytes != wantCut || !bytes.Equal(after, before[wantCut:]) {
		t.Fatalf("apply=%+v suffix=%q", applied, after)
	}
}

func TestHookCleanupRetainsLiveOversizedAndUncertifiedBoundary(t *testing.T) {
	s, path, cert := producerFixture(t)
	if _, err := s.Append([]byte("first")); err != nil {
		t.Fatal(err)
	}
	id := s.IncarnationID()
	if _, err := CleanStoppedHookSpool(path, id, cert, 1, 0, 0, false, nil); !errors.Is(err, ErrHookProducerBusy) {
		t.Fatalf("live preview=%v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanStoppedHookSpool(path, id, cert, 2, 0, 0, false, nil); !errors.Is(err, ErrHookRetirementUnproven) {
		t.Fatalf("future boundary=%v", err)
	}
	if err := os.Truncate(path, (64<<20)+1); err != nil {
		t.Fatal(err)
	}
	for _, apply := range []bool{false, true} {
		if _, err := CleanStoppedHookSpool(path, id, cert, 1, 0, 0, apply, nil); !errors.Is(err, ErrHookCleanupTooLarge) {
			t.Fatalf("oversized apply=%t: %v", apply, err)
		}
	}
}

func TestHookCleanupRechecksSourceAndParentFenceBeforeRename(t *testing.T) {
	for _, change := range []string{"source", "fence"} {
		t.Run(change, func(t *testing.T) {
			s, path, cert := producerFixture(t)
			_, _ = s.Append([]byte("first"))
			id := s.IncarnationID()
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			fenceErr := errors.New("parent changed")
			changed := false
			testHookBeforeCompactRename = func() {
				changed = true
				if change == "source" {
					replacement := path + ".replacement"
					if err := os.WriteFile(replacement, []byte("keep replacement"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(replacement, path); err != nil {
						t.Fatal(err)
					}
				}
			}
			defer func() { testHookBeforeCompactRename = nil }()
			fence := func() error {
				if changed && change == "fence" {
					return fenceErr
				}
				return nil
			}
			result, err := CleanStoppedHookSpool(path, id, cert, 1, 0, 0, true, fence)
			if err == nil || result.Changed {
				t.Fatalf("replaced source/fence accepted: %+v %v", result, err)
			}
			body, _ := os.ReadFile(path)
			if change == "source" && string(body) != "keep replacement" {
				t.Fatalf("replacement changed: %q", body)
			}
			if change == "fence" && !errors.Is(err, fenceErr) {
				t.Fatalf("fence error=%v", err)
			}
		})
	}
}

func TestHookCleanupReportsSafePostReplacementFailure(t *testing.T) {
	s, path, cert := producerFixture(t)
	for _, body := range []string{"first", "second"} {
		if _, err := s.Append([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	id := s.IncarnationID()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("torn")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	before, _ := os.ReadFile(path)
	original := persistHookProducerProof
	persistHookProducerProof = func(string, []byte) error { return syscall.ENOSPC }
	result, err := CleanStoppedHookSpool(path, id, cert, 1, 0, 0, true, nil)
	persistHookProducerProof = original
	after, _ := os.ReadFile(path)
	if !errors.Is(err, syscall.ENOSPC) || !result.Changed || result.After == nil || !bytes.Equal(after, before[hookRecordHeaderLen+len("first"):]) {
		t.Fatalf("result=%+v suffix=%q error=%v", result, after, err)
	}
	if _, err := CleanStoppedHookSpool(path, id, cert, 1, 0, 0, true, nil); !errors.Is(err, ErrHookRetirementUnproven) {
		t.Fatalf("failed proof acquired disposal authority: %v", err)
	}
}

func TestHookCleanupParentReplacementPreservesOutsideFiles(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "session")
	if err := os.Mkdir(session, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(session, HookSpoolFile)
	s, err := OpenHookSpoolProducer(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Append([]byte("first"))
	id, cert := s.IncarnationID(), s.ProducerCertificate()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{HookSpoolFile, HookSpoolFile + ".floor", HookSpoolFile + ".producer"} {
		if err := os.WriteFile(filepath.Join(outside, name), []byte("keep outside"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	testHookBeforeCompactRename = func() {
		if err := os.Rename(session, session+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, session); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { testHookBeforeCompactRename = nil }()
	if result, err := CleanStoppedHookSpool(path, id, cert, 1, 0, 0, true, nil); err == nil || result.Changed {
		t.Fatalf("accepted replaced parent: %+v %v", result, err)
	}
	for _, name := range []string{HookSpoolFile, HookSpoolFile + ".floor", HookSpoolFile + ".producer"} {
		body, _ := os.ReadFile(filepath.Join(outside, name))
		if string(body) != "keep outside" {
			t.Fatalf("outside %s changed: %q", name, body)
		}
	}
}

func TestHookCleanupRefusesReplacedTemporaryInode(t *testing.T) {
	s, path, cert := producerFixture(t)
	_, _ = s.Append([]byte("first"))
	id := s.IncarnationID()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	testHookBeforeCompactRename = func() {
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), HookSpoolFile+".tmp") {
				target := filepath.Join(filepath.Dir(path), entry.Name())
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte("unrelated replacement"), 0600); err != nil {
					t.Fatal(err)
				}
				return
			}
		}
		t.Fatal("missing compaction temporary")
	}
	defer func() { testHookBeforeCompactRename = nil }()
	result, err := CleanStoppedHookSpool(path, id, cert, 1, 0, 0, true, nil)
	after, _ := os.ReadFile(path)
	if !errors.Is(err, ErrHookRetirementUnproven) || result.Changed || !bytes.Equal(before, after) {
		t.Fatalf("temporary replacement accepted: %+v %v", result, err)
	}
}

func TestHookCleanupLateParentChangeCannotPublishProofOrMeasurement(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "session")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, HookSpoolFile)
	s, err := OpenHookSpoolProducer(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Append([]byte("first"))
	id, cert := s.IncarnationID(), s.ProducerCertificate()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	proofPath := filepath.Join(outside, HookSpoolFile+".producer")
	if err := os.WriteFile(proofPath, []byte("outside proof"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fence := func() error {
		calls++
		if calls == 5 {
			if err := os.Rename(dir, dir+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, dir); err != nil {
				t.Fatal(err)
			}
			return ErrHookRetirementUnproven
		}
		return nil
	}
	result, err := CleanStoppedHookSpool(path, id, cert, 1, 0, 0, true, fence)
	proof, _ := os.ReadFile(proofPath)
	if !errors.Is(err, ErrHookRetirementUnproven) || !result.Changed || result.After != nil || string(proof) != "outside proof" {
		t.Fatalf("late parent result=%+v proof=%q err=%v", result, proof, err)
	}
}

func TestHookCleanupUnsafeProofSidecarsAreRetained(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			s, path, cert := producerFixture(t)
			_, _ = s.Append([]byte("first"))
			id := s.IncarnationID()
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			proofPath := path + ".producer"
			if err := os.Rename(proofPath, proofPath+".original"); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err := os.Symlink(proofPath+".original", proofPath); err != nil {
					t.Fatal(err)
				}
			} else if err := syscall.Mkfifo(proofPath, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := CleanStoppedHookSpool(path, id, cert, 1, 0, 0, false, nil); !errors.Is(err, ErrHookRetirementUnproven) {
				t.Fatalf("unsafe proof=%v", err)
			}
		})
	}
}

func TestHookCleanupMalformedIdentityAndProofAreRetained(t *testing.T) {
	for _, tc := range []struct{ name, suffix, body string }{
		{"missing-id", ".incarnation", `{}`},
		{"empty-id", ".incarnation", `{"id":""}`},
		{"blank-id", ".incarnation", `{"id":"  "}`},
		{"null-identity", ".incarnation", `null`},
		{"wrong-id-type", ".incarnation", `{"id":12}`},
		{"invalid-json", ".incarnation", `{`},
		{"missing-proof-fields", ".producer", `{}`},
		{"wrong-proof-type", ".producer", `{"pid":"invalid"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, path, cert := producerFixture(t)
			_, _ = s.Append([]byte("first"))
			id := s.IncarnationID()
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			if err := os.WriteFile(path+tc.suffix, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			for _, apply := range []bool{false, true} {
				result, err := CleanStoppedHookSpool(path, id, cert, 1, 0, 0, apply, nil)
				after, _ := os.ReadFile(path)
				if !errors.Is(err, ErrHookRetirementUnproven) || result.Changed || !bytes.Equal(before, after) {
					t.Fatalf("apply=%t result=%+v err=%v", apply, result, err)
				}
			}
		})
	}
}
