package merge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jkuhn12/agent-session-manager/internal/provider"
)

var base = time.Date(2026, time.November, 8, 12, 0, 0, 0, time.UTC)

// fakeProvider is a scriptable provider used to exercise merge behavior.
type fakeProvider struct {
	name      string
	available bool
	sessions  []provider.Session
	err       error
	panicWith any
	calls     int
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Available() bool { return f.available }

func (f *fakeProvider) Sessions() ([]provider.Session, error) {
	f.calls++
	if f.panicWith != nil {
		panic(f.panicWith)
	}
	return f.sessions, f.err
}

func session(agent, id, dir, title string, created, updated time.Time) provider.Session {
	return provider.Session{
		Agent:       agent,
		ID:          id,
		Directory:   dir,
		Title:       title,
		TimeCreated: created,
		TimeUpdated: updated,
	}
}

func ids(sessions []provider.Session) []string {
	out := make([]string, len(sessions))
	for i, s := range sessions {
		out[i] = s.ID
	}
	return out
}

func TestSessionsInterleavesAndSortsByUpdated(t *testing.T) {
	a := &fakeProvider{
		name:      "a",
		available: true,
		sessions: []provider.Session{
			session("a", "a-old", "", "old", base, base.Add(-3*time.Hour)),
			session("a", "a-new", "", "new", base, base.Add(-1*time.Hour)),
		},
	}
	b := &fakeProvider{
		name:      "b",
		available: true,
		sessions: []provider.Session{
			session("b", "b-mid", "", "mid", base, base.Add(-2*time.Hour)),
		},
	}

	got, errs := Sessions([]provider.Provider{a, b})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := []string{"a-new", "b-mid", "a-old"}
	if strings.Join(ids(got), ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", ids(got), want)
	}
}

func TestSessionsDedupesByAgentAndID(t *testing.T) {
	old := session("opencode", "same", "", "old title", base, base.Add(-time.Hour))
	newer := session("opencode", "same", "", "new title", base, base.Add(time.Hour))
	otherAgent := session("claude", "same", "", "other agent", base, base.Add(-time.Hour))

	// Put the newer row first to prove replacement does not depend on order.
	p1 := &fakeProvider{name: "p1", available: true, sessions: []provider.Session{newer}}
	p2 := &fakeProvider{name: "p2", available: true, sessions: []provider.Session{old, otherAgent}}

	got, errs := Sessions([]provider.Provider{p1, p2})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2 (same id on different agents must not dedupe): %v", len(got), ids(got))
	}

	byAgent := map[string]provider.Session{}
	for _, s := range got {
		byAgent[s.Agent] = s
	}
	if byAgent["opencode"].Title != "new title" {
		t.Errorf("opencode title = %q, want %q", byAgent["opencode"].Title, "new title")
	}
	if byAgent["claude"].Title != "other agent" {
		t.Errorf("claude title = %q, want %q", byAgent["claude"].Title, "other agent")
	}
}

func TestSessionsDedupeTieBreaksOnCreationThenKeepsFirst(t *testing.T) {
	updated := base
	laterCreated := session("opencode", "id", "", "later", base.Add(time.Hour), updated)
	earlierCreated := session("opencode", "id", "", "earlier", base, updated)
	tiedButSecond := session("opencode", "id", "", "second", base, updated)

	p := &fakeProvider{name: "p", available: true, sessions: []provider.Session{earlierCreated, laterCreated}}
	got, _ := Sessions([]provider.Provider{p})
	if len(got) != 1 || got[0].Title != "later" {
		t.Fatalf("tie break by creation failed: %+v", got)
	}

	p2 := &fakeProvider{name: "p", available: true, sessions: []provider.Session{earlierCreated, tiedButSecond}}
	got, _ = Sessions([]provider.Provider{p2})
	if len(got) != 1 || got[0].Title != "earlier" {
		t.Fatalf("full tie should keep first row: %+v", got)
	}
}

func TestSessionsFlagsDirectoryExistence(t *testing.T) {
	existing := t.TempDir()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{
		name:      "p",
		available: true,
		sessions: []provider.Session{
			session("p", "exists", existing, "exists", base, base),
			session("p", "missing", missing, "missing", base, base),
			session("p", "not-dir", notDir, "not a dir", base, base),
		},
	}

	got, errs := Sessions([]provider.Provider{p})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	for _, s := range got {
		switch s.ID {
		case "exists":
			if !s.DirExists {
				t.Errorf("existing temp dir reported as missing")
			}
		case "missing", "not-dir":
			if s.DirExists {
				t.Errorf("%s reported as existing", s.ID)
			}
		}
	}
}

