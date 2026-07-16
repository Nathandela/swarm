# Agentic Codebase Patterns

This document is the operational companion to [The Agentic Codebase Manifesto](MANIFESTO.md). Where the manifesto establishes principles, this file provides the concrete structures, templates, and schemas that implement them. An AI agent pointed at this document should be able to set up or audit an agentic codebase from scratch.

---

## 1. Reference Architecture

Every agentic codebase follows a consistent top-level structure. The exact contents scale with project maturity, but the skeleton remains stable.

```
project/
├── AGENTS.md                    # Agent entry point (map, not manual)
├── .claude/
│   ├── CLAUDE.md                # Project rules and workflow
│   ├── settings.json            # Tool configuration
│   ├── agents/                  # Verification subagent definitions
│   ├── commands/                # Custom slash commands
│   ├── skills/                  # Workflow skills
│   ├── lessons/                 # Session memory (JSONL, git-tracked)
│   │   └── index.jsonl
│   └── .cache/                  # Rebuildable indexes (gitignored)
├── docs/
│   ├── INDEX.md                 # Documentation navigation hub
│   ├── adr/                     # Architectural Decision Records
│   ├── invariants/              # Safety/liveness/data properties
│   ├── specifications/          # Feature specs with acceptance criteria
│   ├── research/                # Domain knowledge by topic
│   ├── solutions/               # Post-mortem fixes with metadata
│   ├── standards/               # Coding conventions and anti-patterns
│   ├── verification/            # TDD pipeline and review processes
│   ├── governance/              # Philosophy and guiding principles
│   └── archive/                 # Historical specs and deprecated plans
├── src/                         # Source code (single responsibility per file)
├── tests/                       # Test suite (or colocated with src/)
└── .beads/                      # Git-tracked issue management
    ├── issues.jsonl
    └── config.yaml
```

**Scaling by maturity**: An early-stage project needs AGENTS.md, `.claude/CLAUDE.md`, `docs/adr/`, and `docs/research/`. A production system adds invariants, specifications, solutions, standards, and the full verification pipeline. Start lean, grow as the codebase demands.

---

## 2. Agent Entry Point: AGENTS.md

AGENTS.md is the first file an agent reads. It functions as a table of contents, not an encyclopedia. Target length: 50-150 lines for early projects, up to 500 for mature libraries with complex APIs.

### Minimal Template (Early-Stage)

```markdown
# Agent Instructions

## Project Overview
- **Name**: [project-name]
- **Purpose**: [one sentence]
- **Type**: [library | web app | data pipeline | monorepo]
- **Stack**: [languages, frameworks, package manager]

## Build, Test, Run
[bash commands for install, build, test, lint, run]

## Code Style
- Files: kebab-case
- Functions: snake_case (Python) or camelCase (TypeScript)
- Classes/Types: PascalCase
- Constants: SCREAMING_SNAKE_CASE
- [Any project-specific conventions]

## Module Boundaries
[Table or list of modules, their responsibilities, and public interfaces]

## Key Documentation
- Architecture: docs/adr/
- Research: docs/research/
- Rules: .claude/CLAUDE.md

## Landing the Plane (Session Completion)
1. Run all tests (100% pass rate)
2. Run linter (zero violations)
3. Stage changes and commit
4. Push to remote
5. Verify git status shows "up to date with origin"
```

### Extended Template (Production)

Add these sections as the project matures:

```markdown
## Architecture
[ASCII directory tree showing module structure and layer boundaries]

## API Contracts
[Key function signatures, Zod schemas, or Pydantic models]

## Security and Data Handling
[Secrets policy, SQL injection prevention, file path rules, error handling]

## Common Pitfalls
[Table of "DO NOT" patterns with explanations of why]

## Compound Agent Integration
[MCP tools, workflow commands, lesson capture protocol]
```

**The pattern**: Start with what an agent needs to make its first contribution. Add sections only when their absence causes repeated mistakes.

---

## 3. Project Rules: .claude/CLAUDE.md

CLAUDE.md encodes the non-negotiable rules for agent behavior within the project. It inherits from the global `~/.claude/CLAUDE.md` and overrides where project needs differ.

### Core Sections

