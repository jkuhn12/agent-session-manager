package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jkuhn12/agent-session-manager/internal/provider"
)

func testSessions() []provider.Session {
	now := time.Now()
	return []provider.Session{
		{
			ID:          "ses_alpha",
			Agent:       provider.AgentOpenCode,
			Directory:   "/tmp/alpha-project",
			Title:       "Fix parser bug",
			TimeCreated: now.Add(-48 * time.Hour),
			TimeUpdated: now,
			DirExists:   true,
		},
		{
			ID:          "ses_beta",
			Agent:       provider.AgentClaude,
			Directory:   "/tmp/beta-project",
			Title:       "Add feature",
			TimeCreated: now.Add(-72 * time.Hour),
			TimeUpdated: now.Add(-2 * time.Hour),
			DirExists:   false,
		},
	}
}

// updateModel forwards msg and runs any resulting commands long enough for
// the list's asynchronous filter results to arrive.
func updateModel(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, cmd := m.Update(msg)
	updated, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", next)
	}
	return drainCommands(t, updated, cmd)
}

func drainCommands(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}

	messages := make(chan tea.Msg, 32)
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() { messages <- c() }()
	}
	run(cmd)

	// Filtering is pure and returns immediately; cursor-blink commands take
	// much longer, so a short window is enough and keeps tests fast.
	timeout := time.After(100 * time.Millisecond)
	for {
		select {
		case msg := <-messages:
			switch value := msg.(type) {
			case tea.BatchMsg:
				for _, sub := range value {
					run(sub)
				}
			case list.FilterMatchesMsg:
				next, nextCmd := m.Update(value)
				if updated, ok := next.(Model); ok {
					m = updated
				}
				run(nextCmd)
			}
		case <-timeout:
			return m
		}
	}
}

func sendRunes(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func TestViewModelAt80x24(t *testing.T) {
	m := NewModel(testSessions(), Options{})
	m = updateModel(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	view := m.View()
	for _, want := range []string{
		"Agent Sessions",
		"[oc]",
		"[cc]",
		"alpha-project",
		"beta-project",
		"Fix parser bug",
		"Add feature",
		"gone",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q\n%s", want, view)
		}
	}

	for i, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 80 {
			t.Errorf("line %d is %d cells wide, want <= 80: %q", i, got, line)
		}
	}
}