func TestSessionsSkipsUnavailableProvider(t *testing.T) {
	unavailable := &fakeProvider{name: "unavailable", available: false}
	available := &fakeProvider{
		name:      "available",
		available: true,
		sessions:  []provider.Session{session("available", "1", "", "one", base, base)},
	}

	got, errs := Sessions([]provider.Provider{unavailable, available})
	if unavailable.calls != 0 {
		t.Errorf("unavailable provider Sessions called %d times, want 0", unavailable.calls)
	}
	if len(errs) != 0 {
		t.Fatalf("unavailable provider should not produce an error: %v", errs)
	}
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("got %v, want one session from available provider", ids(got))
	}
}

func TestSessionsKeepsPartialResultsWithError(t *testing.T) {
	boom := errors.New("database exploded")
	p := &fakeProvider{
		name:      "opencode",
		available: true,
		sessions:  []provider.Session{session("opencode", "1", "", "partial", base, base)},
		err:       boom,
	}

	got, errs := Sessions([]provider.Provider{p})
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("partial sessions lost: %v", ids(got))
	}
	if len(errs) != 1 {
		t.Fatalf("got %d errors, want 1", len(errs))
	}
	if !strings.Contains(errs[0].Error(), "opencode") || !strings.Contains(errs[0].Error(), "database exploded") {
		t.Fatalf("error missing context: %v", errs[0])
	}
	if !errors.Is(errs[0], boom) {
		t.Fatalf("error should wrap provider error: %v", errs[0])
	}
}

func TestSessionsRecoversPanics(t *testing.T) {
	bad := &fakeProvider{name: "bad", available: true, panicWith: "kaboom"}
	good := &fakeProvider{
		name:      "good",
		available: true,
		sessions:  []provider.Session{session("good", "1", "", "fine", base, base)},
	}

	got, errs := Sessions([]provider.Provider{bad, good})
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("healthy provider result lost: %v", ids(got))
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "bad") || !strings.Contains(errs[0].Error(), "kaboom") {
		t.Fatalf("panic not reported with provider name: %v", errs)
	}
}

func TestSessionsEmptyProviderList(t *testing.T) {
	for _, providers := range [][]provider.Provider{nil, []provider.Provider{}} {
		got, errs := Sessions(providers)
		if got == nil {
			t.Fatal("sessions slice is nil, want empty non-nil slice")
		}
		if len(got) != 0 {
			t.Fatalf("got %d sessions, want 0", len(got))
		}
		if len(errs) != 0 {
			t.Fatalf("got %d errors, want 0", len(errs))
		}
	}
}

func TestSessionsSkipsNilProviders(t *testing.T) {
	got, errs := Sessions([]provider.Provider{nil})
	if len(got) != 0 || len(errs) != 0 {
		t.Fatalf("nil provider should be ignored: sessions=%v errors=%v", got, errs)
	}
}

func TestSessionsCleansTitlesAndFallsBack(t *testing.T) {
	p := &fakeProvider{
		name:      "p",
		available: true,
		sessions: []provider.Session{
			session("p", "1", "", "  hello \n\t  world  ", base, base),
			session("p", "2", "", "   ", base, base),
		},
	}

	got, errs := Sessions([]provider.Provider{p})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	titles := map[string]string{}
	for _, s := range got {
		titles[s.ID] = s.Title
	}
	if titles["1"] != "hello world" {
		t.Errorf("title = %q, want cleaned %q", titles["1"], "hello world")
	}
	if titles["2"] != "Untitled session" {
		t.Errorf("empty title = %q, want %q", titles["2"], "Untitled session")
	}
}

func TestSessionsSortsByAgentThenTitleCaseInsensitive(t *testing.T) {
	updated := base
	p := &fakeProvider{
		name:      "p",
		available: true,
		sessions: []provider.Session{
			session("opencode", "1", "", "banana", base, updated),
			session("claude", "2", "", "Apple", base, updated),
			session("claude", "3", "", "apple", base, updated),
		},
	}

	got, _ := Sessions([]provider.Provider{p})
	want := []string{"2", "3", "1"} // claude before opencode; case-insensitive title order
	if strings.Join(ids(got), ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", ids(got), want)
	}
}
