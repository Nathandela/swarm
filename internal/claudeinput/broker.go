// Package claudeinput delivers text through Claude's own prompt API, never terminal keys.
package claudeinput

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const DescriptorFile = "claude-input.json"
const maxOperations = 128

// SupportedVersion names actual native runtime qualification, not an optimistic range.
func SupportedVersion(version string) bool {
	return version == "2.1.296" || version == "2.1.310" || version == "2.1.311"
}

type Descriptor struct {
	SessionID       string `json:"session_id"`
	Generation      string `json:"generation"`
	Token           string `json:"token"`
	SocketPath      string `json:"socket_path"`
	ProviderVersion string `json:"provider_version"`
	ShimPID         int    `json:"shim_pid"`
	ShimStartTime   int64  `json:"shim_start_time"`
}

type Worker struct {
	NativeSessionID string `json:"native_session_id"`
	Generation      string `json:"generation"`
	Version         string `json:"version"`
}

type Message struct {
	OperationID string `json:"operation_id"`
	Text        string `json:"text,omitempty"`
	Hash        string `json:"hash"`
	State       string `json:"state"`
	Reason      string `json:"reason,omitempty"`
	Worker      Worker `json:"worker"`
}

type request struct {
	Generation  string `json:"generation"`
	Worker      Worker `json:"worker"`
	OperationID string `json:"operation_id,omitempty"`
	Text        string `json:"text,omitempty"`
	State       string `json:"state,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type Broker struct {
	descriptor Descriptor
	server     *http.Server
	mu         sync.Mutex
	worker     Worker
	seen       time.Time
	revoked    bool
	operations map[string]Message
	order      []string
}

func Start(dir, session, version string, pid int, started int64) (*Broker, error) {
	if !SupportedVersion(version) || session == "" {
		return nil, errors.New("claude native input runtime is not qualified")
	}
	var nonce [48]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	desc := Descriptor{SessionID: session, Generation: hex.EncodeToString(nonce[:16]), Token: hex.EncodeToString(nonce[16:]), SocketPath: filepath.Join(dir, "claude-input.sock"), ProviderVersion: version, ShimPID: pid, ShimStartTime: started}
	listener, err := net.Listen("unix", desc.SocketPath)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(desc.SocketPath, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	b := &Broker{descriptor: desc, operations: map[string]Message{}}
	b.server = &http.Server{Handler: http.HandlerFunc(b.serve), ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, MaxHeaderBytes: 4096}
	raw, err := json.Marshal(desc)
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, DescriptorFile), raw, 0o600)
	}
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	go func() { _ = b.server.Serve(listener) }()
	return b, nil
}
func (b *Broker) Descriptor() Descriptor { return b.descriptor }
func (b *Broker) Close() error           { return b.server.Close() }

func (b *Broker) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+b.descriptor.Token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req request
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil || req.Generation != b.descriptor.Generation {
		http.Error(w, "invalid generation", http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	respond := func(value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	refuse := func(reason string) { http.Error(w, reason, http.StatusConflict) }
	live := !b.revoked && b.worker.Generation != "" && time.Since(b.seen) < 3*time.Second
	switch r.URL.Path {
	case "/exchange", "/revoke":
		worker := req.Worker
		if worker.NativeSessionID == "" || len(worker.NativeSessionID) > 128 || worker.Generation == "" || len(worker.Generation) > 128 || worker.Version != b.descriptor.ProviderVersion || !SupportedVersion(worker.Version) {
			refuse("unqualified native worker")
			return
		}
		if b.worker.Generation != "" && b.worker != worker {
			refuse("native worker changed; refresh the session")
			return
		}
		if r.URL.Path == "/revoke" {
			b.worker = worker
			b.revoked = true
			for id, msg := range b.operations {
				switch msg.State {
				case "prepared":
					msg.State = "rejected"
				case "executing", "submitted":
					msg.State = "uncertain"
				default:
					continue
				}
				msg.Text = ""
				msg.Reason = "native session ended; explicitly refresh this session"
				b.operations[id] = msg
			}
			respond(nil)
			return
		}
		if b.revoked && req.OperationID == "" {
			refuse("native session ended; explicitly refresh this session")
			return
		}
		b.worker = worker
		b.seen = time.Now()
		if req.OperationID != "" {
			msg, ok := b.operations[req.OperationID]
			if !ok || msg.Worker != worker || req.State != "submitted" && req.State != "started" && req.State != "rejected" && req.State != "uncertain" || len(req.Reason) > 512 {
				refuse("invalid native receipt")
				return
			}
			// A transport retry cannot move a settled native outcome backwards.
			if msg.State != "started" && msg.State != "rejected" {
				msg.State = req.State
				msg.Text = ""
				msg.Reason = req.Reason
				b.operations[req.OperationID] = msg
			}
		}
		if req.OperationID != "" {
			respond(nil)
			return
		}
		for _, id := range b.order {
			msg := b.operations[id]
			if msg.State == "prepared" {
				msg.State = "executing"
				b.operations[id] = msg
				respond(msg)
				return
			}
		}
		respond(nil)
	case "/ready":
		if !live {
			refuse("native input is unproven; refresh the session")
			return
		}
		respond(b.worker)
	case "/submit":
		if !live || req.Worker != b.worker {
			refuse("native input proof is stale")
			return
		}
		if req.OperationID == "" || len(req.OperationID) > 128 || req.Text == "" || len(req.Text) > 4096 {
			refuse("invalid native input")
			return
		}
		hash := sha256.Sum256([]byte(req.Text))
		digest := hex.EncodeToString(hash[:])
		if msg, ok := b.operations[req.OperationID]; ok {
			if msg.Hash != digest || msg.Worker != req.Worker {
				refuse("native input identity conflict")
				return
			}
			respond(msg)
			return
		}
		// Bound live shim metadata; completed receipts are already in daemon custody.
		if len(b.operations) >= maxOperations {
			oldest := ""
			for _, id := range b.order {
				if m := b.operations[id]; m.State == "started" || m.State == "rejected" {
					oldest = id
					break
				}
			}
			if oldest == "" {
				refuse("native input receipt capacity reached")
				return
			}
			delete(b.operations, oldest)
			for i, id := range b.order {
				if id == oldest {
					b.order = append(b.order[:i:i], b.order[i+1:]...)
					break
				}
			}
		}
		msg := Message{OperationID: req.OperationID, Text: req.Text, Hash: digest, State: "prepared", Worker: b.worker}
		b.operations[req.OperationID] = msg
		b.order = append(b.order, req.OperationID)
		respond(msg)
	case "/operation":
		msg, ok := b.operations[req.OperationID]
		if !ok || msg.Worker != req.Worker {
			refuse("native operation unavailable")
			return
		}
		msg.Text = ""
		respond(msg)
	default:
		http.NotFound(w, r)
	}
}

// Client is bound to one shim's private descriptor and exact native worker proof.
type Client struct {
	descriptor Descriptor
	http       *http.Client
}

func NewClient(desc Descriptor) *Client {
	return &Client{descriptor: desc, http: &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", desc.SocketPath)
	}}}}
}
func (c *Client) call(ctx context.Context, path string, req request, out any) error {
	req.Generation = c.descriptor.Generation
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost"+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+c.descriptor.Token)
	r.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("claude native input refused (%d)", response.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(out)
}
func (c *Client) Ready(ctx context.Context) (Worker, error) {
	var out Worker
	err := c.call(ctx, "/ready", request{}, &out)
	return out, err
}
func (c *Client) Exchange(ctx context.Context, worker Worker, id, state, reason string) (*Message, error) {
	var out *Message
	err := c.call(ctx, "/exchange", request{Worker: worker, OperationID: id, State: state, Reason: reason}, &out)
	return out, err
}
func (c *Client) Submit(ctx context.Context, worker Worker, id, text string) (Message, error) {
	var out Message
	err := c.call(ctx, "/submit", request{Worker: worker, OperationID: id, Text: text}, &out)
	return out, err
}
func (c *Client) Operation(ctx context.Context, worker Worker, id string) (Message, error) {
	var out Message
	err := c.call(ctx, "/operation", request{Worker: worker, OperationID: id}, &out)
	return out, err
}

// Revoke permanently retires the shim's native binding after a native session ends.
func (c *Client) Revoke(ctx context.Context, worker Worker) error {
	return c.call(ctx, "/revoke", request{Worker: worker}, nil)
}
