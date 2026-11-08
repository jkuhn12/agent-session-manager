package provider

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

const (
	openCodeBusyTimeoutMS = 5000
	openCodeOpenAttempts  = 3
	openCodePreviewRunes  = 300
)

// OpenCode reads opencode's SQLite session store. It never writes to the
// database and tolerates the schema drifting between releases.
type OpenCode struct {
	DBPath string
}

var (
	_ Provider  = (*OpenCode)(nil)
	_ Previewer = (*OpenCode)(nil)
	_ Diagnoser = (*OpenCode)(nil)
)

// NewOpenCode returns a provider pointed at the default opencode database.
func NewOpenCode() *OpenCode {
	return &OpenCode{DBPath: OpenCodeDBPath()}
}

// OpenCodeDBPath resolves the opencode database location. ASM_OPENCODE_DB wins,
// then $XDG_DATA_HOME/opencode/opencode.db, then ~/.local/share/opencode/opencode.db.
func OpenCodeDBPath() string {
	if path := os.Getenv("ASM_OPENCODE_DB"); path != "" {
		return ExpandHome(path)
	}
	return filepath.Join(DataHome(), "opencode", "opencode.db")
}

// Name reports the stable agent identifier.
func (p *OpenCode) Name() string { return AgentOpenCode }

// path resolves the configured database path, falling back to the default.
func (p *OpenCode) path() string {
	if p.DBPath != "" {
		return ExpandHome(p.DBPath)
	}
	return OpenCodeDBPath()
}

// Available reports whether the database file exists.
func (p *OpenCode) Available() bool {
	return FileExists(p.path())
}

