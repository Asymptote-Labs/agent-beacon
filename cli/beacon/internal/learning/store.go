package learning

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const (
	storeFile          = "memory.db"
	storeSchemaVersion = 1
)

type Store struct {
	dbPath string
}

type Query struct {
	ProjectPath string
	ProjectID   string
	State       string
	Kind        string
	Q           string
	Limit       int
	Page        int
}

func Open(path string) *Store {
	return &Store{dbPath: path}
}

func PathForRuntimeLog(logPath string) string {
	if strings.TrimSpace(logPath) == "" {
		return storeFile
	}
	dir := filepath.Dir(logPath)
	if filepath.Base(dir) == "logs" {
		return filepath.Join(filepath.Dir(dir), storeFile)
	}
	return filepath.Join(dir, storeFile)
}

func (s *Store) Path() string {
	return s.dbPath
}

func (s *Store) db() (*sql.DB, error) {
	if s.dbPath == "" {
		s.dbPath = storeFile
	}
	if err := os.MkdirAll(filepath.Dir(s.dbPath), 0700); err != nil && filepath.Dir(s.dbPath) != "." {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteURI(s.dbPath))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func sqliteURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return "file:" + filepath.ToSlash(abs) + "?_pragma=busy_timeout(5000)"
}

func ensureSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > storeSchemaVersion {
		return fmt.Errorf("memory store schema %d is newer than this beacon supports (%d)", version, storeSchemaVersion)
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS evaluations (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			trace_id TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			rubric_hash TEXT NOT NULL,
			evaluator TEXT NOT NULL,
			evaluation_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS evaluations_by_project_updated ON evaluations (project_id, updated_at DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS evaluations_by_project_trace_rubric ON evaluations (project_id, trace_id, rubric_hash)`,
		`CREATE TABLE IF NOT EXISTS candidates (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			state TEXT NOT NULL,
			kind TEXT NOT NULL,
			title TEXT NOT NULL,
			source_evaluation_id TEXT,
			memory_id TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			candidate_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS candidates_by_project_state_updated ON candidates (project_id, state, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS candidates_by_source_eval ON candidates (source_evaluation_id)`,
		`CREATE TABLE IF NOT EXISTS memories (
			id TEXT PRIMARY KEY,
			candidate_id TEXT NOT NULL,
			project_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			title TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			superseded_by TEXT,
			memory_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS memories_by_project_updated ON memories (project_id, updated_at DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS memories_by_candidate ON memories (candidate_id)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	if version != storeSchemaVersion {
		if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, storeSchemaVersion)); err != nil {
			return err
		}
	}
	return nil
}

func ResolveProject(path string) (asymptoteobserve.LearningProjectV1, error) {
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = os.Getwd()
		if err != nil {
			return asymptoteobserve.LearningProjectV1{}, err
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return asymptoteobserve.LearningProjectV1{}, err
	}
	root := findGitRoot(abs)
	if root == "" {
		root = abs
	}
	project := asymptoteobserve.LearningProjectV1{
		Path:      filepath.Clean(root),
		RemoteURL: readGitConfigValue(root, "remote \"origin\"", "url"),
		Branch:    readGitHeadBranch(root),
	}
	project.ID = ProjectID(project)
	return project, nil
}

func ProjectID(project asymptoteobserve.LearningProjectV1) string {
	key := firstNonEmpty(project.RemoteURL, project.Path)
	if key == "" {
		key = "unknown"
	}
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.ToSlash(key))))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func findGitRoot(path string) string {
	current := filepath.Clean(path)
	info, err := os.Stat(current)
	if err == nil && !info.IsDir() {
		current = filepath.Dir(current)
	}
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		next := filepath.Dir(current)
		if next == current {
			return ""
		}
		current = next
	}
}

func readGitHeadBranch(root string) string {
	data, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(data))
	const prefix = "ref: refs/heads/"
	if strings.HasPrefix(text, prefix) {
		return strings.TrimPrefix(text, prefix)
	}
	return ""
}

