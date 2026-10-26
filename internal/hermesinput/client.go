// Package hermesinput submits messages to a provider-owned Hermes native gateway.
// A prompt receipt is admission evidence, never proof the model read the message.
package hermesinput

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/coder/websocket"
)

const nativeProtocol = "hermes-gateway-v1"

// Binding names one profile, native owner incarnation and canonical session.
type Binding struct {
	Directory      string `json:"directory,omitempty"`
	Home           string `json:"home"`
	ControlHome    string `json:"control_home,omitempty"`
	OwnerPID       int    `json:"owner_pid"`
	OwnerStartTime int64  `json:"owner_start_time"`
	InstanceID     string `json:"instance_id"`
	SessionID      string `json:"session_id"`
	AuthorityEpoch int64  `json:"authority_epoch,omitempty"`
	CodeSHA        string `json:"code_sha,omitempty"`
}

type Receipt struct {
	InputID             string `json:"input_id,omitempty"`
	AdmissionID         string `json:"admission_id"`
	Status              string `json:"status"`
	ExecutionGeneration int64  `json:"execution_generation"`
	AuthorityEpoch      int64  `json:"authority_epoch"`
}

type Refusal struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Refusal) Error() string {
	return fmt.Sprintf("Hermes native refusal %d: %s", e.Code, e.Message)
}

var errUncertain = errors.New("hermes native submission outcome is uncertain; do not replay")

func IsUncertain(err error) bool { return errors.Is(err, errUncertain) }

type endpoint struct {
	PID            int    `json:"pid"`
	Protocol       int    `json:"runtime_protocol"`
	InstanceID     string `json:"instance_id"`
	State          string `json:"state"`
	Origin         string `json:"api_origin"`
	Home           string `json:"hermes_home"`
	AuthorityEpoch int64  `json:"authority_epoch"`
	CodeSHA        string `json:"code_sha"`
	Profiles       []struct {
		ID   string `json:"profile_id"`
		Home string `json:"home"`
	} `json:"served_profiles"`
}

func privateNode(path string, kind os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0077 != 0 || info.Mode().Type() != kind {
		return fmt.Errorf("hermes native path is not private: %s", path)
	}
	return nil
}

func controlSocket(home string) (string, error) {
	if !filepath.IsAbs(home) {
		return "", errors.New("hermes home must be absolute")
	}
	if err := privateNode(home, os.ModeDir); err != nil {
		return "", err
	}
	path := filepath.Join(home, "gateway.sock")
	if err := privateNode(path, os.ModeSocket); err == nil {
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	pointer := filepath.Join(home, "gateway.sock.path")
	if err := privateNode(pointer, 0); err != nil {
		return "", err
	}
	f, err := os.Open(pointer)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return "", errors.New("invalid Hermes control pointer")
	}
	path = strings.TrimSpace(string(raw))
	if !filepath.IsAbs(path) || filepath.Base(path) != "control.sock" {
		return "", errors.New("invalid Hermes control pointer")
	}
	if err := privateNode(filepath.Dir(path), os.ModeDir); err != nil {
		return "", err
	}
	return path, privateNode(path, os.ModeSocket)
}

