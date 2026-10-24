package taskstore

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestCASAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cas.db")
	a := openTest(t, path)
	b := openTest(t, path)
	r := root(t, a)
	ctx := context.Background()
	scope := Scope{"alice", r.ID}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, s := range []*Store{a, b} {
		wg.Go(func() { _, e := s.Transition(ctx, scope, r.ID, Created, Preparing); results <- e })
	}
	wg.Wait()
	success, conflicts := 0, 0
	for range 2 {
		e := <-results
		if e == nil {
			success++
		} else {
			var typed *Error
			if !errors.As(e, &typed) || typed.Kind != Conflict {
				t.Fatal(e)
			}
			conflicts++
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal(success, conflicts)
	}
}

func TestTerminalReplayAndInvalidRequests(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "contract.db"))
	ctx := context.Background()
	r := root(t, s)
	scope := Scope{"alice", r.ID}
	req := CreateRequest{Scope: scope, ParentID: r.ID, IdempotencyKey: "child", Goal: "synthetic"}
	c, e := s.CreateChild(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Transition(ctx, scope, r.ID, Created, Failed)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := s.CreateChild(ctx, req)
	if e != nil || replay != c {
		t.Fatal("replay must survive parent termination", replay, e)
	}
	req.IdempotencyKey = "new"
	_, e = s.CreateChild(ctx, req)
	kind(t, e, Invalid)
	req.ParentID = "absent"
	_, e = s.CreateChild(ctx, req)
	kind(t, e, NotFound)
	for _, bad := range []CreateRequest{{}, {Scope: Scope{CallerID: "alice"}, Goal: "goal"}, {Scope: Scope{CallerID: "alice"}, IdempotencyKey: "key"}, {Scope: scope, Goal: "goal", IdempotencyKey: "key"}} {
		_, e = s.CreateRoot(ctx, bad)
		kind(t, e, Invalid)
	}
	_, e = s.CreateChild(ctx, CreateRequest{})
	kind(t, e, Invalid)
	_, e = s.Get(ctx, Scope{}, r.ID)
	kind(t, e, Invalid)
	_, e = s.List(ctx, scope, 0)
	kind(t, e, Invalid)
	_, e = Open(filepath.Join(t.TempDir(), "missing-parent", "db"))
	kind(t, e, Invalid)
}
