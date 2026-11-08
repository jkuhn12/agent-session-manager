package provider

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func claudeFixtureRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "claude", "projects")
	if !DirExists(root) {
		t.Fatalf("fixture directory missing: %s", root)
	}
	return root
}

// claudeCopyFixtures copies the fixture tree so tests can mutate mtimes.
func claudeCopyFixtures(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixtures: %v", err)
	}
	return dst
}

func newClaudeForTest(t *testing.T, root string) *Claude {
	t.Helper()
	t.Setenv("ASM_CLAUDE_PROJECTS", root)
	provider := NewClaude()
	if provider.ProjectsDir != root {
		t.Fatalf("ProjectsDir = %q, want %q", provider.ProjectsDir, root)
	}
	return provider
}

func claudeSessionByID(t *testing.T, sessions []Session, id string) Session {
	t.Helper()
	for _, session := range sessions {
		if session.ID == id {
			return session
		}
	}
	t.Fatalf("session %q not found (got %d sessions)", id, len(sessions))
	return Session{}
}

func claudeDiagnosticValue(diagnostics []Diagnostic, key string) (string, bool) {
	for _, diagnostic := range diagnostics {
		if diagnostic.Key == key {
			return diagnostic.Value, true
		}
	}
	return "", false
}

func TestClaudeSessions(t *testing.T) {
	root := claudeFixtureRoot(t)
	provider := newClaudeForTest(t, root)

	if provider.Name() != AgentClaude {
		t.Errorf("Name() = %q, want %q", provider.Name(), AgentClaude)
	}
	if !provider.Available() {
		t.Fatal("Available() = false for fixture root")
	}

	sessions, err := provider.Sessions()
	if err != nil {
		t.Fatalf("Sessions() error: %v", err)
	}
	if len(sessions) != 7 {
		t.Fatalf("Sessions() returned %d sessions, want 7", len(sessions))
	}

	cases := []struct {
		id      string
		title   string
		dir     string
		created string
	}{
		{
			id:      "aaaaaaaa-0000-0000-0000-000000000001",
			title:   "Login bug deep dive", // custom-title beats summary
			dir:     "/Users/me/git/app",
			created: "2026-11-01T10:00:00Z",
		},
		{
			id:      "bbbbbbbb-0000-0000-0000-000000000002",
			title:   "Refactor auth flow", // summary fallback
			dir:     "/Users/me/git/app",
			created: "2026-11-02T09:00:00Z",
		},
		{
			id:      "cccccccc-0000-0000-0000-000000000003",
			title:   "Explain the payment flow", // skips tool_result-only user line
			dir:     "/Users/me/git/first",
			created: "2026-11-03T08:00:00Z",
		},
		{
			id:      "dddddddd-0000-0000-0000-000000000004",
			title:   "Session dddddddd", // no title and no prose
			dir:     "/Users/me/git/nocwd",
			created: "2026-11-04T08:00:00Z",
		},
		{
			id:      "eeeeeeee-0000-0000-0000-000000000005",
			title:   "Before the malformed line", // malformed JSON ignored
			dir:     "/Users/me/git/malformed",
			created: "2026-11-05T08:00:00Z",
		},
		{
			id:      "ffffffff-0000-0000-0000-000000000006",
			title:   "Main sidechain test", // sidechain prose ignored
			dir:     "/Users/me/git/side",
			created: "2026-11-06T08:00:01Z",
		},
		{
			id:      "12121212-0000-0000-0000-000000000007",
			title:   "Top level session",
			dir:     "/Users/me/git/nested",
			created: "2026-11-07T08:00:00Z",
		},
	}

	for _, tc := range cases {
		session := claudeSessionByID(t, sessions, tc.id)
		if session.Agent != AgentClaude {
			t.Errorf("%s: Agent = %q, want %q", tc.id, session.Agent, AgentClaude)
		}
		if session.Title != tc.title {
			t.Errorf("%s: Title = %q, want %q", tc.id, session.Title, tc.title)
		}
		if session.Directory != tc.dir {
			t.Errorf("%s: Directory = %q, want %q", tc.id, session.Directory, tc.dir)
		}
		wantCreated, err := time.Parse(time.RFC3339, tc.created)
		if err != nil {
			t.Fatalf("bad test timestamp %q: %v", tc.created, err)
		}
		if !session.TimeCreated.Equal(wantCreated) {
			t.Errorf("%s: TimeCreated = %v, want %v", tc.id, session.TimeCreated, wantCreated)
		}
		if session.TimeUpdated.IsZero() {
			t.Errorf("%s: TimeUpdated is zero", tc.id)
		}
		if session.DirExists {
			t.Errorf("%s: DirExists = true; providers leave it for merge", tc.id)
		}
	}

	// The subagent transcript below <session>/subagents/ must not be listed.
	if _, found := claudeID(sessions, "99999999-0000-0000-0000-000000000008"); found {
		t.Error("subagent transcript was listed as a top-level session")
	}
}

