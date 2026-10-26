package discord

import (
	"bytes"
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
	"strconv"
	"syscall"
	"time"
)

type RequestHeader struct {
	Ref        string    `json:"ref"`
	Author     Actor     `json:"author"`
	EndorserID string    `json:"endorser_id,omitempty"`
	Authority  Authority `json:"authority"`
	Route      Route     `json:"route"`
	Target     Target    `json:"target"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}
type LocalContext struct {
	Request RequestHeader `json:"request"`
	Page    ContextPage   `json:"page"`
}
type LocalContextRequest struct {
	Ref    string `json:"ref"`
	Offset int    `json:"offset"`
}
type LocalWorker struct {
	Worker
	Excluded bool `json:"excluded"`
}
type LocalStatus struct {
	Name           string        `json:"name"`
	InstallationID string        `json:"installation_id"`
	Connected      bool          `json:"connected"`
	Workers        []LocalWorker `json:"workers"`
}
type LocalReply struct {
	Ref  string `json:"ref"`
	ID   string `json:"id"`
	Body string `json:"body"`
}
type LocalTarget struct {
	TargetID string `json:"target_id"`
}
type LocalProposal struct {
	Ref          string   `json:"ref"`
	CandidateIDs []string `json:"candidate_ids"`
}

const localRequestLimit = MaxPublicationBytes*6 + 4096

func localMethod(operation string) string {
	switch operation {
	case "context", "status", "roster":
		return http.MethodGet
	case "reply", "include", "exclude", "propose", "shutdown":
		return http.MethodPost
	}
	return ""
}

func CallLocal(ctx context.Context, stateDir, operation string, request, response any) error {
	method := localMethod(operation)
	if method == "" {
		return ErrInvalid
	}
	endpoint := "http://local/" + operation
	var body []byte
	var err error
	if operation == "context" {
		raw, e := json.Marshal(request)
		if e != nil {
			return e
		}
		var r LocalContextRequest
		if e = json.Unmarshal(raw, &r); e != nil {
			return e
		}
		q := url.Values{"ref": {r.Ref}, "offset": {strconv.Itoa(r.Offset)}}
		endpoint += "?" + q.Encode()
	} else if method == http.MethodPost {
		body, err = json.Marshal(request)
		if err != nil {
			return err
		}
		if len(body) > localRequestLimit {
			return ErrCapacity
		}
	}
	path := filepath.Join(stateDir, "bridge.sock")
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || stat.Nlink != 1 || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm() != 0600 {
		return errors.New("discord: unsafe local socket")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Transport: transport, Timeout: 35 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20+1))
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return ErrCapacity
	}
	if res.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &failure) != nil || failure.Error == "" {
			return errors.New("discord: local request failed")
		}
		return errors.New(failure.Error)
	}
	if response == nil {
		return nil
	}
	return json.Unmarshal(data, response)
}

func (s *Service) localStatus(ctx context.Context) (LocalStatus, error) {
	status := LocalStatus{Name: s.Config.Name, InstallationID: s.Config.InstallationID, Connected: true}
	rows, err := s.Workers.Roster(ctx)
	if err != nil {
		return status, err
	}
	if len(rows) > MaxRequests {
		return status, ErrCapacity
	}
	for _, row := range rows {
		excluded, e := s.Store.IsOptedOut(row.Target)
		if e != nil {
			return status, e
		}
		status.Workers = append(status.Workers, LocalWorker{Worker: row, Excluded: excluded})
	}
	return status, nil
}
func decodeLocal(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, localRequestLimit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if e := dec.Decode(out); e != nil {
		return ErrInvalid
	}
	if e := dec.Decode(&struct{}{}); e != io.EOF {
		return ErrInvalid
	}
	return nil
}
func (s *Service) localHandler(w http.ResponseWriter, r *http.Request) {
	operation := r.URL.Path[1:]
	method := localMethod(operation)
	w.Header().Set("Content-Type", "application/json")
	if method == "" || r.Method != method {
		http.Error(w, `{"error":"discord: unknown operation or method"}`, http.StatusMethodNotAllowed)
		return
	}
	var result any
	var err error
	switch operation {
	case "shutdown":
		if err = decodeLocal(w, r, &struct{}{}); err != nil {
			break
		}
		if s.Stop == nil {
			err = errors.New("discord: connector shutdown unavailable")
			break
		}
		payload := []byte("{\"stopped\":true}\n")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		if _, err = w.Write(payload); err == nil {
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			s.Stop()
		}
		return
	case "status", "roster":
		result, err = s.localStatus(r.Context())
	case "context":
		var offset int
		offset, err = strconv.Atoi(r.URL.Query().Get("offset"))
		if err != nil {
			err = ErrInvalid
			break
		}
		var request Request
		request, err = s.Store.Request(r.URL.Query().Get("ref"), s.Now())
		if err != nil {
			break
		}
		var page ContextPage
		page, err = s.Context(request.Ref, offset)
		if err != nil {
			break
		}
		result = LocalContext{Request: RequestHeader{Ref: request.Ref, Author: request.Author, EndorserID: request.EndorserID, Authority: request.Authority, Route: request.Route, Target: request.Target, CreatedAt: request.CreatedAt, ExpiresAt: request.ExpiresAt}, Page: page}
	case "reply":
		var input LocalReply
		if err = decodeLocal(w, r, &input); err == nil {
			result, err = s.Reply(r.Context(), input.Ref, input.ID, input.Body)
		}
	case "exclude", "include":
		var input LocalTarget
		if err = decodeLocal(w, r, &input); err != nil {
			break
		}
		var rows []Worker
		rows, err = s.Workers.Roster(r.Context())
		if err != nil {
			break
		}
		found := false
		for _, row := range rows {
			if row.ID == input.TargetID {
				if found {
					err = ErrConflict
					break
				}
				found = true
				result = row
			}
		}
		if err == nil && !found {
			err = ErrNotFound
		}
		if err == nil {
			err = s.Store.SetOptOut(result.(Worker).Target, operation == "exclude")
		}
		if err == nil {
			result, err = s.localStatus(r.Context())
		}
	case "propose":
		var input LocalProposal
		if err = decodeLocal(w, r, &input); err == nil {
			err = s.Propose(r.Context(), input.Ref, input.CandidateIDs)
			result = struct {
				Proposed bool `json:"proposed"`
			}{err == nil}
		}
	}
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(struct {
			Error string `json:"error"`
		}{err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

// ServeLocal exposes the connector only through an owner-private Unix socket.
func (s *Service) ServeLocal(ctx context.Context, stateDir string) error {
	if err := privateDir(stateDir); err != nil {
		return err
	}
	lock, err := openStoreFile(filepath.Join(stateDir, "bridge.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("discord: local service already running")
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	path := filepath.Join(stateDir, "bridge.sock")
	if info, e := os.Lstat(path); e == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode()&os.ModeSocket == 0 || stat.Nlink != 1 || stat.Uid != uint32(os.Getuid()) {
			return errors.New("discord: unsafe existing local socket")
		}
		conn, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return errors.New("discord: local socket already active")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return dialErr
		}
		current, e := os.Lstat(path)
		if e != nil || !os.SameFile(info, current) {
			return errors.New("discord: local socket changed")
		}
		if e = os.Remove(path); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	listener.SetUnlinkOnClose(false)
	identity, err := os.Lstat(path)
	if err != nil {
		return err
	}
	defer func() {
		if current, e := os.Lstat(path); e == nil && os.SameFile(identity, current) {
			_ = os.Remove(path)
		}
	}()
	if err = os.Chmod(path, 0600); err != nil {
		return err
	}
	server := &http.Server{Handler: http.HandlerFunc(s.localHandler), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 35 * time.Second, MaxHeaderBytes: 8192, BaseContext: func(net.Listener) context.Context { return ctx }}
	defer func() { _ = server.Close() }()
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = server.Close()
		case <-stopped:
		}
	}()
	err = server.Serve(listener)
	close(stopped)
	if errors.Is(err, http.ErrServerClosed) || ctx.Err() != nil {
		return nil
	}
	return fmt.Errorf("discord local service: %w", err)
}
