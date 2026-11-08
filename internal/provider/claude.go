package provider

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Claude reads session transcripts written by Claude Code.
//
// Each project directory under the projects root is named after the working
// directory with "/" replaced by "-" (an ambiguous encoding), and contains one
// <session-uuid>.jsonl file per top-level session. Subagent transcripts live in
// <project>/<session-id>/subagents/ and are deliberately never scanned.
type Claude struct {
	ProjectsDir string // resolved path; exported so tests/doctor can inspect it
}

var (
	_ Provider  = (*Claude)(nil)
	_ Previewer = (*Claude)(nil)
	_ Diagnoser = (*Claude)(nil)
)

const (
	// Claude metadata lives near the two ends of a transcript: cwd and the
	// first user turn at the head, and an updated custom title/summary often
	// appended at the tail. Bound both reads during Sessions.
	claudeHeadBytes = 512 * 1024
	claudeTailBytes = 64 * 1024

	claudeTitleLimit   = 200
	claudePreviewLimit = 300
)

// NewClaude returns a Claude provider pointed at the resolved storage root.
func NewClaude() *Claude {
	return &Claude{ProjectsDir: ClaudeProjectsDir()}
}

// ClaudeProjectsDir resolves the Claude Code projects directory:
// $ASM_CLAUDE_PROJECTS, then $CLAUDE_CONFIG_DIR/projects, then
// ~/.claude/projects.
func ClaudeProjectsDir() string {
	fallback := filepath.Join(HomeDir(), ".claude", "projects")
	if configDir := EnvOr("CLAUDE_CONFIG_DIR", ""); configDir != "" {
		fallback = filepath.Join(configDir, "projects")
	}
	return EnvOr("ASM_CLAUDE_PROJECTS", fallback)
}

// Name returns the stable agent identifier used by the result protocol.
func (p *Claude) Name() string { return AgentClaude }

// Available reports whether the projects root exists.
func (p *Claude) Available() bool {
	return p != nil && DirExists(p.ProjectsDir)
}

// Sessions lists immediate *.jsonl transcripts in each immediate project
// directory. Metadata is read from the head and tail of each file; the whole
// transcript is never parsed here.
func (p *Claude) Sessions() ([]Session, error) {
	if p == nil || p.ProjectsDir == "" {
		return nil, nil
	}

	projects, err := os.ReadDir(p.ProjectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []Session
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		projectPath := filepath.Join(p.ProjectsDir, project.Name())
		files, err := os.ReadDir(projectPath)
		if err != nil {
			// Unreadable project directory: skip it rather than failing the
			// whole provider.
			continue
		}
		for _, file := range files {
			// Only immediate files: nested subagent directories are skipped.
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".jsonl") {
				continue
			}
			sessionID := strings.TrimSuffix(file.Name(), ".jsonl")
			if sessionID == "" {
				continue
			}
			meta, err := readClaudeTranscript(filepath.Join(projectPath, file.Name()), sessionID, project.Name())
			if err != nil {
				continue
			}
			sessions = append(sessions, Session{
				ID:          sessionID,
				Agent:       AgentClaude,
				Directory:   meta.directory,
				Title:       meta.title,
				TimeCreated: meta.created,
				TimeUpdated: meta.updated,
			})
		}
	}
	return sessions, nil
}

// Preview scans one full transcript for a best-effort detail view.
func (p *Claude) Preview(sessionID string) (Preview, error) {
	path, err := p.findSessionFile(sessionID)
	if err != nil {
		return Preview{}, err
	}
	if path == "" {
		return Preview{}, nil
	}

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Preview{}, nil
		}
		return Preview{}, err
	}
	defer file.Close()

	var preview Preview
	reader := bufio.NewReaderSize(file, 64*1024)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var parsed claudeLine
			if err := json.Unmarshal(bytes.TrimSpace(line), &parsed); err == nil && !parsed.IsSidechain {
				switch parsed.Type {
				case "user":
					preview.MessageCount++
					// Tool-result-only turns are not human prose.
					if text := claudeProse(parsed.Message.Content); text != "" {
						preview.LastUserMessage = Truncate(text, claudePreviewLimit)
					}
				case "assistant":
					preview.MessageCount++
					if parsed.Message.Model != "" {
						preview.Model = parsed.Message.Model
					}
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return preview, readErr
		}
	}
	return preview, nil
}