```markdown
# [Project Name]

## Project Overview
- **Goal**: [one sentence]
- **Current Phase**: [what's being worked on now]
- **Tech Stack**: [languages, data formats, key dependencies]

## Work Tracking
- Use beads for cross-session work (strategic)
- Use Tasks for within-session decomposition (tactical)
- Start: `bd ready` -> pick issue -> `bd update <id> --status=in_progress`
- End: `bd close <id>` -> `bd sync && git push`

## Context Management
- When context drops to 15-10%, STOP and use /compact
- Quality standards remain absolute regardless of context pressure
- Half-baked implementations are never acceptable

## TDD Workflow
1. Define invariants (what must be true)
2. Write tests first (tests call real, not-yet-existing code)
3. Verify tests fail for the right reason
4. Implement minimum code to pass
5. Refactor only when green
6. Run verification pipeline before declaring done

## Critical Rules
- NO post-hoc tests (tests written after implementation are rejected)
- NO mocked business logic (only mock external dependencies)
- NO modifying tests to make them pass
- MANDATORY independent review before completion

## Key Documentation
[Links to roadmap, philosophy, verification guide, invariants]
```

**The pattern**: CLAUDE.md is a contract between human and agent. It specifies what is inviolable (testing discipline, review gates) and adapts to the project's specific technology and workflow.

---

## 4. Architectural Decision Records (ADRs)

ADRs capture the why behind architectural choices. They prevent agents from "fixing" intentional design decisions by making the reasoning explicit and queryable.

### Template

```markdown
# ADR-NNN: Decision Title

**Status**: Proposed | Accepted | Deprecated | Superseded by ADR-XXX
**Date**: YYYY-MM-DD

## Context
What situation requires a decision? What constraints apply?
[4-10 lines: problem statement + constraints]

## Decision
What we decided to do. Be specific and concrete.
[1 sentence + 3-5 implementation bullets]

## Consequences

### Positive
- [Benefit with mechanism explanation]
- [Benefit with mechanism explanation]

### Negative
- [Trade-off, explicitly acknowledged]
- [Trade-off, explicitly acknowledged]

## Alternatives Considered

### Alternative A: [Name]
[2-3 sentences: what it is, why it was rejected]

### Alternative B: [Name]
[2-3 sentences: what it is, why it was rejected]
```

### Conventions

- **Naming**: `ADR-NNN-kebab-case-title.md` (sequential numbering)
- **Location**: `docs/adr/` with an `index.md` tracking all decisions
- **Scope**: One ADR per architectural decision (technology choice, data model, deployment strategy, licensing)
- **Status lifecycle**: Proposed -> Accepted -> Deprecated or Superseded
- **Rejection reasoning**: Always explain why alternatives were rejected, not just what was rejected
- **Known limitations**: Optionally include a "Known Limitations (Accepted)" section for trade-offs the team explicitly accepts

### Index Format

```markdown
# ADR Index

| ADR | Title | Status | Date |
|-----|-------|--------|------|
| [001](ADR-001-title.md) | Short description | Accepted | 2026-01-15 |
| [002](ADR-002-title.md) | Short description | Accepted | 2026-01-20 |
```

---

## 5. Invariants: Safety, Liveness, and Data Properties

Invariants formalize what must always hold true, what must never happen, and what must eventually occur. They follow Lamport's framework and serve as the source of truth for testing. Each module maintains its own invariant file.

### Template

```markdown
# [Module Name] Invariants

**Created**: YYYY-MM-DD
**Component**: src/path/to/module
**Framework**: Lamport Safety/Liveness Properties

## Data Invariants

Properties that MUST ALWAYS be true about data.

**DI-1**: [Property name]
- **Invariant**: [Precise statement of the property]
- **Rationale**: [Why this matters]
- **Validation**: [How to check it]
- **Test**: [Specific test assertion]

**DI-2**: [Property name]
- ...

## Safety Properties

What MUST NEVER happen.

**SP-1**: [Bad thing that must be prevented]
- **Violation example**: [Concrete scenario]
- **Prevention**: [Implementation strategy]
- **Test strategy**: [How to verify the violation cannot occur]

**SP-2**: [Bad thing]
- ...

## Liveness Properties

What MUST EVENTUALLY happen.

**LP-1**: [Good thing that must eventually occur]
- **Timeline**: [Expected duration or deadline]
- **Monitoring**: [How to track progress]
- **Test strategy**: [How to verify eventual completion]

## Edge Cases

| Scenario | Expected Behavior | Test |
|----------|-------------------|------|
| [Empty input] | [Behavior] | [Assertion] |
| [Boundary value] | [Behavior] | [Assertion] |
```

