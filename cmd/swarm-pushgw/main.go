// Command swarm-pushgw boots the Swarm push gateway
// (docs/specifications/push-gateway-api.md, ADR-015). It fails closed: a missing -db, an
// unreadable FCM credential when one is configured, or an unusable quota bucket is a clean
// error rather than a boot on unspecified defaults.
//
// PG-TR-1 requires HTTPS on the ordinary Web PKI for every operation, TLS 1.3 as the floor.
// This binary satisfies that in one of two DECLARED ways, never silently: pass -tls-cert
// and -tls-key to terminate TLS in-process (ListenAndServeTLS, MinVersion TLS 1.3), or pass
// -insecure-http to declare that TLS is terminated by a reverse proxy in front of this
// process -- the recorded deployment assumption PG-TR-1's absence of a self-hosted relay's
// certificate-provisioning excuses invites. -insecure-http is a deliberate, explicit flag
// specifically so an operator cannot reach a plaintext listener by omission. A
// TLS-terminating proxy also means every caller presents the proxy's address, not its own
// (PG-Q-4): pair -insecure-http with -trusted-proxies naming that proxy's CIDR(s), the same
// trusted-proxy principle internal/remote/relay's bundle already uses, so quota accounting
// still keys on the real caller instead of collapsing every source into one shared bucket.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/pushgw"
	"github.com/Nathandela/swarm/internal/remote/push"
)

// defaultRetentionInterval is the production cadence for the retention sweep (spec
// section 8.1) when -retention-interval is not set.
const defaultRetentionInterval = time.Hour

// shutdownTimeout bounds how long a graceful shutdown waits for in-flight requests.
const shutdownTimeout = 10 * time.Second

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("swarm-pushgw", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	listen := fs.String("listen", ":8443", "address to serve the gateway on")
	dbPath := fs.String("db", "", "path to the gateway's bbolt file (required)")
	fcmCredentials := fs.String("fcm-credentials", "", "path to the FCM v1 service-account JSON (unset: dev mode -- wakes are refused upstream_unavailable rather than delivered)")
	retentionInterval := fs.Duration("retention-interval", defaultRetentionInterval, "how often the retention sweep (spec section 8.1) runs")

	tlsCert := fs.String("tls-cert", "", "path to a PEM certificate; terminates TLS in-process (PG-TR-1)")
	tlsKey := fs.String("tls-key", "", "path to the PEM private key matching -tls-cert")
	insecureHTTP := fs.Bool("insecure-http", false, "serve plain HTTP, declaring that TLS is terminated by a reverse proxy in front of this process (PG-TR-1) -- pair with -trusted-proxies")
	trustedProxies := fs.String("trusted-proxies", "", "comma-separated CIDRs of reverse proxies to trust for X-Forwarded-For (PG-Q-4); only consulted when the TCP peer itself is inside one of these")

	wakesPerAddr := fs.Int("quota-wakes-per-address", 20, "max wakes per push address per window (PG-Q-1)")
	wakesPerAddrWindow := fs.Duration("quota-wakes-per-address-window", 5*time.Minute, "window for -quota-wakes-per-address")
	wakesPerSrc := fs.Int("quota-wakes-per-source", 600, "max wakes per source IP per window (PG-Q-1)")
	wakesPerSrcWindow := fs.Duration("quota-wakes-per-source-window", time.Hour, "window for -quota-wakes-per-source")
	regsPerSrc := fs.Int("quota-registrations-per-source", 10, "max registrations per source IP per window (PG-Q-3)")
	regsPerSrcWindow := fs.Duration("quota-registrations-per-source-window", time.Hour, "window for -quota-registrations-per-source")
	regsGlobal := fs.Int("quota-registrations-global", 2000, "max registrations gateway-wide per window (PG-Q-3)")
	regsGlobalWindow := fs.Duration("quota-registrations-global-window", time.Hour, "window for -quota-registrations-global")
	allocsPerSrc := fs.Int("quota-allocations-per-source", 40, "max address allocations per source IP per window (PG-Q-3)")
	allocsPerSrcWindow := fs.Duration("quota-allocations-per-source-window", time.Hour, "window for -quota-allocations-per-source")
	allocsGlobal := fs.Int("quota-allocations-global", 4000, "max address allocations gateway-wide per window (PG-Q-3)")
	allocsGlobalWindow := fs.Duration("quota-allocations-global-window", time.Hour, "window for -quota-allocations-global")
	allocsPerInst := fs.Int("quota-allocations-per-installation", 20, "max live address allocations per installation (PG-ALLOC-4)")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dbPath == "" {
		return errors.New("swarm-pushgw: -db is required")
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("swarm-pushgw: -tls-cert and -tls-key must both be set")
	}
	tlsConfigured := *tlsCert != ""
	if !tlsConfigured && !*insecureHTTP {
		return errors.New("swarm-pushgw: TLS is required (PG-TR-1): pass -tls-cert and -tls-key, or set -insecure-http to declare a TLS-terminating reverse proxy deployment")
	}
	if tlsConfigured && *insecureHTTP {
		return errors.New("swarm-pushgw: -tls-cert/-tls-key and -insecure-http are mutually exclusive")
	}

	var trustedProxyCIDRs []string
	if *trustedProxies != "" {
		trustedProxyCIDRs = strings.Split(*trustedProxies, ",")
	}

	sender, err := buildSender(*fcmCredentials)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	srv, err := pushgw.NewServer(pushgw.Config{
		DBPath:         *dbPath,
		Sender:         sender,
		Attest:         notImplementedAttestor{},
		Logger:         logger,
		TrustedProxies: trustedProxyCIDRs,
		Quotas: pushgw.QuotaConfig{
			WakesPerAddress:            pushgw.RateLimit{Max: *wakesPerAddr, Window: *wakesPerAddrWindow},
			WakesPerSourceIP:           pushgw.RateLimit{Max: *wakesPerSrc, Window: *wakesPerSrcWindow},
			RegistrationsPerSourceIP:   pushgw.RateLimit{Max: *regsPerSrc, Window: *regsPerSrcWindow},
			RegistrationsGlobal:        pushgw.RateLimit{Max: *regsGlobal, Window: *regsGlobalWindow},
			AllocationsPerSourceIP:     pushgw.RateLimit{Max: *allocsPerSrc, Window: *allocsPerSrcWindow},
			AllocationsGlobal:          pushgw.RateLimit{Max: *allocsGlobal, Window: *allocsGlobalWindow},
			AllocationsPerInstallation: *allocsPerInst,
		},
	})
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()

	stopRetention := make(chan struct{})
	defer close(stopRetention)
	go runRetentionLoop(ctx, srv, *retentionInterval, stopRetention, logger)

	httpSrv := &http.Server{Addr: *listen, Handler: srv}
	errCh := make(chan error, 1)
	if tlsConfigured {
		httpSrv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS13}
		go func() { errCh <- httpSrv.ListenAndServeTLS(*tlsCert, *tlsKey) }()
	} else {
		go func() { errCh <- httpSrv.ListenAndServe() }()
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// runRetentionLoop drives Server.RunRetention on a real-clock ticker (the fake-clock seam
// is Config.Now, exercised by the test suite; production always uses the wall clock).
func runRetentionLoop(ctx context.Context, srv *pushgw.Server, interval time.Duration, stop <-chan struct{}, logger *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-t.C:
			if err := srv.RunRetention(ctx); err != nil {
				logger.Error("pushgw retention sweep failed", "error", err)
			}
		}
	}
}

