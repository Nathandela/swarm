package hookclient

// The SHIM-AWARE half of the poster: playbook §6.1's "Claude hooks post to a
// per-session shim-owned socket", with requirement 7's transition compatibility --
// "the swarm hook CLI keeps working against old shims during the transition
// (feature-detect the shim socket, fall back to the daemon socket, honest about
// which path served)". Post/Decode (hookclient.go) are unchanged: they remain the
// daemon-socket pair, and are exactly what PostSmart falls back to.
import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/Nathandela/swarm/internal/engine"
)

// EnvHookSocket names the per-session shim hook-socket path injected at spawn --
// the hookclient-side twin of EnvSocket. Unset (an old shim, or a daemon that has
// not yet wired the hook socket) means "no shim hook socket": PostSmart goes
// straight to Post, unchanged from today.
const EnvHookSocket = "SWARM_SHIM_HOOK_SOCK"

// HookPath names which transport actually carried a post.
type HookPath string

const (
	HookPathShim   HookPath = "shim"
	HookPathDaemon HookPath = "daemon"
)

// postAckTimeout bounds PostToShim's wait for the shim's single ack byte.
const postAckTimeout = 2 * time.Second

// hookShimPostRetries bounds how many times PostSmart retries the SAME shim post
// (same cb, so its Sequence stays retry-stable) after a reachable-but-silent
// attempt, before falling back to the daemon path.
const hookShimPostRetries = 2

// PostToShim dials hookSocketPath and posts cb with the same JSON encoding Post
// uses, then waits up to postAckTimeout for the shim's single ack byte.
//   - a dial failure returns acked=false with a non-nil err.
//   - a successful post with no ack observed before the deadline returns
//     acked=false, err=nil -- reachable, not (yet) confirmed; the caller MAY retry.
//   - a successful post whose ack byte arrives returns acked=true, err=nil.
func PostToShim(hookSocketPath string, cb engine.Callback) (acked bool, err error) {
	conn, err := net.Dial("unix", hookSocketPath)
	if err != nil {
		return false, fmt.Errorf("hookclient: dial shim %s: %w", hookSocketPath, err)
	}
	defer func() { _ = conn.Close() }()

	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false) // Callback.Raw is untrusted; see Post's own comment
	if err := e.Encode(cb); err != nil {
		return false, fmt.Errorf("hookclient: encode callback: %w", err)
	}
	if _, err := conn.Write(buf.Bytes()); err != nil {
		return false, fmt.Errorf("hookclient: write callback to shim: %w", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(postAckTimeout))
	var ack [1]byte
	n, rerr := conn.Read(ack[:])
	if rerr != nil || n != 1 || ack[0] != hookAckByte {
		return false, nil // reachable, not durably confirmed -- retryable, not fatal
	}
	return true, nil
}

// hookAckByte mirrors internal/shim's HookAckByte. hookclient cannot import the
// shim package (a heavy PTY/VT dependency this thin poster must never carry), so
// the single-byte wire constant is restated here rather than shared.
const hookAckByte = 0x01

// PostSmart is the CLI's one entrypoint (requirement 7). hookSocketPath=="" skips
// straight to the daemon path -- no dial attempt at all, so an old shim never pays
// a timeout. Otherwise: try PostToShim; a dial failure falls back to the daemon
// path immediately (no retry against a socket that plainly is not there); an
// ack-less-but-reachable outcome is retried against the SAME shim socket with the
// SAME cb up to hookShimPostRetries times before giving up and falling back. The
// returned HookPath names whichever transport actually carried the post.
func PostSmart(hookSocketPath, daemonSocketPath string, cb engine.Callback) (HookPath, error) {
	if hookSocketPath == "" {
		return HookPathDaemon, Post(daemonSocketPath, cb)
	}

	acked, err := PostToShim(hookSocketPath, cb)
	if err != nil {
		return HookPathDaemon, Post(daemonSocketPath, cb) // dial failure: fall back immediately
	}
	if acked {
		return HookPathShim, nil
	}
	for i := 0; i < hookShimPostRetries; i++ {
		acked, err = PostToShim(hookSocketPath, cb)
		if err == nil && acked {
			return HookPathShim, nil
		}
	}
	return HookPathDaemon, Post(daemonSocketPath, cb)
}
