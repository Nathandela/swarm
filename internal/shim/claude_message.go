package shim

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/shimwire"
	"golang.org/x/sys/unix"
)

// Control keeps the descriptor stable without invoking os.File.Read's polling
// loop while admission is held. The Run-owned read end is already nonblocking.
type claudePipeReader struct{ file *os.File }

func (r claudePipeReader) Read(buf []byte) (n int, err error) {
	raw, err := r.file.SyscallConn()
	if err != nil {
		return 0, err
	}
	var readErr error
	if controlErr := raw.Control(func(fd uintptr) { n, readErr = unix.Read(int(fd), buf) }); controlErr != nil {
		return 0, controlErr
	}
	err = readErr
	if n == 0 && err == nil {
		return 0, io.EOF
	}
	if n < 0 {
		n = 0
	}
	return n, err
}

func claudePipeNonblock(file *os.File) error {
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var result error
	if err := raw.Control(func(fd uintptr) { result = unix.SetNonblock(int(fd), true) }); err != nil {
		return err
	}
	return result
}

func claudePipePoll(file *os.File, events int16, timeout int) (int16, error) {
	raw, err := file.SyscallConn()
	if err != nil {
		return 0, err
	}
	var revents int16
	var result error
	if err := raw.Control(func(fd uintptr) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: events}}
		_, result = unix.Poll(fds, timeout)
		revents = fds[0].Revents
	}); err != nil {
		return 0, err
	}
	return revents, result
}

// The test-owned Reader seam may pause after a real read but before returning.
// Holding admission across it prevents a falsely clean snapshot in that window.
func (c *claudeChatCapture) consumeStdout(reader io.Reader, buf []byte) (int, error) {
	c.admission.Lock()
	defer c.admission.Unlock()
	n, err := reader.Read(buf)
	if n == 0 && (errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR)) {
		return n, err
	}
	if n > 0 || err != nil {
		if c.readerRevision == ^uint64(0) {
			c.fail()
			return n, err
		}
		c.readerRevision++
	}
	for _, b := range buf[:n] {
		if c.oversized {
			if b == '\n' {
				c.oversized = false
			}
			continue
		}
		if b == '\n' {
			c.admit(c.partial)
			c.partial = nil
			continue
		}
		if len(c.partial) == claudeChatBytes {
			c.fail()
			c.partial = nil
			c.oversized = true
			continue
		}
		c.partial = append(c.partial, b)
	}
	if err != nil {
		if len(c.partial) > 0 && !c.oversized {
			c.admit(c.partial)
			c.partial = nil
			c.fail()
		}
		if errors.Is(err, io.EOF) {
			c.mu.Lock()
			c.eof[0] = true
			c.mu.Unlock()
		} else {
			c.fail()
		}
	}
	return n, err
}

func (c *claudeChatCapture) primarySnapshot() *shimwire.ClaudePrimarySnapshot {
	if !c.admission.TryLock() {
		return nil
	}
	defer c.admission.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	return &shimwire.ClaudePrimarySnapshot{Identity: c.identity, ObservedSequence: c.sequence, DurableSequence: c.durableSequence, ReaderRevision: c.readerRevision, OwnerRevision: c.ownerRevision, Healthy: !c.lost && !c.eof[0] && !c.eof[1], PendingPartial: len(c.partial) > 0 || c.oversized, StdoutEOF: c.eof[0], StderrEOF: c.eof[1]}
}

func (c *claudeChatCapture) writeMessage(req shimwire.ClaudeMessageRequest) shimwire.ClaudeMessageResult {
	result := shimwire.ClaudeMessageResult{Version: 1, OperationID: req.OperationID, NativeUserUUID: req.NativeUserUUID, Identity: req.Identity, OwnerRevision: req.OwnerRevision, Status: "refused"}
	if !c.admission.TryLock() {
		return result
	}
	defer c.admission.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	c.mu.Lock()
	healthy := !c.lost && !c.eof[0] && !c.eof[1] && c.durableSequence == c.sequence
	observed := c.sequence
	c.mu.Unlock()
	if req.Version != 1 || len(req.OperationID) == 0 || len(req.OperationID) > 128 || !adapter.IsCanonicalConversationID(req.NativeUserUUID) || len(req.Text) == 0 || len(req.Text) > shimwire.MaxClaudeFixtureTextBytes || !utf8.ValidString(req.Text) || req.Identity != c.identity || req.ExpectedOwnerRevision != c.ownerRevision || req.OwnerRevision <= c.ownerRevision || req.ThroughStreamSequence != observed || req.ReaderRevision != c.readerRevision || !healthy || observed == 0 || len(c.partial) != 0 || c.oversized || c.stdin == nil || c.stdout == nil {
		return result
	}
	ready, err := claudePipePollUntil(c.stdout, unix.POLLIN, 0, deadline)
	if err != nil || ready != 0 {
		return result
	} // pending output/HUP cannot be silently overtaken
	ready, err = claudePipePollUntil(c.stdin, unix.POLLOUT, 0, deadline)
	if err != nil || ready&unix.POLLOUT == 0 || ready&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
		return result
	}
	var native struct {
		Type    string `json:"type"`
		UUID    string `json:"uuid"`
		Session string `json:"session_id"`
		Parent  any    `json:"parent_tool_use_id"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	native.Type, native.UUID, native.Session = "user", req.NativeUserUUID, c.identity.NativeSessionID
	native.Message.Role = "user"
	native.Message.Content = append(native.Message.Content, struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{"text", req.Text})
	body, err := json.Marshal(native)
	if err != nil {
		return result
	}
	body = append(body, '\n')
	raw, err := c.stdin.SyscallConn()
	if err != nil {
		return result
	}
	// Every later result is uncertain until the complete LF-terminated write
	// returns. Reserving before the first syscall forbids duplicate attempts.
	c.mu.Lock()
	if c.lost || c.eof[0] || c.eof[1] {
		c.mu.Unlock()
		return result
	}
	c.ownerRevision = req.OwnerRevision
	c.mu.Unlock()
	result.Status = "uncertain"
	written := 0
	for written < len(body) {
		if !time.Now().Before(deadline) {
			c.fail()
			return result
		}
		var n int
		var writeErr error
		expired := false
		controlErr := raw.Control(func(fd uintptr) {
			if !time.Now().Before(deadline) {
				expired = true
				return
			}
			n, writeErr = unix.Write(int(fd), body[written:])
		})
		if expired {
			c.fail()
			return result
		}
		if n > 0 {
			written += n
		}
		if controlErr != nil || writeErr != nil && !errors.Is(writeErr, unix.EAGAIN) && !errors.Is(writeErr, unix.EINTR) {
			c.fail()
			return result
		}
		if written == len(body) {
			result.Status = "written"
			return result
		}
		if !time.Now().Before(deadline) {
			c.fail()
			return result
		}
		if _, err := claudePipePollUntil(c.stdin, unix.POLLOUT, 10, deadline); err != nil {
			c.fail()
			return result
		}
	}
	return result
}

// Interrupted readiness consumes no stream bytes. Retry only EINTR, within the
// same absolute operation deadline; all real descriptor errors still refuse.
func claudePipePollUntil(file *os.File, events int16, timeout int, deadline time.Time) (int16, error) {
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, os.ErrDeadlineExceeded
		}
		wait := timeout
		if wait > 0 && int64(wait) > remaining.Milliseconds() {
			wait = int(remaining.Milliseconds())
		}
		ready, err := claudePipePoll(file, events, wait)
		if !errors.Is(err, unix.EINTR) {
			return ready, err
		}
	}
}
