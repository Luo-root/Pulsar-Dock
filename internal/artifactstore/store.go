// Package artifactstore stores immutable small artifacts for trusted control-plane callers.
// It does not authenticate identities or validate caller/root/task/environment relationships:
// the trusted caller must do so before constructing a Scope or PutRequest. Read access is
// NOT permission to forward data to a model, MCP, or another environment. Content is
// untrusted and unscanned for secrets; this package neither executes nor applies it.
// The private directory and its ancestors must remain under trusted host control for
// the lifetime of every Store (path checks do not defeat hostile host rename races).
package artifactstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const MaxBytes = 4 << 20
const MaxPageSize = 8

type ErrorKind string

const (
	Invalid     ErrorKind = "invalid"
	NotFound    ErrorKind = "notfound"
	Conflict    ErrorKind = "conflict"
	Corrupt     ErrorKind = "corrupt"
	Closed      ErrorKind = "closed"
	Unavailable ErrorKind = "unavailable"
)

type Error struct{ Kind ErrorKind }

func (e *Error) Error() string { return "artifact store: " + string(e.Kind) }
func fault(k ErrorKind) error  { return &Error{Kind: k} }

// Scope is an exact read boundary, not an authorization claim accepted from an Agent.
type Scope struct{ CallerID, RootID, TaskID string }
type PutRequest struct {
	Scope                                               Scope
	EnvironmentID, Kind, BaselineDigest, IdempotencyKey string
	Bytes                                               []byte
}

// Metadata is immutable. Sensitivity is deliberately fixed to restricted-unscanned;
// downstream publication must independently establish labels and propagation policy.
type Metadata struct {
	ID                                                  string
	Scope                                               Scope
	EnvironmentID, Kind, BaselineDigest, IdempotencyKey string
	Digest                                              string
	Size                                                int
	CreatedAt                                           time.Time
	Sensitivity                                         string
}
type Artifact struct {
	Metadata Metadata
	Bytes    []byte
}

// Page uses an exclusive opaque ID cursor. Each page is bounded; concurrent inserts
// need not appear in an ongoing traversal. This is not a stable snapshot listing.
type Page struct {
	Items []Metadata
	Next  string
}
type Store struct {
	db     *sql.DB
	closed atomic.Bool
}