// Diagnostics describes the Claude Code storage location without parsing any
// transcript.
func (p *Claude) Diagnostics() []Diagnostic {
	dir := ""
	if p != nil {
		dir = p.ProjectsDir
	}
	diagnostics := []Diagnostic{{Level: "info", Key: "projects dir", Value: dir}}

	if !DirExists(dir) {
		return append(diagnostics,
			Diagnostic{Level: "info", Key: "exists", Value: "no"},
			Diagnostic{Level: "warn", Key: "session files", Value: "no Claude Code sessions yet"},
		)
	}

	projectDirs, sessionFiles := countClaudeStorage(dir)
	return append(diagnostics,
		Diagnostic{Level: "ok", Key: "exists", Value: "yes"},
		Diagnostic{Level: "info", Key: "project dirs", Value: strconv.Itoa(projectDirs)},
		Diagnostic{Level: "info", Key: "session files", Value: strconv.Itoa(sessionFiles)},
	)
}

// findSessionFile looks for <sessionID>.jsonl directly inside one of the
// project directories. It never descends into subagent directories.
func (p *Claude) findSessionFile(sessionID string) (string, error) {
	if p == nil || p.ProjectsDir == "" || sessionID == "" {
		return "", nil
	}
	// Session IDs are filenames; reject anything that could escape the
	// project directory.
	if sessionID != filepath.Base(sessionID) || strings.ContainsAny(sessionID, `/\`) {
		return "", nil
	}

	projects, err := os.ReadDir(p.ProjectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	filename := sessionID + ".jsonl"
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		candidate := filepath.Join(p.ProjectsDir, project.Name(), filename)
		if FileExists(candidate) {
			return candidate, nil
		}
	}
	return "", nil
}

// countClaudeStorage counts project directories and immediate session files
// without reading or parsing them.
func countClaudeStorage(root string) (projectDirs, sessionFiles int) {
	projects, err := os.ReadDir(root)
	if err != nil {
		return 0, 0
	}
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		projectDirs++
		files, err := os.ReadDir(filepath.Join(root, project.Name()))
		if err != nil {
			continue
		}
		for _, file := range files {
			if !file.IsDir() && strings.HasSuffix(file.Name(), ".jsonl") {
				sessionFiles++
			}
		}
	}
	return projectDirs, sessionFiles
}

// claudeTranscriptMeta is the cheap metadata collected during Sessions.
type claudeTranscriptMeta struct {
	title     string
	directory string
	created   time.Time
	updated   time.Time
}

// claudeMetadata accumulates fields observed while scanning transcript ends.
type claudeMetadata struct {
	customTitle string
	summary     string
	firstUser   string
	cwd         string
	created     time.Time
}

// claudeLine is a tolerant view of one JSONL record. Unknown types and unknown
// fields are ignored by design.
type claudeLine struct {
	Type        string `json:"type"`
	Summary     string `json:"summary"`
	CustomTitle string `json:"customTitle"`
	Title       string `json:"title"`
	Cwd         string `json:"cwd"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// readClaudeTranscript gathers title, cwd and timestamps from the head and tail
// of a transcript. mtime is authoritative for TimeUpdated.
func readClaudeTranscript(path, sessionID, projectName string) (claudeTranscriptMeta, error) {
	file, err := os.Open(path)
	if err != nil {
		return claudeTranscriptMeta{}, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return claudeTranscriptMeta{}, err
	}
	size := info.Size()

	var parsed claudeMetadata
	if headSize := min(size, int64(claudeHeadBytes)); headSize > 0 {
		head := make([]byte, headSize)
		n, readErr := io.ReadFull(file, head)
		if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
			return claudeTranscriptMeta{}, readErr
		}
		head = head[:n]
		// If the head stopped before EOF, its final line may be partial.
		forEachClaudeLine(head, false, int64(n) < size, parsed.observe)
	}

	if size > claudeHeadBytes {
		tailSize := min(size, int64(claudeTailBytes))
		if _, err := file.Seek(size-tailSize, io.SeekStart); err == nil {
			tail := make([]byte, tailSize)
			n, readErr := io.ReadFull(file, tail)
			if readErr == nil || readErr == io.ErrUnexpectedEOF {
				// The tail usually starts mid-line; drop that partial line.
				forEachClaudeLine(tail[:n], true, false, parsed.observe)
			}
		}
	}

	directory := parsed.cwd
	if directory == "" {
		directory = decodeClaudeProjectName(projectName)
	}
	created := parsed.created
	if created.IsZero() {
		created = info.ModTime()
	}

	return claudeTranscriptMeta{
		title:     claudeTitle(parsed, sessionID),
		directory: directory,
		created:   created,
		updated:   info.ModTime(),
	}, nil
}

// observe folds one JSONL record into the accumulated metadata. Sidechain
// records belong to subagent turns and never influence the top-level session.
func (m *claudeMetadata) observe(data []byte) {
	var line claudeLine
	if err := json.Unmarshal(data, &line); err != nil {
		return
	}
	if line.IsSidechain {
		return
	}

	if m.cwd == "" {
		if cwd := strings.TrimSpace(line.Cwd); cwd != "" {
			m.cwd = cwd
		}
	}
	if m.created.IsZero() && line.Timestamp != "" {
		if created, ok := parseClaudeTime(line.Timestamp); ok {
			m.created = created
		}
	}
	if line.Type == "user" && m.firstUser == "" {
		if text := claudeProse(line.Message.Content); text != "" && !isClaudeMeta(text) {
			m.firstUser = text
		}
	}

	switch line.Type {
	case "custom-title", "custom_title":
		title := line.CustomTitle
		if title == "" {
			title = line.Title
		}
		if title = CleanTitle(title); title != "" {
			m.customTitle = title
		}
	case "summary":
		if summary := CleanTitle(line.Summary); summary != "" {
			m.summary = summary
		}
	}
}

// claudeTitle applies the title precedence rules.
func claudeTitle(meta claudeMetadata, sessionID string) string {
	title := meta.customTitle
	if title == "" {
		title = meta.summary
	}
	if title == "" {
		title = meta.firstUser
	}
	if title == "" {
		title = "Session " + shortClaudeID(sessionID)
	}
	return Truncate(title, claudeTitleLimit)
}

// claudeProse extracts human-readable text from a message content field.
// Content may be a plain string or an array of blocks; tool_result and other
// non-text blocks produce no prose.
func claudeProse(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}

	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return CleanTitle(text)
	}

	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" {
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return CleanTitle(strings.Join(parts, " "))
}

