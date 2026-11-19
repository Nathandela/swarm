package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/schedule"
)

func TestScheduleRunOperationIDFlags(t *testing.T) {
	const operationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	for _, args := range [][]string{
		{"job", "--operation-id", operationID},
		{"--operation-id=" + operationID, "job"},
	} {
		job, id, err := parseScheduleRun(args, io.Discard)
		if err != nil || job != "job" || id != operationID {
			t.Fatalf("parse %q = %q, %q, %v", args, job, id, err)
		}
	}
	_, first, err := parseScheduleRun([]string{"job"}, io.Discard)
	if err != nil || first == "" {
		t.Fatalf("first generated operation ID = %q, %v", first, err)
	}
	_, next, err := parseScheduleRun([]string{"job"}, io.Discard)
	if err != nil || next == "" || first == next {
		t.Fatalf("second generated operation ID = %q, %v", next, err)
	}
	if _, _, err := parseScheduleRun([]string{"job", "--operation-id="}, io.Discard); err == nil {
		t.Fatal("explicitly empty operation ID was accepted")
	}
}

func TestScheduleSpecFlagsCalendarAndLiteralPrompt(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "my prompt")
	const prompt = "Review Gaston. Keep $(literal) and $HOME as prompt text.\n"
	if err := os.WriteFile(file, []byte(prompt), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--name", "Gaston review", "--dir", dir, "--authority", "agent-default", "--workspace", "worktree", "--agents", "codex,claude", "--weekly", "Monday", "--time", "09:00", "--timezone", "Europe/Zurich", "--catch-up", "1h", "--prompt-file", file}
	spec, err := readScheduleSpec(args)
	if err != nil || spec.Prompt != prompt || spec.Calendar.Kind != "weekly" || spec.Grace.Hours() != 1 || len(spec.Agents) != 2 || !spec.AllowOverlap {
		t.Fatalf("spec: %#v, %v", spec, err)
	}
	if _, err = readScheduleSpec(append(args, "--daily")); err == nil {
		t.Fatal("accepted conflicting calendars")
	}
}
func TestScheduleCatalogEditRevision(t *testing.T) {
	dir := t.TempDir()
	store, err := schedule.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "job.json")
	body := `{"name":"review","prompt":"Review setup","directory":"` + dir + `","workspace":"directory","authority":"agent-default","agents":["codex"],"calendar":{"kind":"daily","time":"09:00","time_zone":"UTC"}}`
	if err = os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	v, err := scheduleCatalog(store, []string{"create", "--spec-file", file}, &out)
	if err != nil {
		t.Fatal(err)
	}
	job := v.(schedule.Job)
	if _, err = scheduleCatalog(store, []string{"edit", job.ID, "--spec-file", file, "--revision", "stale"}, &out); err == nil {
		t.Fatal("overwrote stale revision")
	}
	if _, err = scheduleCatalog(store, []string{"list"}, &out); err != nil || !strings.Contains(out.String(), "09:00 UTC") {
		t.Fatalf("list %q: %v", out.String(), err)
	}
}
