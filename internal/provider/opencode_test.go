package provider

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	openCodeTestSessionTable = `CREATE TABLE session (
		id TEXT PRIMARY KEY,
		project_id TEXT,
		parent_id TEXT,
		directory TEXT,
		title TEXT,
		version TEXT,
		time_created INTEGER,
		time_updated INTEGER,
		time_archived INTEGER,
		agent TEXT,
		model TEXT,
		cost REAL
	)`

	openCodeTestSessionV2Table = `CREATE TABLE session_v2 (
		id TEXT PRIMARY KEY,
		project_id TEXT,
		parent_id TEXT,
		directory TEXT,
		title TEXT,
		version TEXT,
		time_created INTEGER,
		time_updated INTEGER,
		time_archived INTEGER,
		model TEXT,
		cost REAL
	)`

	openCodeTestSessionMessageTable = `CREATE TABLE session_message (
		id TEXT PRIMARY KEY,
		session_id TEXT,
		type TEXT,
		seq INTEGER,
		time_created INTEGER,
		time_updated INTEGER,
		data TEXT
	)`

	openCodeTestMessageTable = `CREATE TABLE message (
		id TEXT PRIMARY KEY,
		session_id TEXT,
		time_created INTEGER,
		time_updated INTEGER,
		data TEXT
	)`

	openCodeTestPartTable = `CREATE TABLE part (
		id TEXT PRIMARY KEY,
		message_id TEXT,
		session_id TEXT,
		time_created INTEGER,
		data TEXT
	)`

	openCodeTestTodoTable = `CREATE TABLE todo (
		session_id TEXT,
		content TEXT,
		status TEXT,
		priority TEXT,
		position INTEGER,
		time_created INTEGER,
		time_updated INTEGER
	)`
)

func openCodeTestDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return path, db
}

func openCodeTestExec(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("exec %q: %v", statement, err)
		}
	}
}

func openCodeTestInsert(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("insert %q: %v", query, err)
	}
}

