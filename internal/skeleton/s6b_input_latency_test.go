package skeleton

// Slice S6b — the PB-NET-5 acceptance clause (b) HARNESS: phone `Type` -> PTY
// write, measured end to end at §6.0's p50 <= 150 ms / p95 <= 400 ms / p99 <=
// 800 ms over n >= 200.
//
// GATING (§6.0 describes a benchmark, not a unit test: median of 3 runs, n >= 200
// samples each, 20-sample warm-up discarded). This file is gated by the ENV
// VARIABLE SWARM_S6B_LATENCY=1, deliberately NOT by a build tag. GG-4 requires
// `go build ./...`, `go test ./...`, `go vet ./...` and `golangci-lint run` to be
// green, and all four run UNTAGGED: a build-tagged file is never compiled, never
// vetted and never linted by the gate, so it rots into a place where the hardest
// requirement in the phase can quietly die. An env gate keeps the file compiled
// and vetted on every run and costs one t.Skip.
//
// Because it is gated, it is NOT the only thing standing between S6b and a
// regression. The fast, always-run tests that fail if the mechanism regresses are:
//
//	internal/remote/relay/s6b_wait_test.go        — the relay protocol change
//	internal/remote/transport/s6b_input_test.go   — the phone hop's live tail
//	internal/remotegw/s6b_gateway_input_test.go   — the gateway hop's 500 ms poll
//
// WHAT IS MEASURED. The full production chain on one machine: phonesim (over the
// real phonecore) seals an input frame under the epoch content key and appends it
// to the machine's mailbox on a REAL in-process relay; the gateway's command-IN
// path opens it, persists the inbound checkpoint (PB-GW-3 puts that fsync BEFORE
// the PTY write), and rides it down the take_control lease conn to the daemon,
// which writes it to the session's PTY. Arrival at the PTY is observed on a
// READ-ONLY shared session tap (coreAPI.TerminalTap, A7 F1) — the shim's own
// output pipe, with no render debounce in the path — by looking for the unique
// marker the keystroke carries. The fake agent only echoes `got: <marker>` after
// its `ask` has consumed the line from the PTY, so a marker on the tap is proof
// the write landed.
//
// TWO §6.0 HARNESS RULES ARE STRUCTURAL PRECONDITIONS, not comments, and they are
// checked BEFORE any measurement:
//
//  1. The measured path must carry NO fixed command-IN poll cadence. This was
//     this file's RED: ServiceConfig.PollInterval then defaulted to 500 ms and the
//     phonesim harness tuned it to 20 ms — a test-only value that would have made
//     this harness certify a path production never runs. IT NOW PASSES, and by
//     removal rather than by tuning: ServiceConfig carries no command-IN cadence at
//     all (service.go:40-45 records the absence and why), and the command loop is
//     driven by the relay's bounded server-side wait (command_loop.go:325).
//  2. The gateway must use a REAL FILE-BACKED InboundState. §6.0: "The harness
//     MUST use a real file-backed InboundState, not the in-memory default" — S2
//     measured the per-keystroke fsync at 13-15 ms on an M1/APFS host, ~10% of the
//     p50 budget, and it sits on the keystroke path. Measuring with the in-memory
//     store measures a fiction. Asserted by requiring the checkpoint file to exist
//     and be non-empty after the run.
//
// Tunable through the environment so CI can record its own shape (§6.0 requires CI
// to record the environment): SWARM_S6B_SAMPLES (default 200), SWARM_S6B_WARMUP
// (20), SWARM_S6B_RUNS (3), SWARM_S6B_PACING (125ms — §6.0's <=8 frames/s
// sustained input rate).
//
// This file contains NO implementation.

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonesim"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/enroll"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// §6.0's binding numbers for PB-NET-5(b).
const (
	s6bBudgetP50 = 150 * time.Millisecond
	s6bBudgetP95 = 400 * time.Millisecond
	s6bBudgetP99 = 800 * time.Millisecond
)

