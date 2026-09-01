package pushgw

import (
	"fmt"
	"net/http"
	"sort"
)

// AdminHandler exposes process health, production readiness, and aggregate metrics. The
// executable binds it to a separately validated loopback listener; it is never mounted on
// the public API handler.
func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", getOnly(s.handleAdminHealth))
	mux.HandleFunc("/readyz", getOnly(s.handleAdminReady))
	mux.HandleFunc("/metrics", getOnly(s.handleAdminMetrics))
	return mux
}

func getOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleAdminHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleAdminReady(w http.ResponseWriter, _ *http.Request) {
	var reasons []string
	if !s.serving.Load() {
		reasons = append(reasons, "public listener not serving")
	}
	if !s.retentionWorker.Load() {
		reasons = append(reasons, "retention worker not running")
	}
	if !s.ready.ProductionSender {
		reasons = append(reasons, "production sender not configured")
	}
	if !s.ready.ProductionAttestor {
		reasons = append(reasons, "production attestor not configured")
	}
	if !s.ready.RequiredConfig {
		reasons = append(reasons, "required production configuration not validated")
	}
	if err := s.store.healthCheck(); err != nil {
		reasons = append(reasons, err.Error())
	}
	if len(reasons) > 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		for _, reason := range reasons {
			_, _ = fmt.Fprintln(w, reason)
		}
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleAdminMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	store := s.store.metrics()
	_, _ = fmt.Fprintln(w, "# TYPE pushgw_database_bytes gauge")
	_, _ = fmt.Fprintf(w, "pushgw_database_bytes %d\n", store.DBBytes)
	_, _ = fmt.Fprintln(w, "# TYPE pushgw_installations gauge")
	_, _ = fmt.Fprintf(w, "pushgw_installations %d\n", store.Installations)
	_, _ = fmt.Fprintln(w, "# TYPE pushgw_addresses gauge")
	_, _ = fmt.Fprintf(w, "pushgw_addresses %d\n", store.Addresses)
	_, _ = fmt.Fprintln(w, "# TYPE pushgw_tombstones gauge")
	_, _ = fmt.Fprintf(w, "pushgw_tombstones %d\n", store.Tombstones)
	_, _ = fmt.Fprintln(w, "# TYPE pushgw_retention_last_success_timestamp_seconds gauge")
	_, _ = fmt.Fprintf(w, "pushgw_retention_last_success_timestamp_seconds %d\n", s.lastRetentionOK.Load())
	_, _ = fmt.Fprintln(w, "# TYPE pushgw_requests_total counter")

	s.requestMetrics.mu.Lock()
	keys := make([]requestMetricKey, 0, len(s.requestMetrics.counts))
	counts := make(map[requestMetricKey]uint64, len(s.requestMetrics.counts))
	for key, count := range s.requestMetrics.counts {
		keys = append(keys, key)
		counts[key] = count
	}
	s.requestMetrics.mu.Unlock()
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].operation == keys[j].operation {
			return keys[i].status < keys[j].status
		}
		return keys[i].operation < keys[j].operation
	})
	for _, key := range keys {
		_, _ = fmt.Fprintf(w, "pushgw_requests_total{operation=%q,status=%q} %d\n", key.operation, statusLabel(key.status), counts[key])
	}
}