func TestOpenCodeSessionsUnionAndDedupe(t *testing.T) {
	path, db := openCodeTestDB(t)
	openCodeTestExec(t, db, openCodeTestSessionTable, openCodeTestSessionV2Table)

	openCodeTestInsert(t, db,
		`INSERT INTO session_v2 (id, directory, title, time_created, time_updated) VALUES (?, ?, ?, ?, ?)`,
		"ses_shared", "/v2/shared", "V2 Title", 1000, 2000)
	openCodeTestInsert(t, db,
		`INSERT INTO session (id, directory, title, time_created, time_updated) VALUES (?, ?, ?, ?, ?)`,
		"ses_shared", "/legacy/shared", "Legacy Shared", 900, 1900)

	openCodeTestInsert(t, db,
		`INSERT INTO session_v2 (id, directory, title, time_created, time_updated) VALUES (?, ?, ?, ?, ?)`,
		"ses_empty", "", "", 3000, 4000)
	openCodeTestInsert(t, db,
		`INSERT INTO session (id, directory, title, time_created, time_updated) VALUES (?, ?, ?, ?, ?)`,
		"ses_empty", "/legacy/fallback", "  Legacy   Fallback  ", 2900, 3900)

	openCodeTestInsert(t, db,
		`INSERT INTO session_v2 (id, directory, title, time_created, time_updated) VALUES (?, ?, ?, ?, ?)`,
		"ses_v2_only", "/v2/only", "V2 Only", 5000, 6000)
	openCodeTestInsert(t, db,
		`INSERT INTO session (id, directory, title, time_created, time_updated) VALUES (?, ?, ?, ?, ?)`,
		"ses_legacy_only", "/legacy/only", "Legacy Only", 7000, 8000)

	// Excluded: subagent and archived rows in both tables.
	openCodeTestInsert(t, db,
		`INSERT INTO session_v2 (id, parent_id, directory, title, time_created, time_updated) VALUES (?, ?, ?, ?, ?, ?)`,
		"ses_v2_parent", "ses_shared", "/v2/child", "Child", 100, 200)
	openCodeTestInsert(t, db,
		`INSERT INTO session_v2 (id, directory, title, time_created, time_updated, time_archived) VALUES (?, ?, ?, ?, ?, ?)`,
		"ses_v2_archived", "/v2/archived", "Archived", 100, 200, 123)
	openCodeTestInsert(t, db,
		`INSERT INTO session (id, parent_id, directory, title, time_created, time_updated) VALUES (?, ?, ?, ?, ?, ?)`,
		"ses_legacy_parent", "ses_shared", "/legacy/child", "Child", 100, 200)
	openCodeTestInsert(t, db,
		`INSERT INTO session (id, directory, title, time_created, time_updated, time_archived) VALUES (?, ?, ?, ?, ?, ?)`,
		"ses_legacy_archived", "/legacy/archived", "Archived", 100, 200, 456)

	provider := &OpenCode{DBPath: path}
	if !provider.Available() {
		t.Fatal("Available() = false for an existing database")
	}
	sessions, err := provider.Sessions()
	if err != nil {
		t.Fatalf("Sessions() error: %v", err)
	}
	if len(sessions) != 4 {
		t.Fatalf("Sessions() returned %d sessions, want 4: %+v", len(sessions), sessions)
	}

	byID := make(map[string]Session, len(sessions))
	for _, session := range sessions {
		byID[session.ID] = session
		if session.Agent != AgentOpenCode {
			t.Errorf("session %s agent = %q, want %q", session.ID, session.Agent, AgentOpenCode)
		}
	}
	for _, excluded := range []string{"ses_v2_parent", "ses_v2_archived", "ses_legacy_parent", "ses_legacy_archived"} {
		if _, ok := byID[excluded]; ok {
			t.Errorf("excluded session %s was returned", excluded)
		}
	}

	shared, ok := byID["ses_shared"]
	if !ok {
		t.Fatal("missing ses_shared")
	}
	if shared.Title != "V2 Title" || shared.Directory != "/v2/shared" {
		t.Errorf("ses_shared = title %q dir %q, want v2 values", shared.Title, shared.Directory)
	}
	if !shared.TimeCreated.Equal(time.UnixMilli(1000)) || !shared.TimeUpdated.Equal(time.UnixMilli(2000)) {
		t.Errorf("ses_shared times = %v/%v, want UnixMilli(1000)/UnixMilli(2000)", shared.TimeCreated, shared.TimeUpdated)
	}

	fallback, ok := byID["ses_empty"]
	if !ok {
		t.Fatal("missing ses_empty")
	}
	if fallback.Title != "Legacy Fallback" {
		t.Errorf("ses_empty title = %q, want legacy fallback", fallback.Title)
	}
	if fallback.Directory != "/legacy/fallback" {
		t.Errorf("ses_empty directory = %q, want legacy fallback", fallback.Directory)
	}

	legacyOnly, ok := byID["ses_legacy_only"]
	if !ok {
		t.Fatal("missing ses_legacy_only")
	}
	if legacyOnly.Title != "Legacy Only" || legacyOnly.Directory != "/legacy/only" {
		t.Errorf("ses_legacy_only = %+v, want legacy values", legacyOnly)
	}
	if !legacyOnly.TimeCreated.Equal(time.UnixMilli(7000)) || !legacyOnly.TimeUpdated.Equal(time.UnixMilli(8000)) {
		t.Errorf("ses_legacy_only times = %v/%v, want UnixMilli(7000)/UnixMilli(8000)", legacyOnly.TimeCreated, legacyOnly.TimeUpdated)
	}
}

func TestOpenCodeSessionsLegacyOnlyDatabase(t *testing.T) {
	path, db := openCodeTestDB(t)
	openCodeTestExec(t, db, openCodeTestSessionTable)

	openCodeTestInsert(t, db,
		`INSERT INTO session (id, directory, title, time_created, time_updated) VALUES (?, ?, ?, ?, ?)`,
		"ses_legacy", "/legacy/project", "Legacy Session", 111, 222)
	openCodeTestInsert(t, db,
		`INSERT INTO session (id, parent_id, directory, title, time_created, time_updated) VALUES (?, ?, ?, ?, ?, ?)`,
		"ses_child", "ses_legacy", "/legacy/project", "Child", 111, 222)
	openCodeTestInsert(t, db,
		`INSERT INTO session (id, directory, title, time_created, time_updated, time_archived) VALUES (?, ?, ?, ?, ?, ?)`,
		"ses_old", "/legacy/project", "Archived", 111, 222, 333)

	sessions, err := (&OpenCode{DBPath: path}).Sessions()
	if err != nil {
		t.Fatalf("Sessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("Sessions() returned %d sessions, want 1: %+v", len(sessions), sessions)
	}
	if sessions[0].ID != "ses_legacy" || sessions[0].Title != "Legacy Session" {
		t.Errorf("Sessions() = %+v, want ses_legacy", sessions[0])
	}
}

func TestOpenCodeSessionsMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope", "opencode.db")
	provider := &OpenCode{DBPath: path}
	if provider.Available() {
		t.Fatal("Available() = true for a missing database")
	}
	sessions, err := provider.Sessions()
	if err == nil {
		t.Fatal("Sessions() error = nil, want an error for a missing database")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Sessions() error = %v, want it to mention not found", err)
	}
	if len(sessions) != 0 {
		t.Errorf("Sessions() returned %d sessions for a missing database, want 0", len(sessions))
	}
}

