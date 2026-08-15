package relay

// The R2 admin surface (playbook 6.5): /healthz and /readyz on a SEPARATE
// loopback-only port from the public relay listener. The public protocol gains
// no unauthenticated endpoint from this -- the doctor rule ("the normal public
// protocol gains no privileged unauthenticated doctor endpoint") applies to
// health too, so Start refuses to bind admin_listen anywhere but loopback.

import (
	"fmt"
	"net"
	"net/http"
	"path/filepath"
)

// WithDiskFreeFunc overrides the low-disk check's free-space source (default:
// a real statfs of DBPath's directory). Tests use this instead of touching the
// filesystem, the same seam WithClock/WithSourceKeyFunc already establish.
func WithDiskFreeFunc(fn func() (uint64, error)) Option {
	return func(s *Server) {
		if fn != nil {
			s.diskFreeFn = fn
		}
	}
}

// AdminURL is the bound admin listener's http:// base, or "" if admin_listen
// was empty (admin serving disabled).
func (s *Server) AdminURL() string { return s.adminURL }

// isLoopbackHostPort reports whether addr's host is loopback-only
// (127.0.0.0/8, ::1, or the literal "localhost"). It is a pure string check --
// no socket is opened -- so validating an address never has a side effect.
func isLoopbackHostPort(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, err
	}
	if host == "localhost" {
		return true, nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false, fmt.Errorf("host %q is neither an IP address nor \"localhost\"", host)
	}
	return ip.IsLoopback(), nil
}

// startAdmin validates and binds the admin listener, if configured. Called
// from Start BEFORE the public listener binds, so a rejected/failed admin
// bind never leaves the public listener open with nothing to close it.
func (s *Server) startAdmin() error {
	if s.cfg.AdminListen == "" {
		return nil
	}
	loopback, err := isLoopbackHostPort(s.cfg.AdminListen)
	if err != nil {
		return fmt.Errorf("relay: admin_listen %q: %w", s.cfg.AdminListen, err)
	}
	if !loopback {
		return fmt.Errorf("relay: admin_listen %q must be loopback-only (127.0.0.1, ::1, or localhost): "+
			"the health/readiness surface is never exposed on a public interface", s.cfg.AdminListen)
	}
	ln, err := net.Listen("tcp", s.cfg.AdminListen)
	if err != nil {
		return fmt.Errorf("relay: admin listener: %w", err)
	}
	s.adminLn = ln
	s.adminURL = "http://" + ln.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	s.adminSrv = &http.Server{Handler: mux}
	go func() { _ = s.adminSrv.Serve(ln) }()
	return nil
}

// closeAdmin tears down the admin listener/server. Safe to call even when
// admin serving was never started (both fields nil).
func (s *Server) closeAdmin() {
	if s.adminSrv != nil {
		_ = s.adminSrv.Close()
	}
	if s.adminLn != nil {
		_ = s.adminLn.Close()
	}
}

// handleHealthz reports only that the process is up and serving HTTP at all --
// no dependency checks. A Compose/Kubernetes liveness probe uses this to decide
// whether to restart the container; it must stay trivially true whenever the
// process can still answer a request.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// storageHealth is the persistence-health snapshot BOTH /readyz
// (handleReadyz) and the diag_status capability op (diag.go; R2 review
// MEDIUM, playbook 6.5's doctor "storage" result) report — the SAME check
// computed in ONE place, so an operator polling /readyz locally and an
// operator running `swarm relay doctor` remotely can never see the two
// surfaces disagree.
type storageHealth struct {
	storeOK          bool
	storeErr         string
	diskCheckEnabled bool
	diskOK           bool
	diskErr          string
	diskFreeBytes    uint64
	diskFreeMinBytes int64
}

// checkStorage runs the store-writable and free-disk checks against the
// relay's OWN current state. The low-disk log warning (setDiskLow) is driven
// from here so it fires exactly once per transition regardless of which
// caller (handleReadyz or diag_status) happens to observe it.
func (s *Server) checkStorage() storageHealth {
	var h storageHealth
	h.storeOK = true
	if err := s.st.healthCheck(); err != nil {
		h.storeOK = false
		h.storeErr = err.Error()
	}
	h.diskFreeMinBytes = s.cfg.Quotas.DiskFreeMinBytes
	h.diskOK = true
	if h.diskFreeMinBytes > 0 {
		h.diskCheckEnabled = true
		free, err := s.diskFreeFn()
		switch {
		case err != nil:
			h.diskOK = false
			h.diskErr = err.Error()
		case free < uint64(h.diskFreeMinBytes):
			h.diskOK = false
			h.diskFreeBytes = free
			s.setDiskLow(true)
		default:
			h.diskFreeBytes = free
			s.setDiskLow(false)
		}
	}
	return h
}

// handleReadyz reports whether the relay is ready to serve real traffic: the
// public listener is accepting, the bbolt store is writable, and free disk is
// above the configured alarm threshold. Any one failing is a clean 503 naming
// every failing reason, never a panic or a hang.
func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	var reasons []string

	if !s.listening.Load() {
		reasons = append(reasons, "listener not accepting")
	}
	h := s.checkStorage()
	if !h.storeOK {
		reasons = append(reasons, fmt.Sprintf("store not writable: %s", h.storeErr))
	}
	if h.diskCheckEnabled {
		switch {
		case h.diskErr != "":
			reasons = append(reasons, fmt.Sprintf("disk check failed: %s", h.diskErr))
		case !h.diskOK:
			reasons = append(reasons, fmt.Sprintf("low disk space: %d bytes free, want >= %d", h.diskFreeBytes, h.diskFreeMinBytes))
		}
	}

	if len(reasons) > 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		for _, r := range reasons {
			_, _ = fmt.Fprintln(w, r)
		}
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// setDiskLow records the low-disk state and logs a warning ONLY on the
// false->true transition (playbook 6.5: "a bounded log warning"). An
// orchestrator healthcheck polls /readyz every few seconds for the life of the
// container; without this guard the log would grow one line per poll for as
// long as the disk stays low.
func (s *Server) setDiskLow(low bool) {
	s.mu.Lock()
	already := s.diskLowWarned
	s.diskLowWarned = low
	s.mu.Unlock()
	if low && !already {
		s.logger.Printf("low disk space: relay storage is below the configured disk_free_min_bytes threshold")
	}
}

// defaultDiskFreeFn returns the real free-space check, rooted at DBPath's
// directory (where the bbolt file and its writes actually land).
func defaultDiskFreeFn(dbPath string) func() (uint64, error) {
	dir := filepath.Dir(dbPath)
	return func() (uint64, error) { return diskFreeBytes(dir) }
}
