package skeleton

// Trust selection and confirmation use separate existing grid polls. Neither a
// wire write nor a disappeared dialog proves native trust, auth, or turn adoption.
import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

const launchGateSettle = 250 * time.Millisecond

type launchGatePhase uint8

const (
	launchGateInitial launchGatePhase = iota
	launchGateAwaitYes
	launchGateObserveYes
	launchGateConsumed
	launchGateAbandoned
)

type launchGateIdentity struct {
	instance  string
	shimPID   int
	shimStart int64
}

func (i launchGateIdentity) valid() bool { return i.instance != "" && i.shimPID > 0 && i.shimStart > 0 }

type launchGateEntry struct {
	identity launchGateIdentity
	phase    launchGatePhase
	digest   [32]byte
	observed time.Time
}

// This read never loads a side-file or waits behind a capability author's fsync.
func (d *Daemon) cachedLaunchGateIdentity(id string) (launchGateIdentity, bool) {
	if d.core == nil || !d.capStore.mu.TryLock() {
		return launchGateIdentity{}, false
	}
	instance := d.capStore.instances[id]
	incarnation := d.capStore.incarnations[id]
	d.capStore.mu.Unlock()
	m, ok := d.core.Get(id)
	identity := launchGateIdentity{instance, incarnation.ShimPID, incarnation.ShimStartTime}
	return identity, ok && identity.valid() && m.Status.Process == status.ProcessRunning && m.ShimPID == identity.shimPID && m.ShimStartTime == identity.shimStart
}

func launchGateDigest(snap *vt.Snap) [32]byte {
	raw, _ := json.Marshal(snap)
	return sha256.Sum256(raw)
}

func launchGateShape(keys string) bool {
	return keys == "\r" || keys == "\x1b[A\r" || keys == "\x1b[B\r"
}

func (d *Daemon) abandonLaunchGate(id string) {
	d.gateMu.Lock()
	defer d.gateMu.Unlock()
	if e := d.gateDone[id]; e != nil {
		e.phase = launchGateAbandoned
	}
}

func (d *Daemon) abandonLaunchGateEntry(id string, entry *launchGateEntry) {
	d.gateMu.Lock()
	defer d.gateMu.Unlock()
	if entry != nil && d.gateDone[id] == entry {
		entry.phase = launchGateAbandoned
	}
}

func (d *Daemon) launchGateEntryForIdentity(id string, identity launchGateIdentity) *launchGateEntry {
	d.gateMu.Lock()
	defer d.gateMu.Unlock()
	if entry := d.gateDone[id]; identity.valid() && entry != nil && entry.identity == identity {
		return entry
	}
	return nil
}

func (d *Daemon) retireLaunchGate(id string, entry *launchGateEntry) {
	d.gateMu.Lock()
	defer d.gateMu.Unlock()
	if entry != nil && d.gateDone[id] == entry {
		entry.phase = launchGateAbandoned
		delete(d.gateDone, id)
	}
}
func (d *Daemon) abandonLaunchGateBinding(id string, identity launchGateIdentity) {
	d.gateMu.Lock()
	defer d.gateMu.Unlock()
	if e := d.gateDone[id]; e != nil && identity.valid() && e.identity == identity {
		e.phase = launchGateAbandoned
	}
}

// observeLaunchGate is memory-only; reservation happens after the final live read.
func (d *Daemon) observeLaunchGate(id string, identity launchGateIdentity, g adapter.LaunchGateAnswerer, snap *vt.Snap, now time.Time) (*launchGateEntry, string) {
	keys, ok := g.LaunchGateKeys(snap)
	d.gateMu.Lock()
	defer d.gateMu.Unlock()
	current, currentOK := d.cachedLaunchGateIdentity(id)
	if !currentOK || current != identity {
		return nil, ""
	}
	e := d.gateDone[id]
	if !ok || !launchGateShape(keys) {
		if e != nil && e.identity == identity && e.phase != launchGateConsumed {
			e.phase = launchGateAbandoned
		}
		return nil, ""
	}
	digest := launchGateDigest(snap)
	if e == nil || e.identity != identity {
		e = &launchGateEntry{identity: identity, digest: digest, observed: now}
		if d.gateDone == nil {
			d.gateDone = map[string]*launchGateEntry{}
		}
		d.gateDone[id] = e
		return nil, ""
	}
	switch e.phase {
	case launchGateInitial:
		if e.digest != digest {
			e.phase = launchGateAbandoned
			return nil, ""
		}
	case launchGateAwaitYes:
		if keys != "\r" {
			e.phase = launchGateAbandoned
			return nil, ""
		}
		e.phase = launchGateObserveYes
		e.digest = digest
		e.observed = now
		return nil, ""
	case launchGateObserveYes:
		if keys != "\r" || e.digest != digest {
			e.phase = launchGateAbandoned
			return nil, ""
		}
	default:
		return nil, ""
	}
	if now.Sub(e.observed) < launchGateSettle {
		return nil, ""
	}
	if keys != "\r" {
		keys = keys[:len(keys)-1]
	}
	return e, keys
}