func TestTypingFiltersVisibleItems(t *testing.T) {
	sessions := []provider.Session{
		{ID: "alpha", Agent: provider.AgentOpenCode, Directory: "/tmp/alpha-dir", Title: "Alpha task", DirExists: true},
		{ID: "zulu", Agent: provider.AgentClaude, Directory: "/tmp/zulu-dir", Title: "Zulu task", DirExists: true},
	}
	m := NewModel(sessions, Options{})
	m = updateModel(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = sendRunes(t, m, "zulu")

	if got := m.list.FilterValue(); got != "zulu" {
		t.Fatalf("FilterValue() = %q, want %q", got, "zulu")
	}
	if got := m.list.FilterState(); got != list.Filtering {
		t.Fatalf("FilterState() = %v, want filtering", got)
	}

	visible := m.list.VisibleItems()
	if len(visible) != 1 {
		t.Fatalf("visible items = %d, want 1", len(visible))
	}
	item, ok := visible[0].(SessionItem)
	if !ok {
		t.Fatalf("visible item is %T, want SessionItem", visible[0])
	}
	if item.ID != "zulu" {
		t.Errorf("visible session = %q, want zulu", item.ID)
	}
	if view := m.View(); !strings.Contains(view, "zulu-dir") || strings.Contains(view, "alpha-dir") {
		t.Errorf("filtered view did not show only zulu session:\n%s", view)
	}
}

func TestEnterSelectsHighlightedItem(t *testing.T) {
	sessions := []provider.Session{{
		ID:        "ses_123",
		Agent:     provider.AgentOpenCode,
		Directory: "/tmp/project",
		Title:     "Resume me",
		DirExists: true,
	}}
	m := NewModel(sessions, Options{})
	m = updateModel(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	selection, ok := m.Result()
	if !ok {
		t.Fatal("Result() reported no selection after Enter")
	}
	want := Selection{Directory: "/tmp/project", Agent: provider.AgentOpenCode, SessionID: "ses_123"}
	if selection != want {
		t.Errorf("Result() = %+v, want %+v", selection, want)
	}
	if m.Canceled() {
		t.Error("model is canceled after Enter")
	}
}

func TestEscapeAndCtrlCCancel(t *testing.T) {
	sessions := testSessions()

	esc := NewModel(sessions, Options{})
	esc = updateModel(t, esc, tea.WindowSizeMsg{Width: 80, Height: 24})
	esc = updateModel(t, esc, tea.KeyMsg{Type: tea.KeyEsc})
	if !esc.Canceled() {
		t.Error("Esc did not set canceled state")
	}
	if _, ok := esc.Result(); ok {
		t.Error("Esc produced a selection")
	}

	ctrlC := NewModel(sessions, Options{})
	ctrlC = updateModel(t, ctrlC, tea.WindowSizeMsg{Width: 80, Height: 24})
	ctrlC = updateModel(t, ctrlC, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !ctrlC.Canceled() {
		t.Error("Ctrl-C did not set canceled state")
	}
}

func TestEscapeClearsFilterBeforeCanceling(t *testing.T) {
	m := NewModel(testSessions(), Options{})
	m = updateModel(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = sendRunes(t, m, "beta")

	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.Canceled() {
		t.Fatal("first Esc canceled instead of clearing the filter")
	}
	if got := m.list.FilterState(); got != list.Unfiltered {
		t.Fatalf("FilterState() = %v after Esc, want unfiltered", got)
	}

	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.Canceled() {
		t.Error("second Esc did not cancel")
	}
}

func TestCompactHomePath(t *testing.T) {
	home := provider.HomeDir()
	cases := []struct {
		dir  string
		want string
	}{
		{home, "~"},
		{home + "/git/project", "~/git/project"},
		{"/tmp/project", "/tmp/project"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := compactHomePath(tc.dir); got != tc.want {
			t.Errorf("compactHomePath(%q) = %q, want %q", tc.dir, got, tc.want)
		}
	}
}

func TestSplitDirDisplay(t *testing.T) {
	cases := []struct {
		dir, prefix, base string
	}{
		{"~/git/project", "~/git/", "project"},
		{"/tmp/project", "/tmp/", "project"},
		{"/project", "/", "project"},
		{"project", "", "project"},
		{"~", "", "~"},
		{"", "", ""},
	}
	for _, tc := range cases {
		prefix, base := splitDirDisplay(tc.dir)
		if prefix != tc.prefix || base != tc.base {
			t.Errorf("splitDirDisplay(%q) = (%q, %q), want (%q, %q)", tc.dir, prefix, base, tc.prefix, tc.base)
		}
	}
}

func TestRowUsesCompactHomePath(t *testing.T) {
	home := provider.HomeDir()
	sessions := []provider.Session{{
		ID:        "ses_home",
		Agent:     provider.AgentOpenCode,
		Directory: home + "/git/agent-session-manager",
		Title:     "Compact row",
		DirExists: true,
	}}
	m := NewModel(sessions, Options{})
	m = updateModel(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	view := m.View()
	if !strings.Contains(view, "~/git/agent-session-manager") {
		t.Errorf("view does not use compact home path:\n%s", view)
	}
	if strings.Contains(view, home) {
		t.Errorf("view still contains the full home path:\n%s", view)
	}
	for i, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 80 {
			t.Errorf("line %d is %d cells wide, want <= 80: %q", i, got, line)
		}
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"zero", time.Time{}, ""},
		{"just now", now.Add(-30 * time.Second), "just now"},
		{"minutes", now.Add(-5 * time.Minute), "5m"},
		{"hours", now.Add(-2 * time.Hour), "2h"},
		{"days", now.Add(-3 * 24 * time.Hour), "3d"},
		{"weeks", now.Add(-21 * 24 * time.Hour), "3w"},
		{"months", now.Add(-120 * 24 * time.Hour), "4mo"},
		{"years", now.Add(-400 * 24 * time.Hour), "1y"},
		{"future", now.Add(time.Hour), "just now"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := relativeTime(now, tc.t); got != tc.want {
				t.Errorf("relativeTime() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPreviewPane(t *testing.T) {
	sessions := testSessions()
	calls := 0
	options := Options{
		Preview: func(provider.Session) provider.Preview {
			calls++
			return provider.Preview{
				LastUserMessage: "hello preview message",
				MessageCount:    7,
				Model:           "claude-opus",
				CostUSD:         0.12,
				TodosOpen:       2,
				TodosTotal:      5,
			}
		},
	}

	m := NewModel(sessions, options)
	m = updateModel(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	view := m.View()

	for _, want := range []string{
		"preview",
		"hello preview message",
		"7 messages",
		"claude-opus",
		"$0.12",
		"2/5",
		"created",
		"updated",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("preview missing %q\n%s", want, view)
		}
	}
	if calls != 1 {
		t.Errorf("preview loader called %d times, want 1 (cached)", calls)
	}

	// Re-rendering must not refetch the preview.
	_ = m.View()
	if calls != 1 {
		t.Errorf("preview loader called %d times after second View, want 1", calls)
	}

	// Moving the selection loads the other session once.
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if calls != 2 {
		t.Errorf("preview loader called %d times after moving down, want 2", calls)
	}
}

func TestPreviewPaneAbsentWithoutLoader(t *testing.T) {
	m := NewModel(testSessions(), Options{})
	m = updateModel(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if view := m.View(); strings.Contains(strings.ToLower(view), "preview") {
		t.Errorf("preview pane rendered without a Preview option:\n%s", view)
	}
}

func TestTabScopesToSelectedFolderAndBack(t *testing.T) {
	m := NewModel(testSessions(), Options{})
	m = updateModel(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})

	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if len(m.sessions) != 1 {
		t.Fatalf("scoped sessions = %d, want 1", len(m.sessions))
	}
	if m.sessions[0].ID != "ses_alpha" {
		t.Errorf("scoped session = %q, want ses_alpha", m.sessions[0].ID)
	}
	if view := m.View(); !strings.Contains(view, "in /tmp/alpha-project") {
		t.Errorf("header missing scope label:\n%s", view)
	}

	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if len(m.sessions) != 2 {
		t.Errorf("cleared scope sessions = %d, want 2", len(m.sessions))
	}
}

func TestEscapeClearsScopeBeforeQuitting(t *testing.T) {
	m := NewModel(testSessions(), Options{})
	m = updateModel(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyTab})

	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.Canceled() {
		t.Fatal("Esc quit instead of clearing the folder scope")
	}
	if len(m.sessions) != 2 {
		t.Errorf("Esc did not widen scope: %d sessions", len(m.sessions))
	}

	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.Canceled() {
		t.Error("second Esc did not quit")
	}
}

func TestFolderSearchPromptFiltersSessions(t *testing.T) {
	m := NewModel(testSessions(), Options{})
	m = updateModel(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})

	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlF})
	if !m.prompting {
		t.Fatal("Ctrl+F did not open the folder prompt")
	}
	if view := m.View(); !strings.Contains(view, "folder:") {
		t.Errorf("footer missing folder prompt:\n%s", view)
	}

	m = sendRunes(t, m, "beta")
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.prompting {
		t.Error("prompt still active after Enter")
	}
	if len(m.sessions) != 1 || m.sessions[0].ID != "ses_beta" {
		t.Fatalf("folder search sessions = %+v, want only ses_beta", m.sessions)
	}
	if view := m.View(); !strings.Contains(view, `folder ~ "beta"`) {
		t.Errorf("header missing folder query:\n%s", view)
	}
}

func TestFolderSearchPromptEscapeCancels(t *testing.T) {
	m := NewModel(testSessions(), Options{})
	m = updateModel(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlF})
	m = sendRunes(t, m, "beta")
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})

	if m.Canceled() {
		t.Fatal("Esc in the prompt canceled the picker")
	}
	if m.prompting {
		t.Error("prompt still active after Esc")
	}
	if len(m.sessions) != 2 {
		t.Errorf("canceled prompt changed scope: %d sessions", len(m.sessions))
	}
}

func TestCLIScopeLabelIsShown(t *testing.T) {
	m := NewModel(testSessions(), Options{ScopeLabel: "~/git/project"})
	m = updateModel(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	if view := m.View(); !strings.Contains(view, "in ~/git/project") {
		t.Errorf("header missing CLI scope label:\n%s", view)
	}
}

func TestEmptySessionList(t *testing.T) {
	m := NewModel(nil, Options{})
	m = updateModel(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	view := m.View()
	if !strings.Contains(view, "No sessions.") {
		t.Errorf("empty view missing empty-state message:\n%s", view)
	}

	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.Canceled() {
		t.Error("Enter on an empty list did not cancel")
	}
	if _, ok := m.Result(); ok {
		t.Error("Enter on an empty list produced a selection")
	}
}
