package voice

import "github.com/Nathandela/swarm/internal/protocol"

// AcceptRecoverySnapshot consumes an atomic snapshot whose persistent source namespace
// the daemon adapter already verified. A connection ID, PID or guessed history instance is never a namespace.
// Initial connection, gaps and namespace replacement are recovery-only, never fresh task authorization.
func (c *Controller) AcceptRecoverySnapshot(cursor JournalCursor, snapshot JournalSnapshot) (Wake, error) {
	c.scheduler.Lock()
	defer c.scheduler.Unlock()
	id := "snapshot-" + hashValue(struct {
		Conversation string
		Cursor       JournalCursor
	}{c.binding.ConversationID, cursor})
	operation, err := protocol.NewSemanticOperationID()
	if err != nil {
		return Wake{}, err
	}
	return c.store.acceptControllerRecoverySnapshot(c.binding, cursor, snapshot, id, operation)
}