// A gate never queues behind an operation which may itself be waiting on trust.
func tryLaunchGateLane(lane *composerLane) (uint64, bool) {
	if !lane.mu.TryLock() {
		return 0, false
	}
	defer lane.mu.Unlock()
	if lane.nextTicket != lane.servingTicket || lane.stopsInFlight != 0 || lane.retired || lane.ownerEnding || lane.recycling {
		return 0, false
	}
	lane.nextTicket++
	return lane.barrier, true
}

func (d *Daemon) launchGateInputClean(id string) bool {
	if !d.directInput.mu.TryLock() {
		return false
	}
	defer d.directInput.mu.Unlock()
	_, unresolved := d.directInput.unresolved[id]
	return !unresolved
}

func (d *Daemon) answerLaunchGate(id string, snap *vt.Snap, identity launchGateIdentity) {
	if d.api == nil || d.core == nil {
		return
	}
	select {
	case <-d.closing:
		d.abandonLaunchGateBinding(id, identity)
		return
	default:
	}
	// Lifecycle authoring primes this cache; a missing/busy cache grants no input.
	current, ok := d.cachedLaunchGateIdentity(id)
	if !ok || current != identity {
		d.abandonLaunchGateBinding(id, identity)
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
	e, keys := d.observeLaunchGate(id, identity, g, snap, time.Now())
	if e == nil {
		return
	}
	sub, err := d.api.tap.subscribe(id, readWrite)
	if err != nil {
		d.abandonLaunchGateBinding(id, identity)
		return
	}
	// Detach only after all later defers release lifecycle/input/FIFO holds.
	defer func() { _ = sub.Close() }()
	lane := d.composerLaneFor(id)
	barrier, ok := tryLaunchGateLane(lane)
	if !ok {
		d.abandonLaunchGateBinding(id, identity)
		return
	}
	defer lane.leave()
	if !lane.endMu.TryLock() {
		d.abandonLaunchGateBinding(id, identity)
		return
	}
	defer lane.endMu.Unlock()
	attempt := func() error {
		if !lane.mu.TryLock() {
			return errors.New("launch gate: composer state busy")
		}
		defer lane.mu.Unlock()
		if lane.retired || lane.ownerEnding || lane.recycling || lane.barrier != barrier || lane.stopsInFlight != 0 {
			return errors.New("launch gate: lifecycle changed")
		}
		select {
		case <-d.closing:
			return errors.New("launch gate: daemon closing")
		default:
		}
		current, ok := d.cachedLaunchGateIdentity(id)
		if !ok || current != identity || !d.launchGateInputClean(id) || d.anyControlled(id) {
			return errors.New("launch gate: input custody changed")
		}
		live, err := sub.launchGateSnapshot(identity)
		if err != nil {
			return err
		}
		answer, ok := g.LaunchGateKeys(live)
		d.gateMu.Lock()
		digest := e.digest
		d.gateMu.Unlock()
		if !ok || !launchGateShape(answer) || launchGateDigest(live) != digest || (keys == "\r" && answer != "\r") || (keys != "\r" && answer != keys+"\r") {
			return errors.New("launch gate: current dialog changed")
		}
		d.gateMu.Lock()
		valid := d.gateDone[id] == e && e.digest == digest && (e.phase == launchGateInitial || e.phase == launchGateObserveYes) && time.Since(e.observed) >= launchGateSettle
		if valid {
			if keys == "\r" {
				e.phase = launchGateConsumed
			} else {
				e.phase = launchGateAwaitYes
			}
		}
		d.gateMu.Unlock()
		if !valid {
			return errors.New("launch gate: observation retired")
		}
		return sub.tryLaunchGateControl([]byte(keys), func() bool {
			d.gateMu.Lock()
			defer d.gateMu.Unlock()
			phase := launchGateAwaitYes
			if keys == "\r" {
				phase = launchGateConsumed
			}
			return d.gateDone[id] == e && e.phase == phase
		})
	}
	fenced := attempt
	if d.remoteSrv != nil {
		next := fenced
		fenced = func() error { return d.remoteSrv.TryWithInputFence(id, next) }
	}
	if d.srv != nil {
		next := fenced
		fenced = func() error { return d.srv.TryWithInputFence(id, next) }
	}
	if err = fenced(); err != nil {
		d.abandonLaunchGateBinding(id, identity)
		log.Printf("skeleton: session %s: launch gate attempt refused or uncertain: %v", id, err)
	}
}