func claudeID(sessions []Session, id string) (Session, bool) {
	for _, session := range sessions {
		if session.ID == id {
			return session, true
		}
	}
	return Session{}, false
}

func TestClaudePreview(t *testing.T) {
	root := claudeFixtureRoot(t)
	provider := newClaudeForTest(t, root)

	preview, err := provider.Preview("aaaaaaaa-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("Preview() error: %v", err)
	}
	if preview.MessageCount != 3 {
		t.Errorf("MessageCount = %d, want 3", preview.MessageCount)
	}
	if preview.LastUserMessage != "also add a regression test" {
		t.Errorf("LastUserMessage = %q, want last user prose", preview.LastUserMessage)
	}
	if preview.Model != "claude-sonnet-4-5" {
		t.Errorf("Model = %q, want claude-sonnet-4-5", preview.Model)
	}
	if preview.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0", preview.CostUSD)
	}

	// A tool_result-only user line counts as a line but is not selected as the
	// last human message.
	preview, err = provider.Preview("cccccccc-0000-0000-0000-000000000003")
	if err != nil {
		t.Fatalf("Preview() error: %v", err)
	}
	if preview.MessageCount != 3 {
		t.Errorf("tool_result session MessageCount = %d, want 3", preview.MessageCount)
	}
	if preview.LastUserMessage != "Explain the payment flow" {
		t.Errorf("tool_result session LastUserMessage = %q", preview.LastUserMessage)
	}
	if preview.Model != "claude-opus-4-1" {
		t.Errorf("tool_result session Model = %q, want claude-opus-4-1", preview.Model)
	}

	missing, err := provider.Preview("does-not-exist")
	if err != nil {
		t.Fatalf("Preview(missing) error: %v", err)
	}
	if missing != (Preview{}) {
		t.Errorf("Preview(missing) = %+v, want zero Preview", missing)
	}
}

func TestClaudeMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "projects")
	provider := newClaudeForTest(t, missing)

	if provider.Available() {
		t.Error("Available() = true for a missing directory")
	}
	sessions, err := provider.Sessions()
	if err != nil {
		t.Fatalf("Sessions() error for missing directory: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("Sessions() = %d sessions, want none", len(sessions))
	}

	preview, err := provider.Preview("aaaaaaaa-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("Preview() error for missing directory: %v", err)
	}
	if preview != (Preview{}) {
		t.Errorf("Preview() = %+v, want zero Preview", preview)
	}

	diagnostics := provider.Diagnostics()
	if value, ok := claudeDiagnosticValue(diagnostics, "projects dir"); !ok || value != missing {
		t.Errorf("diagnostic projects dir = %q (found=%v), want %q", value, ok, missing)
	}
	if value, ok := claudeDiagnosticValue(diagnostics, "exists"); !ok || value != "no" {
		t.Errorf("diagnostic exists = %q (found=%v), want no", value, ok)
	}
	foundWarn := false
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == "warn" && strings.Contains(diagnostic.Value, "no Claude Code sessions yet") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("missing warning for absent storage: %+v", diagnostics)
	}
}

func TestClaudeDiagnostics(t *testing.T) {
	root := claudeFixtureRoot(t)
	provider := newClaudeForTest(t, root)

	diagnostics := provider.Diagnostics()
	if value, ok := claudeDiagnosticValue(diagnostics, "exists"); !ok || value != "yes" {
		t.Errorf("diagnostic exists = %q (found=%v), want yes", value, ok)
	}
	if value, ok := claudeDiagnosticValue(diagnostics, "project dirs"); !ok || value != "6" {
		t.Errorf("diagnostic project dirs = %q (found=%v), want 6", value, ok)
	}
	if value, ok := claudeDiagnosticValue(diagnostics, "session files"); !ok || value != "7" {
		t.Errorf("diagnostic session files = %q (found=%v), want 7", value, ok)
	}
}

