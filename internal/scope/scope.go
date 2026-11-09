// Package scope narrows session lists to a set of project folders.
//
// A scope can be built from explicit directories, from the current working
// directory, or from the git repository containing the current directory. The
// matching rules are path based and deliberately conservative: a session
// matches a folder when it lives in that folder or one of its descendants.
package scope

import (
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jkuhn12/agent-session-manager/internal/provider"
)

// Matcher decides whether a session directory belongs to the active scope.
// The zero value is an inactive scope that matches every directory.
type Matcher struct {
	cwd         string
	roots       []string
	currentRepo bool

	repoRoot      string
	repoCommonDir string
	hasRepo       bool

	commonDirs map[string]string
}

// New builds a matcher for dirs, relative to cwd, and optionally the git
// repository containing cwd. When currentRepo is set but cwd is not inside a
// git repository, cwd and its descendants are used instead.
func New(cwd string, dirs []string, currentRepo bool) Matcher {
	m := Matcher{
		cwd:         canonical(cwd),
		roots:       Expand(cwd, dirs),
		currentRepo: currentRepo,
		commonDirs:  make(map[string]string),
	}
	if currentRepo {
		m.repoRoot, m.repoCommonDir = repoInfo(m.cwd)
		m.hasRepo = m.repoRoot != ""
	}
	return m
}

// Active reports whether the matcher restricts sessions at all.
func (m Matcher) Active() bool {
	return len(m.roots) > 0 || m.currentRepo
}

// Label is a short, human-readable description of the scope.
func (m Matcher) Label() string {
	parts := make([]string, 0, len(m.roots)+1)
	if m.currentRepo {
		if m.hasRepo {
			parts = append(parts, provider.CompactHome(m.repoRoot))
		} else {
			parts = append(parts, provider.CompactHome(m.cwd))
		}
	}
	for _, root := range m.roots {
		parts = append(parts, provider.CompactHome(root))
	}
	return strings.Join(dedupe(parts), ", ")
}

// Match reports whether directory belongs to the scope.
func (m Matcher) Match(directory string) bool {
	if !m.Active() {
		return true
	}
	dir := canonical(directory)
	if dir == "" {
		return false
	}

	for _, root := range m.roots {
		if within(root, dir) {
			return true
		}
	}

	if m.currentRepo {
		if !m.hasRepo {
			return within(m.cwd, dir)
		}
		if within(m.repoRoot, dir) {
			return true
		}
		if m.repoCommonDir != "" && m.commonDir(dir) == m.repoCommonDir {
			// A linked git worktree shares the common directory but lives
			// outside the repository root.
			return true
		}
	}

	return false
}

// Filter returns the sessions that match the scope. Order is preserved.
func (m Matcher) Filter(sessions []provider.Session) []provider.Session {
	if !m.Active() {
		return sessions
	}
	filtered := make([]provider.Session, 0, len(sessions))
	for _, session := range sessions {
		if m.Match(session.Directory) {
			filtered = append(filtered, session)
		}
	}
	return filtered
}

// MatchDir reports whether directory is root or below it.
func MatchDir(root, directory string) bool {
	root = canonical(root)
	if root == "" {
		return true
	}
	return within(root, canonical(directory))
}

// MatchQuery reports whether a directory matches a folder search. Queries are
// case-insensitive substrings. When the query contains glob metacharacters
// (*, ? or [) it is matched with filepath.Match against the absolute and
// home-compacted forms of the directory.
func MatchQuery(directory, query string) bool {
	query = strings.ToLower(strings.TrimSpace(provider.ExpandHome(query)))
	if query == "" {
		return true
	}

	candidates := []string{
		strings.ToLower(directory),
		strings.ToLower(provider.CompactHome(directory)),
	}

	if strings.ContainsAny(query, "*?[") {
		for _, candidate := range candidates {
			if ok, err := filepath.Match(query, candidate); err == nil && ok {
				return true
			}
		}
		// A glob that matches nothing is more useful as a literal search than
		// as a guaranteed empty list.
	}

	for _, candidate := range candidates {
		if strings.Contains(candidate, query) {
			return true
		}
	}
	return false
}

// Expand resolves scope arguments relative to cwd, expands "~", expands glob
// patterns that match anything, and removes duplicates. Arguments without
// matches are kept verbatim so scoping to a deleted folder still works.
func Expand(cwd string, args []string) []string {
	cwd = canonical(cwd)
	if cwd == "" {
		cwd = "."
	}

	expanded := make([]string, 0, len(args))
	for _, arg := range args {
		arg = provider.ExpandHome(strings.TrimSpace(arg))
		if arg == "" {
			continue
		}
		if !filepath.IsAbs(arg) {
			arg = filepath.Join(cwd, arg)
		}
		arg = filepath.Clean(arg)

		if strings.ContainsAny(arg, "*?[") {
			matches, err := filepath.Glob(arg)
			if err == nil && len(matches) > 0 {
				for _, match := range matches {
					expanded = append(expanded, canonical(match))
				}
				continue
			}
		}
		expanded = append(expanded, canonical(arg))
	}
	return dedupe(expanded)
}

// canonical makes path absolute and resolves symlinks on a best-effort basis.
// Missing paths (for example a deleted session directory) stay absolute.
func canonical(path string) string {
	if path == "" {
		return ""
	}
	path = filepath.Clean(provider.ExpandHome(path))
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return path
		}
		path = abs
	}

	// Resolve symlinks as far as the filesystem allows. Session directories
	// may no longer exist, but their existing ancestors (such as /var on
	// macOS, a symlink to /private/var) must still be resolved so path
	// containment comparisons stay consistent.
	remainder := ""
	current := path
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, remainder)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}

// within reports whether path equals root or is nested beneath it.
func within(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	if root == path {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// repoInfo returns the git top-level and common directory for dir. Both are
// empty when dir is not inside a git repository.
func repoInfo(dir string) (root, commonDir string) {
	root = gitOutput(dir, "rev-parse", "--show-toplevel")
	if root == "" {
		return "", ""
	}
	root = canonical(root)

	commonDir = gitOutput(dir, "rev-parse", "--git-common-dir")
	if commonDir != "" {
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(dir, commonDir)
		}
		commonDir = canonical(commonDir)
	}
	return root, commonDir
}

func gitOutput(dir string, args ...string) string {
	if dir == "" {
		return ""
	}
	cmdArgs := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", cmdArgs...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// commonDir returns the git common directory for a session directory, cached
// per directory. It is only consulted for sessions that live in another
// worktree of the current repository.
func (m Matcher) commonDir(dir string) string {
	if cached, ok := m.commonDirs[dir]; ok {
		return cached
	}
	_, common := repoInfo(dir)
	if m.commonDirs != nil {
		m.commonDirs[dir] = common
	}
	return common
}

func dedupe(values []string) []string {
	if len(values) < 2 {
		return values
	}
	seen := make(map[string]bool, len(values))
	out := values[:0]
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
