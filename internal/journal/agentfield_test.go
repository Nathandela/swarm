package journal

// FAILING-FIRST (TDD RED, GG-5) for the DURABLE-FORMAT half of adding Record.Agent.
//
// Record is not only a wire shape: it is the daemon's own on-disk journal, one JSON line per
// record, carrying a SchemaVersion whose whole job is to make a field-set change fail loudly
// instead of silently (R-JRN.1). So "add a field" is a format change and has to answer three
// questions with tests rather than with confidence:
//
//	1. does the CURRENT build read a record written BEFORE the field existed?
//	2. does a PREVIOUS build read a record written AFTER it?
//	3. does SchemaVersion have to bump?
//
// The answers are yes, yes, and no, and the third follows from the first two. Bumping would
// make things strictly WORSE: DecodeRecord REJECTS a record whose schema_version exceeds the
// build's own, so a bump would make every pre-bump daemon refuse every post-bump record
// outright -- the whole journal, not just the agent -- to gain nothing, because an unknown
// JSON key is already ignored by encoding/json and an absent one already decodes to "" which
// is exactly the seam's meaning for "this record carries no agent".
//
import (
	"encoding/json"
	"strings"
	"testing"
)

// preAgentSchemaVersion is the constant shipped by the last build that had no Agent field. It
// is hardcoded because it is history, not configuration. If a future change bumps
// SchemaVersion, TestAgentAdditionDidNotBumpTheSchema below fails ON PURPOSE: that bump makes
// every older daemon reject the journal, and it should be a deliberate, ADR'd decision rather
// than something that rides along with a field addition.
const preAgentSchemaVersion = 1

// preAgentLine is a real journal line as builds before the Agent field wrote it: every key
// they emitted, and no "agent". It is a literal rather than something re-encoded by this
// build, because a fixture produced by the code under test cannot prove anything about the
// code that came before it.
const preAgentLine = `{"schema_version":1,"cursor":42,"ts":"2026-07-30T10:00:00Z","session_id":"m/s1","type":"launched"}`

func TestDecodeRecordWrittenBeforeAgentExisted(t *testing.T) {
	rec, err := DecodeRecord([]byte(preAgentLine))
	if err != nil {
		t.Fatalf("DecodeRecord rejected a pre-Agent journal line: %v -- an installed journal must "+
			"still load after the upgrade", err)
	}
	if rec.Agent != "" {
		t.Errorf("Agent = %q decoded from a line that carries no agent key; want the empty string", rec.Agent)
	}
	// Non-vacuity: the rest of the record must actually have decoded, or the assertion above
	// is measuring a zero value produced by a failed parse.
	if rec.Cursor != 42 || rec.SessionID != "m/s1" || rec.Type != TypeLaunched {
		t.Fatalf("pre-Agent line decoded to %+v; want cursor 42, session m/s1, type launched", rec)
	}
}

// TestPreviousBuildReadsARecordCarryingAnAgent is question 2: the DOWNGRADE direction, which
// no amount of running the current build can exercise. It simulates the older reader the only
// honest way available -- by decoding into the Record shape that build had, which is this one
// minus Agent -- and checks the version gate that build would have applied.
func TestPreviousBuildReadsARecordCarryingAnAgent(t *testing.T) {
	line := EncodeRecord(Record{
		SchemaVersion: SchemaVersion,
		Cursor:        43,
		SessionID:     "m/s1",
		Type:          TypeLaunched,
		Agent:         "claude",
	})

	// The pre-Agent Record, field for field.
	var old struct {
		SchemaVersion int    `json:"schema_version"`
		Cursor        uint64 `json:"cursor"`
		SessionID     string `json:"session_id"`
		Type          string `json:"type"`
	}
	if err := json.Unmarshal(line, &old); err != nil {
		t.Fatalf("a build predating the Agent field could not parse a record carrying one: %v", err)
	}
	if old.SchemaVersion > preAgentSchemaVersion {
		t.Fatalf("the record declares schema_version %d, which a build supporting %d REJECTS outright "+
			"(DecodeRecord fails loudly on a future version) -- the agent addition would cost that build "+
			"its entire journal, not just the agent", old.SchemaVersion, preAgentSchemaVersion)
	}
	if old.Cursor != 43 || old.SessionID != "m/s1" || old.Type != string(TypeLaunched) {
		t.Errorf("the older shape decoded to %+v; want the record's other fields intact", old)
	}
}

// TestAgentAdditionDidNotBumpTheSchema is question 3, stated as the property rather than as a
// restatement of the constant: a record with no agent must serialise to exactly what previous
// builds wrote. omitempty is what makes that true, and it is also what keeps every existing
// journal segment byte-identical after the upgrade.
func TestAgentAdditionDidNotBumpTheSchema(t *testing.T) {
	if SchemaVersion != preAgentSchemaVersion {
		t.Fatalf("SchemaVersion is %d, was %d before the Agent field. A bump makes every older daemon "+
			"reject every record this build writes; if that is intended it needs its own ADR entry, and "+
			"this test is where the decision is recorded", SchemaVersion, preAgentSchemaVersion)
	}
	line := string(EncodeRecord(Record{
		SchemaVersion: SchemaVersion,
		Cursor:        44,
		SessionID:     "m/s1",
		Type:          TypeExited,
	}))
	if want := `"agent"`; strings.Contains(line, want) {
		t.Errorf("a record with no agent encoded to %s, which carries an %s key; omitempty must keep "+
			"the agentless on-disk form unchanged", line, want)
	}
}
