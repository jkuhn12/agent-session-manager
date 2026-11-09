package scope

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jkuhn12/agent-session-manager/internal/provider"
)

func TestInactiveMatchesEverything(t *testing.T) {
	m := New("/tmp", nil, false)
	if m.Active() {
		t.Fatal("empty scope is active")
	}
	if !m.Match("/anywhere") {
		t.Error("inactive scope did not match")
	}
	if got := m.Label(); got != "" {
		t.Errorf("Label() = %q, want empty", got)
	}
}

func TestDirScopeMatchesSubtree(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	sibling := root + "-sibling"

	m := New("/", []string{root}, false)
	for _, dir := range []string{root, nested} {
		if !m.Match(dir) {
			t.Errorf("expected %q to match %q", dir, root)
		}
	}
	if m.Match(sibling) {
		t.Errorf("sibling path %q matched %q", sibling, root)
	}
}

func TestExpandRelativeHomeGlobAndDedupe(t *testing.T) {
	cwd := t.TempDir()
	alpha := filepath.Join(cwd, "projects", "alpha")
	beta := filepath.Join(cwd, "projects", "beta")
	for _, dir := range []string{alpha, beta} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got := Expand(cwd, []string{"projects/alpha", "projects/*", "./projects/alpha"})
	want := []string{canonical(alpha), canonical(beta)}
	if len(got) != len(want) {
		t.Fatalf("Expand() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Expand()[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	home := provider.HomeDir()
	if home == "" {
		t.Skip("no home directory")
	}
	expanded := Expand(cwd, []string{"~"})
	if len(expanded) != 1 || expanded[0] != canonical(home) {
		t.Errorf("Expand(~/ ) = %v, want %q", expanded, canonical(home))
	}
}

func TestMatchQuerySubstringAndGlob(t *testing.T) {
	home := provider.HomeDir()
	dir := filepath.Join(home, "git", "agent-session-manager")

	tests := []struct {
		query string
		want  bool
	}{
		{"", true},
		{"agent-session", true},
		{"AGENT-SESSION", true},
		{"~/git/agent", true},
		{"no-such-folder", false},
		{"~/git/*", true},
		{"**/agent-session-manager", false}, // filepath.Match has no **
	}
	for _, test := range tests {
		if got := MatchQuery(dir, test.query); got != test.want {
			t.Errorf("MatchQuery(%q) = %v, want %v", test.query, got, test.want)
		}
	}
}

func TestCurrentRepoScope(t *testing.T) {
	requireGit(t)

	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q")

	outside := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	// Running from a subdirectory still scopes to the whole repository.
	m := New(filepath.Join(repo, "sub"), nil, true)
	if !m.Match(repo) || !m.Match(filepath.Join(repo, "sub")) || !m.Match(filepath.Join(repo, "other")) {
		t.Error("current repo scope missed a repository directory")
	}
	if m.Match(outside) {
		t.Error("current repo scope matched a directory outside the repository")
	}
}

func TestCurrentRepoIncludesLinkedWorktree(t *testing.T) {
	requireGit(t)

	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.email", "test@example.com")
	git(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "README")
	git(t, repo, "commit", "-qm", "init")

	worktree := filepath.Join(tmp, "worktree")
	git(t, repo, "worktree", "add", "-q", worktree, "-b", "other")

	m := New(repo, nil, true)
	if !m.Match(worktree) {
		t.Error("current repo scope did not include linked worktree")
	}
}

func TestNonGitCurrentScopeFallsBackToCwd(t *testing.T) {
	cwd := t.TempDir()
	nested := filepath.Join(cwd, "nested")
	outside := t.TempDir()

	m := New(cwd, nil, true)
	if m.hasRepo {
		t.Skip("temp directory unexpectedly inside a git repository")
	}
	if !m.Match(nested) {
		t.Errorf("non-git scope did not match descendant %q", nested)
	}
	if m.Match(outside) {
		t.Error("non-git scope matched an unrelated directory")
	}
}

func TestFilterPreservesOrder(t *testing.T) {
	root := t.TempDir()
	sessions := []provider.Session{
		{ID: "a", Directory: root},
		{ID: "b", Directory: "/elsewhere"},
		{ID: "c", Directory: filepath.Join(root, "child")},
	}
	got := New("/", []string{root}, false).Filter(sessions)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Errorf("Filter() = %+v, want a then c", got)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"-C", dir}, args...)
	if out, err := exec.Command("git", cmdArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
