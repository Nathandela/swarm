package opencodebridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const MaxResponseBytes = 2 << 20

// Binding contains no provider credentials. Token authenticates only this private bridge.
type Binding struct {
	Version           int    `json:"version"`
	WorkerID          string `json:"workerID"`
	SessionInstance   string `json:"sessionInstance"`
	RootID            string `json:"rootID"`
	Directory         string `json:"directory"`
	BackendGeneration string `json:"backendGeneration"`
	SocketPath        string `json:"socketPath"`
	Token             string `json:"token"`
	Mode              string `json:"mode"`
	NativeCreatedAtMS int64  `json:"nativeCreatedAtMS"`
	InitialPrompt     bool   `json:"initialPrompt"`
}

type Identity struct {
	Version           int    `json:"version"`
	WorkerID          string `json:"workerID"`
	SessionInstance   string `json:"sessionInstance"`
	RootID            string `json:"rootID"`
	Directory         string `json:"directory"`
	BackendGeneration string `json:"backendGeneration"`
	NativeCreatedAtMS int64  `json:"nativeCreatedAtMS"`
}

type Ready struct {
	Identity
	NativeVersion       string          `json:"nativeVersion"`
	Bound               bool            `json:"bound"`
	Acknowledged        bool            `json:"acknowledged"`
	Ready               bool            `json:"ready"`
	InitialSupported    bool            `json:"initialSupported"`
	DraftGuardSupported bool            `json:"draftGuardSupported"`
	InFlight            bool            `json:"inFlight"`
	Degraded            []string        `json:"degraded"`
	Route               json.RawMessage `json:"route"`
}

type AckRequest struct {
	Version           int    `json:"version"`
	SessionInstance   string `json:"sessionInstance"`
	BackendGeneration string `json:"backendGeneration"`
	RootID            string `json:"rootID"`
	NativeCreatedAtMS int64  `json:"nativeCreatedAtMS"`
}

type InitialRequest struct {
	ExpectedModel     string `json:"expectedModel,omitempty"`
	ExpectedAgent     string `json:"expectedAgent,omitempty"`
	Version           int    `json:"version"`
	SessionInstance   string `json:"sessionInstance"`
	BackendGeneration string `json:"backendGeneration"`
	RootID            string `json:"rootID"`
	OperationID       string `json:"operationID"`
	RequestHash       string `json:"requestHash"`
	Text              string `json:"text"`
}

type DispatchRequest struct {
	ExpectedRequest    json.RawMessage `json:"expectedRequest,omitempty"`
	RequestExpiresAtMS int64           `json:"requestExpiresAtMS,omitempty"`
	Version            int             `json:"version"`
	SessionInstance    string          `json:"sessionInstance"`
	BackendGeneration  string          `json:"backendGeneration"`
	RootID             string          `json:"rootID"`
	OperationID        string          `json:"operationID"`
	Kind               string          `json:"kind"`
	TargetID           string          `json:"targetID"`
	MessageID          string          `json:"messageID,omitempty"`
	Text               string          `json:"text,omitempty"`
	RequestID          string          `json:"requestID,omitempty"`
	Reply              string          `json:"reply,omitempty"`
	Answers            [][]string      `json:"answers,omitempty"`
}

type Model struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

type Receipt struct {
	Identity
	Kind               string `json:"kind"`
	RequestID          string `json:"requestID"`
	TargetID           string `json:"targetID"`
	Status             string `json:"status"`
	Reason             string `json:"reason"`
	OperationID        string `json:"operationID"`
	RequestHash        string `json:"requestHash"`
	MessageID          string `json:"messageID"`
	Agent              string `json:"agent"`
	Model              *Model `json:"model,omitempty"`
	Variant            string `json:"variant,omitempty"`
	SelectionBasis     string `json:"selectionBasis"`
	SelectionMessageID string `json:"selectionMessageID"`
	NativeStatus       int    `json:"nativeStatus"`
	NativeEffect       string `json:"nativeEffect"`
	InFlight           bool   `json:"inFlight"`
}

type Snapshot struct {
	Identity
	Session           json.RawMessage            `json:"session"`
	Statuses          map[string]json.RawMessage `json:"statuses"`
	Permissions       []json.RawMessage          `json:"permissions"`
	Questions         []json.RawMessage          `json:"questions"`
	Messages          []json.RawMessage          `json:"messages"`
	NextCursor        string                     `json:"nextCursor"`
	Children          []json.RawMessage          `json:"children"`
	Descendants       []json.RawMessage          `json:"descendants"`
	HierarchyComplete bool                       `json:"hierarchyComplete"`
	HierarchyScope    string                     `json:"hierarchyScope"`
}

type Event struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

type Error struct {
	Code   int
	Reason string
}

func (e *Error) Error() string { return fmt.Sprintf("opencode bridge: %s (HTTP %d)", e.Reason, e.Code) }

type Client struct {
	http      *http.Client
	transport *http.Transport
	token     string
	ctx       context.Context
	cancel    context.CancelFunc
}

func NewClient(socketPath, token string) (*Client, error) {
	if !filepath.IsAbs(socketPath) || len(socketPath) > 100 || strings.ContainsAny(socketPath, "\x00\r\n") || token == "" || len(token) > 256 || strings.ContainsAny(token, "\x00\r\n") {
		return nil, errors.New("opencode bridge: invalid private channel")
	}
	ctx, cancel := context.WithCancel(context.Background())
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, "unix", socketPath)
	}, ResponseHeaderTimeout: 20 * time.Second, MaxResponseHeaderBytes: 32 << 10, MaxIdleConns: 4, MaxIdleConnsPerHost: 4}
	return &Client{http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, transport: transport, token: token, ctx: ctx, cancel: cancel}, nil
}

