package shim

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
)

const AccountInputEmbargoFile = "account-input-embargo.json"

var errAccountSwitching = errors.New("account_switching")

type accountEmbargo struct {
	mu        sync.Mutex
	root      *os.Root
	id, token string
	file      string
	refusal   error
	held      atomic.Bool
}
type accountEmbargoRecord struct {
	SchemaVersion int    `json:"schema_version"`
	IncidentID    string `json:"incident_id"`
	Held          bool   `json:"held"`
}

func openInputEmbargo(cfg Config) (*accountEmbargo, error) {
	id, token, file, refusal := cfg.InputEmbargo, cfg.InputEmbargoToken, AccountInputEmbargoFile, errAccountSwitching
	if cfg.OpenCode != nil {
		if err := ValidateOpenCodeConfig(cfg); err != nil {
			return nil, err
		}
		id, token, file, refusal = cfg.OpenCode.Generation, cfg.OpenCode.EmbargoToken, OpenCodeInputEmbargoFile, errOpenCodeStarting
	} else if cfg.InputEmbargo == "" {
		return nil, nil
	} else if cfg.AccountBinding == nil || len(cfg.InputEmbargo) > 128 || strings.ContainsAny(cfg.InputEmbargo, "/\\\x00\r\n") || len(cfg.InputEmbargoToken) < 32 {
		return nil, refusal
	}
	root, err := os.OpenRoot(cfg.SessionDir)
	if err != nil {
		return nil, refusal
	}
	e := &accountEmbargo{root: root, id: id, token: token, file: file, refusal: refusal}
	f, err := root.OpenFile(file, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		e.held.Store(true)
		if err = e.save(true); err != nil {
			_ = root.Close()
			return nil, refusal
		}
		return e, nil
	}
	if err != nil {
		_ = root.Close()
		return nil, refusal
	}
	info, statErr := f.Stat()
	stat, ok := infoSys(info)
	if statErr != nil || !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || info.Size() > 4096 {
		_ = f.Close()
		_ = root.Close()
		return nil, refusal
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, 4097))
	_ = f.Close()
	var record accountEmbargoRecord
	if readErr != nil || json.Unmarshal(raw, &record) != nil || record.SchemaVersion != 1 || record.IncidentID != e.id {
		_ = root.Close()
		return nil, refusal
	}
	e.held.Store(record.Held)
	return e, nil
}

func infoSys(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}
func (e *accountEmbargo) close() {
	if e != nil {
		_ = e.root.Close()
	}
}
func (e *accountEmbargo) save(held bool) error {
	return writeAccountRecord(e.root, e.file, accountEmbargoRecord{SchemaVersion: 1, IncidentID: e.id, Held: held})
}
func (e *accountEmbargo) release(id, token string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if id != e.id || subtle.ConstantTimeCompare([]byte(token), []byte(e.token)) != 1 {
		return e.refusal
	}
	if !e.held.Load() {
		return nil
	}
	if err := e.save(false); err != nil {
		return e.refusal
	}
	e.held.Store(false)
	return nil
}
func (p *ptyWriter) WriteControl(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.embargo != nil && p.embargo.held.Load() {
		return 0, p.embargo.refusal
	}
	return p.writeLocked(b)
}
