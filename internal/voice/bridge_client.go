package voice

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/jsontext"
	"github.com/Nathandela/swarm/internal/managerproof"
)

type ManagerBridgeRequest struct {
	Version        int             `json:"version"`
	ConversationID string          `json:"conversation_id"`
	SessionID      string          `json:"session_id"`
	Call           ManagerToolCall `json:"call"`
}

type ManagerBridgeReply struct {
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

type ManagerBridgeClient struct {
	conversation, session, credential string
	http                              *http.Client
}

// NewManagerBridgeClient uses only the configured Unix socket, never a remote URL.
func NewManagerBridgeClient(socket, authFile, conversation, session string) (*ManagerBridgeClient, error) {
	for _, path := range []string{socket, authFile} {
		if !configText(path, 4096) || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, ErrInvalid
		}
	}
	if !configText(conversation, 256) || !configText(session, 256) {
		return nil, ErrInvalid
	}
	if err := validateManagerSocket(socket); err != nil {
		return nil, err
	}
	raw, err := readVoiceFile(filepath.Dir(authFile), filepath.Base(authFile), 256)
	if err != nil {
		return nil, errors.New("voice: manager bridge credential unavailable")
	}
	credential := strings.TrimSpace(string(raw))
	_, decodeErr := hex.DecodeString(credential)
	if len(credential) != 64 || decodeErr != nil || strings.ToLower(credential) != credential {
		return nil, errors.New("voice: invalid manager bridge credential")
	}
	transport := &http.Transport{MaxIdleConns: 1, MaxConnsPerHost: 4, IdleConnTimeout: 30 * time.Second, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		if err := validateManagerSocket(socket); err != nil {
			return nil, err
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	return &ManagerBridgeClient{conversation: conversation, session: session, credential: credential, http: &http.Client{Transport: transport, Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func validateManagerSocket(path string) error {
	d, err := openVoiceDir(filepath.Dir(path), false)
	if err != nil {
		return errors.New("voice: manager socket directory must be private")
	}
	defer func() { _ = d.Close() }()
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("voice: manager socket unavailable")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		return errors.New("voice: manager socket must be private and owned by you")
	}
	return nil
}

func (c *ManagerBridgeClient) Close() { c.http.CloseIdleConnections() }

func (c *ManagerBridgeClient) Call(ctx context.Context, call ManagerToolCall) (json.RawMessage, error) {
	raw, err := json.Marshal(ManagerBridgeRequest{Version: 1, ConversationID: c.conversation, SessionID: c.session, Call: call})
	if err != nil || len(raw) > MaxControlFrameBytes || jsontext.ValidateUnicode(raw) != nil {
		return nil, errManagerPayloadTooLarge
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://voice/manager/call", bytes.NewReader(raw))
	if err != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("Authorization", "Bearer "+c.credential)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("voice: manager bridge unavailable; reconcile the original action before retrying")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("voice: manager bridge refused the request; reconcile its binding")
	}
	raw, err = io.ReadAll(io.LimitReader(res.Body, MaxControlFrameBytes+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(raw) > MaxControlFrameBytes || jsontext.ValidateUnicode(raw) != nil {
		return nil, errors.New("voice: manager bridge response unavailable or oversized")
	}
	var reply ManagerBridgeReply
	if validateObjectMembers(raw, "data", "error") != nil {
		return nil, errors.New("voice: invalid manager bridge response")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&reply) != nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("voice: invalid manager bridge response")
	}
	if reply.Error != "" {
		switch reply.Error {
		case "invalid":
			return nil, ErrInvalid
		case "conflict":
			return nil, ErrConflict
		case "stale":
			return nil, ErrStale
		case "expired":
			return nil, ErrExpired
		case "capacity":
			return nil, ErrCapacity
		case "not_found":
			return nil, ErrNotFound
		default:
			return nil, errors.New("voice: manager action is unconfirmed; reconcile before retrying")
		}
	}
	data, err := managerproof.CanonicalJSON(reply.Data)
	if err != nil {
		return nil, errors.New("voice: missing manager bridge result")
	}
	if len(data) > MaxUtteranceBytes {
		return nil, errManagerPayloadTooLarge
	}
	return data, nil
}