func (c *Client) Close() { c.cancel(); c.transport.CloseIdleConnections() }
func (c *Client) Ready(ctx context.Context) (Ready, error) {
	var out Ready
	err := c.request(ctx, "GET", "/ready", nil, &out)
	return out, err
}
func (c *Client) Ack(ctx context.Context, in AckRequest) (Ready, error) {
	var out Ready
	err := c.request(ctx, "POST", "/ack", in, &out)
	return out, err
}
func (c *Client) Initial(ctx context.Context, in InitialRequest) (Receipt, error) {
	var out Receipt
	err := c.request(ctx, "POST", "/initial", in, &out)
	return out, err
}
func (c *Client) InitialReceipt(ctx context.Context) (*Receipt, error) {
	var out *Receipt
	err := c.request(ctx, "GET", "/initial", nil, &out)
	return out, err
}
func (c *Client) Dispatch(ctx context.Context, in DispatchRequest) (Receipt, error) {
	var out Receipt
	err := c.request(ctx, "POST", "/dispatch", in, &out)
	return out, err
}

// Message reads one native row from the bound root. Only Error.Code==404 with
// Reason=="native_message_missing" establishes native message absence.
func (c *Client) Message(ctx context.Context, messageID string) (json.RawMessage, error) {
	return c.MessageForSession(ctx, "", messageID)
}

// MessageForSession reads an exact root or descendant row after the bridge proves
// the target's current ancestry. Empty sessionID selects the bound root.
func (c *Client) MessageForSession(ctx context.Context, sessionID, messageID string) (json.RawMessage, error) {
	var out json.RawMessage
	if sessionID != "" && (!strings.HasPrefix(sessionID, "ses_") || len(sessionID) < 14 || len(sessionID) > 128 || strings.Trim(sessionID[4:], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") != "") {
		return nil, errors.New("opencode bridge: invalid native session ID")
	}
	if !strings.HasPrefix(messageID, "msg_") || len(messageID) < 14 || len(messageID) > 128 || strings.Trim(messageID[4:], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") != "" {
		return nil, errors.New("opencode bridge: invalid native message ID")
	}
	route := "/message?messageID=" + url.QueryEscape(messageID)
	if sessionID != "" {
		route += "&sessionID=" + url.QueryEscape(sessionID)
	}
	err := c.request(ctx, "GET", route, nil, &out)
	return out, err
}

// OperationReceipt never submits. A missing live receipt does not authorize replay.
func (c *Client) OperationReceipt(ctx context.Context, operationID string) (*Receipt, error) {
	var out *Receipt
	if operationID == "" || len(operationID) > 128 || strings.ContainsAny(operationID, "\x00\r\n") {
		return nil, errors.New("opencode bridge: invalid operation ID")
	}
	err := c.request(ctx, "GET", "/receipt?operationID="+url.QueryEscape(operationID), nil, &out)
	return out, err
}
func (c *Client) Snapshot(ctx context.Context, limit int, before string) (Snapshot, error) {
	var out Snapshot
	if limit < 0 || limit > 200 || len(before) > 4096 || strings.ContainsAny(before, "\x00\r\n") {
		return out, errors.New("opencode bridge: invalid history page")
	}
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if before != "" {
		query.Set("before", before)
	}
	err := c.request(ctx, "GET", "/snapshot?"+query.Encode(), nil, &out)
	return out, err
}

func (c *Client) open(ctx context.Context, method, path string, input any) (*http.Response, func(), error) {
	requestCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	release := func() { stop(); cancel() }
	if c.ctx.Err() != nil {
		release()
		return nil, nil, errors.New("opencode bridge: closed")
	}
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil || len(raw) > 64<<10 {
			release()
			return nil, nil, errors.New("opencode bridge: invalid request")
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(requestCtx, method, "http://swarm-bridge"+path, body)
	if err != nil {
		release()
		return nil, nil, errors.New("opencode bridge: invalid request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		release()
		return nil, nil, errors.New("opencode bridge: exchange failed")
	}
	return response, func() { _ = response.Body.Close(); release() }, nil
}

func (c *Client) request(ctx context.Context, method, path string, input, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	response, release, err := c.open(ctx, method, path, input)
	if err != nil {
		return err
	}
	defer release()
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil || len(raw) > MaxResponseBytes {
		return errors.New("opencode bridge: unreadable or oversized response")
	}
	if response.StatusCode >= 300 {
		var failure struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(raw, &failure)
		reason := "request_refused"
		if len(failure.Reason) <= 64 && strings.Trim(failure.Reason, "abcdefghijklmnopqrstuvwxyz_") == "" && failure.Reason != "" && !strings.Contains(failure.Reason, c.token) {
			reason = failure.Reason
		}
		return &Error{Code: response.StatusCode, Reason: reason}
	}
	if json.Unmarshal(raw, out) != nil {
		return errors.New("opencode bridge: malformed response")
	}
	return nil
}

// Events has no replay cursor. Stream loss requires a fresh native snapshot.
func (c *Client) Events(ctx context.Context, handler func(Event) error) error {
	if handler == nil {
		return errors.New("opencode bridge: missing event handler")
	}
	response, release, err := c.open(ctx, "GET", "/events", nil)
	if err != nil {
		return err
	}
	defer release()
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return errors.New("opencode bridge: event stream refused")
	}
	scan := bufio.NewScanner(response.Body)
	scan.Buffer(make([]byte, 4096), (1<<20)+16)
	for scan.Scan() {
		line := scan.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		var event Event
		if json.Unmarshal(line[6:], &event) != nil || event.Type == "" {
			return errors.New("opencode bridge: malformed event")
		}
		if err := handler(event); err != nil {
			return err
		}
	}
	return errors.New("opencode bridge: event stream closed")
}
