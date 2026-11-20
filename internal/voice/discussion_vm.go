package voice

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// VM workers come from ordinary swarm spawn. Observe their daemon-recorded
// parentage so the Discord bridge can notify the right conversation after a restart.
func (c *Controller) observeVMWorkers(st State) (bool, error) {
	rows, err := c.workers.List()
	if err != nil {
		return false, err
	}
	parents := map[string]bool{c.binding.SessionID: true}
	for _, row := range rows {
		if row.Role == "manager" && row.ManagerConversationID == c.binding.ConversationID {
			parents[row.ID] = true
		}
	}
	changed := false
	for _, row := range rows {
		if row.SpawnedFrom == "" || row.Role == "manager" || !parents[protocol.NamespacedID(row.EndpointID, row.SpawnedFrom)] {
			continue
		}
		instance := row.ID // Fallback for adapters without native instance metadata.
		if row.Capabilities != nil && row.Capabilities.SessionInstance != "" {
			instance = row.Capabilities.SessionInstance
		}
		k := key(c.binding.ConversationID, key(row.ID, row.ID))
		// Preserve an existing discussion receipt when a parked manager switches to the native CLI.
		existingKey := key(c.binding.ConversationID, key(row.ID, instance))
		if _, ok := st.Workers[existingKey]; ok {
			k = existingKey
		}
		old, found := st.Workers[k]
		w := old
		if !found {
			job := "vm-child-" + hashValue([]string{c.binding.ConversationID, row.ID})
			w = WorkerAssociation{ConversationID: c.binding.ConversationID, Session: row.ID, Parent: row.SpawnedFrom, Project: row.Cwd, JobID: job, AttemptID: job}
		}
		w.VMChild, w.SessionInstance, w.Ended = true, instance, row.Status.Process != status.ProcessRunning
		event := "vm-state-" + hashValue([]string{w.JobID, string(row.Group), row.GroupEnteredAt.UTC().Format(time.RFC3339Nano)})
		_, seen := st.Events[key(c.binding.ConversationID, event)]
		notify := row.Group != status.GroupWorking && !seen
		if old == w && !notify {
			continue
		}
		err := c.store.update(func(state *State) error {
			if err := checkBinding(state, c.binding); err != nil {
				return err
			}
			putRecord(state, "workers", &state.Workers, k, w)
			if !notify {
				return nil
			}
			if _, exists := state.Events[key(c.binding.ConversationID, event)]; exists {
				return nil
			}
			op, err := protocol.NewSemanticOperationID()
			if err != nil {
				return err
			}
			meta, _ := json.Marshal(struct{ Session, Instance, Group string }{row.ID, instance, string(row.Group)})
			text := fmt.Sprintf("Your Swarm worker %s is %s. Inspect its output with the ordinary swarm CLI. Reply to this thread if you have a new result or need input. If a follow-up was waiting, steer the same worker. This status alone does not prove task completion.", row.ID, row.Group)
			request, _ := json.Marshal(struct {
				Text string `json:"text"`
			}{text})
			e := Event{VMWorkerStatus: true, ID: event, WakeID: event, JobID: w.JobID, AttemptID: w.AttemptID, Metadata: meta}
			var saved Wake
			return acceptEventState(state, c.binding, e, Wake{ID: event, OperationID: op, Request: request}, &saved)
		})
		if err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}
