package artifactstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestConcurrentOpenAndConflict(t *testing.T) {
	d := privateDir(t)
	var wg sync.WaitGroup
	stores := make(chan *Store, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Open(d)
			if err != nil {
				t.Error(err)
				return
			}
			stores <- s
		}()
	}
	wg.Wait()
	close(stores)
	var all []*Store
	for s := range stores {
		all = append(all, s)
		defer s.Close()
	}
	if len(all) != 8 {
		t.Fatal("open failures")
	}
	results := make(chan error, 8)
	for i, s := range all {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			r := req()
			r.Bytes = []byte{byte(i)}
			_, err := s.Put(context.Background(), r)
			results <- err
		}(i, s)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			wantKind(t, err, Conflict)
			conflicts++
		}
	}
	if success != 1 || conflicts != 7 {
		t.Fatal(success, conflicts)
	}
}
func TestCancellationWhileWaitingForConnection(t *testing.T) {
	s := openTest(t, privateDir(t))
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, f := range []func(context.Context) error{
		func(ctx context.Context) error { _, err := s.Put(ctx, req()); return err },
		func(ctx context.Context) error { _, err := s.Read(ctx, req().Scope, "id"); return err },
		func(ctx context.Context) error { _, err := s.List(ctx, req().Scope, "", 1); return err },
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		err = f(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	}
}
