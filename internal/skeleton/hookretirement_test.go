package skeleton

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

func TestEndedSessionRetiresFinalHookPayload(t *testing.T) {
	dir, err := os.MkdirTemp("", "swhret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })
	scriptedCapture(sk, "last-payload")
	m := launchFake(t, sk, "idle 600s\n")
	ch, ok := sk.Core().SessionHookChannel(m.ID)
	if !ok {
		t.Fatal("missing hook channel")
	}
	waitForSocket(t, ch.SocketPath)
	token := envValue(launchEnv(t, dir, m.ID), hookclient.EnvToken)
	postRawEvent(t, ch.SocketPath, m.ID, token, 1)
	if err := sk.Core().Kill(m.ID); err != nil {
		t.Fatal(err)
	}
	texts := awaitJournalTexts(t, sk, m.ID, 1)
	if texts[0] != "last-payload" {
		t.Fatal(texts)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		info, err := os.Stat(ch.SpoolPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ended hook spool retains %d payload bytes", info.Size())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEndedSessionPartialCursorCommitPreservesLaterPayload(t *testing.T) {
	dir, err := os.MkdirTemp("", "swhrpart")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })
	const id, token = "partial-end", "partial-token"
	sessionDir := filepath.Join(dir, id)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	h := startHookShim(t, sessionDir, id)
	raw, _ := json.Marshal(map[string]string{"session_id": id, "hook_socket_path": h.cfg.HookSocketPath})
	if err := os.WriteFile(filepath.Join(sessionDir, "shim-launch.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	registerTestSession(sk, id, token)
	scriptedCapture(sk, "first", "second")
	postRawEvent(t, h.cfg.HookSocketPath, id, token, 1)
	postRawEvent(t, h.cfg.HookSocketPath, id, token, 2)
	h.kill(t)
	original := persistHookDrainCursor
	persistHookDrainCursor = func(path string, seq uint64) error {
		if seq == 2 {
			return syscall.ENOSPC
		}
		return original(path, seq)
	}
	defer func() { persistHookDrainCursor = original }()
	sk.startHookDrain(id)
	sk.endSession(id)
	ch, _ := sk.Core().SessionHookChannel(id)
	recs, _, gap, err := shim.ReadHookSpoolFile(ch.SpoolPath, 1)
	if err != nil || gap || len(recs) != 1 || recs[0].Seq != 2 {
		t.Fatalf("partial final drain records=%v gap=%v err=%v", recs, gap, err)
	}
	if got := readHookCursor(ch.CursorPath); got != 1 {
		t.Fatalf("durable cursor=%d", got)
	}
}

func TestHookCursorCertificateCannotBlessOldNumericCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fold")
	if err := persistHookCursor(path, 1); err != nil {
		t.Fatal(err)
	}
	hd := NewHookDrainer(&Daemon{}, "test", "", path)
	_, _, err := hd.applyLocked(shim.HookDrainResponse{SpoolIncarnation: "new", ProducerCertificate: "new-producer", Records: []shim.HookRecord{{Seq: 2, Body: []byte(`not-json`)}}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".cert"); !os.IsNotExist(err) {
		t.Fatalf("old decimal cursor gained certificate: %v", err)
	}
}

func TestHookCertificateFailureIsRetryableWithoutResurrectingAuthentication(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, shim.HookSpoolFile)
	s, err := shim.OpenHookSpoolProducer(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := s.Append([]byte(`not-json`))
	if err != nil {
		t.Fatal(err)
	}
	response := shim.HookDrainResponse{SpoolIncarnation: s.IncarnationID(), ProducerCertificate: s.ProducerCertificate(), Records: []shim.HookRecord{{Seq: seq, Body: []byte(`not-json`)}}}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	hd := NewHookDrainer(&Daemon{}, "test", "", filepath.Join(dir, "fold"))
	hd.SetSpoolPath(path)
	original := persistHookCursorCertificate
	persistHookCursorCertificate = func(string, []byte) error { return syscall.ENOSPC }
	_, _, err = hd.applyLocked(response, 0)
	persistHookCursorCertificate = original
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Size() == 0 {
		t.Fatal("certificate failure removed accepted bytes")
	}
	if got := hd.cursor(); got != 1 {
		t.Fatalf("cursor was not committed before certificate: %d", got)
	}
	// This retry has no engine at all: only the in-process durable-cursor proof is used.
	if err := hd.retireCertifiedPrefix(); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(path)
	if info.Size() != 0 {
		t.Fatal("retry did not retire certified prefix")
	}
}

func TestHookCursorFailureNeverCertifiesOrRetiresPayload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, shim.HookSpoolFile)
	s, err := shim.OpenHookSpoolProducer(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	seq, _ := s.Append([]byte(`not-json`))
	response := shim.HookDrainResponse{SpoolIncarnation: s.IncarnationID(), ProducerCertificate: s.ProducerCertificate(), Records: []shim.HookRecord{{Seq: seq, Body: []byte(`not-json`)}}}
	_ = s.Close()
	cursor := filepath.Join(dir, "fold")
	if err := os.Mkdir(cursor, 0o700); err != nil {
		t.Fatal(err)
	}
	hd := NewHookDrainer(&Daemon{}, "test", "", cursor)
	hd.SetSpoolPath(path)
	if _, _, err := hd.applyLocked(response, 0); err == nil {
		t.Fatal("cursor write unexpectedly succeeded")
	}
	if _, err := os.Stat(cursor + ".cert"); !os.IsNotExist(err) {
		t.Fatal("failed cursor gained certificate")
	}
	if err := hd.retireCertifiedPrefix(); !errors.Is(err, shim.ErrHookRetirementUnproven) {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Size() == 0 {
		t.Fatal("uncommitted fold retired bytes")
	}
}

func TestHookRetirementBusyRetriesOnlyCertifiedPrefix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, shim.HookSpoolFile)
	s, err := shim.OpenHookSpoolProducer(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_, _ = s.Append([]byte(`not-json`))
	d := &Daemon{}
	hd := NewHookDrainer(d, "ended", "", filepath.Join(dir, "fold"))
	hd.SetSpoolPath(path)
	_, _, err = hd.applyLocked(shim.HookDrainResponse{SpoolIncarnation: s.IncarnationID(), ProducerCertificate: s.ProducerCertificate(), Records: []shim.HookRecord{{Seq: 1, Body: []byte(`not-json`)}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Append([]byte(`later-unfolded`))
	d.retireHookPayload(hd)
	if len(d.drains.retirement) != 1 {
		t.Fatal("busy retirement not registered")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	d.retryHookRetirements() // no engine exists; this path cannot ingest or authenticate.
	if len(d.drains.retirement) != 0 {
		t.Fatal("successful retry remains registered")
	}
	recs, _, gap, err := shim.ReadHookSpoolFile(path, 1)
	if err != nil || gap || len(recs) != 1 || string(recs[0].Body) != "later-unfolded" {
		t.Fatalf("uncertified records=%v gap=%v err=%v", recs, gap, err)
	}
}

func TestFinalHookDrainStopsQueuedPolls(t *testing.T) {
	hd := NewHookDrainer(&Daemon{}, "ended", "", filepath.Join(t.TempDir(), "fold"))
	hd.FinalDrain()
	if _, _, err := hd.DrainOnce(); !errors.Is(err, errHookDrainStopped) {
		t.Fatalf("queued post-final poll=%v", err)
	}
}

func TestTerminalStartupRetirementRetriesWithoutEngineRegistration(t *testing.T) {
	dir, err := os.MkdirTemp("", "swhrstart")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	const id = "terminal"
	store, err := persist.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(persist.Meta{ID: id, AgentType: "reference", CreatedAt: time.Now(), LastActivity: time.Now(), Status: status.Status{Process: status.ProcessExited}}); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(dir, id)
	path := filepath.Join(sessionDir, shim.HookSpoolFile)
	s, err := shim.OpenHookSpoolProducer(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Append([]byte(`not-json`))
	raw, _ := json.Marshal(map[string]string{"session_id": id, "hook_socket_path": filepath.Join(sessionDir, "h.sock")})
	if err := os.WriteFile(filepath.Join(sessionDir, "shim-launch.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var cursor string
	// Use the core's persisted channel path rather than duplicating that filename.
	core, err := daemon.Open(daemon.Config{StateDir: dir, SocketPath: filepath.Join(dir, "d.sock"), LockPath: filepath.Join(dir, "d.lock"), LogPath: filepath.Join(dir, "d.log"), MaxSessions: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = core.Close() }()
	channel, ok := core.SessionHookChannel(id)
	if !ok {
		t.Fatal("missing terminal channel")
	}
	cursor = channel.CursorPath
	hd := NewHookDrainer(&Daemon{}, id, "", cursor)
	hd.SetSpoolPath(path)
	_, _, err = hd.applyLocked(shim.HookDrainResponse{SpoolIncarnation: s.IncarnationID(), ProducerCertificate: s.ProducerCertificate(), Records: []shim.HookRecord{{Seq: 1, Body: []byte(`not-json`)}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(path+".producer.lock", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{core: core}
	d.startHookDrainsForRunning()
	if len(d.drains.retirement) != 1 {
		t.Fatal("terminal busy spool was not registered for retry")
	}
	_ = lock.Close()
	d.retryHookRetirements()
	if len(d.drains.retirement) != 0 {
		t.Fatal("terminal retry did not complete")
	}
	info, _ := os.Stat(path)
	if info.Size() != 0 {
		t.Fatal("terminal certified prefix retained")
	}
	if d.eng != nil || len(d.drains.stop) != 0 {
		t.Fatal("terminal recovery registered ingestion/authentication")
	}
}

func TestReplacementSpoolCannotExtendPriorIncarnationCertificate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, shim.HookSpoolFile)
	old, err := shim.OpenHookSpoolProducer(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = old.Append([]byte(`not-json`))
	hd := NewHookDrainer(&Daemon{}, "test", "", filepath.Join(dir, "fold"))
	hd.SetSpoolPath(path)
	_, _, err = hd.applyLocked(shim.HookDrainResponse{SpoolIncarnation: old.IncarnationID(), ProducerCertificate: old.ProducerCertificate(), Records: []shim.HookRecord{{Seq: 1, Body: []byte(`not-json`)}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = old.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	replacement, err := shim.OpenHookSpoolProducer(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = replacement.Append([]byte(`not-json`))
	_, _ = replacement.Append([]byte(`not-json`))
	_ = replacement.Close()
	if _, _, err := hd.drainFromSpoolFile(); err != nil {
		t.Fatal(err)
	}
	if err := hd.retireCertifiedPrefix(); !errors.Is(err, shim.ErrHookRetirementUnproven) {
		t.Fatalf("replacement inherited old certification: %v", err)
	}
	recs, _, _, err := shim.ReadHookSpoolFile(path, 0)
	if err != nil || len(recs) != 2 {
		t.Fatalf("replacement accepted bytes removed: %v %v", recs, err)
	}
}

func TestHookRetirementExhaustionLogsOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, shim.HookSpoolFile)
	s, err := shim.OpenHookSpoolProducer(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_, _ = s.Append([]byte(`not-json`))
	d := &Daemon{}
	hd := NewHookDrainer(d, "exhausted-attempt", "", filepath.Join(dir, "fold"))
	hd.SetSpoolPath(path)
	_, _, err = hd.applyLocked(shim.HookDrainResponse{SpoolIncarnation: s.IncarnationID(), ProducerCertificate: s.ProducerCertificate(), Records: []shim.HookRecord{{Seq: 1, Body: []byte(`not-json`)}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	d.drains.retirement = map[string]hookRetirementRetry{hd.sessionID: {hd, 1}}
	var output bytes.Buffer
	original := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(original)
	d.retryHookRetirements()
	d.retryHookRetirements()
	text := output.String()
	if strings.Count(text, "exhausted retries") != 1 || !strings.Contains(text, hd.sessionID) || !strings.Contains(text, shim.ErrHookProducerBusy.Error()) {
		t.Fatalf("exhaustion diagnostic=%q", text)
	}
}