func readGitConfigValue(root, section, key string) string {
	data, err := os.ReadFile(filepath.Join(root, ".git", "config"))
	if err != nil {
		return ""
	}
	var inSection bool
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inSection = strings.Trim(trimmed, "[]") == section
			continue
		}
		if !inSection || !strings.Contains(trimmed, "=") {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if strings.TrimSpace(parts[0]) == key {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

func (s *Store) Status() (asymptoteobserve.LearningStatusV1, error) {
	db, err := s.db()
	if err != nil {
		return asymptoteobserve.LearningStatusV1{}, err
	}
	defer db.Close()
	status := asymptoteobserve.LearningStatusV1{
		SchemaVersion: asymptoteobserve.LearningSchemaVersion,
		Path:          s.dbPath,
		SizeBytes:     fileSize(s.dbPath),
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM evaluations`).Scan(&status.Evaluations)
	_ = db.QueryRow(`SELECT COUNT(*) FROM candidates`).Scan(&status.Candidates)
	_ = db.QueryRow(`SELECT COUNT(*) FROM memories WHERE superseded_by IS NULL OR superseded_by = ''`).Scan(&status.ApprovedMemories)
	return status, nil
}

func (s *Store) PutEvaluation(e asymptoteobserve.LearningEvaluationV1) error {
	if e.SchemaVersion == "" {
		e.SchemaVersion = asymptoteobserve.LearningSchemaVersion
	}
	now := nowString()
	if e.CreatedAt == "" {
		e.CreatedAt = now
	}
	e.UpdatedAt = now
	if e.ID == "" {
		e.ID = EvaluationID(e)
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO evaluations (id, project_id, trace_id, status, created_at, updated_at, rubric_hash, evaluator, evaluation_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at, evaluator = excluded.evaluator, evaluation_json = excluded.evaluation_json`,
		e.ID, e.Project.ID, e.Trace.ID, e.Status, e.CreatedAt, e.UpdatedAt, e.RubricHash, e.Evaluator, string(data))
	return err
}

func (s *Store) ListEvaluations(query Query) ([]asymptoteobserve.LearningEvaluationV1, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	where, args := queryWhere(query, "project_id", "status", "")
	rows, err := db.Query(`SELECT evaluation_json FROM evaluations`+where+` ORDER BY updated_at DESC LIMIT ? OFFSET ?`, append(args, normalizeLimit(query.Limit), offset(query))...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []asymptoteobserve.LearningEvaluationV1
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var value asymptoteobserve.LearningEvaluationV1
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, err
		}
		if query.Q == "" || matchesText(query.Q, value.ID, value.Trace.ID, value.Trace.Title, value.Evaluator, value.Status) {
			out = append(out, value)
		}
	}
	return out, rows.Err()
}

func (s *Store) GetEvaluation(id string) (asymptoteobserve.LearningEvaluationV1, bool, error) {
	var raw string
	ok, err := s.getJSON(`SELECT evaluation_json FROM evaluations WHERE id = ?`, id, &raw)
	if err != nil || !ok {
		return asymptoteobserve.LearningEvaluationV1{}, ok, err
	}
	var value asymptoteobserve.LearningEvaluationV1
	return value, true, json.Unmarshal([]byte(raw), &value)
}

func (s *Store) PutCandidate(c asymptoteobserve.LearningCandidateV1) error {
	if c.SchemaVersion == "" {
		c.SchemaVersion = asymptoteobserve.LearningSchemaVersion
	}
	now := nowString()
	if c.CreatedAt == "" {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	if c.State == "" {
		c.State = asymptoteobserve.LearningCandidateStateCandidate
	}
	if c.ID == "" {
		c.ID = CandidateID(c)
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO candidates (id, project_id, state, kind, title, source_evaluation_id, memory_id, created_at, updated_at, candidate_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET state = excluded.state, kind = excluded.kind, title = excluded.title, memory_id = excluded.memory_id, updated_at = excluded.updated_at, candidate_json = excluded.candidate_json`,
		c.ID, c.Project.ID, c.State, c.Kind, c.Title, c.SourceEvaluationID, c.MemoryID, c.CreatedAt, c.UpdatedAt, string(data))
	return err
}

func (s *Store) ListCandidates(query Query) ([]asymptoteobserve.LearningCandidateV1, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	where, args := queryWhere(query, "project_id", "state", "kind")
	rows, err := db.Query(`SELECT candidate_json FROM candidates`+where+` ORDER BY updated_at DESC LIMIT ? OFFSET ?`, append(args, normalizeLimit(query.Limit), offset(query))...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []asymptoteobserve.LearningCandidateV1
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var value asymptoteobserve.LearningCandidateV1
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, err
		}
		if query.Q == "" || matchesText(query.Q, value.ID, value.Title, value.Body, value.Kind, value.State) {
			out = append(out, value)
		}
	}
	return out, rows.Err()
}

func (s *Store) GetCandidate(id string) (asymptoteobserve.LearningCandidateV1, bool, error) {
	var raw string
	ok, err := s.getJSON(`SELECT candidate_json FROM candidates WHERE id = ?`, id, &raw)
	if err != nil || !ok {
		return asymptoteobserve.LearningCandidateV1{}, ok, err
	}
	var value asymptoteobserve.LearningCandidateV1
	return value, true, json.Unmarshal([]byte(raw), &value)
}

func (s *Store) GetCandidateBySourceEvaluation(sourceEvalID string) (asymptoteobserve.LearningCandidateV1, bool, error) {
	var raw string
	ok, err := s.getJSON(`SELECT candidate_json FROM candidates WHERE source_evaluation_id = ? LIMIT 1`, sourceEvalID, &raw)
	if err != nil || !ok {
		return asymptoteobserve.LearningCandidateV1{}, ok, err
	}
	var value asymptoteobserve.LearningCandidateV1
	return value, true, json.Unmarshal([]byte(raw), &value)
}

func (s *Store) PutMemory(m asymptoteobserve.LearningMemoryV1) error {
	if m.SchemaVersion == "" {
		m.SchemaVersion = asymptoteobserve.LearningSchemaVersion
	}
	now := nowString()
	if m.CreatedAt == "" {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	if m.ID == "" {
		m.ID = MemoryID(m)
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO memories (id, candidate_id, project_id, kind, title, created_at, updated_at, superseded_by, memory_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET title = excluded.title, updated_at = excluded.updated_at, superseded_by = excluded.superseded_by, memory_json = excluded.memory_json`,
		m.ID, m.CandidateID, m.Project.ID, m.Kind, m.Title, m.CreatedAt, m.UpdatedAt, m.SupersededBy, string(data))
	return err
}

func (s *Store) ListMemories(query Query) ([]asymptoteobserve.LearningMemoryV1, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	where, args := queryWhere(query, "project_id", "", "kind")
	if where == "" {
		where = ` WHERE (superseded_by IS NULL OR superseded_by = '')`
	} else {
		where += ` AND (superseded_by IS NULL OR superseded_by = '')`
	}
	rows, err := db.Query(`SELECT memory_json FROM memories`+where+` ORDER BY updated_at DESC LIMIT ? OFFSET ?`, append(args, normalizeLimit(query.Limit), offset(query))...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []asymptoteobserve.LearningMemoryV1
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var value asymptoteobserve.LearningMemoryV1
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, err
		}
		if query.Q == "" || matchesText(query.Q, value.ID, value.Title, value.Body, value.Kind) {
			out = append(out, value)
		}
	}
	return out, rows.Err()
}

func (s *Store) GetMemory(id string) (asymptoteobserve.LearningMemoryV1, bool, error) {
	var raw string
	ok, err := s.getJSON(`SELECT memory_json FROM memories WHERE id = ?`, id, &raw)
	if err != nil || !ok {
		return asymptoteobserve.LearningMemoryV1{}, ok, err
	}
	var value asymptoteobserve.LearningMemoryV1
	return value, true, json.Unmarshal([]byte(raw), &value)
}

func (s *Store) getJSON(query, id string, dest *string) (bool, error) {
	db, err := s.db()
	if err != nil {
		return false, err
	}
	defer db.Close()
	err = db.QueryRow(query, id).Scan(dest)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func EvaluationID(e asymptoteobserve.LearningEvaluationV1) string {
	return idFor("eval", e.Project.ID, e.Trace.ID, e.RubricHash)
}

func CandidateID(c asymptoteobserve.LearningCandidateV1) string {
	var evidence []string
	for _, item := range c.Evidence {
		evidence = append(evidence, item.TraceID, strings.Join(item.EventIDs, ","))
	}
	return idFor("candidate", c.Project.ID, c.Kind, c.Title, c.Body, strings.Join(evidence, "|"))
}

func MemoryID(m asymptoteobserve.LearningMemoryV1) string {
	return idFor("memory", m.Project.ID, m.CandidateID, m.Kind, m.Title)
}

func idFor(prefix string, values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return prefix + "_" + hex.EncodeToString(sum[:16])
}

func queryWhere(query Query, projectColumn, stateColumn, kindColumn string) (string, []interface{}) {
	var clauses []string
	var args []interface{}
	projectID := strings.TrimSpace(query.ProjectID)
	if projectID == "" && query.ProjectPath != "" {
		if project, err := ResolveProject(query.ProjectPath); err == nil {
			projectID = project.ID
		}
	}
	if projectID != "" && projectColumn != "" {
		clauses = append(clauses, projectColumn+" = ?")
		args = append(args, projectID)
	}
	if query.State != "" && stateColumn != "" {
		clauses = append(clauses, stateColumn+" = ?")
		args = append(args, query.State)
	}
	if query.Kind != "" && kindColumn != "" {
		clauses = append(clauses, kindColumn+" = ?")
		args = append(args, query.Kind)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func offset(query Query) int {
	page := query.Page
	if page <= 1 {
		return 0
	}
	return (page - 1) * normalizeLimit(query.Limit)
}

func matchesText(q string, values ...string) bool {
	terms := strings.Fields(strings.ToLower(q))
	if len(terms) == 0 {
		return true
	}
	haystack := strings.ToLower(strings.Join(values, "\n"))
	for _, term := range terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func nowString() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
