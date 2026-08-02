package gate

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// PB-RUN-3 -- "An explicit foreground/background connectivity policy: what the
// socket does on backgrounding, Doze, App Standby, and battery saver; whether a
// foreground service is used and with what foregroundServiceType. State machine
// documented and tested; the policy is compatible with PB-NET-5's waiting
// mechanism."
//
// PB-RUN-4 -- "FCM message priority is chosen deliberately (normal-priority is
// deferred in Doze; high-priority wakes the device but is quota'd). Decision
// recorded; behavior tested."
//
// These two are tested together because the requirements make them one decision.
// ADR-007 B7 fixes the inbound mechanism as a bounded server-side wait with a
// 25 s ceiling (§6.0, relay.Config.MaxServerWait), chosen to sit under common
// 30-60 s idle-proxy timeouts. A socket parked for 25 s is precisely what Doze,
// App Standby and battery saver exist to kill. So:
//
//   - if a state issues a wait, something must be holding the process up in that
//     state -- being foreground, or a foreground service;
//   - if a state does not sustain the socket, push is the ONLY path by which the
//     machine can reach the phone, which makes the FCM priority load-bearing;
//   - and a normal-priority push is deferred in Doze, so the state that most
//     needs the wake is the state where the cheap priority does not work.
//
// The policy is therefore pinned as a TABLE, not as prose: android/
// connectivity-policy.tsv and android/fcm-priority.tsv. Prose cannot be checked
// for totality, and the failure this slice must prevent is not a wrong decision
// but an UNSTATED one.

func policyPath(t *testing.T) string {
	return filepath.Join(androidRoot(t), "connectivity-policy.tsv")
}

func fcmPolicyPath(t *testing.T) string {
	return filepath.Join(androidRoot(t), "fcm-priority.tsv")
}

// requiredPolicyStates are exactly the conditions PB-RUN-3 enumerates. A policy
// that omits one has left the question open.
var requiredPolicyStates = []string{
	"foreground",
	"background",
	"doze",
	"app_standby",
	"battery_saver",
}

// Vocabularies. A closed vocabulary is what stops "TBD", "maybe" and "" from
// being recorded as decisions.
var (
	socketStates = map[string]bool{"connected": true, "suspended": true, "closed": true}
	yesNo        = map[string]bool{"yes": true, "no": true}
	wakePaths    = map[string]bool{"socket": true, "push": true, "none": true}
	waitOnEntry  = map[string]bool{"keep": true, "cancel": true}

	// The foregroundServiceType tokens Android accepts. Declaring one is
	// mandatory from API 34 and Google reviews the choice, so a typo here is a
	// rejected release, not a warning.
	fgsTypes = map[string]bool{
		"dataSync":           true,
		"connectedDevice":    true,
		"remoteMessaging":    true,
		"shortService":       true,
		"specialUse":         true,
		"mediaPlayback":      true,
		"location":           true,
		"camera":             true,
		"microphone":         true,
		"phoneCall":          true,
		"health":             true,
		"mediaProjection":    true,
		"systemExempted":     true,
		"mediaProcessing":    true,
		"fileManagement":     true,
		"cameraOrMicrophone": true,
	}
)

// maxServerWaitSeconds is §6.0's binding server-side wait ceiling, recorded in
// ADR-007 B7 and implemented as relay.Config.MaxServerWait by slice S6b. It is
// duplicated as a literal here on purpose: S6b is in flight, so a compile-time
// reference to the field would couple this slice's RED run to another slice's
// GREEN run. Once S6b lands, a direct reference is the better form.
const maxServerWaitSeconds = 25

type policyRow struct {
	state       string
	socket      string
	maxWaitS    int
	fgs         string
	fgsType     string
	wakePath    string
	waitOnEntry string
	line        int
}