// Sessions returns top-level, non-archived sessions from both the current
// session_v2 table and the legacy session table. Duplicate ids prefer v2.
func (p *OpenCode) Sessions() ([]Session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, schema, err := p.openSchema(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var errs []error
	v2Rows, err := openCodeQuerySessions(ctx, db, schema, "session_v2")
	if err != nil {
		errs = append(errs, fmt.Errorf("query session_v2: %w", err))
	}
	legacyRows, err := openCodeQuerySessions(ctx, db, schema, "session")
	if err != nil {
		errs = append(errs, fmt.Errorf("query session: %w", err))
	}

	legacyByID := make(map[string]openCodeSessionRow, len(legacyRows))
	for _, row := range legacyRows {
		if row.id != "" {
			legacyByID[row.id] = row
		}
	}

	inV2 := make(map[string]bool, len(v2Rows))
	sessions := make([]Session, 0, len(v2Rows)+len(legacyRows))

	for _, row := range v2Rows {
		if row.id == "" {
			continue
		}
		inV2[row.id] = true
		if !row.topLevel() || row.isArchived() {
			continue
		}
		sessions = append(sessions, row.toSession(legacyByID[row.id]))
	}
	for _, row := range legacyRows {
		if row.id == "" || inV2[row.id] {
			continue
		}
		if !row.topLevel() || row.isArchived() {
			continue
		}
		sessions = append(sessions, row.toSession(openCodeSessionRow{}))
	}

	if len(errs) > 0 {
		return sessions, errors.Join(errs...)
	}
	return sessions, nil
}

// Preview loads best-effort detail for one session. Missing tables or sessions
// yield a zero Preview; only an unusable database produces an error.
func (p *OpenCode) Preview(sessionID string) (Preview, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return Preview{}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, schema, err := p.openSchema(ctx)
	if err != nil {
		return Preview{}, err
	}
	defer db.Close()

	var preview Preview
	switch {
	case openCodeSessionExists(ctx, db, schema, "session_v2", sessionID):
		if schema.hasTable("session_message") {
			preview.MessageCount = openCodeV2MessageCount(ctx, db, schema, sessionID)
			preview.LastUserMessage = Truncate(
				openCodeV2UserText(openCodeLatestV2UserData(ctx, db, schema, sessionID)),
				openCodePreviewRunes,
			)
		}
		preview.Model, preview.CostUSD = openCodeSessionModel(ctx, db, schema, "session_v2", sessionID)
	case openCodeSessionExists(ctx, db, schema, "session", sessionID):
		if schema.hasTable("message") {
			preview.MessageCount = openCodeLegacyMessageCount(ctx, db, schema, sessionID)
			preview.LastUserMessage = Truncate(
				openCodeLegacyLastUserText(ctx, db, schema, sessionID),
				openCodePreviewRunes,
			)
		}
		preview.Model, preview.CostUSD = openCodeSessionModel(ctx, db, schema, "session", sessionID)
	default:
		return Preview{}, nil
	}

	preview.TodosTotal, preview.TodosOpen = openCodeTodos(ctx, db, schema, sessionID)
	return preview, nil
}

// Diagnostics describes the resolved store, tables, and usable session counts.
func (p *OpenCode) Diagnostics() []Diagnostic {
	path := p.path()
	diags := make([]Diagnostic, 0, 12)
	diags = append(diags, Diagnostic{Level: "info", Key: "opencode.db", Value: path})
	if !FileExists(path) {
		diags = append(diags, Diagnostic{Level: "error", Key: "opencode.db.exists", Value: "false"})
		return diags
	}
	diags = append(diags, Diagnostic{Level: "ok", Key: "opencode.db.exists", Value: "true"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, schema, err := p.openSchema(ctx)
	if err != nil {
		diags = append(diags, Diagnostic{Level: "error", Key: "opencode.db.open", Value: err.Error()})
		return diags
	}
	defer db.Close()

	for _, table := range []string{"session", "session_v2", "session_message"} {
		if schema.hasTable(table) {
			diags = append(diags, Diagnostic{Level: "ok", Key: "opencode.table." + table, Value: "found"})
			continue
		}
		level := "warn"
		if table == "session_v2" {
			// A legacy-only database is valid; it just lacks v2 sessions.
			level = "info"
		}
		diags = append(diags, Diagnostic{Level: level, Key: "opencode.table." + table, Value: "missing"})
	}

	totalUsable := 0
	for _, table := range []string{"session_v2", "session"} {
		if !schema.hasTable(table) || !schema.hasColumn(table, "id") {
			continue
		}
		total, parent, archived, excluded, err := openCodeCountSessions(ctx, db, schema, table)
		if err != nil {
			diags = append(diags, Diagnostic{Level: "error", Key: "opencode.sessions." + table, Value: err.Error()})
			continue
		}
		usable := total - excluded
		totalUsable += usable
		diags = append(diags, Diagnostic{
			Level: "info",
			Key:   "opencode.sessions." + table,
			Value: fmt.Sprintf("%d usable of %d (parent=%d, archived=%d)", usable, total, parent, archived),
		})
		if caveats := openCodeSchemaCaveats(schema, table); len(caveats) > 0 {
			diags = append(diags, Diagnostic{
				Level: "warn",
				Key:   "opencode.schema." + table,
				Value: strings.Join(caveats, "; "),
			})
		}
	}
	diags = append(diags, Diagnostic{
		Level: "info",
		Key:   "opencode.sessions.total",
		Value: fmt.Sprintf("%d usable before dedupe", totalUsable),
	})

	if !schema.hasTable("session_v2") {
		diags = append(diags, Diagnostic{Level: "info", Key: "opencode.mode", Value: "legacy-only database"})
	} else if !schema.hasTable("session_message") {
		diags = append(diags, Diagnostic{
			Level: "warn",
			Key:   "opencode.preview",
			Value: "session_v2 is present but session_message is missing; previews will be empty",
		})
	}
	return diags
}

// openCodeSchema caches which tables and columns exist. All access is defensive:
// callers check presence before building queries.
type openCodeSchema struct {
	tables  map[string]bool
	columns map[string]map[string]bool
}

func (s *openCodeSchema) hasTable(name string) bool {
	return s != nil && s.tables[name]
}

func (s *openCodeSchema) hasColumn(table, column string) bool {
	return s != nil && s.columns[table][column]
}

func (p *OpenCode) openSchema(ctx context.Context) (*sql.DB, *openCodeSchema, error) {
	db, err := p.openDB(ctx)
	if err != nil {
		return nil, nil, err
	}
	schema, err := openCodeLoadSchema(ctx, db)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, schema, nil
}

// openDB opens the database read-only. It retries a few times when SQLite
// reports the file as busy (opencode may be writing concurrently).
func (p *OpenCode) openDB(ctx context.Context) (*sql.DB, error) {
	path := p.path()
	if path == "" {
		return nil, errors.New("opencode database path is empty")
	}
	if !FileExists(path) {
		return nil, fmt.Errorf("opencode database not found: %s", path)
	}

	u := url.URL{Scheme: "file", Path: path}
	query := u.Query()
	query.Set("mode", "ro")
	u.RawQuery = query.Encode()
	dsn := u.String()

	var lastErr error
	for attempt := 0; attempt < openCodeOpenAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 50 * time.Millisecond):
			}
		}

		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			lastErr = err
			continue
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)

		_, err = db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", openCodeBusyTimeoutMS))
		if err == nil {
			err = db.PingContext(ctx)
		}
		if err == nil {
			return db, nil
		}
		db.Close()
		lastErr = err
		if !openCodeIsBusy(err) {
			break
		}
	}
	return nil, fmt.Errorf("open opencode database %s: %w", path, lastErr)
}

func openCodeIsBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "sqlite_locked") ||
		strings.Contains(msg, "database is locked")
}

func openCodeLoadSchema(ctx context.Context, db *sql.DB) (*openCodeSchema, error) {
	schema := &openCodeSchema{
		tables:  make(map[string]bool),
		columns: make(map[string]map[string]bool),
	}
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		schema.tables[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for table := range schema.tables {
		schema.columns[table] = openCodeTableColumns(ctx, db, table)
	}
	return schema, nil
}

// openCodeTableColumns returns the column set for one table. Unexpected PRAGMA
// failures are treated as "no known columns" rather than aborting the store.
func openCodeTableColumns(ctx context.Context, db *sql.DB, table string) map[string]bool {
	columns := make(map[string]bool)
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+openCodeQuoteIdent(table)+")")
	if err != nil {
		return columns
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, typ        string
			defaultValue     any
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			continue
		}
		columns[name] = true
	}
	return columns
}

func openCodeQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// openCodeColumnExpr selects a column when present, otherwise a literal that
// keeps the scan shape stable across schema versions.
func openCodeColumnExpr(schema *openCodeSchema, table, column, fallback string) string {
	if schema.hasColumn(table, column) {
		return "COALESCE(" + openCodeQuoteIdent(column) + ", " + fallback + ")"
	}
	return fallback
}

type openCodeSessionRow struct {
	id        string
	directory string
	title     string
	created   int64
	updated   int64
	parentID  string
	archived  int64
}

func (r openCodeSessionRow) topLevel() bool {
	return strings.TrimSpace(r.parentID) == ""
}

// isArchived reports whether time_archived carries a non-zero value.
func (r openCodeSessionRow) isArchived() bool {
	return r.archived != 0
}

// toSession builds a Session, borrowing title and directory from the legacy
// row when the v2 row left them empty.
func (r openCodeSessionRow) toSession(legacy openCodeSessionRow) Session {
	directory := r.directory
	if directory == "" {
		directory = legacy.directory
	}
	title := CleanTitle(r.title)
	if title == "" {
		title = CleanTitle(legacy.title)
	}
	if title == "" {
		title = "Untitled session"
	}
	return Session{
		ID:          r.id,
		Agent:       AgentOpenCode,
		Directory:   directory,
		Title:       title,
		TimeCreated: time.UnixMilli(r.created),
		TimeUpdated: time.UnixMilli(r.updated),
	}
}

func openCodeQuerySessions(ctx context.Context, db *sql.DB, schema *openCodeSchema, table string) ([]openCodeSessionRow, error) {
	if !schema.hasTable(table) || !schema.hasColumn(table, "id") {
		return nil, nil
	}
	columns := []string{
		openCodeColumnExpr(schema, table, "id", "''"),
		openCodeColumnExpr(schema, table, "directory", "''"),
		openCodeColumnExpr(schema, table, "title", "''"),
		openCodeColumnExpr(schema, table, "time_created", "0"),
		openCodeColumnExpr(schema, table, "time_updated", "0"),
		openCodeColumnExpr(schema, table, "parent_id", "''"),
		openCodeColumnExpr(schema, table, "time_archived", "0"),
	}
	query := "SELECT " + strings.Join(columns, ", ") + " FROM " + openCodeQuoteIdent(table)

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := make([]openCodeSessionRow, 0, 128)
	for rows.Next() {
		var values [7]any
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return sessions, err
		}
		sessions = append(sessions, openCodeSessionRow{
			id:        openCodeString(values[0]),
			directory: openCodeString(values[1]),
			title:     openCodeString(values[2]),
			created:   openCodeInt64(values[3]),
			updated:   openCodeInt64(values[4]),
			parentID:  openCodeString(values[5]),
			archived:  openCodeInt64(values[6]),
		})
	}
	return sessions, rows.Err()
}