// s6bEnvironment is the environment line §6.0 requires CI to record.
//
// GOARCH alone is not enough, and "M1" alone is actively misleading. This
// project's dev host is an Apple M1 (uname -m = arm64) running an x86_64 Go
// toolchain (/usr/local/bin/go is Mach-O x86_64; go version reports
// darwin/amd64), so every number this project has recorded — including the S2
// "13-15 ms per-keystroke fsync on an M1/APFS host" figure §6.0 cites — was taken
// through Rosetta 2 translation. Recording the arch PAIR plus the derived
// translation flag is what makes a later native-arm64 run, or a CI linux/amd64
// run, comparable rather than silently incomparable.
//
// Direction of the bias: translation is a cost, so a budget met here is met
// natively with margin. It is never a reason to loosen a bound; it is a reason to
// distrust a MARGINAL pass or a marginal failure.
// The translation probe MUST be sysctl.proc_translated, not uname. Verified on
// this host from inside a translated Go test process:
//
//	sysctl.proc_translated   = "1"          <- correct
//	hw.optional.arm64        = "1"          <- correct
//	machdep.cpu.brand_string = "Apple M1"   <- correct
//	hw.machine               = "x86_64"     <- LIES
//	uname -m                 = "x86_64"     <- LIES
//
// A child process inherits the translated personality, so `uname -m` reports
// x86_64 and a uname-based check confidently reports "native" on a Rosetta host —
// exactly the misleading answer this line exists to prevent.
func s6bEnvironment() string {
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

// s6bMargin renders a measurement as a percentage of its budget, so a pass that
// only just cleared the line is visible in the log instead of looking identical to
// one with 5x headroom.
func s6bMargin(got, budget time.Duration) string {
	return fmt.Sprintf("%v/%v (%.0f%% of budget)", got, budget, 100*float64(got)/float64(budget))
}

func s6bEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func s6bEnvDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return def
}

// s6bLatencyRig is the full remote wire with a PRODUCTION-shaped gateway: no poll
// cadence override and a file-backed inbound checkpoint. It duplicates
// newPhonesimHarness's bootstrap rather than calling it because those two fields
// are exactly what that harness hardcodes differently (PollInterval: 20ms,
// Inbound: nil), and §6.0 forbids measuring either of them.
type s6bLatencyRig struct {
	ctx        context.Context
	phone      *phonesim.Phone
	sk         *Daemon
	inboundPth string
}

