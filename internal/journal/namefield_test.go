package journal

// FAILING-FIRST (TDD RED, GG-5) for the DURABLE-FORMAT half of adding Record.Name.
//
// agentfield_test.go asked these three questions when Agent was added and this file asks them
// again, because the answers are properties of THIS field addition and not of the last one:
//
//	1. does the CURRENT build read a record written BEFORE the field existed?
//	2. does a PREVIOUS build read a record written AFTER it?
//	3. does SchemaVersion have to bump?
//
// The answers are yes, yes and no, for Agent's reasons unchanged: an unknown JSON key is
// ignored by encoding/json, an absent one decodes to "" which is exactly this seam's meaning
// for "this record carries no name", and DecodeRecord REJECTS a record whose schema_version
// exceeds the build's own -- so a bump would make every older daemon refuse the whole journal
// to gain nothing.

import (
	"encoding/json"
	"strings"
	"testing"
)

// preNameSchemaVersion is the constant shipped by the last build that had no Name field. It is
// hardcoded because it is history, not configuration.
const preNameSchemaVersion = 1

// preNameLine is a real journal line as builds before the Name field wrote it -- including the
// Agent key, because that field already shipped, so this fixture is the CURRENT installed
// format rather than an ancestor of it. A literal rather than something re-encoded by this
// build: a fixture produced by the code under test cannot prove anything about the code that
// came before it.
const preNameLine = `{"schema_version":1,"cursor":42,"ts":"2026-07-30T10:00:00Z","session_id":"m/s1","type":"launched","agent":"claude"}`

func TestDecodeRecordWrittenBeforeNameExisted(t *testing.T) {
	rec, err := DecodeRecord([]byte(preNameLine))
	if err != nil {
		t.Fatalf("DecodeRecord rejected a pre-Name journal line: %v -- an installed journal must "+
			"still load after the upgrade", err)
	}
	if rec.Name != "" {
		t.Errorf("Name = %q decoded from a line that carries no name key; want the empty string", rec.Name)
	}
	// Non-vacuity: the rest of the record must actually have decoded, or the assertion above
	// is measuring a zero value produced by a failed parse.
	if rec.Cursor != 42 || rec.SessionID != "m/s1" || rec.Type != TypeLaunched || rec.Agent != "claude" {
		t.Fatalf("pre-Name line decoded to %+v; want cursor 42, session m/s1, type launched, agent claude", rec)
	}
}

// TestPreviousBuildReadsARecordCarryingAName is the DOWNGRADE direction, which no amount of
// running the current build can exercise. It simulates the older reader the only honest way
// available -- by decoding into the Record shape that build had, which is this one minus Name.
func TestPreviousBuildReadsARecordCarryingAName(t *testing.T) {
	line := EncodeRecord(Record{
		SchemaVersion: SchemaVersion,
		Cursor:        43,
		SessionID:     "m/s1",
		Type:          TypeLaunched,
		Agent:         "claude",
		Name:          "api refactor",
	})
	if !strings.Contains(string(line), `"name":"api refactor"`) {
		t.Fatalf("EncodeRecord wrote %s; the fixture must actually carry a name or the downgrade "+
			"check below is measuring nothing", line)
	}

	// The previous build's Record: every key it knew, and no Name.
	var old struct {
		SchemaVersion int    `json:"schema_version"`
		Cursor        uint64 `json:"cursor"`
		SessionID     string `json:"session_id"`
		Type          string `json:"type"`
		Agent         string `json:"agent,omitempty"`
	}
	if err := json.Unmarshal(line, &old); err != nil {
		t.Fatalf("a build predating Name could not parse a record carrying one: %v", err)
	}
	if old.SchemaVersion > preNameSchemaVersion {
		t.Fatalf("schema_version %d exceeds the %d that build gates on, so it would REJECT this "+
			"record outright -- losing the journal, not just the name", old.SchemaVersion, preNameSchemaVersion)
	}
	if old.Cursor != 43 || old.SessionID != "m/s1" || old.Type != string(TypeLaunched) || old.Agent != "claude" {
		t.Errorf("previous build decoded %+v; want cursor 43, session m/s1, type launched, agent claude", old)
	}
}

func TestNameAdditionDidNotBumpTheSchema(t *testing.T) {
	if SchemaVersion != preNameSchemaVersion {
		t.Errorf("SchemaVersion = %d, was %d before Name was added. A bump makes every daemon "+
			"predating it REJECT every record this build writes (DecodeRecord's future-version "+
			"gate), which costs them the whole journal to gain nothing: an absent key already "+
			"decodes to the empty string, which is this seam's own meaning for 'carries no name'. "+
			"If the bump is deliberate it needs an ADR, not a test edit.", SchemaVersion, preNameSchemaVersion)
	}
}
