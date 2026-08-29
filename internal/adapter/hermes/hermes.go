// Package hermes implements Swarm's pure adapter strategy for the
// NousResearch Hermes Agent classic CLI. It owns no process, terminal, file,
// socket, or configuration: core resolves and executes the argv it describes.
package hermes

import (
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/vt"
)

const (
	binary             = "hermes"
	sessionMarker      = "Session:"
	resumeHeader       = "Resume this session with:"
	resumeCommand      = "hermes --resume "
	classicSessionSize = 22
)

// hermesAdapter has no fields so values are immutable, goroutine-safe strategy
// objects.
type hermesAdapter struct{}

var _ adapter.Adapter = hermesAdapter{}
var _ adapter.ConversationIDValidator = hermesAdapter{}

// New constructs the Hermes adapter.
func New() adapter.Adapter { return hermesAdapter{} }

func (hermesAdapter) Name() string { return "hermes" }

func (hermesAdapter) Binary() string { return binary }

func (hermesAdapter) VersionArgs() []string { return []string{"--version"} }

// ParseVersion extracts the three-component dotted numeric token immediately
// following the official product name in `hermes --version`. Hermes 0.20.6
// prints:
//
//	Hermes Agent v0.20.6 (2026.8.27) · upstream aff5125f
//
// Requiring the product name prevents an unrelated executable named hermes, or
// the build-date token later in the banner, from being accepted as the agent.
// Optional leading "v" and surrounding banner punctuation are tolerated. A
// component-length bound keeps malformed, unbounded input from becoming a
// plausible version that the core's integer comparison cannot represent.
func (hermesAdapter) ParseVersion(output string) (string, bool) {
	const product = "Hermes Agent"
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, product) {
			continue
		}
		fields := strings.Fields(line[len(product):])
		if len(fields) == 0 {
			continue
		}
		candidate := strings.Trim(fields[0], "()[]{}<>,;:")
		candidate = strings.TrimPrefix(candidate, "v")
		parts := strings.Split(candidate, ".")
		if len(parts) != 3 || !numericVersionParts(parts) {
			continue
		}
		return candidate, true
	}
	return "", false
}

func numericVersionParts(parts []string) bool {
	const maxComponentDigits = 9
	for _, part := range parts {
		if len(part) == 0 || len(part) > maxComponentDigits {
			return false
		}
		for i := 0; i < len(part); i++ {
			if part[i] < '0' || part[i] > '9' {
				return false
			}
		}
	}
	return true
}

// SupportedVersions pins the live-characterized Hermes release as the floor.
func (hermesAdapter) SupportedVersions() adapter.VersionConstraint {
	return adapter.VersionConstraint{Min: "0.20.6", Max: "9999.0.0"}
}

// Command builds a fresh classic-CLI invocation. --cli is unconditional so a
// user's display.interface or HERMES_TUI setting cannot silently change the
// terminal protocol Swarm characterized.
func (hermesAdapter) Command(spec adapter.LaunchSpec) ([]string, error) {
	argv := commandPrefix(spec.Options)
	argv = append(argv, runtimeOptionFlags(spec.Options)...)
	if spec.InitialPrompt != "" {
		argv = append(argv, "-q", spec.InitialPrompt)
	}
	return argv, nil
}

// Options describes only per-invocation flags. In particular, Swarm does not
// expose Hermes's --worktree because Swarm already owns worktree isolation.
func (hermesAdapter) Options() []adapter.OptionSpec {
	return []adapter.OptionSpec{
		{Key: "profile", Label: "Profile", Type: "string"},
		{Key: "provider", Label: "Provider", Type: "string"},
		{Key: "model", Label: "Model", Type: "string"},
		{
			Key:     "reasoning",
			Label:   "Reasoning",
			Type:    "choice",
			Choices: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"},
		},
		{Key: "toolsets", Label: "Toolsets (comma-separated)", Type: "string"},
		{Key: "skills", Label: "Skills (comma-separated)", Type: "string"},
		{
			Key:     "yolo",
			Label:   "YOLO — auto-approve all tool calls (dangerous)",
			Type:    "bool",
			Default: "false",
		},
	}
}