func s6bNewLatencyRig(t *testing.T) s6bLatencyRig {
	t.Helper()

	rcfg := relay.DefaultConfig()
	rcfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	relaySrv, err := relay.New(rcfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := relaySrv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	t.Cleanup(func() { _ = relaySrv.Close() })

	sk, rsock := assembleWithRemote(t)

	machineID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("machine identity: %v", err)
	}
	machineSignPub, machineSignPriv, _ := ed25519.GenerateKey(nil)
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("phone keystore: %v", err)
	}

	const epochID = uint32(1)
	mp := pairing.MachineParams{
		Static:       machineID.NoiseStatic(),
		Secret:       fillKey(0x5A),
		RendezvousID: fill16(0x11),
		LocalConsole: true,
		Confirm:      func(context.Context, [6]string, string) (bool, error) { return true, nil },
		Payload: pairing.MachinePayload{
			Hostname:            "s6b-latency.local",
			MachineRoutingID:    []byte("machine-routing-id-0001"),
			MachineRelayAuthPub: make([]byte, 32),
			RecipientPub:        machineID.RecipientPublic(),
			MachineSignPub:      machineSignPub,
			EpochID:             epochID,
		},
	}
	dp := pairing.DeviceParams{
		Static:       mustNoiseStatic(t, ks),
		Secret:       fillKey(0x5A),
		RendezvousID: fill16(0x11),
		Payload: pairing.DevicePayload{
			DeviceName:           "S6b Latency Phone",
			DeviceRoutingID:      []byte("device-routing-id-0001"),
			DeviceRelayAuthPub:   ks.RelayAuthPublic(),
			RecipientPub:         ks.RecipientPublic(),
			DeviceCommandSignPub: ks.CommandSigningPublic(),
		},
		Consent: phoneConsentFor(ks, fill16(0x11)),
	}

	mEnd, dEnd := rendezvousPair()
	m := pairing.NewMachine(mp)
	var (
		mo   *pairing.MachineOutcome
		do   *pairing.DeviceOutcome
		mErr error
		dErr error
		wg   sync.WaitGroup
	)
	wg.Add(2)
	go func() { defer wg.Done(); mo, mErr = m.Pair(ctx, mEnd) }()
	go func() { defer wg.Done(); do, dErr = pairing.RunDevice(ctx, dp, dEnd) }()
	wg.Wait()
	if mErr != nil || dErr != nil {
		t.Fatalf("pairing failed: machine=%v device=%v", mErr, dErr)
	}

	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	res, err := enroll.Enroll(mo, device.CapFull, machineSignPriv, epochID, 1, keys, time.Now())
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if err := sk.api.devices.Add(res.Record); err != nil {
		t.Fatalf("daemon registry rejected the enrolled record: %v", err)
	}

	mPub, mPriv, _ := ed25519.GenerateKey(nil)
	pPub, pPriv, _ := ed25519.GenerateKey(nil)
	machineRelay, err := relay.Dial(ctx, relaySrv.URL(), relayAuth(mPub, mPriv))
	if err != nil {
		t.Fatalf("machine dial: %v", err)
	}
	t.Cleanup(func() { _ = machineRelay.Close() })
	phoneRelay, err := relay.Dial(ctx, relaySrv.URL(), relayAuth(pPub, pPriv))
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneRelay.Close() })
	if err := machineRelay.AuthorizeDevice(ctx, pPub,
		e2eConsent(pPriv, relay.RoutingID(mPub))); err != nil {
		t.Fatalf("machine authorize phone: %v", err)
	}
	if err := phoneRelay.AuthorizeDevice(ctx, mPub,
		e2eConsent(mPriv, relay.RoutingID(pPub))); err != nil {
		t.Fatalf("phone authorize machine: %v", err)
	}

	// §6.0 harness rule: a REAL file-backed inbound checkpoint, because PB-GW-3
	// persists it BEFORE the PTY write and that fsync is on the measured path.
	inboundPth := filepath.Join(t.TempDir(), "inbound.json")
	inbound, err := remotegw.OpenInboundState(inboundPth, sk.api.endpointID)
	if err != nil {
		t.Fatalf("OpenInboundState: %v", err)
	}

	// NOTE: no PollInterval. After S6b the field does not exist; before S6b the
	// default is 500 ms, which is precisely what this harness must not hide.
	svc := remotegw.NewService(remotegw.ServiceConfig{
		DaemonSocket:   rsock,
		Relay:          machineRelay,
		PhoneTarget:    phoneRelay.RoutingID(),
		Key:            keys.ContentKey,
		EpochID:        epochID,
		SenderKeyID:    crypto.KeyID(machineID.RecipientPublic()),
		Inbound:        inbound,
		ReconnectDelay: 50 * time.Millisecond,
	})
	svcCtx, svcCancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- svc.Run(svcCtx) }()
	t.Cleanup(func() {
		svcCancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("gateway service did not stop within 5s of cancel")
		}
	})

	phone, err := phonesim.New(phonesim.Config{
		KeyStore:       ks,
		MachineSignPub: do.Machine.MachineSignPub,
		Grant:          res.Grant,
		Relay:          phoneRelay,
		MachineTarget:  machineRelay.RoutingID(),
		Machine:        sk.api.endpointID,
	})
	if err != nil {
		t.Fatalf("phonesim.New: %v", err)
	}

	return s6bLatencyRig{ctx: ctx, phone: phone, sk: sk, inboundPth: inboundPth}
}

// s6bPTYWatcher observes a session's PTY output on the READ-ONLY shared tap and
// reports when a marker first appears. TerminalTap is the same seam the remote
// peek uses (coreAPI.TerminalTap, A7 F1): it joins the single upstream shim
// connection without taking any lease, so watching does not disturb the phone's
// take_control, and it carries raw shim frames with no render debounce in the way.
type s6bPTYWatcher struct {
	mu   sync.Mutex
	seen map[string]time.Time
	buf  strings.Builder
	subs []string
}