func readConnectivityPolicy(t *testing.T) map[string]policyRow {
	t.Helper()
	body := readFileOrFail(t, policyPath(t), "PB-RUN-3")
	rows := map[string]policyRow{}
	for n, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 7 {
			t.Fatalf("PB-RUN-3: %s:%d has %d tab-separated fields, want 7.\nSchema:\n"+
				"  state\\tsocket\\tmax_wait_s\\tforeground_service\\tfgs_type\\twake_path\\twait_on_entry\n"+
				"  state              one of %v\n"+
				"  socket             connected | suspended | closed\n"+
				"  max_wait_s         ceiling in seconds on a server-side wait issued in this state; 0 = none\n"+
				"  foreground_service yes | no\n"+
				"  fgs_type           an Android foregroundServiceType token, or - when foreground_service is no\n"+
				"  wake_path          socket | push | none\n"+
				"  wait_on_entry      keep | cancel -- what happens to an OUTSTANDING wait when this state is entered\n"+
				"got: %q", mustRel(t, policyPath(t)), n+1, len(f), requiredPolicyStates, line)
		}
		waitS, err := strconv.Atoi(strings.TrimSpace(f[2]))
		if err != nil {
			t.Fatalf("PB-RUN-3: %s:%d max_wait_s=%q is not an integer number of seconds",
				mustRel(t, policyPath(t)), n+1, f[2])
		}
		r := policyRow{
			state:       strings.TrimSpace(f[0]),
			socket:      strings.TrimSpace(f[1]),
			maxWaitS:    waitS,
			fgs:         strings.TrimSpace(f[3]),
			fgsType:     strings.TrimSpace(f[4]),
			wakePath:    strings.TrimSpace(f[5]),
			waitOnEntry: strings.TrimSpace(f[6]),
			line:        n + 1,
		}
		if prev, dup := rows[r.state]; dup {
			t.Fatalf("PB-RUN-3: state %q is declared twice (%s:%d and :%d); a state machine "+
				"with two rows for one state has no defined behaviour",
				r.state, mustRel(t, policyPath(t)), prev.line, r.line)
		}
		rows[r.state] = r
	}
	if len(rows) == 0 {
		t.Fatalf("PB-RUN-3: %s declares no states", mustRel(t, policyPath(t)))
	}
	return rows
}

// TestPBRUN3_PolicyCoversEveryNamedRuntimeCondition is the totality check. This
// is the assertion that makes it impossible for the implementer to leave the
// policy unstated, which is what the requirement is actually for.
func TestPBRUN3_PolicyCoversEveryNamedRuntimeCondition(t *testing.T) {
	rows := readConnectivityPolicy(t)
	for _, want := range requiredPolicyStates {
		if _, ok := rows[want]; !ok {
			have := sortedKeys(rows)
			t.Errorf("PB-RUN-3: no policy row for %q. PB-RUN-3 names backgrounding, Doze, "+
				"App Standby and battery saver explicitly. Declared: %v", want, have)
		}
	}
}

// TestPBRUN3_EveryPolicyCellIsFromAClosedVocabulary. An open field lets "TBD"
// count as a decision.
func TestPBRUN3_EveryPolicyCellIsFromAClosedVocabulary(t *testing.T) {
	for _, name := range sortedKeys(readConnectivityPolicy(t)) {
		r := readConnectivityPolicy(t)[name]
		if !socketStates[r.socket] {
			t.Errorf("PB-RUN-3: state %q: socket=%q, want connected|suspended|closed", r.state, r.socket)
		}
		if !yesNo[r.fgs] {
			t.Errorf("PB-RUN-3: state %q: foreground_service=%q, want yes|no", r.state, r.fgs)
		}
		if !wakePaths[r.wakePath] {
			t.Errorf("PB-RUN-3: state %q: wake_path=%q, want socket|push|none", r.state, r.wakePath)
		}
		if !waitOnEntry[r.waitOnEntry] {
			t.Errorf("PB-RUN-3: state %q: wait_on_entry=%q, want keep|cancel", r.state, r.waitOnEntry)
		}
		if r.maxWaitS < 0 {
			t.Errorf("PB-RUN-3: state %q: max_wait_s=%d", r.state, r.maxWaitS)
		}
	}
}