func TestOpenCodePreviewV2(t *testing.T) {
	path, db := openCodeTestDB(t)
	openCodeTestExec(t, db,
		openCodeTestSessionV2Table,
		openCodeTestSessionMessageTable,
		openCodeTestTodoTable,
	)

	openCodeTestInsert(t, db,
		`INSERT INTO session_v2 (id, directory, title, time_created, time_updated, model, cost) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"ses_v2", "/v2/project", "V2 Session", 1000, 2000,
		`{"providerID":"anthropic","id":"claude-sonnet-4-5"}`, 0.42)

	openCodeTestInsert(t, db,
		`INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"msg_1", "ses_v2", "user", 1, 1000, 1000, `{"text":"first question"}`)
	openCodeTestInsert(t, db,
		`INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"msg_2", "ses_v2", "assistant", 2, 1100, 1100, `{"text":"an answer"}`)
	openCodeTestInsert(t, db,
		`INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"msg_3", "ses_v2", "user", 3, 1200, 1200, `{"text":"latest   user message"}`)
	openCodeTestInsert(t, db,
		`INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"msg_4", "ses_v2", "assistant", 4, 1300, 1300, `{"text":"last answer"}`)
	openCodeTestInsert(t, db,
		`INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"msg_5", "ses_v2", "tool", 5, 1400, 1400, `{"text":"tool output"}`)

	openCodeTestInsert(t, db,
		`INSERT INTO todo (session_id, content, status) VALUES (?, ?, ?)`, "ses_v2", "pending task", "pending")
	openCodeTestInsert(t, db,
		`INSERT INTO todo (session_id, content, status) VALUES (?, ?, ?)`, "ses_v2", "working task", "in_progress")
	openCodeTestInsert(t, db,
		`INSERT INTO todo (session_id, content, status) VALUES (?, ?, ?)`, "ses_v2", "done task", "completed")

	preview, err := (&OpenCode{DBPath: path}).Preview("ses_v2")
	if err != nil {
		t.Fatalf("Preview() error: %v", err)
	}
	if preview.LastUserMessage != "latest user message" {
		t.Errorf("LastUserMessage = %q, want %q", preview.LastUserMessage, "latest user message")
	}
	if preview.MessageCount != 4 {
		t.Errorf("MessageCount = %d, want 4", preview.MessageCount)
	}
	if preview.Model != "anthropic/claude-sonnet-4-5" {
		t.Errorf("Model = %q, want anthropic/claude-sonnet-4-5", preview.Model)
	}
	if preview.CostUSD != 0.42 {
		t.Errorf("CostUSD = %v, want 0.42", preview.CostUSD)
	}
	if preview.TodosTotal != 3 || preview.TodosOpen != 2 {
		t.Errorf("todos = %d/%d, want 3 total and 2 open", preview.TodosTotal, preview.TodosOpen)
	}
}

