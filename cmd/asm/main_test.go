package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
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
	for _, want := range []string{"asm()", "opencode -s", "claude --resume", "IFS=$'\\t'", `command asm "$@"`, "list|ls|doctor|init", "~*|/*|.*|*/*", `[[ -d "$1" ]]`} {
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

func TestRunListScopeByDirAndPositionalPath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	createLegacyDB(t, dbPath)
	t.Setenv("ASM_OPENCODE_DB", dbPath)
	t.Setenv("ASM_CLAUDE_PROJECTS", filepath.Join(dir, "no-claude"))

	all := listJSON(t)
	if len(all) != 2 {
		t.Fatalf("unscoped sessions = %d, want 2", len(all))
	}
	existing := all[0].Directory

	scoped := listJSON(t, "--dir", filepath.Join(existing, "gone"))
	if len(scoped) != 1 || scoped[0].Title != "Old work" {
		t.Errorf("--dir gone sessions = %+v, want only Old work", scoped)
	}

	positional := listJSON(t, existing)
	if len(positional) != 2 {
		t.Errorf("positional path sessions = %d, want 2", len(positional))
	}

	empty := listJSON(t, filepath.Join(dir, "does-not-exist"))
	if len(empty) != 0 {
		t.Errorf("unmatched scope sessions = %+v, want none", empty)
	}
}

func TestRunListHereScopesToCurrentRepository(t *testing.T) {
	requireGitBinary(t)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	createLegacyDB(t, dbPath)
	t.Setenv("ASM_OPENCODE_DB", dbPath)
	t.Setenv("ASM_CLAUDE_PROJECTS", filepath.Join(dir, "no-claude"))

	existing := listJSON(t)[0].Directory
	runGit(t, existing, "init", "-q")
	sub := filepath.Join(existing, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(sub)
	if got := listJSON(t, "--here"); len(got) != 2 {
		t.Errorf("--here from repo subdirectory sessions = %d, want 2", len(got))
	}

	t.Chdir(t.TempDir())
	if got := listJSON(t, "--here"); len(got) != 0 {
		t.Errorf("--here outside the repo sessions = %d, want 0", len(got))
	}
}

func TestReorderFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"flags first", []string{"--dir", "y", "x"}, []string{"--dir", "y", "x"}},
		{"flag after path", []string{"x", "--dir", "y", "z"}, []string{"--dir", "y", "x", "z"}},
		{"inline value", []string{"x", "--dir=y"}, []string{"--dir=y", "x"}},
		{"bool flag after path", []string{"x", "--no-preview"}, []string{"--no-preview", "x"}},
		{"terminator", []string{"x", "--", "--dir"}, []string{"--", "x", "--dir"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := reorderFlags(test.args, pickFlagArity)
			if strings.Join(got, "\x00") != strings.Join(test.want, "\x00") {
				t.Errorf("reorderFlags(%v) = %v, want %v", test.args, got, test.want)
			}
		})
	}
}

func TestLooksLikeScopeArg(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		arg  string
		want bool
	}{
		{".", true},
		{"..", true},
		{"~/git", true},
		{"/tmp", true},
		{"./project", true},
		{"parent/child", true},
		{dir, true},
		{"list", false},
		{"wat", false},
	}
	for _, test := range tests {
		if got := looksLikeScopeArg(test.arg); got != test.want {
			t.Errorf("looksLikeScopeArg(%q) = %v, want %v", test.arg, got, test.want)
		}
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

// listJSON runs `asm list --json` with extra arguments and decodes the result.
func listJSON(t *testing.T, args ...string) []provider.Session {
	t.Helper()
	full := append([]string{"list", "--json"}, args...)
	var stdout, stderr bytes.Buffer
	if code := run(full, &stdout, &stderr); code != 0 {
		t.Fatalf("asm %v exit = %d, stderr = %s", full, code, stderr.String())
	}
	var sessions []provider.Session
	if err := json.Unmarshal(stdout.Bytes(), &sessions); err != nil {
		t.Fatalf("decode json: %v\noutput: %s", err, stdout.String())
	}
	if sessions == nil {
		t.Fatalf("json output is null, want an empty array: %s", stdout.String())
	}
	return sessions
}

func requireGitBinary(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"-C", dir}, args...)
	if out, err := exec.Command("git", cmdArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