// Open requires an explicit existing private absolute local directory. It never
// accepts a source path, creates parent directories, or repairs permissions.
// On Unix the directory must have no group/other permissions; on Windows the
// trusted caller must provision an owner-only ACL (ACL policy is not inferred).
func Open(dir string) (*Store, error) {
	if dir == "" || !filepath.IsAbs(dir) || strings.ContainsRune(dir, 0) || strings.HasPrefix(dir, `\\`) || strings.HasPrefix(dir, "//") || strings.Contains(dir, "://") {
		return nil, fault(Invalid)
	}
	// Reject lexical aliases rather than clean away a linked component before checking it.
	if filepath.Clean(dir) != dir {
		return nil, fault(Invalid)
	}
	vol := filepath.VolumeName(dir)
	if strings.Contains(strings.TrimPrefix(dir, vol), ":") {
		return nil, fault(Invalid)
	}
	for p := dir; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fault(Invalid)
		}
		if err = checkPath(p, info, p == dir); err != nil {
			return nil, fault(Invalid)
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if err := checkLocal(dir); err != nil {
		return nil, fault(Invalid)
	}
	path := filepath.Join(dir, "artifacts.sqlite")
	for _, p := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || checkPath(p, info, true) != nil {
			return nil, fault(Invalid)
		}
	}
	// Precreate with private permissions before SQLite can open it.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		err = f.Close()
	} else if os.IsExist(err) {
		err = nil
	}
	if err != nil {
		return nil, fault(Unavailable)
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	if vol != "" {
		u.Path = "/" + u.Path
	}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fault(Unavailable)
	}
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
	tx, err := s.db.Begin()
	if err != nil {
		return fault(Unavailable)
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fault(Unavailable)
	}
	if version != 0 && version != 1 {
		return fault(Invalid)
	}
	if version == 0 {
		// Refuse unversioned databases containing unrelated objects.
		var n int
		if err = tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&n); err != nil || n != 0 {
			return fault(Invalid)
		}
		_, err = tx.Exec(`CREATE TABLE artifacts (
 id TEXT PRIMARY KEY, caller TEXT NOT NULL, root TEXT NOT NULL, task TEXT NOT NULL,
 idem TEXT NOT NULL, request_digest TEXT NOT NULL, metadata BLOB NOT NULL,
 record_digest TEXT NOT NULL, payload BLOB NOT NULL CHECK(length(payload)<=4194304),
 UNIQUE(caller,root,task,idem));
 CREATE INDEX artifact_scope ON artifacts(caller,root,task,id);
 PRAGMA user_version=1;`)
		if err != nil {
			return fault(Unavailable)
		}
	}
	if err = tx.Commit(); err != nil {
		return fault(Unavailable)
	}
	return nil
}
func (s *Store) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	if s.db.Close() != nil {
		return fault(Unavailable)
	}
	return nil
}
func (s *Store) check(ctx context.Context) error {
	if s.closed.Load() {
		return fault(Closed)
	}
	return ctx.Err()
}
func (s *Store) normalize(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.closed.Load() {
		return fault(Closed)
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return fault(Unavailable)
}
func valid(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= 512 && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func validScope(s Scope) bool { return valid(s.CallerID) && valid(s.RootID) && valid(s.TaskID) }
func digest(b []byte) string  { d := sha256.Sum256(b); return hex.EncodeToString(d[:]) }
func requestDigest(r PutRequest) string { // JSON length-delimits all fields; nil and empty bytes are equivalent.
	r.Bytes = append([]byte{}, r.Bytes...)
	b, _ := json.Marshal(r)
	return digest(b)
}

const columns = "id,caller,root,task,idem,request_digest,metadata,record_digest,payload"

type scanner interface{ Scan(...any) error }

func decode(row scanner) (Artifact, string, error) {
	var id, caller, root, task, key, rd, md string
	var meta, payload []byte
	if err := row.Scan(&id, &caller, &root, &task, &key, &rd, &meta, &md, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Artifact{}, "", fault(NotFound)
		}
		return Artifact{}, "", err
	}
	var m Metadata
	if len(payload) > MaxBytes || digest(meta) != md || json.Unmarshal(meta, &m) != nil || m.ID != id || m.Scope != (Scope{caller, root, task}) || m.IdempotencyKey != key || m.Size != len(payload) || m.Digest != digest(payload) || m.Sensitivity != "restricted-unscanned" {
		return Artifact{}, "", fault(Corrupt)
	}
	r := PutRequest{m.Scope, m.EnvironmentID, m.Kind, m.BaselineDigest, m.IdempotencyKey, payload}
	if requestDigest(r) != rd {
		return Artifact{}, "", fault(Corrupt)
	}
	return Artifact{m, payload}, rd, nil
}
func (s *Store) Put(ctx context.Context, r PutRequest) (Metadata, error) {
	if err := s.check(ctx); err != nil {
		return Metadata{}, err
	}
	if !validScope(r.Scope) || !valid(r.EnvironmentID) || !valid(r.Kind) || !valid(r.BaselineDigest) || !valid(r.IdempotencyKey) || len(r.Bytes) > MaxBytes {
		return Metadata{}, fault(Invalid)
	}
	r.Bytes = append([]byte{}, r.Bytes...)
	rd := requestDigest(r)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Metadata{}, s.normalize(ctx, err)
	}
	defer tx.Rollback()
	a, old, err := decode(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM artifacts WHERE caller=? AND root=? AND task=? AND idem=?", r.Scope.CallerID, r.Scope.RootID, r.Scope.TaskID, r.IdempotencyKey))
	if err == nil {
		if old != rd {
			return Metadata{}, fault(Conflict)
		}
		return a.Metadata, nil
	}
	var e *Error
	if !errors.As(err, &e) || e.Kind != NotFound {
		return Metadata{}, s.normalize(ctx, err)
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return Metadata{}, fault(Unavailable)
	}
	m := Metadata{hex.EncodeToString(random[:]), r.Scope, r.EnvironmentID, r.Kind, r.BaselineDigest, r.IdempotencyKey, digest(r.Bytes), len(r.Bytes), time.Now().UTC(), "restricted-unscanned"}
	meta, _ := json.Marshal(m)
	_, err = tx.ExecContext(ctx, "INSERT INTO artifacts VALUES(?,?,?,?,?,?,?,?,?)", m.ID, m.Scope.CallerID, m.Scope.RootID, m.Scope.TaskID, m.IdempotencyKey, rd, meta, digest(meta), r.Bytes)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return Metadata{}, s.normalize(ctx, err)
	}
	return m, nil
}
func (s *Store) Read(ctx context.Context, scope Scope, id string) (Artifact, error) {
	if err := s.check(ctx); err != nil {
		return Artifact{}, err
	}
	if !validScope(scope) || !valid(id) {
		return Artifact{}, fault(Invalid)
	}
	a, _, err := decode(s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM artifacts WHERE caller=? AND root=? AND task=? AND id=?", scope.CallerID, scope.RootID, scope.TaskID, id))
	return a, s.normalize(ctx, err)
}

// List reads and verifies each payload before returning its metadata. It also
// verifies one lookahead row to determine Next: at most (MaxPageSize+1)*MaxBytes
// (36 MiB) of payload per call. Payloads are processed per row, not kept in Page.
func (s *Store) List(ctx context.Context, scope Scope, after string, limit int) (Page, error) {
	if err := s.check(ctx); err != nil {
		return Page{}, err
	}
	if !validScope(scope) || limit < 1 || limit > MaxPageSize || (after != "" && !valid(after)) {
		return Page{}, fault(Invalid)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+columns+" FROM artifacts WHERE caller=? AND root=? AND task=? AND id>? ORDER BY id LIMIT ?", scope.CallerID, scope.RootID, scope.TaskID, after, limit+1)
	if err != nil {
		return Page{}, s.normalize(ctx, err)
	}
	defer rows.Close()
	p := Page{Items: []Metadata{}}
	for rows.Next() {
		a, _, err := decode(rows)
		if err != nil {
			return Page{}, s.normalize(ctx, err)
		}
		if len(p.Items) == limit {
			p.Next = p.Items[len(p.Items)-1].ID
			break
		}
		p.Items = append(p.Items, a.Metadata)
	}
	if err = rows.Err(); err != nil {
		return Page{}, s.normalize(ctx, err)
	}
	return p, nil
}
