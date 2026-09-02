package skeleton

// THE LAUNCH GATE (ADR-025, bead swarm-1mq): the daemon answers a CLI's own startup gate --
// claude's folder-trust dialog -- from the grid tap, once per session.
//
// WHY HERE. The tap already samples every running session's grid on a 500 ms cadence for
// the status heuristic, so the sighting costs nothing new; and the answer goes through the
// SAME apply-by-injection path M1.2 uses for a phone approval (inject.go): a readWrite tap
// subscription, the keys read off the grid THAT subscription was seeded with, one write. A
// dialog the owner answered a beat earlier is therefore never typed at.
//
// ONCE PER SESSION is the ceiling on what a CLI that ignores the keys can cost: the engine
// reads that screen as needs_input on its own (heuristic.go's claude dialog hints), so an
// unanswered gate is a visible row, never a keystroke every poll.

import (
	"log"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/vt"
)

// answerLaunchGate types the session adapter's recorded gate answer when snap -- the grid
// the tap just sampled -- shows the gate. It writes nothing for a session already answered,
// a session whose adapter records no gate, or a live grid that no longer shows it.
func (d *Daemon) answerLaunchGate(id string, snap *vt.Snap) {
	if d.api == nil || d.launchGateAnswered(id) {
		return
	}
	m, ok := d.core.Get(id)
	if !ok {
		return
	}
	ad, ok := d.resolveAdapter(m.AgentType)
	if !ok {
		return
	}
	g, ok := adapter.AsLaunchGateAnswerer(ad)
	if !ok {
		return
	}
	if _, ok := g.LaunchGateKeys(snap); !ok {
		return
	}
	sub, err := d.api.tap.subscribe(id, readWrite)
	if err != nil {
		log.Printf("skeleton: session %s: launch gate seen but the tap could not be joined: %v", id, err)
		return
	}
	defer func() { _ = sub.Close() }()
	live, err := vt.DecodeSnapshot(sub.Snapshot())
	if err != nil {
		return
	}
	keys, ok := g.LaunchGateKeys(live)
	if !ok {
		return // gone between the sample and the join: somebody answered it
	}
	if err := sub.ControlKeys([]byte(keys)); err != nil {
		log.Printf("skeleton: session %s: writing the launch gate answer: %v", id, err)
		return
	}
	d.markLaunchGateAnswered(id)
	log.Printf("skeleton: session %s: answered the %s launch gate", id, m.AgentType)
}

// launchGateAnswered reports whether this daemon has already typed the session's gate answer.
func (d *Daemon) launchGateAnswered(id string) bool {
	d.gateMu.Lock()
	defer d.gateMu.Unlock()
	return d.gateDone[id]
}

// markLaunchGateAnswered records that the session's gate answer was written.
func (d *Daemon) markLaunchGateAnswered(id string) {
	d.gateMu.Lock()
	defer d.gateMu.Unlock()
	if d.gateDone == nil {
		d.gateDone = make(map[string]bool)
	}
	d.gateDone[id] = true
}