// isClaudeMeta reports whether text looks like an injected system/meta line
// (for example "<command-name>...") rather than user prose.
func isClaudeMeta(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "<")
}

// decodeClaudeProjectName reverses the lossy project-directory munging:
// strip one leading "-", turn every "-" back into "/", prefix "/".
func decodeClaudeProjectName(name string) string {
	if name == "" {
		return ""
	}
	decoded := strings.ReplaceAll(strings.TrimPrefix(name, "-"), "-", "/")
	if decoded == "" {
		return "/"
	}
	return "/" + decoded
}

// shortClaudeID returns the first eight characters of a session ID.
func shortClaudeID(sessionID string) string {
	if len(sessionID) > 8 {
		return sessionID[:8]
	}
	return sessionID
}

// parseClaudeTime parses the ISO-8601 timestamps found in transcripts.
func parseClaudeTime(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// forEachClaudeLine invokes fn for each non-empty line, optionally dropping a
// partial first or last line from a bounded read.
func forEachClaudeLine(data []byte, dropFirst, dropLast bool, fn func([]byte)) {
	lines := bytes.Split(data, []byte{'\n'})
	start, end := 0, len(lines)
	if dropFirst && start < end {
		start++
	}
	if dropLast && end > start {
		end--
	}
	for _, line := range lines[start:end] {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		fn(line)
	}
}
