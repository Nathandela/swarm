package engine

// Shared, deterministic test scaffolding for the Epic 10 status engine. Every
// side effect the engine has (clock reads, CPU sampling, status emission) is
// injected through Config so no test depends on wall time or a real process.

import (
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// Callback.Payload keys the engine reads to apply a typed signal's status
// dimensions. The engine core is CLI-agnostic (Epic 10): an adapter normalizes
// each CLI's raw hook into these generic keys (Epic 11). Values are the
// status-package string constants (e.g. "active", "permission").
const (
	payloadKeyTurn        = "turn"
	payloadKeyInteraction = "interaction"
)

func turnSignal(t status.Turn) map[string]string {
	return map[string]string{payloadKeyTurn: string(t)}
}

func interactionSignal(i status.Interaction) map[string]string {
	return map[string]string{payloadKeyInteraction: string(i)}
}

// fakeClock is an injectable, monotonic-by-hand clock. Tests advance it
// explicitly; the engine reads Now via Config.Now, so staleness and precedence
// timing are fully deterministic.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// emitCall is one recorded Emit invocation.
type emitCall struct {
	id string
	s  status.Status
}

// emitRecorder captures Emit calls synchronously, so a test can assert both the
// call count (idempotence, auth no-ops) and the last status (precedence,
// staleness). Emit is expected to be invoked on the calling goroutine.
type emitRecorder struct {
	mu    sync.Mutex
	calls []emitCall
}

func (r *emitRecorder) emit(id string, s status.Status) {
	r.mu.Lock()
	r.calls = append(r.calls, emitCall{id, s})
	r.mu.Unlock()
}

func (r *emitRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *emitRecorder) last() (emitCall, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return emitCall{}, false
	}
	return r.calls[len(r.calls)-1], true
}

// constCPU is a deterministic CPUSampler returning a fixed utilization for any
// pid. 0 means idle; a positive value means the process is busy.
func constCPU(v float64) func(pid int) (float64, error) {
	return func(pid int) (float64, error) { return v, nil }
}

// countingCPU is a CPUSampler that counts invocations, used to prove the engine
// samples only when Tick drives it (no busy-poll, E10.8).
type countingCPU struct {
	mu    sync.Mutex
	n     int
	value float64
}

func (c *countingCPU) sample(pid int) (float64, error) {
	c.mu.Lock()
	c.n++
	v := c.value
	c.mu.Unlock()
	return v, nil
}

func (c *countingCPU) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// kinds builds SignalSource descriptors of the given kinds (hook|event|heuristic).
func kinds(ks ...string) []adapter.SignalSource {
	out := make([]adapter.SignalSource, 0, len(ks))
	for _, k := range ks {
		out = append(out, adapter.SignalSource{Kind: k})
	}
	return out
}

// snapFromLines builds a vt.Snap grid from one string per row, padding each row
// to cols with blanks so the run widths sum to Cols (the Snap contract). Every
// rune is treated as a single cell; fixtures use ASCII plus width-1 braille
// spinner glyphs.
func snapFromLines(cols, cursorX, cursorY int, cursorVisible bool, lines []string) *vt.Snap {
	s := &vt.Snap{Version: 1, Cols: cols, Rows: len(lines), CursorX: cursorX, CursorY: cursorY, CursorVisible: cursorVisible}
	for _, ln := range lines {
		var runs []vt.Run
		w := 0
		for _, r := range ln {
			runs = append(runs, vt.Run{Text: string(r), Width: 1})
			w++
		}
		for ; w < cols; w++ {
			runs = append(runs, vt.Run{Text: " ", Width: 1})
		}
		s.Lines = append(s.Lines, vt.Line{Runs: runs})
	}
	return s
}

// newEngine wires an Engine with every effect injected.
func newEngine(clk *fakeClock, sampler func(pid int) (float64, error), rec *emitRecorder, staleness, poll time.Duration) *Engine {
	return New(Config{
		Now:                clk.now,
		CPUSampler:         sampler,
		StalenessThreshold: staleness,
		PollInterval:       poll,
		Emit:               rec.emit,
	})
}
