package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
)

func doctorStepStatus(t *testing.T, out, name string) string {
	t.Helper()
	prefix := fmt.Sprintf("%-20s ", name)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.Fields(strings.TrimPrefix(line, prefix))[0]
		}
	}
	t.Fatalf("doctor output has no %q step:\n%s", name, out)
	return ""
}

func TestRelayDoctorRejectsLegacyArguments(t *testing.T) {
	for _, args := range [][]string{{"doctor", "wss://relay.example"}, {"doctor", "--relay-pin", "x"}, {"doctor", "--operator-secret-file", "x"}} {
		var out, err bytes.Buffer
		if got := runRelay(args, &out, &err); got != 2 {
			t.Fatalf("runRelay(%v) = %d, want usage error; stderr=%s", args, got, err.String())
		}
	}
}

func TestRelayDoctorRequiresConfiguredMachineState(t *testing.T) {
	t.Setenv(daemon.EnvStateDir, t.TempDir())
	var out, err bytes.Buffer
	if got := runRelay([]string{"doctor"}, &out, &err); got != 1 || !strings.Contains(out.String(), "fail") {
		t.Fatalf("doctor without state = %d, stdout=%s stderr=%s", got, out.String(), err.String())
	}
}

func TestDoctorCheckTCPTLSHonorsConfiguredPin(t *testing.T) {
	front := httptest.NewUnstartedServer(nil)
	front.Config.ErrorLog = log.New(io.Discard, "", 0)
	front.StartTLS()
	defer front.Close()
	sum := sha256.Sum256(front.Certificate().RawSubjectPublicKeyInfo)
	cfg := relaycfg.Config{RelayURL: strings.Replace(front.URL, "https://", "wss://", 1), TLSPolicy: relaycfg.PolicyPinnedSPKI, SPKIPin: base64.StdEncoding.EncodeToString(sum[:])}
	sec, err := cfg.Security()
	if err != nil {
		t.Fatal(err)
	}
	if got := doctorCheckTCPTLS(context.Background(), cfg.RelayURL, sec); got.status != statusOK {
		t.Fatalf("pinned TLS = %+v", got)
	}
	if got := doctorCheckTCPTLS(context.Background(), cfg.RelayURL, relay.Security{}); got.status == statusOK {
		t.Fatalf("untrusted self-signed TLS = %+v, want failure", got)
	}
}

func TestDoctorCheckRelayV2EdgeRefusesRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "swarm relay v2")
	}))
	defer target.Close()
	for _, tc := range []struct {
		name string
		new  func() (string, relay.Security, func())
	}{
		{
			name: "cross origin",
			new: func() (string, relay.Security, func()) {
				source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, target.URL, http.StatusFound)
				}))
				return source.URL, relay.Security{AllowLoopbackCleartext: true}, source.Close
			},
		},
		{
			name: "https to http",
			new: func() (string, relay.Security, func()) {
				source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, target.URL, http.StatusFound)
				}))
				sum := sha256.Sum256(source.Certificate().RawSubjectPublicKeyInfo)
				return strings.Replace(source.URL, "https://", "wss://", 1), relay.Security{PinnedSPKISHA256: sum[:]}, source.Close
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rawURL, sec, closeSource := tc.new()
			defer closeSource()
			if got := doctorCheckRelayV2Edge(context.Background(), rawURL, sec); got.status != statusFail {
				t.Fatalf("redirected edge = %+v, want failure", got)
			}
		})
	}
}

func TestDoctorCheckRelayV2EdgeRequiresExactMarker(t *testing.T) {
	for _, body := range []string{"swarm relay v2 ", "swarm relay v2\n", "swarm relay v2 hidden"} {
		t.Run(fmt.Sprintf("%q", body), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			if got := doctorCheckRelayV2Edge(context.Background(), server.URL, relay.Security{AllowLoopbackCleartext: true}); got.status != statusFail {
				t.Fatalf("edge body %q = %+v, want failure", body, got)
			}
		})
	}
}
