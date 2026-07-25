package gate

import (
	"strings"
	"testing"
)

// A deliberately small GitHub-Actions-shaped YAML reader.
//
// gopkg.in/yaml.v3 is in go.sum but NOT in go.mod's require block, and go.mod is
// frozen for this slice (it belongs to S8, under remediation), so importing it
// would need a module edit this slice may not make. The workflow file's shape is
// narrow and fixed, so a scanner over it is honest -- and unlike a hand-written
// regex it is itself unit-tested below, including against the mutations the
// PB-TOOL-7 assertions must catch.

type ciStep struct {
	name string
	uses string
	run  string
	with map[string]string
	env  map[string]string
}

type ciJob struct {
	id              string
	name            string
	runsOn          string
	ifCond          string
	continueOnError bool
	env             map[string]string
	steps           []ciStep
}

// effectiveEnv resolves a variable for this job against the workflow-level
// defaults, job env winning. GitHub applies them in that order, and a check that
// looked only at the job would miss a workflow-wide setting (and vice versa).
func (j ciJob) effectiveEnv(workflow map[string]string, key string) (string, bool) {
	if v, ok := j.env[key]; ok {
		return v, true
	}
	v, ok := workflow[key]
	return v, ok
}

// setupGoVersions returns the `go-version` of every actions/setup-go step in the
// job, in order. Empty when the job never sets Go up.
func (j ciJob) setupGoVersions() []string {
	var out []string
	for _, s := range j.steps {
		if strings.HasPrefix(strings.TrimSpace(s.uses), "actions/setup-go") {
			out = append(out, strings.Trim(strings.TrimSpace(s.with["go-version"]), `"'`))
		}
	}
	return out
}

// runsGoCommand reports whether any step shells out to the go tool. A job that
// does and never sets Go up is running the runner image's preinstalled Go, whose
// version nothing in this repository pins.
func (j ciJob) runsGoCommand() bool {
	for _, line := range strings.Split(j.allRun(), "\n") {
		f := strings.Fields(line)
		for i, tok := range f {
			if tok != "go" {
				continue
			}
			// `go build`, and `GOOS=linux go build`, but not `goreleaser`.
			if i+1 < len(f) {
				return true
			}
		}
	}
	return false
}

// allRun concatenates every `run:` body in the job, which is what the PB-TOOL-7
// assertions search.
func (j ciJob) allRun() string {
	var b strings.Builder
	for _, s := range j.steps {
		b.WriteString(s.run)
		b.WriteString("\n")
	}
	return b.String()
}

func (j ciJob) usesAction(prefix string) bool {
	for _, s := range j.steps {
		if strings.HasPrefix(strings.TrimSpace(s.uses), prefix) {
			return true
		}
	}
	return false
}

type yamlLine struct {
	indent int
	text   string // trimmed of leading space; "" for blank/comment
	raw    string
}

func scanYAMLLines(src string) []yamlLine {
	var out []yamlLine
	for _, raw := range strings.Split(src, "\n") {
		trimmed := strings.TrimLeft(raw, " ")
		indent := len(raw) - len(trimmed)
		trimmed = strings.TrimRight(trimmed, " \t\r")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, yamlLine{indent: -1, text: "", raw: raw})
			continue
		}
		out = append(out, yamlLine{indent: indent, text: trimmed, raw: raw})
	}
	return out
}

// parseCIJobs extracts the `jobs:` mapping.
func parseCIJobs(src string) []ciJob {
	lines := scanYAMLLines(src)

	start := -1
	for i, l := range lines {
		if l.indent == 0 && l.text == "jobs:" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil
	}

	// The block of lines belonging to `jobs:`.
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if lines[i].indent == 0 {
			end = i
			break
		}
	}
	block := lines[start:end]

	jobIndent := -1
	for _, l := range block {
		if l.indent < 0 {
			continue
		}
		if jobIndent < 0 || l.indent < jobIndent {
			jobIndent = l.indent
		}
	}
	if jobIndent < 0 {
		return nil
	}

	var jobs []ciJob
	var cur *ciJob
	var curLines []yamlLine
	flush := func() {
		if cur != nil {
			cur.steps = parseSteps(curLines)
			parseJobScalars(cur, curLines)
			jobs = append(jobs, *cur)
		}
		cur, curLines = nil, nil
	}
	for _, l := range block {
		if l.indent == jobIndent && strings.HasSuffix(l.text, ":") {
			flush()
			cur = &ciJob{id: strings.TrimSuffix(l.text, ":")}
			continue
		}
		if cur != nil {
			curLines = append(curLines, l)
		}
	}
	flush()
	return jobs
}