// SignalSources selects the Hermes-specific grid evaluator in the engine.
func (hermesAdapter) SignalSources() []adapter.SignalSource {
	return []adapter.SignalSource{
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "hermes"}},
	}
}

// Resume builds a classic-CLI resume invocation. --no-restore-cwd requests that
// Hermes keep core's selected working directory and is retained for releases
// where upstream honors it. Hermes 0.20.6 has a later unconditional cwd restore,
// so the documented integration limitation is that this version still returns
// to the session's recorded directory despite the flag.
func (hermesAdapter) Resume(spec adapter.ResumeSpec) ([]string, error) {
	if spec.ConversationID == "" {
		return nil, nil
	}
	argv := commandPrefix(spec.Options)
	argv = append(argv, "--resume", spec.ConversationID, "--no-restore-cwd")
	argv = append(argv, runtimeOptionFlags(spec.Options)...)
	return argv, nil
}

// IsValidConversationID accepts only the calendar-valid classic CLI identity
// format. It deliberately excludes Gateway's different eight-hex ID surface.
// This extension validates captured and saved-session identities; it does not
// widen core's UUID-only external resume/handoff API.
func (hermesAdapter) IsValidConversationID(id string) bool {
	return validClassicSessionID(id)
}

func commandPrefix(options map[string]string) []string {
	argv := []string{binary}
	if profile := options["profile"]; profile != "" {
		argv = append(argv, "--profile", profile)
	}
	return append(argv, "chat", "--cli")
}

// runtimeOptionFlags translates resolved values in a fixed order. Values stay
// individual argv elements and are never shell-interpreted.
func runtimeOptionFlags(options map[string]string) []string {
	var flags []string
	for _, item := range []struct {
		key  string
		flag string
	}{
		{key: "provider", flag: "--provider"},
		{key: "model", flag: "--model"},
		{key: "reasoning", flag: "--reasoning"},
		{key: "toolsets", flag: "--toolsets"},
		{key: "skills", flag: "--skills"},
	} {
		if value := options[item.key]; value != "" {
			flags = append(flags, item.flag, value)
		}
	}
	if options["yolo"] == "true" {
		flags = append(flags, "--yolo")
	}
	return flags
}

// ExtractConversationID recovers a classic CLI session identity from one
// supplied snapshot. If that first scan happens after a complete graceful exit,
// the final summary wins over an older startup banner because Hermes can rotate
// IDs during /new, /branch, and compression. In the live daemon, however, the
// startup ID is normally captured early and persisted write-once; a later scan
// cannot replace it after a mid-process rotation. The exit preference therefore
// helps only when no earlier ID was latched. Generic prose and bare commands are
// deliberately not identity evidence.
func (hermesAdapter) ExtractConversationID(grid *vt.Snap, tail []byte) (string, bool) {
	raw := string(tail)
	visible := gridText(grid)

	if id, ok := lastExitBlockID(raw); ok {
		return id, true
	}
	if id, ok := lastExitBlockID(visible); ok {
		return id, true
	}

	if id, state := uniqueBannerID(raw); state == identityFound {
		return id, true
	} else if state == identityAmbiguous {
		return "", false
	}
	if id, state := uniqueBannerID(visible); state == identityFound {
		return id, true
	}
	return "", false
}

type identityState uint8

const (
	identityAbsent identityState = iota
	identityFound
	identityAmbiguous
)

