package protocol

import "errors"

// ValidJournalSourceID accepts the canonical UUID namespace minted by the journal.
func ValidJournalSourceID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || id[14] != '4' || id[19] != '8' && id[19] != '9' && id[19] != 'a' && id[19] != 'b' {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

// JournalRecordsHaveIdentityGap reports notification history lacking exact custody.
func JournalRecordsHaveIdentityGap(records []JournalRecord) bool {
	for _, r := range records {
		if r.SessionID == "" {
			continue
		}
		switch r.Type {
		case "roster", "session_state", "capability_transition", "group_transition", "launched", "exited", "lost", "deleted", "structured_gap":
			if r.SessionInstance == "" {
				return true
			}
		}
	}
	return false
}
func legacyJournalRecords(records []JournalRecord) []JournalRecord {
	var out []JournalRecord
	for i, r := range records {
		if r.SessionInstance != "" || r.Role != "" {
			out = append([]JournalRecord(nil), records...)
			for n := i; n < len(out); n++ {
				out[n].SessionInstance = ""
				out[n].Role = ""
			}
			return out
		}
	}
	return records
}
func (cc *clientConn) qualifyJournalIdentity(reply *Control, source string, gap bool) error {
	if !cc.hasCap(CapJournalSourceIdentity) {
		reply.Journal = legacyJournalRecords(reply.Journal)
		reply.Roster = legacyJournalRecords(reply.Roster)
		return nil
	}
	backend, ok := cc.srv.d.(JournalIdentityBackend)
	if !ok || !ValidJournalSourceID(source) || backend.JournalSourceID() != source {
		return errors.New("journal source identity is unavailable or changed; refresh the atomic snapshot")
	}
	reply.JournalSourceID = source
	reply.JournalHistoryGap = gap || JournalRecordsHaveIdentityGap(reply.Journal) || JournalRecordsHaveIdentityGap(reply.Roster)
	return nil
}
