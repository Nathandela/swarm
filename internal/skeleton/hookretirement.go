package skeleton

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/shim"
)

type hookCursorCertificate struct {
	Incarnation string `json:"incarnation"`
	Producer    string `json:"producer"`
	Sequence    uint64 `json:"sequence"`
}

func readHookRetirementSidecar(path string) ([]byte, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, shim.ErrHookRetirementUnproven
		}
	} else {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, shim.ErrHookRetirementUnproven
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
		return nil, shim.ErrHookRetirementUnproven
	}
	data, err := io.ReadAll(io.LimitReader(f, 2049))
	if len(data) > 2048 {
		return nil, shim.ErrHookRetirementUnproven
	}
	return data, err
}

func readHookRetirementCursor(path string) (uint64, error) {
	data, err := readHookRetirementSidecar(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

var persistHookCursorCertificate = func(path string, data []byte) error { return persistHookBytes(path+".cert", data) }

// A pending certificate comes only from a cursor this drainer just committed.
// Reading an old decimal cursor can never manufacture retirement authority.
func (hd *HookDrainer) flushCursorCertificate() error {
	if hd.d.claudeChatQuarantined(hd.sessionID) {
		return errClaudeChatQuarantined
	}
	p := hd.certificatePending
	if p == nil {
		return nil
	}
	if cursor, err := readHookRetirementCursor(hd.cursorPath); err != nil || cursor != p.Sequence {
		hd.certificatePending = nil
		return nil
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if hd.d.claudeChatQuarantined(hd.sessionID) {
		return errClaudeChatQuarantined
	}
	if err := persistHookCursorCertificate(hd.cursorPath, data); err != nil {
		return err
	}
	hd.certificatePending = nil
	return nil
}

func (hd *HookDrainer) canCertifyResponse(resp shim.HookDrainResponse, cursor uint64) bool {
	if resp.ProducerCertificate == "" || resp.SpoolIncarnation == "" {
		return false
	}
	if cursor == 0 {
		return true
	}
	data, err := readHookRetirementSidecar(hd.cursorPath + ".cert")
	if err != nil {
		return false
	}
	var prior hookCursorCertificate
	return json.Unmarshal(data, &prior) == nil && prior.Sequence == cursor && prior.Incarnation == resp.SpoolIncarnation && prior.Producer == resp.ProducerCertificate
}

// ReadHookRetirementCertificate reads existing durable disposal authority without
// flushing pending state or inferring authority from a decimal cursor alone.
func ReadHookRetirementCertificate(cursorPath string) (incarnation, producer string, sequence uint64, err error) {
	raw, err := readHookRetirementSidecar(cursorPath + ".cert")
	if err != nil {
		return "", "", 0, err
	}
	var p hookCursorCertificate
	if json.Unmarshal(raw, &p) != nil || p.Sequence == 0 || p.Incarnation == "" || p.Producer == "" {
		return "", "", 0, shim.ErrHookRetirementUnproven
	}
	cursor, err := readHookRetirementCursor(cursorPath)
	if err != nil {
		var malformed *strconv.NumError
		if errors.As(err, &malformed) {
			return "", "", 0, shim.ErrHookRetirementUnproven
		}
		return "", "", 0, err
	}
	if cursor != p.Sequence {
		return "", "", 0, shim.ErrHookRetirementUnproven
	}
	return p.Incarnation, p.Producer, p.Sequence, nil
}

func (hd *HookDrainer) retireCertifiedPrefix() error {
	hd.drainMu.Lock()
	defer hd.drainMu.Unlock()
	if hd.d.claudeChatQuarantined(hd.sessionID) {
		return errClaudeChatQuarantined
	}
	if err := hd.flushCursorCertificate(); err != nil {
		return err
	}
	incarnation, producer, sequence, err := ReadHookRetirementCertificate(hd.cursorPath)
	if errors.Is(err, os.ErrNotExist) {
		return shim.ErrHookRetirementUnproven
	}
	if err != nil {
		return err
	}
	hd.mu.Lock()
	path := hd.spoolPath
	hd.mu.Unlock()
	if path == "" {
		return shim.ErrHookRetirementUnproven
	}
	var pid int
	var start int64
	if hd.d != nil && hd.d.core != nil {
		if m, ok := hd.d.core.Get(hd.sessionID); ok {
			pid, start = m.ShimPID, m.ShimStartTime
		}
	}
	if hd.d.claudeChatQuarantined(hd.sessionID) {
		return errClaudeChatQuarantined
	}
	return shim.CompactStoppedHookSpool(path, incarnation, producer, sequence, pid, start)
}

type hookRetirementRetry struct {
	drainer   *HookDrainer
	remaining int
}

func (d *Daemon) retireHookPayload(hd *HookDrainer) {
	err := hd.retireCertifiedPrefix()
	if err == nil || errors.Is(err, errClaudeChatQuarantined) || errors.Is(err, shim.ErrHookRetirementUnproven) || errors.Is(err, os.ErrNotExist) {
		return
	}
	d.drains.mu.Lock()
	if d.drains.retirement == nil {
		d.drains.retirement = make(map[string]hookRetirementRetry)
	}
	d.drains.retirement[hd.sessionID] = hookRetirementRetry{hd, 32}
	d.drains.mu.Unlock()
}

// Existing lifecycle ticks retry only certified storage work; no hook ingestion or
// engine registration happens after terminal authentication has been retired.
func (d *Daemon) retryHookRetirements() {
	d.drains.mu.Lock()
	retries := make(map[string]hookRetirementRetry, len(d.drains.retirement))
	for id, r := range d.drains.retirement {
		retries[id] = r
	}
	d.drains.mu.Unlock()
	for id, r := range retries {
		err := r.drainer.retireCertifiedPrefix()
		d.drains.mu.Lock()
		if current, ok := d.drains.retirement[id]; ok && current.drainer == r.drainer {
			if err == nil || errors.Is(err, errClaudeChatQuarantined) || errors.Is(err, shim.ErrHookRetirementUnproven) || errors.Is(err, os.ErrNotExist) || r.remaining <= 1 {
				if r.remaining <= 1 && err != nil && !errors.Is(err, shim.ErrHookRetirementUnproven) && !errors.Is(err, os.ErrNotExist) {
					log.Printf("skeleton: hook payload retirement for session %s exhausted retries: %v", id, err)
				}
				delete(d.drains.retirement, id)
			} else {
				r.remaining--
				d.drains.retirement[id] = r
			}
		}
		d.drains.mu.Unlock()
	}
}
