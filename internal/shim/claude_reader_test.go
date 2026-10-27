package shim

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/shimwire"
	"golang.org/x/sys/unix"
)

type claudePauseAfterRead struct {
	reader                    io.Reader
	entered, release          chan struct{}
	once                      sync.Once
	positiveReads, pauseAfter int
}

func (r *claudePauseAfterRead) Read(buf []byte) (int, error) {
	n, err := r.reader.Read(buf)
	if n > 0 {
		r.positiveReads++
		if r.positiveReads == r.pauseAfter || r.pauseAfter == 0 {
			r.once.Do(func() { close(r.entered); <-r.release })
		}
	}
	return n, err
}

func TestClaudeChatReadConsumptionAndPublicationAreOneAdmission(t *testing.T) {
	cfg := claudeChatFixtureConfig(t)
	spool, err := OpenHookSpool(filepath.Join(t.TempDir(), HookSpoolFile), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := listen(newSocketPath(t))
	if err != nil {
		_ = spool.Close()
		t.Fatal(err)
	}
	srv := newServer(listener, listener.Addr().String(), nil, nil, nil, 0, time.Second, &Metrics{})
	srv.claudeChat = true
	accepted := make(chan struct{})
	go func() { defer close(accepted); srv.acceptLoop() }()
	var files []*os.File
	var cmd *exec.Cmd
	var capture *claudeChatCapture
	var waited chan error
	var stdoutDone, stderrDone chan struct{}
	joined := false
	var stopOnce, finalOnce sync.Once
	stopControl := func() { stopOnce.Do(func() { srv.shutdown(shimwire.Control{}); <-accepted }) }
	finish := func() {
		finalOnce.Do(func() {
			stopControl()
			if capture != nil {
				capture.retireSignals()
			}
			srv.finishEscalation()
		})
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		if cmd != nil && cmd.Process != nil && !joined && waited != nil {
			select {
			case <-waited:
				joined = true
			default:
				srv.onSignal(shimwire.SigKill)
				<-waited
				joined = true
			}
		}
		finish()
		releaseOnce.Do(func() { close(release) })
		if stdoutDone != nil {
			<-stdoutDone
		}
		if stderrDone != nil {
			<-stderrDone
		}
		if capture != nil {
			close(capture.queue)
			<-capture.done
		}
		for _, f := range files {
			_ = f.Close()
		}
		_ = spool.Close()
	})
	pipe := func() (*os.File, *os.File) {
		r, w, e := os.Pipe()
		if e != nil {
			t.Fatal(e)
		}
		files = append(files, r, w)
		return r, w
	}
	inR, inW := pipe()
	outR, outW := pipe()
	errR, errW := pipe()
	if claudePipeNonblock(inW) != nil || claudePipeNonblock(outR) != nil {
		t.Fatal("nonblocking fixture pipes unavailable")
	}
	trigger := filepath.Join(t.TempDir(), "emit-partial")
	script := filepath.Join(t.TempDir(), "reader-fixture")
	body := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"" + cfg.ConversationID + "\"}'\ni=0;while [ ! -f '" + trigger + "' ] && [ $i -lt 1200 ];do sleep .01;i=$((i+1));done\nprintf '%s' 'partial'\nexec sleep 12\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("/bin/sh", script)
	cmd.Dir = cfg.Cwd
	cmd.Env = cfg.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	srv.setAgentPgid(cmd.Process.Pid)
	waited = make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	_ = inR.Close()
	_ = outW.Close()
	_ = errW.Close()
	start, err := procstart.StartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	shimStart, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	identity := shimwire.ClaudePrimaryIdentity{SessionID: cfg.SessionID, RequiredRuntime: cfg.RequiredRuntime, NativeSessionID: cfg.ConversationID, ShimPID: os.Getpid(), ShimStartTime: shimStart, PrimaryPID: cmd.Process.Pid, PrimaryStartTime: start}
	capture = newClaudeChatCapture(spool, srv, identity)
	capture.stdin, capture.stdout = inW, outR
	srv.claudePrimary.Store(capture)
	stdoutDone, stderrDone = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stdoutDone)
		capture.readStdout(&claudePauseAfterRead{reader: claudePipeReader{file: outR}, entered: entered, release: release, pauseAfter: 2})
	}()
	go func() { defer close(stderrDone); capture.readStderr(errR, io.Discard) }()
	deadline := time.Now().Add(5 * time.Second)
	var ready *shimwire.ClaudePrimarySnapshot
	for time.Now().Before(deadline) {
		ready = capture.primarySnapshot()
		if ready != nil && ready.ObservedSequence == 1 && ready.DurableSequence == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if ready == nil || ready.ObservedSequence != 1 || ready.DurableSequence != 1 {
		t.Fatal("real initial frame never became durably ready")
	}
	if err := os.WriteFile(trigger, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not consume later real stdout bytes")
	}
	staleSnapshot := capture.primarySnapshot()
	request := shimwire.ClaudeMessageRequest{Version: 1, OperationID: "paused-read", NativeUserUUID: "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a", Text: "not-written", Identity: identity, OwnerRevision: 1, ThroughStreamSequence: ready.ObservedSequence, ReaderRevision: ready.ReaderRevision}
	result := capture.writeMessage(request)
	if staleSnapshot != nil || result.Status != "refused" {
		t.Fatal("snapshot/write overtook consumed but unpublished bytes", result.Status)
	}
	client := dialShim(t, listener.Addr().String())
	client.startReader()
	client.hello(shimwire.Version)
	client.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
	if !processGone(cmd.Process.Pid, 2*time.Second) {
		t.Fatal("owner control Stop waited for reader publication")
	}
	select {
	case <-waited:
		joined = true
	case <-time.After(time.Second):
		t.Fatal("exact child Wait did not join before reader release")
	}
	finish() // retire control/capture actors before final containment and reader release
	releaseOnce.Do(func() { close(release) })
	select {
	case <-stdoutDone:
	case <-time.After(time.Second):
		t.Fatal("stdout did not reach real EOF")
	}
	if snapshot := capture.primarySnapshot(); snapshot == nil || snapshot.ReaderRevision < 3 || snapshot.ObservedSequence != 2 || snapshot.OwnerRevision != 0 || !snapshot.StdoutEOF {
		t.Fatal("released read lost sequence/EOF or consumed refused owner revision")
	}
}

