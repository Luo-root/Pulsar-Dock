package controller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

func TestUnknownCreateOutcomeNeverClaimsStopped(t *testing.T) {
	for _, writeFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed-record", true: "state-write-failed"}[writeFails], func(t *testing.T) {
			s, db, b := stopSetup(t)
			b.create = func(context.Context, runtime.EnvironmentSpec) (runtime.Environment, error) {
				return runtime.Environment{}, errors.New("created but listing failed")
			}
			if writeFails {
				s.store = &faultStore{taskStore: db, transition: func(context.Context, taskstore.Scope, string, taskstore.State, taskstore.State) (taskstore.Task, error) {
					return taskstore.Task{}, errors.New("state write failed")
				}}
			}
			root, e := s.CreateRoot(ctx, request)
			if !errors.Is(e, ErrRecoveryRequired) {
				t.Fatal(e)
			}
			s.store = db
			if e = s.StopTask(ctx, root.ID); !errors.Is(e, ErrRecoveryRequired) {
				t.Fatal(e)
			}
			want := taskstore.Failed
			if writeFails {
				want = taskstore.Created
			}
			assertState(t, db, root, want)
			if len(b.stopped) != 0 || len(b.removed) != 0 {
				t.Fatal("unknown identity used")
			}
		})
	}
}

type blockedStore struct {
	taskStore
	root             string
	entered, release chan struct{}
	once             sync.Once
	operation        string
}

func (f *blockedStore) block(c context.Context, root, operation string) error {
	if root != f.root || operation != f.operation {
		return nil
	}
	f.once.Do(func() { close(f.entered) })
	select {
	case <-f.release:
		return nil
	case <-c.Done():
		return c.Err()
	}
}
func (f *blockedStore) ListTasksForRecovery(c context.Context, s taskstore.Scope, cursor string, n int) (taskstore.TaskPage, error) {
	if e := f.block(c, s.RootID, "scan"); e != nil {
		return taskstore.TaskPage{}, e
	}
	return f.taskStore.ListTasksForRecovery(c, s, cursor, n)
}
func (f *blockedStore) CreateChild(c context.Context, r taskstore.CreateRequest) (taskstore.Task, error) {
	if e := f.block(c, r.Scope.RootID, "insert"); e != nil {
		return taskstore.Task{}, e
	}
	return f.taskStore.CreateChild(c, r)
}
func (f *blockedStore) RevokeRoot(c context.Context, s taskstore.Scope) error {
	if e := f.block(c, s.RootID, "revoke"); e != nil {
		return e
	}
	return f.taskStore.RevokeRoot(c, s)
}
func (f *blockedStore) BindEnvironment(c context.Context, s taskstore.Scope, id string, state taskstore.State, env string) (taskstore.Task, error) {
	if e := f.block(c, s.RootID, "adopt"); e != nil {
		return taskstore.Task{}, e
	}
	return f.taskStore.BindEnvironment(c, s, id, state, env)
}
func await(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("operation blocked")
		return nil
	}
}
func TestRootCoordinationContextAndUnrelatedProgress(t *testing.T) {
	for _, operation := range []string{"scan", "insert", "adopt", "revoke"} {
		t.Run(operation, func(t *testing.T) {
			s, db, _ := stopSetup(t)
			a, _ := s.CreateRoot(ctx, request)
			b, _ := s.CreateRoot(ctx, Request{Goal: "other", IdempotencyKey: "other"})
			f := &blockedStore{taskStore: db, root: a.ID, operation: operation, entered: make(chan struct{}), release: make(chan struct{})}
			s.store = f
			first := make(chan error, 1)
			go func() {
				if operation == "revoke" {
					first <- s.StopTask(ctx, a.ID)
				} else {
					_, e := s.CreateChild(ctx, a.ID, request)
					first <- e
				}
			}()
			select {
			case <-f.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("store not entered")
			}
			c, cancel := context.WithCancel(ctx)
			waiting := make(chan error, 1)
			go func() { waiting <- s.StopTask(c, a.ID) }()
			cancel()
			if e := await(t, waiting); !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
			unrelated := make(chan error, 1)
			go func() { unrelated <- s.StopTask(ctx, b.ID) }()
			if e := await(t, unrelated); e != nil {
				t.Fatal(e)
			}
			close(f.release)
			if e := await(t, first); e != nil {
				t.Fatal(e)
			}
			if e := s.StopTask(ctx, a.ID); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestConcurrentSameRootStop(t *testing.T) {
	s, _, b := stopSetup(t)
	root, _ := s.CreateRoot(ctx, request)
	_, _ = s.CreateChild(ctx, root.ID, request)
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func() { results <- s.StopTask(ctx, root.ID) }()
	}
	for i := 0; i < 12; i++ {
		if e := await(t, results); e != nil {
			t.Fatal(e)
		}
	}
	if len(b.removed) != 2 {
		t.Fatalf("duplicate cleanup %d", len(b.removed))
	}
}
