package taskstore

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestBindEnvironment(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(canonicalTempDir(t), "binding.db")
	s := openTest(t, path)
	req := rootRequest()
	req.EnvironmentID = ""
	r, err := s.CreateRoot(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{"alice", r.ID}
	for _, sc := range []Scope{{"bob", r.ID}, {"alice", "other"}} {
		_, err = s.BindEnvironment(ctx, sc, r.ID, Created, "real-id")
		kind(t, err, NotFound)
	}
	_, err = s.BindEnvironment(ctx, scope, r.ID, Preparing, "real-id")
	kind(t, err, Conflict)
	for _, id := range []string{"", " "} {
		_, err = s.BindEnvironment(ctx, scope, r.ID, Created, id)
		kind(t, err, Invalid)
	}
	_, err = s.BindEnvironment(ctx, Scope{}, r.ID, Created, "real-id")
	kind(t, err, Invalid)
	_, err = s.BindEnvironment(ctx, scope, "", Created, "real-id")
	kind(t, err, Invalid)
	got, err := s.Get(ctx, scope, r.ID)
	if err != nil || got != r {
		t.Fatal("failed binds mutated task", got, err)
	}
	bound, err := s.BindEnvironment(ctx, scope, r.ID, Created, "backend-real-id")
	if err != nil || bound.EnvironmentID != "backend-real-id" || bound.State != Created {
		t.Fatal(bound, err)
	}
	again, err := s.BindEnvironment(ctx, scope, r.ID, Created, "backend-real-id")
	if err != nil || again != bound {
		t.Fatal("non-idempotent bind", again, err)
	}
	_, err = s.BindEnvironment(ctx, scope, r.ID, Created, "different")
	kind(t, err, Conflict)
	// Binding must not change the digest used by creation retries.
	again, err = s.CreateRoot(ctx, req)
	if err != nil || again != bound {
		t.Fatal("creation retry changed", again, err)
	}
	s.Close()
	s = openTest(t, path)
	got, err = s.Get(ctx, scope, r.ID)
	if err != nil || got != bound {
		t.Fatal("binding not persisted", got, err)
	}
	_, err = s.Transition(ctx, scope, r.ID, Created, Preparing)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.BindEnvironment(ctx, scope, r.ID, Created, "backend-real-id")
	kind(t, err, Conflict)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = s.BindEnvironment(canceled, scope, r.ID, Preparing, "backend-real-id")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.Close()
	_, err = s.BindEnvironment(ctx, scope, r.ID, Preparing, "backend-real-id")
	kind(t, err, Closed)
}

func TestConcurrentEnvironmentBinding(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(canonicalTempDir(t), "binding.db")
	a, b := openTest(t, path), openTest(t, path)
	req := rootRequest()
	req.EnvironmentID = ""
	r, err := a.CreateRoot(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{"alice", r.ID}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, s := range []*Store{a, b} {
		wg.Add(1)
		go func(s *Store, id string) {
			defer wg.Done()
			<-start
			_, e := s.BindEnvironment(ctx, scope, r.ID, Created, id)
			results <- e
		}(s, []string{"real-a", "real-b"}[i])
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else {
			kind(t, e, Conflict)
		}
	}
	if success != 1 {
		t.Fatal("want exactly one winner", success)
	}
	got, err := a.Get(ctx, scope, r.ID)
	if err != nil || (got.EnvironmentID != "real-a" && got.EnvironmentID != "real-b") {
		t.Fatal(got, err)
	}
}
