// Package taskstore persists task records only; it neither authenticates callers nor starts environments.
package taskstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

type ErrorKind string

const (
	NotFound ErrorKind = "notfound"
	Conflict ErrorKind = "conflict"
	Invalid  ErrorKind = "invalid"
	Closed   ErrorKind = "closed"
)

type Error struct {
	Kind    ErrorKind
	Message string
}

func (e *Error) Error() string          { return string(e.Kind) + ": " + e.Message }
func fault(k ErrorKind, m string) error { return &Error{Kind: k, Message: m} }

type State string

const (
	Created         State = "created"
	Preparing       State = "preparing"
	Running         State = "running"
	WaitingApproval State = "waiting_approval"
	Stopping        State = "stopping"
	Succeeded       State = "succeeded"
	Failed          State = "failed"
	Stopped         State = "stopped"
	Expired         State = "expired"
)

func terminal(s State) bool { return s == Succeeded || s == Failed || s == Stopped || s == Expired }

// AllowedTransition is the explicit first-version state graph. There is no resume from terminal states.
func AllowedTransition(from, to State) bool {
	switch from {
	case Created:
		return to == Preparing || to == Stopping || to == Failed || to == Expired
	case Preparing:
		return to == Running || to == WaitingApproval || to == Stopping || to == Failed || to == Expired
	case Running:
		return to == WaitingApproval || to == Stopping || to == Succeeded || to == Failed || to == Expired
	case WaitingApproval:
		return to == Preparing || to == Running || to == Stopping || to == Failed || to == Expired
	case Stopping:
		return to == Stopped || to == Failed || to == Expired
	}
	return false
}

type Task struct {
	ID, CallerID, ParentID, RootID, Goal         string
	State                                        State
	CreatedAt, UpdatedAt                         time.Time
	BaselineRef, InputArtifactRef, EnvironmentID string
}

// Scope must come from a trusted caller identity, not an Agent-supplied identity field.
// RootID is required for reads, transitions and child creation.
type Scope struct{ CallerID, RootID string }

// CreateRequest is hashed in full, including scope and parent, before persistence.
// Optional references are opaque immutable handles, never credential values.
type CreateRequest struct {
	Scope                                        Scope
	ParentID, IdempotencyKey, Goal               string
	BaselineRef, InputArtifactRef, EnvironmentID string
}
type Store struct {
	db     *sql.DB
	closed atomic.Bool
}

// Open accepts a filesystem path, never a SQLite URI. Existing directories and
// links (including linked ancestors) are rejected. The containing directory must
// already exist. This is not protection against a hostile host swapping paths.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) || strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") || strings.Contains(path, "://") || strings.HasPrefix(strings.ToLower(path), "file:") || path == ":memory:" {
		return nil, fault(Invalid, "local database path required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fault(Invalid, "invalid path")
	}
	// Windows device paths, alternate streams and drive-relative paths are not database paths.
	vol := filepath.VolumeName(abs)
	if strings.Contains(strings.TrimPrefix(abs, vol), ":") || (filepath.VolumeName(path) != "" && !filepath.IsAbs(path)) {
		return nil, fault(Invalid, "invalid local path")
	}
	for p := abs; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil {
			if !(p == abs && os.IsNotExist(e)) {
				return nil, fault(Invalid, "database path or containing directory is unavailable")
			}
		} else {
			if info.Mode()&os.ModeSymlink != 0 || (p == abs && !info.Mode().IsRegular()) {
				return nil, fault(Invalid, "database path must not be a link or directory")
			}
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	if vol != "" && !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	// One physical connection per Store; DSN pragmas also apply to replacements.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err = s.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) initialize() error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var v int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v > 2 {
		return fault(Invalid, "database schema is newer than supported version 2")
	}
	if v == 0 {
		_, err = tx.ExecContext(ctx, `CREATE TABLE tasks (
 id TEXT PRIMARY KEY, caller TEXT NOT NULL, parent_id TEXT REFERENCES tasks(id), root_id TEXT NOT NULL REFERENCES tasks(id),
 goal TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('created','preparing','running','waiting_approval','stopping','succeeded','failed','stopped','expired')),
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, baseline_ref TEXT NOT NULL, input_ref TEXT NOT NULL, environment_id TEXT NOT NULL,
 scope_root TEXT NOT NULL, scope_parent TEXT NOT NULL, idem_key TEXT NOT NULL, request_digest TEXT NOT NULL,
 UNIQUE(caller,scope_root,scope_parent,idem_key));
 CREATE INDEX tasks_scope ON tasks(caller,root_id,id);
 PRAGMA user_version=1;`)
		if err != nil {
			return err
		}
	}
	if v < 2 {
		if _, err = tx.ExecContext(ctx, `CREATE TABLE root_revocations (
 root_id TEXT PRIMARY KEY REFERENCES tasks(id), caller TEXT NOT NULL);
 PRAGMA user_version=2;`); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	var mode string
	if err = s.db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return err
	}
	if mode != "wal" {
		return fault(Invalid, "WAL unavailable")
	}
	return nil
}
func (s *Store) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	return s.db.Close()
}
func (s *Store) check(ctx context.Context) error {
	if s.closed.Load() {
		return fault(Closed, "store is closed")
	}
	return ctx.Err()
}
func (s *Store) normalize(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	if s.closed.Load() {
		return fault(Closed, "store is closed")
	}
	return err
}