func s6bWatchPTY(t *testing.T, sk *Daemon, local string) *s6bPTYWatcher {
	t.Helper()
	stream, err := sk.api.TerminalTap(local)
	if err != nil {
		t.Fatalf("TerminalTap(%s): %v", local, err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	w := &s6bPTYWatcher{seen: map[string]time.Time{}}
	go func() {
		for f := range stream.Frames() {
			now := time.Now()
			w.mu.Lock()
			w.buf.Write(f)
			text := w.buf.String()
			for _, mk := range w.subs {
				if _, done := w.seen[mk]; !done && strings.Contains(text, mk) {
					w.seen[mk] = now
				}
			}
			w.mu.Unlock()
		}
	}()
	return w
}

// expect registers a marker before it is typed, so the arrival timestamp is taken
// by the reader goroutine rather than by a poller.
func (w *s6bPTYWatcher) expect(marker string) {
	w.mu.Lock()
	w.subs = append(w.subs, marker)
	w.mu.Unlock()
}

// await blocks until the marker has been observed on the PTY, or the deadline.
func (w *s6bPTYWatcher) await(marker string, within time.Duration) (time.Time, bool) {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		at, ok := w.seen[marker]
		w.mu.Unlock()
		if ok {
			return at, true
		}
		time.Sleep(time.Millisecond)
	}
	return time.Time{}, false
}

func s6bPercentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)))
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

// s6bRunStats is one run's percentile set.
type s6bRunStats struct{ p50, p95, p99 time.Duration }