### Compact Format (for small modules)

```markdown
# [Module] Invariants

### Data Invariants
D1: [Property statement]
D2: [Property statement]

### Safety Properties
S1: [Must never happen]
S2: [Must never happen]

### Liveness Properties
L1: [Must eventually happen]
```

### Conventions

- **Naming**: `{module_name}_invariants.md` or `{module_name}.md`
- **Location**: `docs/invariants/` with an `index.md`
- **Numbering**: Hierarchical for large modules (DI-1.2.3), flat for small ones (D1, D2)
- **Connection to tests**: Every invariant maps to at least one test assertion
- **Connection to specs**: Invariants define what must be true; specifications define what to build

---

## 6. Solutions Documentation

Solutions capture post-mortem fixes with structured metadata, transforming one-time debugging insights into searchable institutional knowledge.

### Frontmatter Schema

```yaml
---
title: "Human-readable description of the fix"
date: YYYY-MM-DD
category: "logic-errors | data-integrity | performance | security"
severity: "P1 | P2 | P3"
components:
  - "src/path/to/affected/file.py"
  - "src/path/to/another/file.py"
tags:
  - "domain-tag"
  - "architectural-area"
status: "resolved | in-progress | pending"
commits:
  - "abc1234"
  - "def5678"
---
```

### Body Structure

```markdown
# [Title]

## Summary
[2-3 sentences: what was broken, impact, scope]

## Root Causes
1. **[Cause name]**: [Mechanism explanation]
2. **[Cause name]**: [Mechanism explanation]

## Fixes
[Code diffs or descriptions of changes, organized by root cause]

## Tests
[Number of new tests, what they cover, all passing]

## Prevention
- [Checklist item or pattern to prevent recurrence]
- [Checklist item]
```

### Conventions

- **Location**: `docs/solutions/` organized by category (e.g., `logic-errors/`, `performance/`)
- **One file per incident**: Group related fixes into a single document
- **Cross-reference commits**: Always include git hashes for traceability
- **Feed into lessons**: Key insights from solutions should be captured as lessons for session memory

---

## 7. Research Directories

Research directories preserve domain knowledge behind implementation choices. They prevent agents from re-researching topics that have already been investigated.

### Organization Approaches

**By domain** (product-oriented projects):
```
docs/research/
├── market/           # Customer profiles, market analysis
├── tech/             # Technical benchmarks, tool comparisons
├── legal/            # Compliance, regulatory landscape
├── security/         # Threat models, architecture patterns
├── design/           # UX patterns, competitor analysis
└── business/         # Monetization, marketing, SEO
```

**By phase** (pipeline or phased projects):
```
docs/research/
├── phase_2_strategy/
│   ├── literature/   # Academic and industry literature reviews
│   └── platforms/    # Platform-specific analysis
├── phase_3_data/
│   └── literature/
└── phase_4_features/
    └── literature/
```

**By reference** (library projects):
```
docs/research/
├── index.md          # Summary of all research with key themes
├── source-a.md       # Literature summary with actionable takeaways
└── source-b.md       # Literature summary with actionable takeaways
```

### Content Pattern

Research files are not academic papers. They are structured summaries with actionable takeaways:

- Executive summary (context, findings, implications)
- Key insights organized by theme
- Practical application guidance
- References to external sources
- Connection to ADRs (research informs decisions)

---

## 8. Specifications

Specifications define what to build and when it is done. They are written before implementation and provide the acceptance criteria that tests verify. Where invariants define what must always be true, specifications define what a feature must accomplish.

### Template