func openCodeSessionExists(ctx context.Context, db *sql.DB, schema *openCodeSchema, table, id string) bool {
	if !schema.hasTable(table) || !schema.hasColumn(table, "id") {
		return false
	}
	var count int
	query := "SELECT COUNT(*) FROM " + openCodeQuoteIdent(table) + " WHERE id = ?"
	if err := db.QueryRowContext(ctx, query, id).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

func openCodeSessionModel(ctx context.Context, db *sql.DB, schema *openCodeSchema, table, id string) (string, float64) {
	if !schema.hasTable(table) || !schema.hasColumn(table, "id") {
		return "", 0
	}
	query := "SELECT " +
		openCodeColumnExpr(schema, table, "model", "''") + ", " +
		openCodeColumnExpr(schema, table, "cost", "0") +
		" FROM " + openCodeQuoteIdent(table) + " WHERE id = ? LIMIT 1"
	var model, cost any
	if err := db.QueryRowContext(ctx, query, id).Scan(&model, &cost); err != nil {
		return "", 0
	}
	return openCodeFormatModel(openCodeString(model)), openCodeFloat64(cost)
}

func openCodeFormatModel(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var model struct {
		ProviderID string `json:"providerID"`
		Provider   string `json:"provider_id"`
		ID         string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &model); err != nil {
		// Not JSON (older schema?): surface the raw value rather than nothing.
		return raw
	}
	providerID := model.ProviderID
	if providerID == "" {
		providerID = model.Provider
	}
	switch {
	case providerID != "" && model.ID != "":
		return providerID + "/" + model.ID
	case model.ID != "":
		return model.ID
	case providerID != "":
		return providerID
	default:
		return raw
	}
}

func openCodeV2MessageCount(ctx context.Context, db *sql.DB, schema *openCodeSchema, id string) int {
	if !schema.hasTable("session_message") || !schema.hasColumn("session_message", "session_id") {
		return 0
	}
	query := "SELECT COUNT(*) FROM session_message WHERE session_id = ?"
	if schema.hasColumn("session_message", "type") {
		query += " AND type IN ('user', 'assistant')"
	}
	var count int
	if err := db.QueryRowContext(ctx, query, id).Scan(&count); err != nil {
		return 0
	}
	return count
}

func openCodeLatestV2UserData(ctx context.Context, db *sql.DB, schema *openCodeSchema, id string) string {
	const table = "session_message"
	if !schema.hasTable(table) ||
		!schema.hasColumn(table, "session_id") ||
		!schema.hasColumn(table, "data") {
		return ""
	}
	query := "SELECT data FROM " + openCodeQuoteIdent(table) + " WHERE session_id = ?"
	if schema.hasColumn(table, "type") {
		query += " AND type = 'user'"
	}
	switch {
	case schema.hasColumn(table, "seq"):
		query += " ORDER BY seq DESC"
	case schema.hasColumn(table, "time_created"):
		query += " ORDER BY time_created DESC"
	default:
		query += " ORDER BY rowid DESC"
	}
	query += " LIMIT 1"

	var data any
	if err := db.QueryRowContext(ctx, query, id).Scan(&data); err != nil {
		return ""
	}
	return openCodeString(data)
}

// openCodeV2UserText extracts the text field from a session_message user row.
func openCodeV2UserText(data string) string {
	data = strings.TrimSpace(data)
	if data == "" {
		return ""
	}
	var message struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(data), &message); err != nil {
		return ""
	}
	return message.Text
}

func openCodeLegacyMessageCount(ctx context.Context, db *sql.DB, schema *openCodeSchema, id string) int {
	if !schema.hasTable("message") || !schema.hasColumn("message", "session_id") {
		return 0
	}
	var count int
	query := "SELECT COUNT(*) FROM message WHERE session_id = ?"
	if err := db.QueryRowContext(ctx, query, id).Scan(&count); err != nil {
		return 0
	}
	return count
}

