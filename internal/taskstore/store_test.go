package taskstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func openTest(t *testing.T, path string) *Store {
	t.Helper()
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func kind(t *testing.T, e error, k ErrorKind) {
	t.Helper()
	var typed *Error
	if !errors.As(e, &typed) || typed.Kind != k {
		t.Fatalf("want %s, got %v", k, e)
	}
}
func rootRequest() CreateRequest {
	return CreateRequest{Scope: Scope{CallerID: "alice"}, IdempotencyKey: "root", Goal: "synthetic goal", BaselineRef: "sha256:base", InputArtifactRef: "artifact:input", EnvironmentID: "env:reference-only"}
}
func root(t *testing.T, s *Store) Task {
	t.Helper()
	v, e := s.CreateRoot(context.Background(), rootRequest())
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestPersistenceScopeAndRelations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "空 格#question?.db")
	// '?' is not a legal Windows filename. '#' and Unicode still exercise URI escaping there.
	if os.PathSeparator == '\\' {
		path = filepath.Join(filepath.Dir(path), "空 格#question.db")
	}
	s := openTest(t, path)
	r := root(t, s)
	scope := Scope{"alice", r.ID}
	req := CreateRequest{Scope: scope, ParentID: r.ID, IdempotencyKey: "child", Goal: "child", InputArtifactRef: "artifact:child"}
	c, e := s.CreateChild(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	if c.ParentID != r.ID || c.RootID != r.ID || r.RootID != r.ID || r.ParentID != "" {
		t.Fatal("broken relationship")
	}
	req.ParentID = c.ID
	req.IdempotencyKey = "nested"
	_, e = s.CreateChild(ctx, req)
	kind(t, e, Invalid)
	for _, sc := range []Scope{{"bob", r.ID}, {"alice", "another-root"}} {
		_, e = s.Get(ctx, sc, c.ID)
		kind(t, e, NotFound)
		list, e := s.List(ctx, sc, 10)
		if e != nil || len(list) != 0 {
			t.Fatalf("scope leak: %v %v", list, e)
		}
		_, e = s.Transition(ctx, sc, c.ID, Created, Preparing)
		kind(t, e, NotFound)
	}
	req.Scope = Scope{"bob", r.ID}
	req.ParentID = r.ID
	_, e = s.CreateChild(ctx, req)
	kind(t, e, NotFound)
	_, e = s.Get(ctx, scope, "missing")
	kind(t, e, NotFound)
	list, e := s.List(ctx, scope, 1)
	if e != nil || len(list) != 1 {
		t.Fatal(list, e)
	}
	_, e = s.List(ctx, scope, 501)
	kind(t, e, Invalid)
	s.Close()
	s = openTest(t, path)
	got, e := s.Get(ctx, scope, r.ID)
	if e != nil || got != r {
		t.Fatalf("cold reopen: %#v %#v %v", got, r, e)
	}
	got, e = s.Get(ctx, scope, c.ID)
	if e != nil || got != c {
		t.Fatal("child not persisted", got, e)
	}
	replay, e := s.CreateRoot(ctx, rootRequest())
	if e != nil || replay != r {
		t.Fatal("reopen idempotency", replay, e)
	}
	var fk, timeout int
	var mode string
	if e = s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); e != nil {
		t.Fatal(e)
	}
	s.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout)
	s.db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if fk != 1 || timeout != 5000 || mode != "wal" || s.db.Stats().MaxOpenConnections != 1 {
		t.Fatalf("configuration: %d %d %s", fk, timeout, mode)
	}
}
func TestConcurrentIdempotency(t *testing.T) {
	for _, two := range []bool{false, true} {
		t.Run(map[bool]string{false: "one-store", true: "two-stores"}[two], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tasks.db")
			a := openTest(t, path)
			b := a
			if two {
				b = openTest(t, path)
			}
			ctx := context.Background()
			const n = 24
			results := make(chan Task, n)
			errs := make(chan error, n)
			var wg sync.WaitGroup
			run := func(req CreateRequest, child bool) {
				for i := 0; i < n; i++ {
					wg.Go(func() {
						s := a
						if i%2 == 1 {
							s = b
						}
						var v Task
						var e error
						if child {
							v, e = s.CreateChild(ctx, req)
						} else {
							v, e = s.CreateRoot(ctx, req)
						}
						results <- v
						errs <- e
					})
				}
				wg.Wait()
				var first string
				for i := 0; i < n; i++ {
					v := <-results
					if e := <-errs; e != nil {
						t.Fatal(e)
					}
					if first == "" {
						first = v.ID
					}
					if first != v.ID {
						t.Fatal("duplicate tasks")
					}
				}
			}
			req := rootRequest()
			run(req, false)
			r := root(t, a)
			child := CreateRequest{Scope: Scope{"alice", r.ID}, ParentID: r.ID, IdempotencyKey: "child", Goal: "work"}
			run(child, true)
			for _, mutate := range []func(*CreateRequest){func(r *CreateRequest) { r.Goal += "!" }, func(r *CreateRequest) { r.BaselineRef += "!" }, func(r *CreateRequest) { r.InputArtifactRef += "!" }, func(r *CreateRequest) { r.EnvironmentID += "!" }} {
				changed := child
				mutate(&changed)
				_, e := a.CreateChild(ctx, changed)
				kind(t, e, Conflict)
				changed = req
				mutate(&changed)
				_, e = a.CreateRoot(ctx, changed)
				kind(t, e, Conflict)
			}
			bob := req
			bob.Scope.CallerID = "bob"
			other, e := a.CreateRoot(ctx, bob)
			if e != nil || other.ID == r.ID {
				t.Fatal("caller key scope", e)
			}
			list, e := a.List(ctx, Scope{"alice", r.ID}, 100)
			if e != nil || len(list) != 2 {
				t.Fatal(list, e)
			}
		})
	}
}
func TestTransitionsAndTerminalParent(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, filepath.Join(t.TempDir(), "tasks.db"))
	r := root(t, s)
	scope := Scope{"alice", r.ID}
	_, e := s.Transition(ctx, scope, r.ID, Created, Succeeded)
	kind(t, e, Invalid)
	_, e = s.Transition(ctx, scope, r.ID, Running, Preparing)
	kind(t, e, Conflict)
	for _, pair := range [][2]State{{Created, Preparing}, {Preparing, Running}, {Running, WaitingApproval}, {WaitingApproval, Running}, {Running, Stopping}, {Stopping, Stopped}} {
		got, e := s.Transition(ctx, scope, r.ID, pair[0], pair[1])
		if e != nil || got.State != pair[1] || got.UpdatedAt.Before(r.CreatedAt) {
			t.Fatal(got, e)
		}
	}
	_, e = s.Transition(ctx, scope, r.ID, Stopped, Preparing)
	kind(t, e, Invalid)
	_, e = s.CreateChild(ctx, CreateRequest{Scope: scope, ParentID: r.ID, IdempotencyKey: "new", Goal: "no"})
	kind(t, e, Invalid)
	for _, st := range []State{Succeeded, Failed, Stopped, Expired} {
		for _, next := range []State{Created, Preparing, Running, WaitingApproval, Stopping, Succeeded, Failed, Stopped, Expired} {
			if AllowedTransition(st, next) {
				t.Fatal("terminal transition")
			}
		}
	}
}
func TestCancelCloseAndRollback(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "tasks.db"))
	r := root(t, s)
	scope := Scope{"alice", r.ID}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e := s.CreateRoot(ctx, rootRequest())
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	_, e = s.Get(ctx, scope, r.ID)
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	_, e = s.List(ctx, scope, 10)
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	_, e = s.Transition(ctx, scope, r.ID, Created, Preparing)
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	// Cancel while waiting for the only connection; no partial insert may survive.
	tx, e := s.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	waitCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	req := rootRequest()
	req.IdempotencyKey = "cancelled"
	go func() { _, err := s.CreateRoot(waitCtx, req); done <- err }()
	stop()
	if e = <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	tx.Rollback()
	list, e := s.List(context.Background(), scope, 10)
	if e != nil || len(list) != 1 {
		t.Fatal(list, e)
	}
	s.Close()
	s.Close()
	_, e = s.Get(context.Background(), scope, r.ID)
	kind(t, e, Closed)
	_, e = s.CreateRoot(context.Background(), rootRequest())
	kind(t, e, Closed)
	_, e = s.List(context.Background(), scope, 10)
	kind(t, e, Closed)
	_, e = s.Transition(context.Background(), scope, r.ID, Created, Preparing)
	kind(t, e, Closed)
}
func TestPathsAndFutureSchema(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{"", ":memory:", "file:anything?mode=memory", "https://example/db", `\\server\share\db`, "//server/db", dir} {
		_, e := Open(path)
		kind(t, e, Invalid)
	}
	target := filepath.Join(dir, "target.db")
	s := openTest(t, target)
	s.Close()
	link := filepath.Join(dir, "link.db")
	if e := os.Symlink(target, link); e != nil {
		t.Log("symlink creation unavailable:", e)
	} else {
		_, e = Open(link)
		kind(t, e, Invalid)
	}
	future := filepath.Join(dir, "future.db")
	db, e := sql.Open("sqlite", future)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec("CREATE TABLE sentinel(value TEXT); INSERT INTO sentinel VALUES('preserve'); PRAGMA user_version=99;")
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	before, e := os.ReadFile(future)
	if e != nil {
		t.Fatal(e)
	}
	_, e = Open(future)
	kind(t, e, Invalid)
	after, e := os.ReadFile(future)
	if e != nil {
		t.Fatal(e)
	}
	if string(before) != string(after) {
		t.Fatal("future schema file changed")
	}
	db, e = sql.Open("sqlite", future)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var version int
	db.QueryRow("PRAGMA user_version").Scan(&version)
	if version != 99 {
		t.Fatal(version)
	}
}
