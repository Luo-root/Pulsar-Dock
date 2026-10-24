package taskstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// seedRecovery intentionally returns no task IDs or creation requests.
func seedRecovery(t *testing.T, path string) {
	t.Helper()
	s := openTest(t, path)
	defer s.Close()
	ctx := context.Background()
	for _, caller := range []string{"alice", "bob"} {
		for i := 0; i < 7; i++ {
			r, e := s.CreateRoot(ctx, CreateRequest{Scope: Scope{CallerID: caller}, IdempotencyKey: fmt.Sprint(i), Goal: fmt.Sprint("root-", i), EnvironmentID: fmt.Sprint("root-env-", i)})
			if e != nil {
				t.Fatal(e)
			}
			_, e = s.CreateChild(ctx, CreateRequest{Scope: Scope{caller, r.ID}, ParentID: r.ID, IdempotencyKey: "child", Goal: "child", EnvironmentID: fmt.Sprint("child-env-", i)})
			if e != nil {
				t.Fatal(e)
			}
		}
	}
}

func TestColdRecoveryDiscovery(t *testing.T) {
	path := filepath.Join(canonicalTempDir(t), "recovery.db")
	seedRecovery(t, path)
	s := openTest(t, path)
	ctx := context.Background()
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		page, e := s.ListRootsForRecovery(ctx, "alice", cursor, 2)
		if e != nil {
			t.Fatal(e)
		}
		pages++
		for _, r := range page.Tasks {
			if seen[r.ID] || r.CallerID != "alice" || r.ParentID != "" || r.RootID != r.ID {
				t.Fatal("duplicate or scope leak", r)
			}
			seen[r.ID] = true
			scope := Scope{"alice", r.ID}
			got, e := s.Get(ctx, scope, r.ID)
			if e != nil || got.EnvironmentID != r.EnvironmentID || got.EnvironmentID == "" {
				t.Fatal(got, e)
			}
			tasks, e := s.List(ctx, scope, 10)
			if e != nil || len(tasks) != 2 {
				t.Fatal(tasks, e)
			}
			children := 0
			for _, task := range tasks {
				if task.ParentID != "" {
					children++
					if task.ParentID != r.ID || task.EnvironmentID == "" {
						t.Fatal(task)
					}
				}
			}
			if children != 1 {
				t.Fatal(children)
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		// A cursor must work on a fresh Store without in-memory recovery state.
		s.Close()
		s = openTest(t, path)
	}
	if len(seen) != 7 || pages != 4 {
		t.Fatal(len(seen), pages)
	}
	for _, limit := range []int{1, 7, 8, 500} {
		total := 0
		cursor = ""
		for {
			p, e := s.ListRootsForRecovery(ctx, "bob", cursor, limit)
			if e != nil {
				t.Fatal(e)
			}
			total += len(p.Tasks)
			if p.NextCursor == "" {
				break
			}
			cursor = p.NextCursor
		}
		if total != 7 {
			t.Fatal(limit, total)
		}
	}
	empty, e := s.ListRootsForRecovery(ctx, "unknown", "", 2)
	if e != nil || len(empty.Tasks) != 0 || empty.NextCursor != "" {
		t.Fatal(empty, e)
	}
	// Existing Agent-facing scope remains strict.
	_, e = s.List(ctx, Scope{CallerID: "alice"}, 10)
	kind(t, e, Invalid)
}

func TestRecoveryCursorBoundariesAndConcurrency(t *testing.T) {
	path := filepath.Join(canonicalTempDir(t), "recovery.db")
	seedRecovery(t, path)
	s := openTest(t, path)
	other := openTest(t, path)
	ctx := context.Background()
	first, e := s.ListRootsForRecovery(ctx, "alice", "", 2)
	if e != nil {
		t.Fatal(e)
	}
	_, e = other.CreateRoot(ctx, CreateRequest{Scope: Scope{CallerID: "alice"}, IdempotencyKey: "later", Goal: "later"})
	if e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for _, r := range first.Tasks {
		seen[r.ID] = true
	}
	cursor := first.NextCursor
	for cursor != "" {
		p, e := s.ListRootsForRecovery(ctx, "alice", cursor, 2)
		if e != nil {
			t.Fatal(e)
		}
		for _, r := range p.Tasks {
			if seen[r.ID] || r.Goal == "later" {
				t.Fatal("duplicate or watermark leak")
			}
			seen[r.ID] = true
		}
		cursor = p.NextCursor
	}
	if len(seen) != 7 {
		t.Fatal(len(seen))
	}
	all, e := s.ListRootsForRecovery(ctx, "alice", "", 500)
	if e != nil || len(all.Tasks) != 8 {
		t.Fatal(all, e)
	}
	for _, bad := range []string{"!", "e30", first.NextCursor + "=", encodeRootCursor(rootCursor{Version: 2, Caller: "alice", After: 1, Through: 100}), encodeRootCursor(rootCursor{Version: 1, Caller: "alice", After: 2, Through: 999999}), encodeRootCursor(rootCursor{Version: 1, Caller: "alice", After: -1, Through: 9})} {
		_, e = s.ListRootsForRecovery(ctx, "alice", bad, 2)
		kind(t, e, Invalid)
	}
	_, e = s.ListRootsForRecovery(ctx, "bob", first.NextCursor, 2)
	kind(t, e, Invalid)
	for _, limit := range []int{0, -1, 501} {
		_, e = s.ListRootsForRecovery(ctx, "alice", "", limit)
		kind(t, e, Invalid)
	}
	_, e = s.ListRootsForRecovery(ctx, " ", "", 1)
	kind(t, e, Invalid)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, e = s.ListRootsForRecovery(canceled, "alice", "", 2)
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	tx, e := s.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	waiting, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := s.ListRootsForRecovery(waiting, "alice", first.NextCursor, 2); done <- err }()
	stop()
	if e = <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	tx.Rollback()
	s.Close()
	_, e = s.ListRootsForRecovery(ctx, "alice", "", 2)
	kind(t, e, Closed)
}
