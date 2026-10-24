package shim

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/Nathandela/swarm/internal/wire"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
)

func testDiskProducerCertificate(t *testing.T, path string) string {
	t.Helper()
	response, err := ReadHookSpoolFileResponse(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	return response.ProducerCertificate
}

func producerFixture(t *testing.T) (*HookSpool, string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), HookSpoolFile)
	s, err := OpenHookSpoolProducer(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cert := s.ProducerCertificate()
	if cert == "" {
		t.Fatal("new producer has no physical capability")
	}
	return s, path, cert
}

func TestHookProducerRetirementRequiresQuiescenceAndPreservesSequence(t *testing.T) {
	s, path, cert := producerFixture(t)
	if _, err := s.Append([]byte(`{"last":true}`)); err != nil {
		t.Fatal(err)
	}
	id := s.IncarnationID()
	before, _ := os.ReadFile(path)
	if err := CompactStoppedHookSpool(path, id, cert, 1, 0, 0); !errors.Is(err, ErrHookProducerBusy) {
		t.Fatalf("live compact: %v", err)
	}
	if _, err := OpenHookSpoolProducer(path, 0); !errors.Is(err, ErrHookProducerBusy) {
		t.Fatalf("second producer: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("live producer bytes changed")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := CompactStoppedHookSpool(path, id, cert, 1, 0, 0); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Size() != 0 {
		t.Fatal("folded payload retained")
	}
	reopened, err := OpenHookSpoolProducer(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if got, err := reopened.Append([]byte(`{"next":true}`)); err != nil || got != 2 {
		t.Fatalf("reopen sequence=%d err=%v", got, err)
	}
}

func TestHookProducerRetirementPreservesPartialFoldAndTornTail(t *testing.T) {
	s, path, cert := producerFixture(t)
	for _, body := range []string{`{"a":1}`, `{"b":2}`} {
		if _, err := s.Append([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	id := s.IncarnationID()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var torn [15]byte
	binary.BigEndian.PutUint64(torn[:8], 3)
	binary.BigEndian.PutUint32(torn[8:12], 100)
	copy(torn[12:], "bad")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(torn[:]); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	before, _ := os.ReadFile(path)
	cut := hookRecordHeaderLen + len(`{"a":1}`)
	if err := CompactStoppedHookSpool(path, id, cert, 1, 0, 0); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, before[cut:]) {
		t.Fatal("partial compaction changed retained/torn bytes")
	}
	resp, err := ReadHookSpoolFileResponse(path, 1)
	if err != nil || len(resp.Records) != 1 || resp.Records[0].Seq != 2 || !resp.Gap || resp.GapBoundary != 3 {
		t.Fatalf("records=%v gap=%v/%d err=%v", resp.Records, resp.Gap, resp.GapBoundary, err)
	}
}

func TestHookProducerRetirementRefusesUnknownLegacyAndReplacement(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), HookSpoolFile)
		old, err := OpenHookSpool(path, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = old.Append([]byte(`{"legacy":true}`))
		_ = old.Close()
		s, err := OpenHookSpoolProducer(path, 0)
		if err != nil {
			t.Fatal(err)
		}
		if s.ProducerCertificate() != "" {
			t.Fatal("unknown spool gained retirement proof")
		}
		_ = s.Close()
		if err := CompactStoppedHookSpool(path, s.IncarnationID(), "fake", 1, 0, 0); !errors.Is(err, ErrHookRetirementUnproven) {
			t.Fatal(err)
		}
	})
	t.Run("preidentity", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), HookSpoolFile)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := OpenHookSpoolProducer(path, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		if s.ProducerCertificate() != "" {
			t.Fatal("legacy spool gained retirement proof")
		}
	})
	t.Run("replaced", func(t *testing.T) {
		s, path, cert := producerFixture(t)
		_, _ = s.Append([]byte(`{"old":1}`))
		id := s.IncarnationID()
		_ = s.Close()
		replacement := path + ".replacement"
		data, _ := os.ReadFile(path)
		if err := os.WriteFile(replacement, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
		if testDiskProducerCertificate(t, path) != "" {
			t.Fatal("replacement inherited physical proof")
		}
		if err := CompactStoppedHookSpool(path, id, cert, 1, 0, 0); !errors.Is(err, ErrHookRetirementUnproven) {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(after, data) {
			t.Fatal("replaced spool changed")
		}
	})
}

func TestHookProducerProofFailureRetainsAcceptedPayload(t *testing.T) {
	s, path, cert := producerFixture(t)
	_, _ = s.Append([]byte(`{"accepted":true}`))
	id := s.IncarnationID()
	_ = s.Close()
	before, _ := os.ReadFile(path)
	original := persistHookProducerProof
	persistHookProducerProof = func(string, []byte) error { return syscall.ENOSPC }
	err := CompactStoppedHookSpool(path, id, cert, 1, 0, 0)
	persistHookProducerProof = original
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("proof persistence error=%v", err)
	}
	after, _ := os.ReadFile(path)
	// The replacement is already durably safe when its new physical proof fails.
	if !bytes.Equal(after, before) && len(after) != 0 {
		t.Fatal("failed proof left unsafe bytes")
	}
	resp, err := ReadHookSpoolFileResponse(path, 1)
	if err != nil || resp.Gap || len(resp.Records) != 0 {
		t.Fatalf("safe state records=%v gap=%v err=%v", resp.Records, resp.Gap, err)
	}
	if testDiskProducerCertificate(t, path) != "" {
		t.Fatal("failed proof certified replacement")
	}
}