func control(ctx context.Context, home, verb string, params any, result any) error {
	path, err := controlSocket(home)
	if err != nil {
		return err
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	if err := json.NewEncoder(c).Encode(map[string]any{"protocol": 1, "id": 1, "verb": verb, "params": params}); err != nil {
		return err
	}
	raw, err := bufio.NewReader(io.LimitReader(c, 65537)).ReadBytes('\n')
	if err != nil || len(raw) > 65536 {
		return errors.New("invalid Hermes control response")
	}
	var reply struct {
		OK     bool            `json:"ok"`
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &reply) != nil || !reply.OK || reply.ID != 1 {
		return errors.New("hermes private bootstrap refused")
	}
	return json.Unmarshal(reply.Result, result)
}

func identify(ctx context.Context, b Binding) (endpoint, error) {
	var e endpoint
	start, err := procstart.StartTime(b.OwnerPID)
	if err != nil || b.OwnerPID <= 0 || b.OwnerStartTime <= 0 || start != b.OwnerStartTime {
		return e, errors.New("hermes owner incarnation changed")
	}
	controlHome := b.ControlHome
	if controlHome == "" {
		controlHome = b.Home
	}
	if err := control(ctx, controlHome, "identify", map[string]any{}, &e); err != nil {
		return e, err
	}
	if e.PID != b.OwnerPID || e.Protocol != 1 || e.State != "ready" || e.InstanceID == "" || e.Home != controlHome || (b.InstanceID != "" && e.InstanceID != b.InstanceID) || b.AuthorityEpoch > 0 && e.AuthorityEpoch != b.AuthorityEpoch || b.CodeSHA != "" && e.CodeSHA != b.CodeSHA {
		return e, errors.New("hermes native owner binding mismatch")
	}
	matched := 0
	for _, profile := range e.Profiles {
		if profile.ID == b.Home && profile.Home == b.Home {
			matched++
		}
	}
	if matched != 1 {
		return e, errors.New("hermes native owner serves another profile")
	}
	return e, nil
}

func connect(ctx context.Context, b Binding) (*websocket.Conn, error) {
	e, err := identify(ctx, b)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(e.Origin)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		return nil, errors.New("hermes native origin is not loopback")
	}
	var grant struct {
		Ticket     string `json:"ticket"`
		ProfileID  string `json:"profile_id"`
		InstanceID string `json:"instance_id"`
		Protocol   int    `json:"runtime_protocol"`
	}
	controlHome := b.ControlHome
	if controlHome == "" {
		controlHome = b.Home
	}
	if err := control(ctx, controlHome, "session-ticket", map[string]string{"profile_id": b.Home, "instance_id": e.InstanceID, "purpose": "interactive"}, &grant); err != nil {
		return nil, err
	}
	if grant.Ticket == "" || grant.ProfileID != b.Home || grant.InstanceID != e.InstanceID || grant.Protocol != 1 {
		return nil, errors.New("hermes native ticket binding mismatch")
	}
	u.Scheme = "ws"
	u.Path = "/api/ws"
	c, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: &http.Client{Transport: &http.Transport{Proxy: nil}}, Subprotocols: []string{nativeProtocol, "hermes-gateway-ticket." + grant.Ticket}})
	if err != nil {
		return nil, err
	}
	if c.Subprotocol() != nativeProtocol {
		_ = c.CloseNow()
		return nil, errors.New("hermes native protocol mismatch")
	}
	c.SetReadLimit(8 * 1024 * 1024)
	return c, nil
}

func rpc(ctx context.Context, c *websocket.Conn, id int, method string, params any, result any) error {
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	if err = c.Write(ctx, websocket.MessageText, raw); err != nil {
		return err
	}
	for {
		_, raw, err = c.Read(ctx)
		if err != nil {
			return err
		}
		var reply struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *Refusal        `json:"error"`
		}
		if err = json.Unmarshal(raw, &reply); err != nil {
			return err
		}
		if reply.ID != id {
			continue
		}
		if reply.Error != nil {
			return reply.Error
		}
		return json.Unmarshal(reply.Result, result)
	}
}

// Submit never writes terminal bytes, answers a native request, or retries an uncertain send.
func Submit(ctx context.Context, b Binding, operationID, text string) (Receipt, error) {
	var receipt Receipt
	if b.SessionID == "" || b.InstanceID == "" || operationID == "" || text == "" {
		return receipt, errors.New("hermes submission requires exact owner, session, operation and text")
	}
	c, err := connect(ctx, b)
	if err != nil {
		return receipt, err
	}
	defer func() { _ = c.CloseNow() }()
	var resumed struct {
		SessionID string `json:"session_id"`
	}
	if err = rpc(ctx, c, 1, "session.resume", map[string]string{"session_id": b.SessionID}, &resumed); err != nil {
		return receipt, err
	}
	if resumed.SessionID != b.SessionID {
		return receipt, errors.New("hermes resumed another native session")
	}
	err = rpc(ctx, c, 2, "prompt.submit", map[string]string{"session_id": b.SessionID, "input_id": operationID, "text": text}, &receipt)
	if err != nil {
		var refusal *Refusal
		if errors.As(err, &refusal) {
			return receipt, err
		}
		return receipt, fmt.Errorf("%w: %v", errUncertain, err)
	}
	if receipt.AdmissionID == "" || receipt.ExecutionGeneration < 0 || receipt.AuthorityEpoch <= 0 || (receipt.Status != "queued" && receipt.Status != "started" && receipt.Status != "terminal") {
		return Receipt{}, errUncertain
	}
	return receipt, nil
}