func TestOpenCodePreviewLegacy(t *testing.T) {
	path, db := openCodeTestDB(t)
	openCodeTestExec(t, db,
		openCodeTestSessionTable,
		openCodeTestMessageTable,
		openCodeTestPartTable,
		openCodeTestTodoTable,
	)

	openCodeTestInsert(t, db,
		`INSERT INTO session (id, directory, title, time_created, time_updated, model, cost) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"ses_legacy", "/legacy/project", "Legacy Session", 1000, 2000, `{"id":"gpt-5"}`, 0.25)

	openCodeTestInsert(t, db,
		`INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?)`,
		"msg_1", "ses_legacy", 100, 100, `{"role":"user"}`)
	openCodeTestInsert(t, db,
		`INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?)`,
		"msg_2", "ses_legacy", 200, 200, `{"role":"assistant"}`)
	openCodeTestInsert(t, db,
		`INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?)`,
		"msg_3", "ses_legacy", 300, 300, `{"role":"user"}`)
	openCodeTestInsert(t, db,
		`INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?)`,
		"msg_4", "ses_legacy", 400, 400, `{"role":"assistant"}`)

	openCodeTestInsert(t, db,
		`INSERT INTO part (id, message_id, session_id, time_created, data) VALUES (?, ?, ?, ?, ?)`,
		"part_1", "msg_3", "ses_legacy", 301, `{"type":"text","text":"Hello"}`)
	openCodeTestInsert(t, db,
		`INSERT INTO part (id, message_id, session_id, time_created, data) VALUES (?, ?, ?, ?, ?)`,
		"part_2", "msg_3", "ses_legacy", 302, `{"type":"tool","text":"ignored"}`)
	openCodeTestInsert(t, db,
		`INSERT INTO part (id, message_id, session_id, time_created, data) VALUES (?, ?, ?, ?, ?)`,
		"part_3", "msg_3", "ses_legacy", 303, `{"type":"text","text":"world"}`)

	preview, err := (&OpenCode{DBPath: path}).Preview("ses_legacy")
	if err != nil {
		t.Fatalf("Preview() error: %v", err)
	}
	if preview.LastUserMessage != "Hello world" {
		t.Errorf("LastUserMessage = %q, want %q", preview.LastUserMessage, "Hello world")
	}
	if preview.MessageCount != 4 {
		t.Errorf("MessageCount = %d, want 4", preview.MessageCount)
	}
	if preview.Model != "gpt-5" {
		t.Errorf("Model = %q, want gpt-5", preview.Model)
	}
	if preview.CostUSD != 0.25 {
		t.Errorf("CostUSD = %v, want 0.25", preview.CostUSD)
	}
	if preview.TodosTotal != 0 || preview.TodosOpen != 0 {
		t.Errorf("todos = %d/%d, want 0/0", preview.TodosTotal, preview.TodosOpen)
	}
}

func TestOpenCodePreviewUnknownSession(t *testing.T) {
	path, db := openCodeTestDB(t)
	openCodeTestExec(t, db, openCodeTestSessionV2Table, openCodeTestSessionMessageTable)

	preview, err := (&OpenCode{DBPath: path}).Preview("ses_missing")
	if err != nil {
		t.Fatalf("Preview() error: %v", err)
	}
	if !reflect.DeepEqual(preview, Preview{}) {
		t.Errorf("Preview() = %+v, want zero Preview", preview)
	}
}

func TestOpenCodeDBPathResolution(t *testing.T) {
	t.Run("env override", func(t *testing.T) {
		custom := filepath.Join(t.TempDir(), "custom.db")
		t.Setenv("ASM_OPENCODE_DB", custom)
		if got := OpenCodeDBPath(); got != custom {
			t.Fatalf("OpenCodeDBPath() = %q, want %q", got, custom)
		}
		if got := NewOpenCode().DBPath; got != custom {
			t.Fatalf("NewOpenCode().DBPath = %q, want %q", got, custom)
		}
	})

	t.Run("xdg data home", func(t *testing.T) {
		t.Setenv("ASM_OPENCODE_DB", "")
		xdg := t.TempDir()
		t.Setenv("XDG_DATA_HOME", xdg)
		want := filepath.Join(xdg, "opencode", "opencode.db")
		if got := OpenCodeDBPath(); got != want {
			t.Fatalf("OpenCodeDBPath() = %q, want %q", got, want)
		}
	})

	t.Run("home fallback", func(t *testing.T) {
		t.Setenv("ASM_OPENCODE_DB", "")
		t.Setenv("XDG_DATA_HOME", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		want := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
		if got := OpenCodeDBPath(); got != want {
			t.Fatalf("OpenCodeDBPath() = %q, want %q", got, want)
		}
	})
}