// buildSender wires the real FCM v1 sender (ADR-015 P2's relocated asset) when
// credentials are configured. Unset credentials is a supported dev-mode boot, mirroring
// cmd/swarm-relay's push_credentials story -- but unlike a silently-absent sink, a wake
// attempted against devSender gets an honest, retryable upstream_unavailable rather than a
// fake accept.
func buildSender(credentialsPath string) (pushgw.WakeSender, error) {
	if credentialsPath == "" {
		return devSender{}, nil
	}
	doc, err := os.ReadFile(credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("swarm-pushgw: read fcm credentials: %w", err)
	}
	acct, err := push.LoadServiceAccount(doc)
	if err != nil {
		return nil, fmt.Errorf("swarm-pushgw: %w", err)
	}
	fcm, err := push.NewFCM(push.FCMConfig{Account: acct, RetryDelay: push.DefaultRetryDelay})
	if err != nil {
		return nil, fmt.Errorf("swarm-pushgw: %w", err)
	}
	return pushgw.NewFCMSender(fcm), nil
}

// devSender is the no-credential dev-mode WakeSender.
type devSender struct{}

func (devSender) Send(context.Context, string, []byte) error { return pushgw.ErrUnavailable }

// notImplementedAttestor fails every registration closed (attestation_unavailable,
// retryable) rather than fabricate a verdict. This repository has no Play Integrity
// client yet -- not one wake has ever left it toward Google (ADR-015, Notes) -- so a real
// verifier is later, out-of-scope work; PG-AUTH-13 requires exactly this failure mode
// ("registration SHALL be refused") rather than an unattested enrollment.
type notImplementedAttestor struct{}

func (notImplementedAttestor) Verify(context.Context, string) (pushgw.VerdictBinding, error) {
	return pushgw.VerdictBinding{}, pushgw.ErrAttestationUnavailable
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
