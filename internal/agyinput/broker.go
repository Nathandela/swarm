// Package agyinput admits messages through agy's provider-owned agentapi, never terminal keys.
package agyinput

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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
)

const DescriptorFile = "agy-input.json"

var ErrRejected = errors.New("agy native input rejected without a new effect")

func SupportedVersion(version string) bool { return version == "1.4.0" || version == "1.4.7" }

type Config struct {
	SessionID        string   `json:"session_id"`
	Executable       string   `json:"executable"`
	Version          string   `json:"version"`
	Fingerprint      string   `json:"fingerprint"`
	Cwd              string   `json:"cwd"`
	BootstrapCommand string   `json:"bootstrap_command"`
	Env              []string `json:"-"`
}
type Descriptor struct {
	Config
	SocketPath    string `json:"socket_path"`
	Token         string `json:"token"`
	Generation    string `json:"generation"`
	ShimPID       int    `json:"shim_pid"`
	ShimStartTime int64  `json:"shim_start_time"`
}
type Source struct {
	WorkingDirectory string `json:"working_directory,omitempty"`
	SwarmSessionID   string `json:"swarm_session_id"`
	ConversationID   string `json:"conversation_id"`
	ProjectID        string `json:"project_id"`
	TrajectoryID     string `json:"trajectory_id"`
	Address          string `json:"address"`
	CSRFToken        string `json:"csrf_token"`
	Executable       string `json:"executable"`
	SourceMetadata   string `json:"source_metadata"`
}
type Proof struct {
	ConversationID string `json:"conversation_id"`
	ProjectID      string `json:"project_id"`
	Generation     string `json:"generation"`
}
type Receipt struct {
	OperationID string `json:"operation_id"`
	Hash        string `json:"hash"`
	State       string `json:"state"`
	Proof       Proof  `json:"proof"`
	Reason      string `json:"reason,omitempty"`
}
type request struct {
	Generation  string  `json:"generation"`
	Source      *Source `json:"source,omitempty"`
	Proof       Proof   `json:"proof"`
	OperationID string  `json:"operation_id,omitempty"`
	Text        string  `json:"text,omitempty"`
}
type Broker struct {
	desc        Descriptor
	server      *http.Server
	mu          sync.Mutex
	source      *Source
	proof       Proof
	operations  map[string]Receipt
	order       []string
	run         func(context.Context, Source, ...string) ([]byte, error)
	fingerprint func(string) (string, error)
}

