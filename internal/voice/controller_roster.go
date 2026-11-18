package voice

import (
	"encoding/json"
	"sort"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

type WorkerView struct {
	Session         string                      `json:"session"`
	SessionInstance string                      `json:"session_instance"`
	Agent           string                      `json:"agent"`
	Name            string                      `json:"name,omitempty"`
	Cwd             string                      `json:"cwd"`
	Group           status.Group                `json:"group"`
	Capabilities    *schema.SessionCapabilities `json:"capabilities"`
	Tracking        *WorkerAssociation          `json:"tracking,omitempty"`
}
type WorkerPage struct {
	Workers       []WorkerView `json:"workers"`
	NextSessionID string       `json:"next_session_id,omitempty"`
}

func (c *Controller) workerPage(input ListInput) (json.RawMessage, error) {
	if c.workers == nil {
		return nil, ErrControllerUnsupported
	}
	rows, err := c.workers.List()
	if err != nil {
		return nil, err
	}
	st, err := c.store.Snapshot()
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}
	page := WorkerPage{Workers: []WorkerView{}}
	for _, row := range rows {
		if row.ID == c.binding.SessionID || row.Role == "manager" || row.ID <= input.AfterSessionID {
			continue
		}
		if len(page.Workers) == limit {
			page.NextSessionID = page.Workers[len(page.Workers)-1].Session
			break
		}
		instance := ""
		if row.Capabilities != nil {
			instance = row.Capabilities.SessionInstance
		}
		var tracking *WorkerAssociation
		if association, ok := st.Workers[key(c.binding.ConversationID, key(row.ID, instance))]; ok {
			copy := association
			tracking = &copy
		}
		candidate := WorkerPage{Workers: append(page.Workers, WorkerView{Session: row.ID, SessionInstance: instance, Agent: row.Agent, Name: row.Name, Cwd: row.Cwd, Group: row.Group, Capabilities: row.Capabilities, Tracking: tracking}), NextSessionID: row.ID}
		raw, err := json.Marshal(candidate)
		if err != nil {
			return nil, err
		}
		_, frameErr := managerToolResponse(raw)
		if len(raw) > MaxUtteranceBytes || frameErr != nil {
			if len(page.Workers) == 0 {
				return nil, errManagerPayloadTooLarge
			}
			page.NextSessionID = page.Workers[len(page.Workers)-1].Session
			break
		}
		page.Workers = candidate.Workers
	}
	return controllerResult(page)
}