func TestClaudeProjectsDir(t *testing.T) {
	t.Setenv("ASM_CLAUDE_PROJECTS", "/tmp/asm-claude-projects")
	if got := ClaudeProjectsDir(); got != "/tmp/asm-claude-projects" {
		t.Errorf("with ASM_CLAUDE_PROJECTS: got %q", got)
	}

	t.Setenv("ASM_CLAUDE_PROJECTS", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/claude-config")
	want := filepath.Join("/tmp/claude-config", "projects")
	if got := ClaudeProjectsDir(); got != want {
		t.Errorf("with CLAUDE_CONFIG_DIR: got %q, want %q", got, want)
	}

	t.Setenv("CLAUDE_CONFIG_DIR", "")
	want = filepath.Join(HomeDir(), ".claude", "projects")
	if got := ClaudeProjectsDir(); got != want {
		t.Errorf("with no overrides: got %q, want %q", got, want)
	}
}

func TestClaudeTimeUpdatedUsesMtime(t *testing.T) {
	root := claudeCopyFixtures(t, claudeFixtureRoot(t))
	target := filepath.Join(root, "-Users-me-git-app", "aaaaaaaa-0000-0000-0000-000000000001.jsonl")
	mtime := time.Date(2026, time.November, 9, 1, 2, 3, 0, time.UTC)
	if err := os.Chtimes(target, mtime, mtime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	provider := newClaudeForTest(t, root)
	sessions, err := provider.Sessions()
	if err != nil {
		t.Fatalf("Sessions() error: %v", err)
	}
	session := claudeSessionByID(t, sessions, "aaaaaaaa-0000-0000-0000-000000000001")
	if !session.TimeUpdated.Equal(mtime) {
		t.Errorf("TimeUpdated = %v, want mtime %v", session.TimeUpdated, mtime)
	}
	wantCreated, _ := time.Parse(time.RFC3339, "2026-11-01T10:00:00Z")
	if !session.TimeCreated.Equal(wantCreated) {
		t.Errorf("TimeCreated = %v, want transcript timestamp %v", session.TimeCreated, wantCreated)
	}
}

func TestClaudeTailMetadata(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "-Users-me-git-big")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	customID := "abababab-0000-0000-0000-000000000009"
	summaryID := "cdcdcdcd-0000-0000-0000-000000000010"
	writeClaudeLargeTranscript(t, filepath.Join(project, customID+".jsonl"), customID,
		`{"type":"custom-title","customTitle":"Tail custom title"}`)
	writeClaudeLargeTranscript(t, filepath.Join(project, summaryID+".jsonl"), summaryID,
		`{"type":"summary","summary":"Tail summary title"}`)

	provider := newClaudeForTest(t, root)
	sessions, err := provider.Sessions()
	if err != nil {
		t.Fatalf("Sessions() error: %v", err)
	}
	if got := claudeSessionByID(t, sessions, customID).Title; got != "Tail custom title" {
		t.Errorf("custom-title from tail = %q", got)
	}
	if got := claudeSessionByID(t, sessions, summaryID).Title; got != "Tail summary title" {
		t.Errorf("summary from tail = %q", got)
	}
}

// writeClaudeLargeTranscript writes a transcript larger than the head scan
// limit so trailer records can only be found by the tail scan.
func writeClaudeLargeTranscript(t *testing.T, path, sessionID, trailer string) {
	t.Helper()
	var transcript strings.Builder
	transcript.WriteString(`{"type":"user","message":{"role":"user","content":"hello"},"cwd":"/Users/me/git/big","sessionId":"` + sessionID + `","timestamp":"2026-11-10T08:00:00Z"}` + "\n")
	filler := `{"type":"assistant","message":{"role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"` + strings.Repeat("x", 1000) + `"}]},"timestamp":"2026-11-10T08:00:01Z"}` + "\n"
	for transcript.Len() < claudeHeadBytes+claudeTailBytes {
		transcript.WriteString(filler)
	}
	transcript.WriteString(trailer)
	transcript.WriteString("\n")

	if err := os.WriteFile(path, []byte(transcript.String()), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