// TestS6B_InputLatencyPhoneTypeToPTYWrite is acceptance clause (b).
func TestS6B_InputLatencyPhoneTypeToPTYWrite(t *testing.T) {
	if os.Getenv("SWARM_S6B_LATENCY") != "1" {
		t.Skip("PB-NET-5(b) latency harness: set SWARM_S6B_LATENCY=1 to run (§6.0 describes a benchmark; the fast mechanism tests live in relay/transport/remotegw)")
	}

	// §6.0 harness precondition 1 — see this file's header. Checked before any
	// measurement, because a number measured on a 20 ms-polled or 500 ms-polled
	// gateway is not a number about the production path.
	rt := reflect.TypeOf(remotegw.ServiceConfig{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type == reflect.TypeOf(time.Duration(0)) && strings.Contains(strings.ToLower(f.Name), "poll") {
			t.Fatalf("the gateway still exposes a fixed command-IN poll cadence (ServiceConfig.%s); PB-NET-5 requires it dropped on BOTH hops, and measuring latency against a tuned poll interval certifies a path production never runs (fable F4)", f.Name)
		}
	}

	samples := s6bEnvInt("SWARM_S6B_SAMPLES", 200)
	warmup := s6bEnvInt("SWARM_S6B_WARMUP", 20)
	runs := s6bEnvInt("SWARM_S6B_RUNS", 3)
	pacing := s6bEnvDur("SWARM_S6B_PACING", 125*time.Millisecond) // §6.0: <=8 frames/s
	if samples < 200 {
		t.Fatalf("SWARM_S6B_SAMPLES=%d; §6.0 binds n >= 200", samples)
	}

	// §6.0 requires CI to record the environment.
	t.Logf("PB-NET-5(b) environment: %s", s6bEnvironment())
	t.Logf("PB-NET-5(b) harness: samples=%d warmup=%d runs=%d pacing=%v budgets p50<=%v p95<=%v p99<=%v",
		samples, warmup, runs, pacing, s6bBudgetP50, s6bBudgetP95, s6bBudgetP99)

	all := make([]s6bRunStats, 0, runs)
	for r := 0; r < runs; r++ {
		all = append(all, s6bOneLatencyRun(t, r, samples, warmup, pacing))
	}

	// §6.0: median of 3 runs.
	median := func(pick func(s6bRunStats) time.Duration) time.Duration {
		vs := make([]time.Duration, 0, len(all))
		for _, s := range all {
			vs = append(vs, pick(s))
		}
		sort.Slice(vs, func(i, j int) bool { return vs[i] < vs[j] })
		return vs[len(vs)/2]
	}
	p50 := median(func(s s6bRunStats) time.Duration { return s.p50 })
	p95 := median(func(s s6bRunStats) time.Duration { return s.p95 })
	p99 := median(func(s s6bRunStats) time.Duration { return s.p99 })
	// Margins, not just values: a pass at 96% of budget and a pass at 20% of budget
	// look identical in a green run, and only one of them survives a slower host.
	t.Logf("median of %d runs: p50=%s p95=%s p99=%s", runs,
		s6bMargin(p50, s6bBudgetP50), s6bMargin(p95, s6bBudgetP95), s6bMargin(p99, s6bBudgetP99))
	for _, m := range []struct {
		name        string
		got, budget time.Duration
	}{{"p50", p50, s6bBudgetP50}, {"p95", p95, s6bBudgetP95}, {"p99", p99, s6bBudgetP99}} {
		if m.got <= m.budget && float64(m.got) > 0.75*float64(m.budget) {
			t.Logf("MARGINAL: %s cleared its budget with under 25%% headroom (%s) on %s. Treat this as at risk on a slower host, not as a comfortable pass.",
				m.name, s6bMargin(m.got, m.budget), s6bEnvironment())
		}
	}

	if p50 > s6bBudgetP50 {
		t.Errorf("phone Type -> PTY write p50 = %v, want <= %v (§6.0, PB-NET-5(b))", p50, s6bBudgetP50)
	}
	if p95 > s6bBudgetP95 {
		t.Errorf("phone Type -> PTY write p95 = %v, want <= %v (§6.0, PB-NET-5(b))", p95, s6bBudgetP95)
	}
	if p99 > s6bBudgetP99 {
		t.Errorf("phone Type -> PTY write p99 = %v, want <= %v (§6.0, PB-NET-5(b))", p99, s6bBudgetP99)
	}
}

// s6bOneLatencyRun stands the whole stack up fresh, types n+warmup markers into a
// live session and returns the run's percentiles over the post-warm-up samples.
func s6bOneLatencyRun(t *testing.T, run, samples, warmup int, pacing time.Duration) s6bRunStats {
	t.Helper()
	rig := s6bNewLatencyRig(t)

	// A script with one `ask` per keystroke. Each ask prints its prompt, blocks
	// reading a line off the PTY, then echoes `got: <line>` — so the marker on the
	// tap is proof the PTY write landed. The prompt carries no marker text.
	total := samples + warmup
	var script strings.Builder
	for i := 0; i < total; i++ {
		script.WriteString("ask ?\n")
	}
	script.WriteString("exit 0\n")
	meta := launchFake(t, rig.sk, script.String())
	session := protocol.NamespacedID(rig.sk.api.endpointID, meta.ID)

	watcher := s6bWatchPTY(t, rig.sk, meta.ID)

	if err := rig.phone.TakeControl(rig.ctx, session, fmt.Sprintf("devSIM:01JSIM00000000000S6B%02d", run)); err != nil {
		t.Fatalf("run %d: take_control: %v", run, err)
	}
	// The lease must exist before the first keystroke, or sample 0 measures the
	// lease grant rather than the input path. The warm-up absorbs the rest.
	time.Sleep(500 * time.Millisecond)

	lats := make([]time.Duration, 0, total)
	for i := 0; i < total; i++ {
		marker := fmt.Sprintf("s6bM%06d", i)
		watcher.expect(marker)
		sent := time.Now()
		if _, err := rig.phone.Type(rig.ctx, session, []byte(marker+"\n")); err != nil {
			t.Fatalf("run %d sample %d: phone.Type: %v", run, i, err)
		}
		at, ok := watcher.await(marker, 10*time.Second)
		if !ok {
			t.Fatalf("run %d sample %d: keystroke %q never reached the PTY within 10s", run, i, marker)
		}
		lats = append(lats, at.Sub(sent))
		time.Sleep(pacing)
	}

	// §6.0 harness precondition 2: the measured path really persisted through a
	// file-backed InboundState, so PB-GW-3's pre-PTY-write fsync is inside these
	// numbers rather than optimised away by the in-memory default.
	fi, err := os.Stat(rig.inboundPth)
	if err != nil || fi.Size() == 0 {
		t.Fatalf("run %d: the inbound checkpoint file at %s was never written (err=%v); §6.0 requires the latency harness to use a REAL file-backed InboundState, since the 13-15 ms per-keystroke fsync sits on the keystroke path", run, rig.inboundPth, err)
	}

	// Discard the warm-up (§6.0).
	measured := append([]time.Duration(nil), lats[warmup:]...)
	sort.Slice(measured, func(i, j int) bool { return measured[i] < measured[j] })
	st := s6bRunStats{
		p50: s6bPercentile(measured, 0.50),
		p95: s6bPercentile(measured, 0.95),
		p99: s6bPercentile(measured, 0.99),
	}
	t.Logf("run %d (n=%d after %d warm-up): p50=%v p95=%v p99=%v", run, len(measured), warmup, st.p50, st.p95, st.p99)
	return st
}