func claudeMessagePipeUnit(t *testing.T) (*claudeChatCapture, *os.File, *os.File) {
	t.Helper()
	spool, err := OpenHookSpool(filepath.Join(t.TempDir(), HookSpoolFile), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if claudePipeNonblock(inW) != nil || claudePipeNonblock(outR) != nil {
		t.Fatal("nonblocking pipe setup")
	}
	// Pipe-only transport fixture; no process or managed-image authority claimed.
	identity := shimwire.ClaudePrimaryIdentity{SessionID: "pipe-unit", RequiredRuntime: "claude-chat", NativeSessionID: claudeChatTestUUID}
	c := newClaudeChatCapture(spool, &server{}, identity)
	c.stdin, c.stdout = inW, outR
	t.Cleanup(func() {
		close(c.queue)
		<-c.done
		for _, f := range []*os.File{inR, inW, outR, outW} {
			_ = f.Close()
		}
		_ = spool.Close()
	})
	if _, err := outW.Write([]byte("{\"type\":\"system\",\"subtype\":\"init\"}\n")); err != nil {
		t.Fatal(err)
	}
	var buf [32 << 10]byte
	if _, err := c.consumeStdout(claudePipeReader{file: outR}, buf[:]); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		c.mu.Lock()
		done := c.durableSequence == 1
		c.mu.Unlock()
		if done {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("init did not persist")
		}
		time.Sleep(time.Millisecond)
	}
	return c, inR, outW
}

func TestClaudeChatPartialAndEAGAINNeverLookClean(t *testing.T) {
	c, inR, outW := claudeMessagePipeUnit(t)
	if _, err := outW.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	var buf [32 << 10]byte
	if _, err := c.consumeStdout(claudePipeReader{file: c.stdout}, buf[:]); err != nil {
		t.Fatal(err)
	}
	p := c.primarySnapshot()
	if p == nil || !p.Healthy || !p.PendingPartial || p.ReaderRevision != 2 {
		t.Fatal("partial read falsely clean")
	}
	if _, err := c.consumeStdout(claudePipeReader{file: c.stdout}, buf[:]); !errors.Is(err, unix.EAGAIN) {
		t.Fatal("empty nonblocking read was not EAGAIN", err)
	}
	if next := c.primarySnapshot(); next == nil || next.ReaderRevision != p.ReaderRevision {
		t.Fatal("EAGAIN fabricated reader progress")
	}
	r := c.writeMessage(shimwire.ClaudeMessageRequest{Version: 1, OperationID: "partial", NativeUserUUID: "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a", Text: "no", Identity: p.Identity, OwnerRevision: 1, ThroughStreamSequence: p.ObservedSequence, ReaderRevision: p.ReaderRevision})
	ready, err := claudePipePoll(inR, unix.POLLIN, 0)
	if r.Status != "refused" || err != nil || ready != 0 {
		t.Fatal("partial read allowed native bytes")
	}
	_ = outW.Close()
	if _, err := c.consumeStdout(claudePipeReader{file: c.stdout}, buf[:]); err != io.EOF {
		t.Fatal("closed pipe did not yield true EOF", err)
	}
	if next := c.primarySnapshot(); next == nil || next.Healthy || !next.StdoutEOF || next.OwnerRevision != 0 {
		t.Fatal("torn EOF became clean or attempted write")
	}
}

func TestClaudeChatPartialWriteTimesOutUncertainAndNeverResends(t *testing.T) {
	c, _, _ := claudeMessagePipeUnit(t)
	p := c.primarySnapshot()
	request := shimwire.ClaudeMessageRequest{Version: 1, OperationID: "partial-native-write", NativeUserUUID: "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a", Text: strings.Repeat("\x01", 16<<10), Identity: p.Identity, OwnerRevision: 1, ThroughStreamSequence: p.ObservedSequence, ReaderRevision: p.ReaderRevision}
	r := c.writeMessage(request)
	if r.Status == "written" {
		t.Skip("host pipe absorbed entire bounded message; no partial-write pressure")
	}
	if r.Status != "uncertain" {
		t.Fatal("actual input attempt was not uncertain", r.Status)
	}
	if next := c.primarySnapshot(); next == nil || next.OwnerRevision != 1 || next.Healthy {
		t.Fatal("partial input lost attempt revision or loss fence")
	}
	if next := c.writeMessage(request); next.Status != "refused" {
		t.Fatal("uncertain native input was resent")
	}
}

func TestClaudeChatReadErrorIsNotEOFSnapshot(t *testing.T) {
	c, _, _ := claudeMessagePipeUnit(t)
	var buf [32 << 10]byte
	if _, err := c.consumeStdout(&claudeChatDataError{}, buf[:]); !errors.Is(err, syscall.EIO) {
		t.Fatal("read error lost", err)
	}
	if p := c.primarySnapshot(); p == nil || p.Healthy || p.StdoutEOF || p.ReaderRevision != 2 {
		t.Fatal("read error fabricated EOF or healthy state")
	}
}
