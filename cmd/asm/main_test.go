package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jkuhn12/agent-session-manager/internal/provider"
)

func TestRunVersionAndInit(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("version exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "asm dev") {
		t.Errorf("version output = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"init"}, &stdout, &stderr); code != 0 {
		t.Fatalf("init exit = %d, stderr = %s", code, stderr.String())
	}
	for _, want := range []string{"asm()", "opencode -s", "claude --resume", "IFS=$'\\t'", `command asm "$@"`, "list|ls|doctor|init"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("init output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"wat"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunListJSONWithLegacyDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	createLegacyDB(t, dbPath)
	t.Setenv("ASM_OPENCODE_DB", dbPath)
	t.Setenv("ASM_CLAUDE_PROJECTS", filepath.Join(dir, "no-claude"))

	var stdout, stderr bytes.Buffer
	if code := run([]string{"list", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}

	var sessions []provider.Session
	if err := json.Unmarshal(stdout.Bytes(), &sessions); err != nil {
		t.Fatalf("decode json: %v\noutput: %s", err, stdout.String())
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2: %+v", len(sessions), sessions)
	}
	if sessions[0].Title != "Latest work" {
		t.Errorf("first title = %q, want Latest work", sessions[0].Title)
	}
	if !sessions[0].DirExists {
		t.Errorf("temp session directory should exist: %+v", sessions[0])
	}
	if sessions[1].DirExists {
		t.Errorf("missing session directory should be flagged gone: %+v", sessions[1])
	}
}

func TestRunListTable(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	createLegacyDB(t, dbPath)
	t.Setenv("ASM_OPENCODE_DB", dbPath)
	t.Setenv("ASM_CLAUDE_PROJECTS", filepath.Join(dir, "no-claude"))

	var stdout, stderr bytes.Buffer
	if code := run([]string{"list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{"AGENT", "UPDATED", "FOLDER", "TITLE", "DIRECTORY", "Latest work", "(gone)"} {
		if !strings.Contains(output, want) {
			t.Errorf("table missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "subagent") {
		t.Errorf("subagent session leaked into table:\n%s", output)
	}
}

func TestRunPickWithoutSessions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ASM_OPENCODE_DB", filepath.Join(dir, "missing.db"))
	t.Setenv("ASM_CLAUDE_PROJECTS", filepath.Join(dir, "missing-claude"))

	var stdout, stderr bytes.Buffer
	if code := run([]string{"pick"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1; stderr = %s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("pick wrote to stdout on failure: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no sessions found") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunDoctor(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	createLegacyDB(t, dbPath)
	t.Setenv("ASM_OPENCODE_DB", dbPath)
	t.Setenv("ASM_CLAUDE_PROJECTS", filepath.Join(dir, "no-claude"))

	var stdout, stderr bytes.Buffer
	if code := run([]string{"doctor"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	for _, want := range []string{"opencode provider", "claude provider", "opencode.db", "merged", "sessions"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("doctor output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestHumanAge(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		then time.Time
		want string
	}{
		{"zero", time.Time{}, "-"},
		{"future", now.Add(time.Hour), "just now"},
		{"seconds", now.Add(-30 * time.Second), "just now"},
		{"minutes", now.Add(-5 * time.Minute), "5m"},
		{"hours", now.Add(-3 * time.Hour), "3h"},
		{"days", now.Add(-3 * 24 * time.Hour), "3d"},
		{"weeks", now.Add(-14 * 24 * time.Hour), "2w"},
		{"months", now.Add(-90 * 24 * time.Hour), "3mo"},
		{"years", now.Add(-800 * 24 * time.Hour), "2y"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := humanAge(now, test.then); got != test.want {
				t.Errorf("humanAge = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSanitizeCell(t *testing.T) {
	if got := sanitizeCell("a\tb\nc\rd"); got != "a b c d" {
		t.Errorf("sanitizeCell = %q", got)
	}
}

// createLegacyDB builds a tiny legacy-schema opencode database: one top-level
// session with an existing directory and one whose directory is gone, plus a
// subagent row that must be excluded.
func createLegacyDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer db.Close()

	schema := `
CREATE TABLE session (
  id text PRIMARY KEY,
  project_id text NOT NULL,
  parent_id text,
  directory text NOT NULL,
  title text NOT NULL,
  time_created integer NOT NULL,
  time_updated integer NOT NULL,
  time_archived integer,
  model text,
  cost real DEFAULT 0 NOT NULL
);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	now := time.Now()
	existing := t.TempDir()
	rows := []struct {
		id, parent, dir, title string
		updated                time.Time
	}{
		{"ses_new", "", existing, "Latest work", now},
		{"ses_old", "", filepath.Join(existing, "gone"), "Old work", now.Add(-48 * time.Hour)},
		{"ses_child", "ses_new", existing, "subagent", now},
	}
	for _, row := range rows {
		_, err := db.Exec(
			`INSERT INTO session (id, project_id, parent_id, directory, title, time_created, time_updated, time_archived, model, cost)
			 VALUES (?, 'proj', ?, ?, ?, ?, ?, NULL, '{"id":"m","providerID":"p"}', 0.5)`,
			row.id, nullable(row.parent), row.dir, row.title, row.updated.UnixMilli(), row.updated.UnixMilli(),
		)
		if err != nil {
			t.Fatalf("insert %s: %v", row.id, err)
		}
	}
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