// TestPBRUN3_WaitCeilingNeverExceedsTheRelayCeiling. A policy that permits a
// longer park than the relay itself will hold is not "compatible with PB-NET-5's
// waiting mechanism"; it is a policy written against a mechanism that does not
// exist.
func TestPBRUN3_WaitCeilingNeverExceedsTheRelayCeiling(t *testing.T) {
	for _, name := range sortedKeys(readConnectivityPolicy(t)) {
		r := readConnectivityPolicy(t)[name]
		if r.maxWaitS > maxServerWaitSeconds {
			t.Errorf("PB-RUN-3: state %q permits a %d s wait; §6.0 and ADR-007 B7 bind the "+
				"server-side wait to %d s", r.state, r.maxWaitS, maxServerWaitSeconds)
		}
	}
}

// TestPBRUN3_ForegroundActuallyUsesTheWaitMechanism. A policy where no state
// issues a wait is internally consistent and useless: PB-NET-5's whole
// mechanism would be unreachable from the app, and the exit criterion's live
// typing would have no inbound path.
func TestPBRUN3_ForegroundActuallyUsesTheWaitMechanism(t *testing.T) {
	rows := readConnectivityPolicy(t)
	fg, ok := rows["foreground"]
	if !ok {
		t.Fatalf("PB-RUN-3: no `foreground` row")
	}
	if fg.maxWaitS <= 0 {
		t.Errorf("PB-RUN-3: the foreground state issues no server-side wait, so the app " +
			"never uses PB-NET-5's inbound mechanism at all and the live tail has no path")
	}
	if fg.socket != "connected" {
		t.Errorf("PB-RUN-3: the foreground state's socket is %q", fg.socket)
	}
}

// TestPBRUN3_AParkedWaitIsHeldUpBySomething is the interaction the brief calls
// out: a 25 s park in the background, Doze, App Standby or battery saver is
// exactly what those mechanisms terminate. Either a foreground service holds the
// process, or the state does not issue a wait. There is no third answer, and an
// implementation that leaves the wait outstanding without an FGS has chosen the
// answer that fails silently on a real handset.
func TestPBRUN3_AParkedWaitIsHeldUpBySomething(t *testing.T) {
	for _, name := range sortedKeys(readConnectivityPolicy(t)) {
		r := readConnectivityPolicy(t)[name]
		if r.maxWaitS == 0 {
			continue
		}
		if r.socket != "connected" {
			t.Errorf("PB-RUN-3: state %q issues a %d s wait on a %q socket",
				r.state, r.maxWaitS, r.socket)
		}
		if r.state != "foreground" && r.fgs != "yes" {
			t.Errorf("PB-RUN-3: state %q parks a wait for up to %d s with no foreground "+
				"service. Doze, App Standby and battery saver exist to kill exactly that; "+
				"either declare the foreground service or set max_wait_s to 0 for this state",
				r.state, r.maxWaitS)
		}
	}
}

// TestPBRUN3_ForegroundServiceTypeIsDeclaredExactlyWhenUsed. From API 34 a
// foreground service without a type does not start, and the type is itself a
// policy decision Google reviews. The check runs in both directions so flipping
// the column without touching the type is caught.
func TestPBRUN3_ForegroundServiceTypeIsDeclaredExactlyWhenUsed(t *testing.T) {
	for _, name := range sortedKeys(readConnectivityPolicy(t)) {
		r := readConnectivityPolicy(t)[name]
		switch r.fgs {
		case "yes":
			if r.fgsType == "-" || r.fgsType == "" {
				t.Errorf("PB-RUN-3: state %q holds a foreground service with no "+
					"foregroundServiceType. Mandatory from API 34", r.state)
				continue
			}
			if !fgsTypes[r.fgsType] {
				t.Errorf("PB-RUN-3: state %q declares foregroundServiceType=%q, which is not "+
					"an Android type token. Legal: %v", r.state, r.fgsType, sortedKeys(fgsTypes))
			}
		case "no":
			if r.fgsType != "-" {
				t.Errorf("PB-RUN-3: state %q declares no foreground service but names "+
					"foregroundServiceType=%q", r.state, r.fgsType)
			}
		}
	}
}