func TestHookShutdownJoinsLastPostBeforeTerminalPublication(t *testing.T) {
	cfg := hookCfg(t)
	done := runShimAsync(cfg)
	conn := dialHookSocket(t, cfg.HookSocketPath)
	entered, release := make(chan struct{}), make(chan struct{})
	testHookAfterSpoolFsync = func() { close(entered); <-release }
	defer func() { testHookAfterSpoolFsync = nil }()
	if _, err := conn.Write([]byte(`{"event":"Stop","sequence":1}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("POST did not reach durable boundary")
	}
	ctl := dialShim(t, cfg.SocketPath)
	ctl.startReader()
	ctl.hello(shimwire.Version)
	killShim(ctl)
	time.Sleep(100 * time.Millisecond)
	for _, frame := range ctl.frames() {
		if frame.typ == wire.TControl {
			var control shimwire.Control
			if json.Unmarshal(frame.payload, &control) == nil && control.Type == shimwire.TypeExitReport {
				t.Error("exit_report published before POST handler joined")
			}
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionDir, ExitFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("exit.json published while last POST handler was active: %v", err)
	}
	select {
	case r := <-done:
		t.Errorf("Run returned before POST joined: %+v", r)
	default:
	}
	close(release)
	_ = conn.Close()
	waitRun(t, done, 10*time.Second)
	proof, err := readHookProducerProof(filepath.Join(cfg.SessionDir, HookSpoolFile))
	if err != nil || !proof.Closed {
		t.Fatalf("terminal writer proof=%+v err=%v", proof, err)
	}
}

func TestHookRetirementRefusesUnsafeOwnedPaths(t *testing.T) {
	for _, suffix := range []string{"", ".producer", ".producer.lock", ".open.lock", ".incarnation", ".floor"} {
		for _, kind := range []string{"symlink", "hardlink", "fifo"} {
			t.Run(suffix+"/"+kind, func(t *testing.T) {
				s, path, cert := producerFixture(t)
				_, _ = s.Append([]byte(`{"accepted":1}`))
				id := s.IncarnationID()
				_ = s.Close()
				target := path + suffix
				if suffix == ".floor" {
					if err := writeHookFloor(path, 0); err != nil {
						t.Fatal(err)
					}
				}
				content, err := os.ReadFile(target)
				if err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(t.TempDir(), "unrelated")
				if err := os.WriteFile(outside, content, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "symlink":
					err = os.Symlink(outside, target)
				case "hardlink":
					err = os.Link(outside, target)
				case "fifo":
					err = syscall.Mkfifo(target, 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- CompactStoppedHookSpool(path, id, cert, 1, 0, 0) }()
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("unsafe path accepted")
					}
				case <-time.After(time.Second):
					t.Fatal("unsafe path blocked retirement")
				}
				after, err := os.ReadFile(outside)
				if err != nil || !bytes.Equal(after, content) {
					t.Fatalf("unrelated target changed: %v", err)
				}
			})
		}
	}
	for _, body := range []string{"not-json", string(bytes.Repeat([]byte("x"), 4096))} {
		t.Run("invalid-proof", func(t *testing.T) {
			s, path, cert := producerFixture(t)
			_, _ = s.Append([]byte(`{"accepted":1}`))
			id := s.IncarnationID()
			_ = s.Close()
			before, _ := os.ReadFile(path)
			if err := os.WriteFile(path+".producer", []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := CompactStoppedHookSpool(path, id, cert, 1, 0, 0); err == nil {
				t.Fatal("invalid proof accepted")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("invalid proof changed payload")
			}
		})
	}
}

func TestHookRetirementCompactionENOSPCAndCrashRetainSafeState(t *testing.T) {
	for _, crash := range []bool{false, true} {
		t.Run(map[bool]string{false: "ENOSPC", true: "crash-before-rename"}[crash], func(t *testing.T) {
			s, path, cert := producerFixture(t)
			_, _ = s.Append([]byte(`{"accepted":1}`))
			id := s.IncarnationID()
			_ = s.Close()
			before, _ := os.ReadFile(path)
			if crash {
				testHookBeforeCompactRename = func() { panic("crash") }
				func() {
					defer func() {
						if recover() == nil {
							t.Error("crash seam not reached")
						}
					}()
					_ = CompactStoppedHookSpool(path, id, cert, 1, 0, 0)
				}()
				testHookBeforeCompactRename = nil
			} else {
				original := hookCompactSync
				hookCompactSync = func(*os.File) error { return syscall.ENOSPC }
				err := CompactStoppedHookSpool(path, id, cert, 1, 0, 0)
				hookCompactSync = original
				if !errors.Is(err, syscall.ENOSPC) {
					t.Fatal(err)
				}
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("pre-rename failure changed payload")
			}
			if err := CompactStoppedHookSpool(path, id, cert, 1, 0, 0); err != nil {
				t.Fatal(err)
			}
			if err := CompactStoppedHookSpool(path, id, cert, 1, 0, 0); err != nil {
				t.Fatalf("repeat retirement: %v", err)
			}
		})
	}
}

func TestHookProducerCrashProcessRequiresExactDeadIdentity(t *testing.T) {
	if path := os.Getenv("SWARM_TEST_CRASH_SPOOL"); path != "" {
		s, err := OpenHookSpoolProducer(path, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append([]byte(`{"accepted":1}`)); err != nil {
			t.Fatal(err)
		}
		os.Exit(0) // producer dies without Close; the OS releases its flock.
	}
	path := filepath.Join(t.TempDir(), HookSpoolFile)
	cmd := exec.Command(os.Args[0], "-test.run=^TestHookProducerCrashProcessRequiresExactDeadIdentity$")
	cmd.Env = append(os.Environ(), "SWARM_TEST_CRASH_SPOOL="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash producer: %v: %s", err, output)
	}
	p, err := readHookProducerProof(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Closed {
		t.Fatal("crashed producer was falsely marked closed")
	}
	before, _ := os.ReadFile(path)
	if err := CompactStoppedHookSpool(path, p.Incarnation, p.Nonce, 1, p.PID, p.Start+1); !errors.Is(err, ErrHookRetirementUnproven) {
		t.Fatalf("mismatched dead identity accepted: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("unknown owner changed payload")
	}
	if err := CompactStoppedHookSpool(path, p.Incarnation, p.Nonce, 1, p.PID, p.Start); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Size() != 0 {
		t.Fatal("crashed certified producer not retired")
	}
}

func TestHookRetirementNeverWaitsForGenerationLock(t *testing.T) {
	s, path, cert := producerFixture(t)
	_, _ = s.Append([]byte(`{"accepted":1}`))
	id := s.IncarnationID()
	_ = s.Close()
	lock, err := acquireHookSpoolOpenLock(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseHookSpoolOpenLock(lock)
	done := make(chan error, 1)
	go func() { done <- CompactStoppedHookSpool(path, id, cert, 1, 0, 0) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrHookProducerBusy) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("retirement waited for generation lock")
	}
}

func TestStoppedHookRetirementNeverCreatesMissingPaths(t *testing.T) {
	for _, missing := range []string{"directory", ".producer.lock", ".open.lock"} {
		t.Run(missing, func(t *testing.T) {
			s, path, cert := producerFixture(t)
			_, _ = s.Append([]byte(`{"accepted":1}`))
			id := s.IncarnationID()
			_ = s.Close()
			if missing == "directory" {
				if err := os.RemoveAll(filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(path + missing); err != nil {
				t.Fatal(err)
			}
			if err := CompactStoppedHookSpool(path, id, cert, 1, 0, 0); err == nil {
				t.Error("retirement accepted missing path")
			}
			target := path + missing
			if missing == "directory" {
				target = filepath.Dir(path)
			}
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("retirement recreated missing %s: %v", missing, err)
			}
		})
	}
}

func TestStoppedHookRetirementDeletionAfterProofNeverRecreates(t *testing.T) {
	for _, removed := range []string{"directory", "spool", ".open.lock", ".incarnation"} {
		t.Run(removed, func(t *testing.T) {
			s, path, cert := producerFixture(t)
			_, _ = s.Append([]byte(`{"accepted":1}`))
			id := s.IncarnationID()
			_ = s.Close()
			target := path + removed
			switch removed {
			case "directory":
				target = filepath.Dir(path)
			case "spool":
				target = path
			}
			testHookBeforeStoppedSpoolOpen = func() {
				if err := os.RemoveAll(target); err != nil {
					t.Fatal(err)
				}
			}
			defer func() { testHookBeforeStoppedSpoolOpen = nil }()
			if err := CompactStoppedHookSpool(path, id, cert, 1, 0, 0); err == nil {
				t.Error("retirement accepted deleted path")
			}
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("retirement recreated deleted %s: %v", removed, err)
			}
		})
	}
}
