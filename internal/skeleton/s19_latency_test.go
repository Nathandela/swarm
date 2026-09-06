package skeleton

// The full relay-v2 replacement for the old S6b relay-v1 latency harness. It is
// opt-in because 3*(20 warm-up + >=200 measured) samples are a benchmark, not a
// unit test. The S19 rig supplies the real facade, Workerd, gateway binary,
// file-backed inbound checkpoint, daemon and PTY.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

const (
	s19LatencyP50 = 150 * time.Millisecond
	s19LatencyP95 = 400 * time.Millisecond
	s19LatencyP99 = 800 * time.Millisecond
)

func s19LatencyEnvInt(key string, fallback int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n > 0 {
		return n
	}
	return fallback
}

func s19LatencyEnvDuration(key string, fallback time.Duration) time.Duration {
	if duration, err := time.ParseDuration(os.Getenv(key)); err == nil && duration >= 0 {
		return duration
	}
	return fallback
}

func s19LatencyPercentile(sorted []time.Duration, percentile float64) time.Duration {
	i := int(percentile * float64(len(sorted)))
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

func s19LatencyEnvironment() string {
	sysctl := func(key string) string {
		out, err := exec.Command("sysctl", "-n", key).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	cpu, translated := "unknown", "n/a-non-darwin"
	if runtime.GOOS == "darwin" {
		if b := sysctl("machdep.cpu.brand_string"); b != "" {
			cpu = b
		}
		switch sysctl("sysctl.proc_translated") {
		case "1":
			translated = "YES-rosetta2"
		case "0":
			translated = "no-native"
		default:
			translated = "undetermined"
		}
	}
	return fmt.Sprintf("GOOS=%s GOARCH=%s cpu=%q NumCPU=%d go=%s translated=%s",
		runtime.GOOS, runtime.GOARCH, cpu, runtime.NumCPU(), runtime.Version(), translated)
}

func s19LatencyMargin(got, budget time.Duration) string {
	return fmt.Sprintf("%v/%v (%.0f%% of budget)", got, budget, 100*float64(got)/float64(budget))
}

func s19AwaitRawInput(t *testing.T, path string, want []byte) time.Time {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got, err := os.ReadFile(path); err == nil && bytes.Equal(got, want) {
			return time.Now()
		}
		time.Sleep(time.Millisecond)
	}
	got, err := os.ReadFile(path)
	t.Fatalf("raw fake-agent stdin = %q (err=%v), want exact %q", got, err, want)
	return time.Time{}
}

func TestS19Workerd_InputLatencyPhoneToPTY(t *testing.T) {
	if os.Getenv("SWARM_S6B_LATENCY") != "1" {
		t.Skip("set SWARM_S6B_LATENCY=1 to run the full relay-v2 input benchmark")
	}
	samples := s19LatencyEnvInt("SWARM_S6B_SAMPLES", 200)
	warmup := s19LatencyEnvInt("SWARM_S6B_WARMUP", 20)
	runs := s19LatencyEnvInt("SWARM_S6B_RUNS", 3)
	pacing := s19LatencyEnvDuration("SWARM_S6B_PACING", 125*time.Millisecond)
	if samples < 200 || runs < 3 {
		t.Fatalf("latency acceptance requires >=200 samples and >=3 runs; got %d and %d", samples, runs)
	}
	t.Logf("PB-NET-5 environment: %s", s19LatencyEnvironment())
	t.Logf("PB-NET-5 harness: samples=%d warmup=%d runs=%d pacing=%v payload=1-16-byte-non-submit budgets p50<=%v p95<=%v p99<=%v",
		samples, warmup, runs, pacing, s19LatencyP50, s19LatencyP95, s19LatencyP99)

	stats := make([][3]time.Duration, 0, runs)
	for run := 0; run < runs; run++ {
		run := run
		if !t.Run(fmt.Sprintf("run-%d", run+1), func(t *testing.T) {
			// A fresh phone, machine, gateway process and terminal per run makes the required
			// median independent rather than a warmed continuation of the first measurement.
			rig := newS19Rig(t)
			rig.Pair()
			rig.StartGateway()
			rig.Eventually("the machine's reconcile record reached the phone", func() bool {
				return rig.Summary().Reconciled
			})

			total := samples + warmup
			logPath := filepath.Join(rig.t.TempDir(), "raw-stdin.bin")
			meta := launchFakeWithOptions(t, rig.sk, "ask s19lat>\nidle 600s\n", "--raw-stdin-log", logPath)
			session := protocol.NamespacedID(rig.sk.api.endpointID, meta.ID)
			rig.Eventually("the latency session reached the phone", func() bool { return rig.RosterHas(session) })
			deadline := time.Now().Add(s19Deadline)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(logPath); err == nil {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			if _, err := os.Stat(logPath); err != nil {
				t.Fatalf("run %d raw PTY observer never opened: %v", run+1, err)
			}

			op, err := rig.App().TakeControl(session)
			if err != nil {
				t.Fatalf("run %d TakeControl: %v", run+1, err)
			}
			rig.Eventually("the latency lease was granted", func() bool {
				out, err := rig.App().Outcome(op.OperationID)
				return err == nil && out.Resolved && out.Code == protocol.OpLease
			})
			rig.Eventually("the latency input gate opened", func() bool {
				return rig.App().SendInput(session, nil) == nil
			})

			latencies := make([]time.Duration, 0, total)
			sendDurations := make([]time.Duration, 0, total)
			postSendDurations := make([]time.Duration, 0, total)
			var observed []byte
			for i := 0; i < total; i++ {
				payload := bytes.Repeat([]byte{byte('a' + i%26)}, 1+i%16)
				observed = append(observed, payload...)
				sent := time.Now()
				if err := rig.App().SendInput(session, payload); err != nil {
					t.Fatalf("run %d sample %d SendInput: %v", run+1, i, err)
				}
				returned := time.Now()
				arrived := s19AwaitRawInput(t, logPath, observed)
				latencies = append(latencies, arrived.Sub(sent))
				sendDurations = append(sendDurations, returned.Sub(sent))
				postSendDurations = append(postSendDurations, arrived.Sub(returned))
				time.Sleep(pacing)
			}

			measured := append([]time.Duration(nil), latencies[warmup:]...)
			measuredSend := append([]time.Duration(nil), sendDurations[warmup:]...)
			measuredPostSend := append([]time.Duration(nil), postSendDurations[warmup:]...)
			sort.Slice(measured, func(i, j int) bool { return measured[i] < measured[j] })
			sort.Slice(measuredSend, func(i, j int) bool { return measuredSend[i] < measuredSend[j] })
			sort.Slice(measuredPostSend, func(i, j int) bool { return measuredPostSend[i] < measuredPostSend[j] })
			runStats := [3]time.Duration{
				s19LatencyPercentile(measured, .50),
				s19LatencyPercentile(measured, .95),
				s19LatencyPercentile(measured, .99),
			}
			stats = append(stats, runStats)
			t.Logf("relay-v2 App.SendInput -> PTY run %d: p50=%v p95=%v p99=%v", run+1, runStats[0], runStats[1], runStats[2])
			t.Logf("relay-v2 run %d split: SendInput return p50=%v p95=%v p99=%v; return -> PTY p50=%v p95=%v p99=%v",
				run+1,
				s19LatencyPercentile(measuredSend, .50), s19LatencyPercentile(measuredSend, .95), s19LatencyPercentile(measuredSend, .99),
				s19LatencyPercentile(measuredPostSend, .50), s19LatencyPercentile(measuredPostSend, .95), s19LatencyPercentile(measuredPostSend, .99))
			checkpoint := filepath.Join(rig.stateDir, "remote", "inbound-state.json")
			if info, err := os.Stat(checkpoint); err != nil || info.Size() == 0 {
				t.Fatalf("run %d production gateway did not persist its inbound checkpoint: info=%v err=%v", run+1, info, err)
			} else {
				t.Logf("run %d production gateway persisted its inbound checkpoint: path=%s size=%d", run+1, checkpoint, info.Size())
			}
		}) {
			return
		}
	}

	median := func(index int) time.Duration {
		values := make([]time.Duration, len(stats))
		for i := range stats {
			values[i] = stats[i][index]
		}
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		return values[len(values)/2]
	}
	p50, p95, p99 := median(0), median(1), median(2)
	t.Logf("median of %d runs: p50=%s p95=%s p99=%s", runs,
		s19LatencyMargin(p50, s19LatencyP50), s19LatencyMargin(p95, s19LatencyP95), s19LatencyMargin(p99, s19LatencyP99))
	if p50 > s19LatencyP50 || p95 > s19LatencyP95 || p99 > s19LatencyP99 {
		t.Fatalf("latency budget exceeded: got (%v,%v,%v), want <= (%v,%v,%v)",
			p50, p95, p99, s19LatencyP50, s19LatencyP95, s19LatencyP99)
	}
}