```markdown
# [Feature Name]

**Spec ID**: NNNN
**Status**: Draft | Review | Approved | Implemented
**Author**: [name]
**Created**: YYYY-MM-DD

## Goal
[1-2 sentences: what we are achieving and why it matters]

## Context
[Why this feature is needed, historical background, relevant ADRs]

## Requirements
- [ ] [Functional requirement, testable]
- [ ] [Functional requirement, testable]

## Acceptance Criteria
- Given [precondition], when [action], then [expected result]
- Given [precondition], when [action], then [expected result]

## Edge Cases

| Scenario | Expected Behavior |
|----------|-------------------|
| [Boundary condition] | [Behavior] |
| [Error condition] | [Behavior] |

## Constraints
[Technical or business limitations that bound the implementation]
```

### Conventions

- **Location**: `docs/specifications/` organized by phase or module
- **Status lifecycle**: Draft -> Review -> Approved -> Implemented
- **Connection to invariants**: Specs reference which invariants the feature must satisfy
- **Connection to tests**: Each acceptance criterion maps to at least one test

---

## 9. Session Memory: Lesson Capture

Lessons capture insights from corrections, discoveries, and debugging sessions in a queryable format that persists across sessions.

### JSONL Schema

```json
{
  "id": "L[8-char-hex]",
  "type": "quick | lesson",
  "trigger": "Human-readable event that prompted capture",
  "insight": "The actual learning (concrete, specific, actionable)",
  "tags": ["domain-tag", "architectural-area"],
  "source": "manual | cli | mcp",
  "context": {
    "tool": "mcp | cli",
    "intent": "lesson capture | manual learning"
  },
  "created": "ISO-8601-timestamp",
  "confirmed": true,
  "supersedes": [],
  "related": []
}
```

### Quality Gate

Before capturing a lesson, verify all three criteria:

1. **Novel**: Not a duplicate or near-duplicate of an existing lesson
2. **Specific**: Contains concrete details (file paths, error messages, patterns), not vague guidance
3. **Actionable**: Another agent reading this lesson can apply it immediately

If any criterion fails, do not capture.

### Workflow

1. **Session start**: Auto-inject high-severity lessons from previous sessions
2. **Before architectural decisions**: Search existing lessons for relevant prior knowledge
3. **After corrections or discoveries**: Capture the insight with structured metadata
4. **Session end**: Lessons are committed with the rest of the work

### Storage Convention

- **Source of truth**: `.claude/lessons/index.jsonl` (git-tracked, append-only)
- **Search index**: `.claude/.cache/lessons.sqlite` (gitignored, rebuildable from JSONL)
- **Access**: Through MCP tools (primary) or CLI (fallback)

---

## 10. Documentation Hierarchy

Documentation follows a four-level hierarchy. Each level serves a different depth of engagement, and agents enter at the level appropriate to their task.

### Level 0: Fast Onboarding (AGENTS.md, CLAUDE.md)

- What does this project do?
- What is the tech stack?
- What are the non-negotiable rules?
- How do I build, test, and run?

**When to read**: Every session start. Quick fixes. First-time orientation.

### Level 1: Strategic Context (governance/, philosophy, roadmap)

- Why are we building this?
- What principles guide decisions?
- What is the long-term plan?

**When to read**: Before proposing architectural changes. When priorities are unclear.

### Level 2: Tactical Design (adr/, specifications/, architecture/)

- How does this component work?
- What must be true (invariants)?
- What is the API contract?

**When to read**: Before implementing features. When modifying existing modules. When debugging complex behavior.

### Level 3: Deep Dives (research/, solutions/, verification/)

- Why did we make that specific choice?
- How do we debug this class of issue?
- What does the domain literature say?

**When to read**: Before major technology decisions. When encountering unfamiliar domain logic. When a bug pattern recurs.

### Navigation Hub: docs/INDEX.md

```markdown
# Documentation Index

## Quick Start
- [AGENTS.md](../AGENTS.md) - Agent entry point
- [.claude/CLAUDE.md](../.claude/CLAUDE.md) - Project rules

## Architecture
- [ADR Index](adr/index.md) - All architectural decisions
- [Architecture Overview](architecture/) - System diagrams

## Development
- [Invariants](invariants/) - Module correctness properties
- [Specifications](specifications/) - Feature specs
- [Standards](standards/) - Coding conventions

## Knowledge
- [Research](research/) - Domain knowledge
- [Solutions](solutions/) - Past fixes and debugging insights

## Process
- [Verification](verification/) - TDD pipeline and review workflow
- [Governance](governance/) - Philosophy and principles
```