// TestPBRUN3_AnUnsustainedSocketMustNameAWakePath. If the socket does not
// survive the state, the machine has no way to reach the phone except push. A
// row with socket=closed and wake_path=none is a state the app never leaves.
func TestPBRUN3_AnUnsustainedSocketMustNameAWakePath(t *testing.T) {
	for _, name := range sortedKeys(readConnectivityPolicy(t)) {
		r := readConnectivityPolicy(t)[name]
		if r.socket == "connected" {
			continue
		}
		if r.wakePath != "push" {
			t.Errorf("PB-RUN-3/PB-RUN-4: state %q leaves the socket %q but declares "+
				"wake_path=%q. With no socket, push is the only path from the machine to "+
				"the phone -- which is what makes the FCM priority a load-bearing decision "+
				"rather than a preference", r.state, r.socket, r.wakePath)
		}
	}
}

// TestPBRUN3_KeepingAnOutstandingWaitIsOnlyLegalWhereWaitsAre. wait_on_entry
// records what happens to a wait that is ALREADY outstanding when the state is
// entered -- the moment the user presses Home mid-session. "keep" in a state
// that may not issue a wait is a contradiction, and it is the exact shape of the
// bug where a backgrounded app holds a dead socket until an unrelated timeout.
func TestPBRUN3_KeepingAnOutstandingWaitIsOnlyLegalWhereWaitsAre(t *testing.T) {
	for _, name := range sortedKeys(readConnectivityPolicy(t)) {
		r := readConnectivityPolicy(t)[name]
		if r.waitOnEntry == "keep" && (r.maxWaitS == 0 || r.socket != "connected") {
			t.Errorf("PB-RUN-3: state %q keeps an outstanding wait but permits none "+
				"(max_wait_s=%d, socket=%q)", r.state, r.maxWaitS, r.socket)
		}
	}
}

// ---------------------------------------------------------------------------
// PB-RUN-4
// ---------------------------------------------------------------------------

type fcmRow struct {
	class      string
	priority   string
	onExhaust  string
	lineNumber int
}

var (
	fcmPriorities = map[string]bool{"high": true, "normal": true}
	fcmExhaust    = map[string]bool{
		"degrade_to_normal": true,
		"drop":              true,
		"coalesce":          true,
		"n/a":               true,
	}
)

func readFCMPolicy(t *testing.T) map[string]fcmRow {
	t.Helper()
	body := readFileOrFail(t, fcmPolicyPath(t), "PB-RUN-4")
	rows := map[string]fcmRow{}
	for n, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			t.Fatalf("PB-RUN-4: %s:%d has %d tab-separated fields, want 3.\nSchema:\n"+
				"  message_class\\tpriority\\ton_quota_exhausted\n"+
				"  message_class       must include `wake`\n"+
				"  priority            high | normal\n"+
				"  on_quota_exhausted  degrade_to_normal | drop | coalesce | n/a\n"+
				"got: %q", mustRel(t, fcmPolicyPath(t)), n+1, len(f), line)
		}
		r := fcmRow{
			class:      strings.TrimSpace(f[0]),
			priority:   strings.TrimSpace(f[1]),
			onExhaust:  strings.TrimSpace(f[2]),
			lineNumber: n + 1,
		}
		rows[r.class] = r
	}
	if len(rows) == 0 {
		t.Fatalf("PB-RUN-4: %s declares no message classes", mustRel(t, fcmPolicyPath(t)))
	}
	return rows
}