func Start(dir string, cfg Config, pid int, started int64) (*Broker, error) {
	if !SupportedVersion(cfg.Version) || cfg.SessionID == "" || !filepath.IsAbs(cfg.Executable) || !filepath.IsAbs(cfg.Cwd) {
		return nil, errors.New("agy native input runtime is unqualified")
	}
	var nonce [48]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	desc := Descriptor{Config: cfg, SocketPath: filepath.Join(dir, "agy-input.sock"), Token: hex.EncodeToString(nonce[16:]), Generation: hex.EncodeToString(nonce[:16]), ShimPID: pid, ShimStartTime: started}
	listener, err := net.Listen("unix", desc.SocketPath)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(desc.SocketPath, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	b := &Broker{desc: desc, operations: map[string]Receipt{}, fingerprint: persist.CLIFingerprint}
	b.run = func(ctx context.Context, source Source, args ...string) ([]byte, error) {
		return runNative(ctx, cfg, source, args...)
	}
	b.server = &http.Server{Handler: http.HandlerFunc(b.serve), ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 8 * time.Second, MaxHeaderBytes: 4096}
	raw, _ := json.Marshal(desc)
	if err = os.WriteFile(filepath.Join(dir, DescriptorFile), raw, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	go func() { _ = b.server.Serve(listener) }()
	return b, nil
}
func (b *Broker) Descriptor() Descriptor { return b.desc }
func (b *Broker) Close() error           { return b.server.Close() }

func (b *Broker) validate(ctx context.Context, source Source) error {
	cfg := b.desc.Config
	if source.SwarmSessionID != cfg.SessionID {
		return errors.New("source Swarm session does not match")
	}
	if source.Executable != cfg.Executable {
		return errors.New("source executable does not match")
	}
	if source.ConversationID == "" || len(source.ConversationID) > 128 || source.ProjectID == "" || len(source.ProjectID) > 128 || source.TrajectoryID == "" || len(source.TrajectoryID) > 128 || source.CSRFToken == "" || len(source.CSRFToken) > 1024 || len(source.SourceMetadata) > 16<<10 {
		return errors.New("native source identity is missing or out of bounds")
	}
	host, port, err := net.SplitHostPort(source.Address)
	if err != nil {
		return errors.New("native endpoint is invalid")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 || (host != "localhost" && !net.ParseIP(host).IsLoopback()) {
		return errors.New("native endpoint is not loopback")
	}
	var metadata struct {
		Tool struct {
			ConversationID string `json:"conversationId"`
			StepIndex      *int   `json:"stepIndex"`
			ToolCall       struct {
				Name          string `json:"name"`
				ArgumentsJSON string `json:"argumentsJson"`
			} `json:"toolCall"`
		} `json:"tool"`
	}
	if json.Unmarshal([]byte(source.SourceMetadata), &metadata) != nil || metadata.Tool.ConversationID != source.ConversationID || metadata.Tool.StepIndex == nil || *metadata.Tool.StepIndex < 0 || metadata.Tool.ToolCall.Name != "run_command" {
		return errors.New("registration was not invoked in its own native tool context")
	}
	var arguments struct {
		Cwd         string
		CommandLine string
	}
	if json.Unmarshal([]byte(metadata.Tool.ToolCall.ArgumentsJSON), &arguments) != nil || arguments.CommandLine != cfg.BootstrapCommand {
		return errors.New("native registration command or workspace mismatched")
	}
	switch cfg.Version {
	case "1.4.0":
		if arguments.Cwd != cfg.Cwd {
			return errors.New("native registration workspace mismatched")
		}
	case "1.4.7":
		expected, err := filepath.EvalSymlinks(cfg.Cwd)
		if err != nil {
			return errors.New("registered workspace is unavailable")
		}
		actual, err := filepath.EvalSymlinks(source.WorkingDirectory)
		if !filepath.IsAbs(source.WorkingDirectory) || err != nil || actual != expected {
			return errors.New("native helper working directory mismatched")
		}
	default:
		return errors.New("native source schema is unqualified")
	}
	stamp, err := b.fingerprint(cfg.Executable)
	if err != nil || stamp != cfg.Fingerprint {
		return errors.New("native executable changed")
	}
	version, err := b.run(ctx, source, "--version")
	if err != nil || strings.TrimSpace(string(version)) != cfg.Version {
		return errors.New("native executable version is unqualified")
	}
	return b.verifyMetadata(ctx, source)
}
func (b *Broker) verifyMetadata(ctx context.Context, source Source) error {
	raw, err := b.run(ctx, source, "agentapi", "get-conversation-metadata", source.ConversationID)
	if err != nil {
		return errors.New("native session metadata is unavailable")
	}
	var reply struct {
		Response struct {
			ConversationMetadata struct {
				Metadata struct {
					RootConversationID string `json:"rootConversationId"`
					ProjectID          string `json:"projectId"`
				} `json:"metadata"`
			} `json:"conversationMetadata"`
		} `json:"response"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &reply) != nil || len(reply.Error) > 0 || reply.Response.ConversationMetadata.Metadata.RootConversationID != source.ConversationID || reply.Response.ConversationMetadata.Metadata.ProjectID != source.ProjectID {
		return errors.New("native metadata does not prove the registered owner")
	}
	return nil
}
func (b *Broker) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+b.desc.Token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req request
	if json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req) != nil || req.Generation != b.desc.Generation {
		http.Error(w, "invalid generation", http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	respond := func(value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	refuse := func() {
		http.Error(w, "native input is unproven; register from its own native context or refresh this session", http.StatusConflict)
	}
	switch r.URL.Path {
	case "/register":
		if req.Source == nil {
			refuse()
			return
		}
		if err := b.validate(ctx, *req.Source); err != nil {
			http.Error(w, "native registration refused: "+err.Error(), http.StatusConflict)
			return
		}
		if b.source != nil && b.source.ConversationID != req.Source.ConversationID {
			refuse()
			return
		}
		source := *req.Source
		b.source = &source
		b.proof = Proof{ConversationID: source.ConversationID, ProjectID: source.ProjectID, Generation: b.desc.Generation}
		respond(b.proof)
	case "/ready":
		if b.source == nil || b.verifyMetadata(ctx, *b.source) != nil {
			refuse()
			return
		}
		respond(b.proof)
	case "/submit":
		if b.source == nil || req.Proof != b.proof || req.OperationID == "" || len(req.OperationID) > 128 || req.Text == "" || len(req.Text) > 4096 || strings.IndexByte(req.Text, 0) >= 0 {
			refuse()
			return
		}
		if stamp, err := b.fingerprint(b.desc.Executable); err != nil || stamp != b.desc.Fingerprint {
			refuse()
			return
		}
		digest := sha256.Sum256([]byte(req.Text))
		hash := hex.EncodeToString(digest[:])
		if prior, ok := b.operations[req.OperationID]; ok {
			if prior.Hash != hash {
				refuse()
				return
			}
			respond(prior)
			return
		}
		if len(b.operations) >= 128 {
			oldest := b.order[0]
			if b.operations[oldest].State != "submitted" {
				refuse()
				return
			}
			delete(b.operations, oldest)
			b.order = b.order[1:]
		}
		receipt := Receipt{OperationID: req.OperationID, Hash: hash, State: "uncertain", Proof: b.proof}
		b.operations[req.OperationID] = receipt
		b.order = append(b.order, req.OperationID)
		raw, err := b.run(ctx, *b.source, "agentapi", "send-message", b.proof.ConversationID, req.Text)
		var reply struct {
			Response struct {
				SendMessage struct {
					RecipientID string `json:"recipientId"`
					Content     string `json:"content"`
				} `json:"sendMessage"`
			} `json:"response"`
			Error json.RawMessage `json:"error"`
		}
		if err == nil && json.Unmarshal(raw, &reply) == nil && len(reply.Error) == 0 && reply.Response.SendMessage.RecipientID == b.proof.ConversationID && reply.Response.SendMessage.Content == req.Text {
			receipt.State = "submitted"
		} else {
			receipt.Reason = "native submission remains unconfirmed; no replay"
		}
		b.operations[req.OperationID] = receipt
		respond(receipt)
	case "/operation":
		receipt, ok := b.operations[req.OperationID]
		if !ok || req.Proof != receipt.Proof {
			refuse()
			return
		}
		respond(receipt)
	default:
		http.NotFound(w, r)
	}
}

type boundedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > 64<<10-b.Len() {
		b.overflow = true
		return 0, errors.New("native output exceeds its bound")
	}
	return b.Buffer.Write(p)
}
func runNative(ctx context.Context, cfg Config, source Source, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, cfg.Executable, args...)
	cmd.Dir = cfg.Cwd
	env := append([]string(nil), cfg.Env...)
	values := map[string]string{"ANTIGRAVITY_CONVERSATION_ID": source.ConversationID, "ANTIGRAVITY_PROJECT_ID": source.ProjectID, "ANTIGRAVITY_TRAJECTORY_ID": source.TrajectoryID, "ANTIGRAVITY_LS_ADDRESS": source.Address, "ANTIGRAVITY_CSRF_TOKEN": source.CSRFToken, "ANTIGRAVITY_SOURCE_METADATA": source.SourceMetadata, "ANTIGRAVITY_AGENTAPI_EXE": source.Executable}
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := values[key]; !ok {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	for key, value := range values {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output := &boundedOutput{}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if err != nil || output.overflow {
		return nil, errors.New("native provider command failed or its reply is unconfirmed")
	}
	return output.Bytes(), nil
}

// LaunchArgs adds only the registration request, preserving the owner's exact prompt suffix.
func LaunchArgs(argv []string, command string) []string {
	out := append([]string(nil), argv...)
	prefix := "Before handling the request below, register this managed session's native message input once by running this exact command in your current workspace, using normal tool permissions: " + command + "\nThis registration grants no tool approvals. If it is declined, continue the owner's work and report that native messaging remains unavailable.\n\nOwner request:\n"
	for i, arg := range out {
		if (arg == "--prompt-interactive" || arg == "-i") && i+1 < len(out) {
			out[i+1] = prefix + out[i+1]
			return out
		}
	}
	return append(out, "--prompt-interactive", prefix+"No additional request; finish after registration.")
}

func ReadDescriptor(dir string) (Descriptor, error) {
	var desc Descriptor
	raw, err := os.ReadFile(filepath.Join(dir, DescriptorFile))
	if err != nil {
		return desc, err
	}
	if len(raw) > 16<<10 || json.Unmarshal(raw, &desc) != nil || !SupportedVersion(desc.Version) || desc.SocketPath != filepath.Join(dir, "agy-input.sock") || len(desc.Token) != 64 || len(desc.Generation) != 32 || desc.ShimPID <= 0 || desc.ShimStartTime <= 0 {
		return desc, os.ErrInvalid
	}
	return desc, nil
}

type Client struct {
	desc Descriptor
	http *http.Client
}

func NewClient(desc Descriptor) *Client {
	return &Client{desc: desc, http: &http.Client{Timeout: 7 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", desc.SocketPath)
	}}}}
}
func (c *Client) call(ctx context.Context, path string, req request, out any) error {
	req.Generation = c.desc.Generation
	raw, _ := json.Marshal(req)
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost"+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+c.desc.Token)
	response, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		if response.StatusCode == http.StatusConflict {
			return fmt.Errorf("%w: %s", ErrRejected, strings.TrimSpace(string(raw)))
		}
		return fmt.Errorf("agy native input refused (%d): %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	return json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(out)
}
func (c *Client) Register(ctx context.Context, source Source) error {
	var out Proof
	return c.call(ctx, "/register", request{Source: &source}, &out)
}
func (c *Client) Ready(ctx context.Context) (Proof, error) {
	var out Proof
	err := c.call(ctx, "/ready", request{}, &out)
	return out, err
}
func (c *Client) Submit(ctx context.Context, proof Proof, id, text string) (Receipt, error) {
	var out Receipt
	err := c.call(ctx, "/submit", request{Proof: proof, OperationID: id, Text: text}, &out)
	return out, err
}
func (c *Client) Operation(ctx context.Context, proof Proof, id string) (Receipt, error) {
	var out Receipt
	err := c.call(ctx, "/operation", request{Proof: proof, OperationID: id}, &out)
	return out, err
}
func SourceFromEnv(getenv func(string) string) Source {
	return Source{SwarmSessionID: getenv("SWARM_SESSION_ID"), ConversationID: getenv("ANTIGRAVITY_CONVERSATION_ID"), ProjectID: getenv("ANTIGRAVITY_PROJECT_ID"), TrajectoryID: getenv("ANTIGRAVITY_TRAJECTORY_ID"), Address: getenv("ANTIGRAVITY_LS_ADDRESS"), CSRFToken: getenv("ANTIGRAVITY_CSRF_TOKEN"), Executable: getenv("ANTIGRAVITY_AGENTAPI_EXE"), SourceMetadata: getenv("ANTIGRAVITY_SOURCE_METADATA")}
}

func BootstrapCommand(binary, descriptor string) string {
	quote := func(word string) string { return "'" + strings.ReplaceAll(word, "'", "'\\''") + "'" }
	return quote(binary) + " native-input register-agy " + quote(descriptor)
}