// uniqueBannerID accepts only a Session marker in Hermes's bordered startup
// panel, locally corroborated by the model/installation branding row that the
// upstream renderer places immediately before cwd + Session. A lone bordered
// line can be reproduced in Markdown and is not identity evidence. Repeated
// redraws of the same ID are fine; conflicting IDs are ambiguous and rejected
// rather than poisoning write-once persistence.
func uniqueBannerID(text string) (string, identityState) {
	var found string
	lines := strings.Split(text, "\n")
	for lineIndex, line := range lines {
		line = strings.TrimLeft(line, " \t\r")
		if !strings.HasPrefix(line, "│") {
			continue
		}
		line = strings.TrimLeft(strings.TrimPrefix(line, "│"), " \t")
		if !strings.HasPrefix(line, sessionMarker) {
			continue
		}
		if !hermesBannerContext(lines, lineIndex) {
			continue
		}
		candidate := strings.TrimLeft(line[len(sessionMarker):], " \t")
		if lineIndex < len(lines)-1 {
			candidate += "\n"
		}
		id, ok := parseClassicSessionID(candidate)
		if !ok {
			continue
		}
		if found == "" {
			found = id
			continue
		}
		if found != id {
			return "", identityAmbiguous
		}
	}
	if found == "" {
		return "", identityAbsent
	}
	return found, identityFound
}

// hermesBannerContext proves that the Session row belongs to the startup
// panel. build_welcome_banner emits either a configured model line ending in
// "Nous Research" or the explicit first-run "no model configured" line,
// followed by cwd and Session. Requiring that branded row in a short,
// contiguous run of outer-panel rows remains available as soon as Session is
// printed; it does not wait for the much later bottom of the tall tools panel.
func hermesBannerContext(lines []string, sessionLine int) bool {
	const contextLookback = 4
	first := sessionLine - contextLookback
	if first < 0 {
		first = 0
	}
	for i := sessionLine - 1; i >= first; i-- {
		line := strings.TrimLeft(lines[i], " \t\r")
		if !strings.HasPrefix(line, "│") {
			return false
		}
		content := strings.TrimSpace(strings.TrimPrefix(line, "│"))
		if strings.Contains(content, "Nous Research") ||
			strings.Contains(content, "no model configured") {
			return true
		}
	}
	return false
}

// lastExitBlockID returns the ID from the last complete exit block. Hermes's
// header and resume command must be corroborated by the bounded, unbordered
// Session/Duration/Messages summary emitted by the same source routine. The
// source-shaped metrics reject a bare command and shorter quoted chrome in
// assistant prose.
func lastExitBlockID(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	var found string
	for i := 0; i+1 < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != resumeHeader {
			continue
		}
		commandLine := strings.TrimLeft(lines[i+1], " \t")
		if !strings.HasPrefix(commandLine, resumeCommand) {
			continue
		}
		candidate := commandLine[len(resumeCommand):]
		if i+1 < len(lines)-1 {
			candidate += "\n"
		}
		id, ok := parseClassicSessionID(candidate)
		if !ok {
			continue
		}
		if exitSummaryCorroborates(lines, i+2, id) {
			found = id
		}
	}
	return found, found != ""
}

// exitSummaryCorroborates looks only a bounded number of lines after the
// command. Hermes may print a title-resume hint and a blank line before its
// final summary. Once Session is found, the source emits an optional Title and
// then contiguous Duration and Messages fields; all are parsed using their
// exact source grammar. A later unrelated transcript occurrence is not
// corroboration for this exit block.
func exitSummaryCorroborates(lines []string, start int, id string) bool {
	const maxSummaryDistance = 8
	end := start + maxSummaryDistance
	if end > len(lines) {
		end = len(lines)
	}
	for i := start; i < end; i++ {
		line := strings.TrimLeft(lines[i], " \t")
		if strings.HasPrefix(line, "│") {
			continue
		}
		if !strings.HasPrefix(line, sessionMarker) {
			continue
		}
		summaryID, ok := exitSummaryValue(lines[i], sessionMarker)
		if !ok || summaryID != id || !validClassicSessionID(summaryID) {
			return false
		}

		next := i + 1
		if next < end {
			if _, hasTitle := exitSummaryValue(lines[next], "Title:"); hasTitle {
				next++
			}
		}
		if next >= end {
			return false
		}
		duration, ok := exitSummaryValue(lines[next], "Duration:")
		if !ok || !validExitDuration(duration) {
			return false
		}
		next++
		if next >= end {
			return false
		}
		messages, ok := exitSummaryValue(lines[next], "Messages:")
		return ok && validExitMessages(messages)
	}
	return false
}