func TestPBRUN4_PriorityIsRecordedPerMessageClass(t *testing.T) {
	rows := readFCMPolicy(t)
	if _, ok := rows["wake"]; !ok {
		t.Fatalf("PB-RUN-4: no `wake` message class. That is the class the whole push path "+
			"exists for. Declared: %v", sortedKeys(rows))
	}
	for _, name := range sortedKeys(rows) {
		r := rows[name]
		if !fcmPriorities[r.priority] {
			t.Errorf("PB-RUN-4: class %q priority=%q, want high|normal", r.class, r.priority)
		}
		if !fcmExhaust[r.onExhaust] {
			t.Errorf("PB-RUN-4: class %q on_quota_exhausted=%q, want one of %v",
				r.class, r.onExhaust, sortedKeys(fcmExhaust))
		}
		// PB-RUN-4 states the quota consequence in the requirement itself, so a
		// high-priority class that does not say what happens when the quota
		// bites has recorded half a decision.
		if r.priority == "high" && r.onExhaust == "n/a" {
			t.Errorf("PB-RUN-4: class %q is high-priority but declares no behaviour when "+
				"the high-priority quota is exhausted", r.class)
		}
	}
}

// TestPBRUN4_DozeWakeRequiresHighPriority is the join between the two tables,
// and it is the assertion that stops each of them being individually defensible
// and jointly wrong: normal-priority FCM messages are DEFERRED in Doze. If the
// connectivity policy says the Doze state's only path back to the phone is push,
// then a normal-priority wake does not arrive until Doze ends -- which may be
// hours, and is exactly when the user is waiting for their agent.
func TestPBRUN4_DozeWakeRequiresHighPriority(t *testing.T) {
	policy := readConnectivityPolicy(t)
	fcm := readFCMPolicy(t)

	doze, ok := policy["doze"]
	if !ok {
		t.Fatalf("PB-RUN-3: no `doze` row (see TestPBRUN3_PolicyCoversEveryNamedRuntimeCondition)")
	}
	wake, ok := fcm["wake"]
	if !ok {
		t.Fatalf("PB-RUN-4: no `wake` class")
	}
	if doze.wakePath == "push" && wake.priority != "high" {
		t.Errorf("PB-RUN-4: the Doze state's wake path is push, but the wake message class "+
			"is %q priority. Normal-priority messages are deferred until Doze ends, so the "+
			"one state that needs the wake is the one state this priority does not reach",
			wake.priority)
	}
	// And the converse: paying the high-priority quota for a class nothing needs.
	if doze.wakePath != "push" && wake.priority == "high" {
		t.Errorf("PB-RUN-4: the wake class is high-priority (quota'd, wakes the device) but "+
			"the Doze state's wake path is %q, so nothing needs it", doze.wakePath)
	}
}

// TestPBRUN34_PolicyIsTrackedAndDocumented. Both tables must be committed, and
// both must carry a header comment naming the requirement -- a bare TSV with no
// provenance is the artifact most likely to be "cleaned up" by a later slice.
func TestPBRUN34_PolicyIsTrackedAndDocumented(t *testing.T) {
	root := repoRoot(t)
	tracked := map[string]bool{}
	for _, f := range trackedFiles(t, root) {
		tracked[f] = true
	}
	for _, p := range []string{policyPath(t), fcmPolicyPath(t)} {
		rel := mustRel(t, p)
		if !tracked[rel] {
			t.Errorf("PB-RUN-3/PB-RUN-4: %s is not tracked by git", rel)
			continue
		}
		body := readFileOrFail(t, p, "PB-RUN-3")
		head := strings.SplitN(body, "\n", 2)[0]
		if !strings.HasPrefix(strings.TrimSpace(head), "#") {
			t.Errorf("PB-RUN-3/PB-RUN-4: %s has no header comment naming the requirement "+
				"and the reason each column exists", rel)
		}
	}
}

var _ = sort.Strings