func parseJobScalars(j *ciJob, lines []yamlLine) {
	minIndent := -1
	for _, l := range lines {
		if l.indent < 0 {
			continue
		}
		if minIndent < 0 || l.indent < minIndent {
			minIndent = l.indent
		}
	}
	for i, l := range lines {
		if l.indent != minIndent {
			continue
		}
		k, v, ok := strings.Cut(l.text, ":")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch key {
		case "name":
			j.name = v
		case "runs-on":
			j.runsOn = v
		case "if":
			j.ifCond = v
		case "continue-on-error":
			j.continueOnError = v == "true"
		case "env":
			j.env = nestedMap(lines, i, minIndent)
		}
	}
}

// nestedMap collects the `k: v` lines indented under lines[at], stopping at the
// first line back at or above baseIndent.
func nestedMap(lines []yamlLine, at, baseIndent int) map[string]string {
	m := map[string]string{}
	for i := at + 1; i < len(lines); i++ {
		l := lines[i]
		if l.indent < 0 {
			continue
		}
		if l.indent <= baseIndent {
			break
		}
		k, v, ok := strings.Cut(l.text, ":")
		if !ok {
			continue
		}
		m[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return m
}

// parseWorkflowEnv reads the top-level `env:` block, which GitHub applies to
// every job unless a job overrides it.
func parseWorkflowEnv(src string) map[string]string {
	lines := scanYAMLLines(src)
	for i, l := range lines {
		if l.indent == 0 && l.text == "env:" {
			return nestedMap(lines, i, 0)
		}
	}
	return map[string]string{}
}

// parseSteps reads the `steps:` sequence, flattening block scalars.
func parseSteps(lines []yamlLine) []ciStep {
	start := -1
	stepsIndent := 0
	for i, l := range lines {
		if l.indent >= 0 && l.text == "steps:" {
			start = i + 1
			stepsIndent = l.indent
			break
		}
	}
	if start < 0 {
		return nil
	}

	var steps []ciStep
	var cur *ciStep
	flush := func() {
		if cur != nil {
			cur.run = strings.TrimRight(cur.run, "\n")
			steps = append(steps, *cur)
		}
		cur = nil
	}

	for i := start; i < len(lines); i++ {
		l := lines[i]
		if l.indent < 0 {
			continue
		}
		if l.indent <= stepsIndent {
			break // dedented out of steps:
		}
		text := l.text
		isItem := strings.HasPrefix(text, "- ")
		if isItem {
			flush()
			cur = &ciStep{}
			text = strings.TrimPrefix(text, "- ")
		}
		if cur == nil {
			continue
		}
		k, v, ok := strings.Cut(text, ":")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		switch key {
		case "name":
			cur.name = strings.Trim(val, `"'`)
		case "uses":
			cur.uses = strings.Trim(val, `"'`)
		case "with":
			// A nested map under the step; its keys sit strictly further right
			// than the `with:` line itself. Kept separate from the step's `env:`
			// so a variable in one namespace cannot satisfy a lookup in the other.
			cur.with = nestedMap(lines, i, l.indent)
		case "env":
			cur.env = nestedMap(lines, i, l.indent)
		case "run":
			if val == "|" || val == ">" || val == "|-" || val == ">-" || val == "" {
				// Block scalar: consume the more-indented lines that follow.
				base := l.indent + 1
				var b strings.Builder
				for j := i + 1; j < len(lines); j++ {
					n := lines[j]
					if n.indent < 0 {
						b.WriteString("\n")
						continue
					}
					if n.indent < base {
						break
					}
					b.WriteString(n.text)
					b.WriteString("\n")
					i = j
				}
				cur.run += b.String()
			} else {
				cur.run += val + "\n"
			}
		}
	}
	flush()
	return steps
}

// ---------------------------------------------------------------------------
// The parser's own tests. Without these the PB-TOOL-7 assertions would rest on
// an unverified reader, and a parser that silently returns nothing makes every
// "a lane exists that does X" check pass or fail for the wrong reason.
// ---------------------------------------------------------------------------

const sampleWorkflow = `name: CI

on:
  push:
  pull_request:

jobs:
  docs:
    name: docs (required files)
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Verify
        run: |
          echo one
          echo two

  android:
    name: android (aar + gradle gate)
    runs-on: ubuntu-latest
    continue-on-error: true
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-java@v4
        with:
          java-version: '17'
      - name: Build AAR
        run: ./android/build-aar.sh
      - name: Gradle gate
        run: ./gradlew lint test
`

func TestCIYAMLParser_ReadsJobsStepsAndBlockScalars(t *testing.T) {
	jobs := parseCIJobs(sampleWorkflow)
	if len(jobs) != 2 {
		t.Fatalf("parsed %d jobs, want 2: %+v", len(jobs), jobs)
	}
	if jobs[0].id != "docs" || jobs[1].id != "android" {
		t.Fatalf("job ids = %q, %q", jobs[0].id, jobs[1].id)
	}
	if got := jobs[0].allRun(); !strings.Contains(got, "echo one") || !strings.Contains(got, "echo two") {
		t.Fatalf("block scalar not flattened: %q", got)
	}
	if !jobs[1].usesAction("actions/setup-java") {
		t.Fatalf("setup-java not detected in %+v", jobs[1].steps)
	}
	if got := jobs[1].allRun(); !strings.Contains(got, "./gradlew lint test") {
		t.Fatalf("inline run not read: %q", got)
	}
	if !jobs[1].continueOnError {
		t.Fatalf("continue-on-error: true not detected")
	}
	if jobs[0].continueOnError {
		t.Fatalf("continue-on-error falsely detected on the docs job")
	}
	if jobs[1].name != "android (aar + gradle gate)" {
		t.Fatalf("job name = %q", jobs[1].name)
	}
}

// The negative control: a workflow with no jobs must parse to nothing, so the
// PB-TOOL-7 assertions fail rather than passing over an empty set.
func TestCIYAMLParser_NoJobsSectionYieldsNoJobs(t *testing.T) {
	if jobs := parseCIJobs("name: CI\non:\n  push:\n"); len(jobs) != 0 {
		t.Fatalf("expected no jobs, got %+v", jobs)
	}
}

// The workflow the ADR-008 assertions read: `with:` maps on steps, and env at
// both the workflow and the job level with the job winning.
const sampleEnvWorkflow = `name: CI

on:
  push:

env:
  GOTOOLCHAIN: local
  SHARED: workflow

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/setup-go@v5
        with:
          go-version: '1.25'
      - name: Test
        run: go test -race ./...

  release:
    runs-on: ubuntu-latest
    env:
      SHARED: job
    steps:
      - uses: actions/setup-go@v5
        with:
          go-version: '1.25.2'
      - name: Release
        run: goreleaser release --snapshot
`

func TestCIYAMLParser_ReadsStepWithMapsAndLayeredEnv(t *testing.T) {
	env := parseWorkflowEnv(sampleEnvWorkflow)
	if env["GOTOOLCHAIN"] != "local" {
		t.Fatalf("workflow env GOTOOLCHAIN = %q, want local (env=%v)", env["GOTOOLCHAIN"], env)
	}

	jobs := parseCIJobs(sampleEnvWorkflow)
	if len(jobs) != 2 {
		t.Fatalf("parsed %d jobs, want 2", len(jobs))
	}
	byID := map[string]ciJob{}
	for _, j := range jobs {
		byID[j.id] = j
	}

	if got := byID["test"].setupGoVersions(); len(got) != 1 || got[0] != "1.25" {
		t.Fatalf("test job setupGoVersions = %v, want [1.25]", got)
	}
	if got := byID["release"].setupGoVersions(); len(got) != 1 || got[0] != "1.25.2" {
		t.Fatalf("release job setupGoVersions = %v, want [1.25.2]", got)
	}

	// Workflow-level env reaches a job that declares none of its own.
	if v, ok := byID["test"].effectiveEnv(env, "GOTOOLCHAIN"); !ok || v != "local" {
		t.Fatalf("test job GOTOOLCHAIN = %q ok=%v, want local", v, ok)
	}
	// A job-level value wins over the workflow-level one.
	if v, _ := byID["release"].effectiveEnv(env, "SHARED"); v != "job" {
		t.Fatalf("release job SHARED = %q, want job (job env must override workflow env)", v)
	}
	if v, _ := byID["test"].effectiveEnv(env, "SHARED"); v != "workflow" {
		t.Fatalf("test job SHARED = %q, want workflow", v)
	}
	// An absent key must report absent, not empty-string-present -- otherwise
	// "GOTOOLCHAIN is unset" and "GOTOOLCHAIN is empty" become the same answer
	// and the unset case stops being detectable.
	if _, ok := byID["test"].effectiveEnv(env, "NOT_SET"); ok {
		t.Fatalf("effectiveEnv reported an absent key as present")
	}

	// `goreleaser release` must not be mistaken for the go tool.
	if byID["release"].runsGoCommand() {
		t.Fatalf("runsGoCommand matched `goreleaser`, which is not the go tool")
	}
	if !byID["test"].runsGoCommand() {
		t.Fatalf("runsGoCommand missed `go test -race ./...`")
	}
}

func TestGoVersionComparison(t *testing.T) {
	mustParse := func(s string) goVersion {
		v, ok := parseGoVersion(s)
		if !ok {
			t.Fatalf("parseGoVersion(%q) failed", s)
		}
		return v
	}
	cases := []struct {
		pin, floor string
		want       bool
	}{
		{"1.24", "1.25.0", false},   // the defect ADR-008 records
		{"1.24.2", "1.25.0", false}, // the release job's exact pin
		{"1.25", "1.25.0", true},    // a release line resolves to its newest patch
		{"1.25.0", "1.25.0", true},
		{"1.25.4", "1.25.0", true},
		{"1.25.0", "1.25.4", false}, // an older patch does not satisfy a newer floor
		{"1.26", "1.25.0", true},
		{"2.0", "1.25.0", true},
		{"1.9", "1.25.0", false}, // minor compared numerically, not as a string
	}
	for _, c := range cases {
		if got := mustParse(c.pin).satisfies(mustParse(c.floor)); got != c.want {
			t.Errorf("Go %s satisfies floor %s = %v, want %v", c.pin, c.floor, got, c.want)
		}
	}
	for _, bad := range []string{"", "stable", "1.25.x", "go1.25", "1"} {
		if _, ok := parseGoVersion(bad); ok {
			t.Errorf("parseGoVersion(%q) accepted a non-version", bad)
		}
	}
}

// The parser must be able to read the repository's REAL workflow, otherwise
// every assertion built on it is vacuous.
func TestCIYAMLParser_ReadsTheRealWorkflow(t *testing.T) {
	jobs := parseCIJobs(readFileOrFail(t, ciWorkflowPath(t), "PB-TOOL-7"))
	if len(jobs) < 5 {
		t.Fatalf("parsed only %d jobs from the real workflow; the reader is broken and "+
			"every PB-TOOL-7 assertion would be meaningless", len(jobs))
	}
	var sawRun bool
	for _, j := range jobs {
		if strings.TrimSpace(j.allRun()) != "" {
			sawRun = true
		}
	}
	if !sawRun {
		t.Fatalf("parsed %d jobs from the real workflow but not one `run:` body", len(jobs))
	}
}