---

## 11. Verification Pipeline

The verification pipeline ensures that agent-produced code meets quality standards through a sequence of specialized review agents. Each agent has a single responsibility and clear authority.

### Standard Pipeline

```
1. /invariant-designer      Define what must be true (pre-code)
         |
2. /test-first-enforcer     Verify tests exist before implementation
         |
3. /property-test-generator  Generate edge-case tests (Hypothesis / fast-check)
         |
4. /anti-cargo-cult-reviewer Reject fake or trivial tests
         |
5. /module-boundary-reviewer Validate information hiding and layer boundaries
         |
6. /implementation-reviewer  FINAL gate (independent authority, cannot be bypassed)
```

### Agent Definition Format

Each verification agent lives in `.claude/agents/{agent-name}/AGENT.md`:

```markdown
# [Agent Name]

## Role
[One sentence: what this agent does]

## Principle
[The intellectual foundation: Lamport, Feynman, Parnas, etc.]

## Checks
- [Specific thing this agent verifies]
- [Specific thing this agent verifies]

## Authority
[What this agent can approve, reject, or escalate]
```

### Exit Criteria

All of the following must be true before declaring work complete:

- [ ] All tests pass (100% pass rate, no skipped tests)
- [ ] Linter reports zero violations
- [ ] No regressions introduced
- [ ] Type checking passes (strict mode)
- [ ] Implementation reviewer approves
- [ ] Changes committed and pushed to remote

---

## 12. Constraint Enforcement

Constraints are encoded as linters, type checkers, and structural tests. They apply automatically to every agent on every run.

### Categories

| Category | Examples | Enforcement |
|----------|----------|-------------|
| **File structure** | Max 400-500 lines per file, max 50-80 lines per function | Custom linter |
| **Naming** | kebab-case files, PascalCase types, snake_case functions | Linter rules |
| **Architecture** | No circular imports, layer boundary enforcement | Structural tests |
| **Types** | Strict mode, no `any`, type hints on public functions | Type checker |
| **Documentation** | JSDoc on public APIs, updated AGENTS.md | CI validation |
| **Dependencies** | License allowlist (MIT, BSD, Apache 2.0), no GPL | Dependency audit |

### Error Messages for Agents

Lint output and error messages are structured for machine consumption:

```
ERROR [file:line] module-boundary-violation: src/api/handler.py imports from src/engine/internal.py
  REMEDIATION: Use the public interface at src/engine/__init__.py instead.
  SEE: docs/adr/ADR-003-layer-boundaries.md
```

The pattern: error identifier, location, violation description, remediation instruction, and reference to the relevant documentation.

---

## 13. Git-Tracked Issue Management

Issue tracking lives in the repository alongside the code, making it accessible to agents without external tool dependencies.

### Structure

```
.beads/
├── issues.jsonl        # Append-only journal (git-tracked)
├── config.yaml         # Configuration
└── beads.db            # SQLite for fast queries (rebuildable)
```

### Workflow

```bash
bd ready                              # List available work
bd show <id>                          # View issue details
bd update <id> --status=in_progress   # Claim work
bd close <id>                         # Complete work
bd sync                               # Commit beads state
```

**Integration with session protocol**: Every session starts with `bd ready` and ends with `bd sync && git push`. Work that is not tracked in beads and pushed to remote does not exist.

---

## Scaling Guide

| Project Phase | Required Patterns | Optional Patterns |
|---------------|-------------------|-------------------|
| **Prototype** | AGENTS.md, CLAUDE.md, basic tests | Research directory |
| **Early development** | + ADRs, research directory, beads | Invariants, specifications |
| **Active development** | + Invariants, specifications, standards | Solutions, verification pipeline |
| **Production** | + Solutions, verification pipeline, lesson capture | Governance, archive |
| **Multi-agent at scale** | All patterns active | Custom linters, background entropy agents |

The principle: adopt patterns when their absence causes pain, not before. Each pattern earns its place by preventing a class of problems that the project has actually encountered.