const columns = "id,caller,COALESCE(parent_id,''),root_id,goal,state,created_at,updated_at,baseline_ref,input_ref,environment_id"

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Task, error) {
	var t Task
	var c, u int64
	err := row.Scan(&t.ID, &t.CallerID, &t.ParentID, &t.RootID, &t.Goal, &t.State, &c, &u, &t.BaselineRef, &t.InputArtifactRef, &t.EnvironmentID)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fault(NotFound, "task not found")
	}
	t.CreatedAt = time.Unix(0, c).UTC()
	t.UpdatedAt = time.Unix(0, u).UTC()
	return t, err
}
func validScope(s Scope) bool {
	return strings.TrimSpace(s.CallerID) != "" && strings.TrimSpace(s.RootID) != ""
}
func (s *Store) CreateRoot(ctx context.Context, r CreateRequest) (Task, error) {
	if r.ParentID != "" || r.Scope.RootID != "" {
		return Task{}, fault(Invalid, "root must not specify parent or root ID")
	}
	return s.create(ctx, r, false)
}
func (s *Store) CreateChild(ctx context.Context, r CreateRequest) (Task, error) {
	if !validScope(r.Scope) || strings.TrimSpace(r.ParentID) == "" {
		return Task{}, fault(Invalid, "child requires root and parent")
	}
	return s.create(ctx, r, true)
}
func (s *Store) create(ctx context.Context, r CreateRequest, child bool) (Task, error) {
	if err := s.check(ctx); err != nil {
		return Task{}, err
	}
	if strings.TrimSpace(r.Scope.CallerID) == "" || strings.TrimSpace(r.Goal) == "" || strings.TrimSpace(r.IdempotencyKey) == "" {
		return Task{}, fault(Invalid, "caller, goal and idempotency key required")
	}
	payload, _ := json.Marshal(r)
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, s.normalize(ctx, err)
	}
	defer tx.Rollback()
	var existingID, existingDigest string
	err = tx.QueryRowContext(ctx, "SELECT id,request_digest FROM tasks WHERE caller=? AND scope_root=? AND scope_parent=? AND idem_key=?", r.Scope.CallerID, r.Scope.RootID, r.ParentID, r.IdempotencyKey).Scan(&existingID, &existingDigest)
	if err == nil {
		if digest != existingDigest {
			return Task{}, fault(Conflict, "idempotency request differs")
		}
		t, e := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM tasks WHERE id=?", existingID))
		return t, s.normalize(ctx, e)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Task{}, s.normalize(ctx, err)
	}
	if child {
		parent, e := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM tasks WHERE id=? AND caller=? AND root_id=?", r.ParentID, r.Scope.CallerID, r.Scope.RootID))
		if e != nil {
			return Task{}, s.normalize(ctx, e)
		}
		if terminal(parent.State) {
			return Task{}, fault(Invalid, "terminal parent cannot derive tasks")
		}
		// P0 delegation is one level only, as required by the architecture contract.
		if parent.ParentID != "" {
			return Task{}, fault(Invalid, "nested child delegation is not supported")
		}
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return Task{}, err
	}
	t := Task{ID: "task-" + hex.EncodeToString(random[:]), CallerID: r.Scope.CallerID, ParentID: r.ParentID, RootID: r.Scope.RootID, Goal: r.Goal, State: Created, CreatedAt: time.Now().UTC(), BaselineRef: r.BaselineRef, InputArtifactRef: r.InputArtifactRef, EnvironmentID: r.EnvironmentID}
	if !child {
		t.RootID = t.ID
	}
	t.UpdatedAt = t.CreatedAt
	var parent any
	if child {
		parent = t.ParentID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks(id,caller,parent_id,root_id,goal,state,created_at,updated_at,baseline_ref,input_ref,environment_id,scope_root,scope_parent,idem_key,request_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, t.ID, t.CallerID, parent, t.RootID, t.Goal, t.State, t.CreatedAt.UnixNano(), t.UpdatedAt.UnixNano(), t.BaselineRef, t.InputArtifactRef, t.EnvironmentID, r.Scope.RootID, r.ParentID, r.IdempotencyKey, digest)
	if err != nil {
		return Task{}, s.normalize(ctx, err)
	}
	if err = tx.Commit(); err != nil {
		return Task{}, s.normalize(ctx, err)
	}
	return t, nil
}
func (s *Store) Get(ctx context.Context, scope Scope, id string) (Task, error) {
	if err := s.check(ctx); err != nil {
		return Task{}, err
	}
	if !validScope(scope) {
		return Task{}, fault(Invalid, "caller and root required")
	}
	t, err := scan(s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM tasks WHERE caller=? AND root_id=? AND id=?", scope.CallerID, scope.RootID, id))
	return t, s.normalize(ctx, err)
}

// List returns at most 500 records ordered by random ID; it is not a chronological feed.
func (s *Store) List(ctx context.Context, scope Scope, limit int) ([]Task, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	if !validScope(scope) || limit < 1 || limit > 500 {
		return nil, fault(Invalid, "scope and limit 1..500 required")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+columns+" FROM tasks WHERE caller=? AND root_id=? ORDER BY id LIMIT ?", scope.CallerID, scope.RootID, limit)
	if err != nil {
		return nil, s.normalize(ctx, err)
	}
	defer rows.Close()
	tasks := []Task{}
	for rows.Next() {
		t, e := scan(rows)
		if e != nil {
			return nil, s.normalize(ctx, e)
		}
		tasks = append(tasks, t)
	}
	return tasks, s.normalize(ctx, rows.Err())
}

// BindEnvironment is a trusted Controller operation, not an Agent-facing API.
// It binds a backend-returned ID once, without changing task state or the original
// creation request digest. Retrying the same ID is idempotent only while the
// expected state still matches. On failure, the caller must first inspect the
// persisted binding: only an environment confirmed as an unadopted candidate may
// be cleaned up. A state conflict, cancellation, or unknown commit result must
// not directly trigger deletion of a possibly adopted environment. This Store
// does not manage runtime resources.
func (s *Store) BindEnvironment(ctx context.Context, scope Scope, id string, expected State, environmentID string) (Task, error) {
	if err := s.check(ctx); err != nil {
		return Task{}, err
	}
	if !validScope(scope) || strings.TrimSpace(id) == "" || strings.TrimSpace(environmentID) == "" {
		return Task{}, fault(Invalid, "caller, root, task and environment required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, s.normalize(ctx, err)
	}
	defer tx.Rollback()
	t, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM tasks WHERE caller=? AND root_id=? AND id=?", scope.CallerID, scope.RootID, id))
	if err != nil {
		return Task{}, s.normalize(ctx, err)
	}
	if t.State != expected {
		return Task{}, fault(Conflict, "state does not match expected")
	}
	if t.EnvironmentID != "" {
		if t.EnvironmentID != environmentID {
			return Task{}, fault(Conflict, "environment is already bound")
		}
		return t, nil
	}
	t.EnvironmentID = environmentID
	t.UpdatedAt = time.Now().UTC()
	result, err := tx.ExecContext(ctx, "UPDATE tasks SET environment_id=?,updated_at=? WHERE id=? AND caller=? AND root_id=? AND state=? AND environment_id=''", environmentID, t.UpdatedAt.UnixNano(), id, scope.CallerID, scope.RootID, expected)
	if err != nil {
		return Task{}, s.normalize(ctx, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Task{}, err
	}
	if n != 1 {
		return Task{}, fault(Conflict, "task changed")
	}
	if err = tx.Commit(); err != nil {
		return Task{}, s.normalize(ctx, err)
	}
	return t, nil
}

func (s *Store) Transition(ctx context.Context, scope Scope, id string, expected, next State) (Task, error) {
	if err := s.check(ctx); err != nil {
		return Task{}, err
	}
	if !validScope(scope) {
		return Task{}, fault(Invalid, "caller and root required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, s.normalize(ctx, err)
	}
	defer tx.Rollback()
	t, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM tasks WHERE caller=? AND root_id=? AND id=?", scope.CallerID, scope.RootID, id))
	if err != nil {
		return Task{}, s.normalize(ctx, err)
	}
	if t.State != expected {
		return Task{}, fault(Conflict, "state does not match expected")
	}
	if !AllowedTransition(expected, next) {
		return Task{}, fault(Invalid, "transition is not allowed")
	}
	t.State = next
	t.UpdatedAt = time.Now().UTC()
	result, err := tx.ExecContext(ctx, "UPDATE tasks SET state=?,updated_at=? WHERE id=? AND caller=? AND root_id=? AND state=?", next, t.UpdatedAt.UnixNano(), id, scope.CallerID, scope.RootID, expected)
	if err != nil {
		return Task{}, s.normalize(ctx, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Task{}, err
	}
	if n != 1 {
		return Task{}, fault(Conflict, "state changed")
	}
	if err = tx.Commit(); err != nil {
		return Task{}, s.normalize(ctx, err)
	}
	return t, nil
}