// openCodeLegacyLastUserText finds the newest message whose JSON role is
// "user", then joins that message's text parts. Roles and part types are
// parsed in Go so the query does not depend on SQLite's JSON1 extension.
func openCodeLegacyLastUserText(ctx context.Context, db *sql.DB, schema *openCodeSchema, id string) string {
	const table = "message"
	if !schema.hasTable(table) ||
		!schema.hasColumn(table, "session_id") ||
		!schema.hasColumn(table, "data") {
		return ""
	}
	query := "SELECT id, data FROM " + openCodeQuoteIdent(table) + " WHERE session_id = ?"
	if schema.hasColumn(table, "time_created") {
		query += " ORDER BY time_created DESC"
	} else {
		query += " ORDER BY rowid DESC"
	}

	rows, err := db.QueryContext(ctx, query, id)
	if err != nil {
		return ""
	}
	defer rows.Close()

	// Find the newest user message first, then close the row set before
	// querying parts (the pool allows only one connection at a time).
	var messageID, messageText string
	for rows.Next() {
		var idValue, data any
		if err := rows.Scan(&idValue, &data); err != nil {
			continue
		}
		var message struct {
			Role string `json:"role"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(openCodeString(data)), &message); err != nil {
			continue
		}
		if message.Role != "user" {
			continue
		}
		messageID = openCodeString(idValue)
		messageText = message.Text
		break
	}
	rows.Close()

	if messageID == "" {
		return ""
	}
	if text := openCodeLegacyParts(ctx, db, schema, messageID); text != "" {
		return text
	}
	return messageText
}

// openCodeLegacyParts concatenates the text parts that belong to one message.
func openCodeLegacyParts(ctx context.Context, db *sql.DB, schema *openCodeSchema, messageID string) string {
	const table = "part"
	if messageID == "" ||
		!schema.hasTable(table) ||
		!schema.hasColumn(table, "message_id") ||
		!schema.hasColumn(table, "data") {
		return ""
	}
	query := "SELECT data FROM " + openCodeQuoteIdent(table) + " WHERE message_id = ?"
	if schema.hasColumn(table, "time_created") {
		query += " ORDER BY time_created ASC"
	} else {
		query += " ORDER BY rowid ASC"
	}

	rows, err := db.QueryContext(ctx, query, messageID)
	if err != nil {
		return ""
	}
	defer rows.Close()

	var texts []string
	for rows.Next() {
		var data any
		if err := rows.Scan(&data); err != nil {
			continue
		}
		var part struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(openCodeString(data)), &part); err != nil {
			continue
		}
		if part.Type == "text" && part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func openCodeTodos(ctx context.Context, db *sql.DB, schema *openCodeSchema, id string) (total, open int) {
	const table = "todo"
	if !schema.hasTable(table) ||
		!schema.hasColumn(table, "session_id") ||
		!schema.hasColumn(table, "status") {
		return 0, 0
	}
	query := "SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'completed' THEN 0 ELSE 1 END), 0) " +
		"FROM " + openCodeQuoteIdent(table) + " WHERE session_id = ?"
	if err := db.QueryRowContext(ctx, query, id).Scan(&total, &open); err != nil {
		return 0, 0
	}
	return total, open
}

func openCodeCountSessions(ctx context.Context, db *sql.DB, schema *openCodeSchema, table string) (total, parent, archived, excluded int, err error) {
	parentExpr := "0"
	if schema.hasColumn(table, "parent_id") {
		parentExpr = "CASE WHEN COALESCE(parent_id, '') <> '' THEN 1 ELSE 0 END"
	}
	archivedExpr := "0"
	if schema.hasColumn(table, "time_archived") {
		archivedExpr = "CASE WHEN COALESCE(time_archived, 0) <> 0 THEN 1 ELSE 0 END"
	}
	excludedExpr := "CASE WHEN (" + parentExpr + ") = 1 OR (" + archivedExpr + ") = 1 THEN 1 ELSE 0 END"
	query := fmt.Sprintf(
		"SELECT COUNT(*), COALESCE(SUM(%s), 0), COALESCE(SUM(%s), 0), COALESCE(SUM(%s), 0) FROM %s",
		parentExpr, archivedExpr, excludedExpr, openCodeQuoteIdent(table),
	)
	err = db.QueryRowContext(ctx, query).Scan(&total, &parent, &archived, &excluded)
	return total, parent, archived, excluded, err
}

func openCodeSchemaCaveats(schema *openCodeSchema, table string) []string {
	checks := []struct {
		column string
		effect string
	}{
		{"title", "titles unavailable"},
		{"directory", "directories unavailable"},
		{"parent_id", "subagent sessions not excluded"},
		{"time_archived", "archived sessions not excluded"},
		{"time_created", "creation times unavailable"},
		{"time_updated", "update times unavailable"},
	}
	var caveats []string
	for _, check := range checks {
		if !schema.hasColumn(table, check.column) {
			caveats = append(caveats, "missing "+check.column+" ("+check.effect+")")
		}
	}
	return caveats
}

// openCodeString converts whatever the driver returned into a string.
func openCodeString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}

// openCodeInt64 converts whatever the driver returned into an int64.
func openCodeInt64(value any) int64 {
	switch v := value.(type) {
	case nil:
		return 0
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case []byte:
		n, _ := strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	default:
		return 0
	}
}

// openCodeFloat64 converts whatever the driver returned into a float64.
func openCodeFloat64(value any) float64 {
	switch v := value.(type) {
	case nil:
		return 0
	case float64:
		return v
	case float32:
		return float64(v)
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case []byte:
		f, _ := strconv.ParseFloat(strings.TrimSpace(string(v)), 64)
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f
	default:
		return 0
	}
}