func exitSummaryValue(line, label string) (string, bool) {
	line = strings.TrimSuffix(line, "\r")
	if !strings.HasPrefix(line, label) {
		return "", false
	}
	rest := line[len(label):]
	value := strings.TrimLeft(rest, " \t")
	if value == rest || value == "" || strings.TrimSpace(value) != value {
		return "", false
	}
	return value, true
}

func validExitDuration(value string) bool {
	fields := strings.Fields(value)
	switch len(fields) {
	case 1:
		return validDurationUnit(fields[0], 's', false)
	case 2:
		return validDurationUnit(fields[0], 'm', true) &&
			validDurationUnit(fields[1], 's', false)
	case 3:
		return validDurationUnit(fields[0], 'h', true) &&
			validDurationUnit(fields[1], 'm', false) &&
			validDurationUnit(fields[2], 's', false)
	default:
		return false
	}
}

func validDurationUnit(token string, unit byte, positive bool) bool {
	if len(token) < 2 || token[len(token)-1] != unit {
		return false
	}
	number := token[:len(token)-1]
	if !canonicalDecimal(number) || positive && number == "0" {
		return false
	}
	if unit == 'h' {
		return true
	}
	return len(number) < 2 || len(number) == 2 && number[0] < '6'
}

func validExitMessages(value string) bool {
	open := strings.Index(value, " (")
	if open <= 0 || !strings.HasSuffix(value, ")") || !canonicalDecimal(value[:open]) {
		return false
	}
	counts := strings.TrimSuffix(value[open+2:], ")")
	user, tools, ok := strings.Cut(counts, " user, ")
	if !ok || !canonicalDecimal(user) || !strings.HasSuffix(tools, " tool calls") {
		return false
	}
	return canonicalDecimal(strings.TrimSuffix(tools, " tool calls"))
}

func canonicalDecimal(value string) bool {
	if value == "" || len(value) > 1 && value[0] == '0' {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

// parseClassicSessionID validates the source-proven classic format:
// YYYYMMDD_HHMMSS_ followed by six lowercase hexadecimal characters. The
// gateway's eight-hex IDs belong to a different protocol and are not accepted.
// A following terminator is mandatory so a transcript read mid-write cannot
// persist a partial identity.
func parseClassicSessionID(text string) (string, bool) {
	if len(text) <= classicSessionSize {
		return "", false
	}
	id := text[:classicSessionSize]
	if !validClassicSessionID(id) {
		return "", false
	}
	if isASCIITokenByte(text[classicSessionSize]) {
		return "", false
	}
	return id, true
}

func validClassicSessionID(id string) bool {
	if len(id) != classicSessionSize {
		return false
	}
	if id[8] != '_' || id[15] != '_' {
		return false
	}
	for i := 0; i < len(id); i++ {
		switch {
		case i == 8 || i == 15:
			continue
		case i < 15 && id[i] >= '0' && id[i] <= '9':
			continue
		case i > 15 && isLowerHex(id[i]):
			continue
		default:
			return false
		}
	}
	if _, err := time.Parse("20060102_150405", id[:15]); err != nil {
		return false
	}
	return true
}

func isLowerHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f'
}

// isASCIITokenByte is intentionally broader than the ID alphabet. It prevents
// accepting a valid-looking prefix of a longer malformed token such as a
// gateway ID or an uppercase/alphanumeric extension, while control bytes,
// ANSI ESC, punctuation, whitespace, and UTF-8 remain valid terminators.
func isASCIITokenByte(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' || value == '_'
}

func gridText(snap *vt.Snap) string {
	if snap == nil {
		return ""
	}
	var text strings.Builder
	for _, line := range snap.Lines {
		for _, run := range line.Runs {
			text.WriteString(run.Text)
		}
		text.WriteByte('\n')
	}
	return text.String()
}
