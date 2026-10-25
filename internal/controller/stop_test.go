package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

type stopBackend struct {
	runtime.RuntimeBackend
	mu                 sync.Mutex
	handles            []runtime.Environment
	stopped, removed   []runtime.Environment
	create             func(context.Context, runtime.EnvironmentSpec) (runtime.Environment, error)
	stopErr, removeErr error
}

func (b *stopBackend) Create(c context.Context, spec runtime.EnvironmentSpec) (runtime.Environment, error) {
	if b.create != nil {
		return b.create(c, spec)
	}
	e := runtime.Environment{ID: "id-" + spec.TaskID, Name: "opaque-name-" + spec.TaskID, Backend: "fake", Agent: "shell", Memory: "1g", CPUs: 2}
	b.mu.Lock()
	b.handles = append(b.handles, e)
	b.mu.Unlock()
	return e, nil
}
func (b *stopBackend) Stop(_ context.Context, e runtime.Environment) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopped = append(b.stopped, e)
	return b.stopErr
}
func (b *stopBackend) Remove(_ context.Context, e runtime.Environment) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removed = append(b.removed, e)
	return b.removeErr
}
func stopSetup(t *testing.T) (*Service, *taskstore.Store, *stopBackend) {
	s, db, _, _ := setup(t)
	b := &stopBackend{}
	s.backend = b
	return s, db, b
}
func assertState(t *testing.T, db *taskstore.Store, task taskstore.Task, want taskstore.State) {
	t.Helper()
	got, e := db.Get(ctx, scope(task), task.ID)
	if e != nil || got.State != want {
		t.Fatalf("state=%s want=%s error=%v", got.State, want, e)
	}
}
func TestStopOwnedTreeAndReplay(t *testing.T) {
	s, db, b := stopSetup(t)
	root, e := s.CreateRoot(ctx, request)
	if e != nil {
		t.Fatal(e)
	}
	child, e := s.CreateChild(ctx, root.ID, request)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Transition(ctx, scope(child), child.ID, child.State, taskstore.Failed); e != nil {
		t.Fatal(e)
	}
	if e = s.StopTask(ctx, root.ID); e != nil {
		t.Fatal(e)
	}
	assertState(t, db, root, taskstore.Stopped)
	assertState(t, db, child, taskstore.Failed)
	if len(b.stopped) != 2 || len(b.removed) != 2 {
		t.Fatal("incomplete tree cleanup")
	}
	for i, h := range b.handles {
		if b.stopped[i] != h || b.removed[i] != h {
			t.Fatal("handle reconstructed")
		}
	}
	if e = s.StopTask(ctx, root.ID); e != nil || len(b.removed) != 2 {
		t.Fatalf("retry: %v", e)
	}
	if _, e = s.CreateChild(ctx, root.ID, request); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateChild(ctx, root.ID, Request{Goal: "new", IdempotencyKey: "new"}); e == nil {
		t.Fatal("revoked delegation accepted")
	}
	if len(b.handles) != 2 {
		t.Fatal("restarted historical environment")
	}
}
func TestStopFailuresAndRetry(t *testing.T) {
	for _, mode := range []string{"stop", "remove"} {
		t.Run(mode, func(t *testing.T) {
			s, db, b := stopSetup(t)
			root, _ := s.CreateRoot(ctx, request)
			child, _ := s.CreateChild(ctx, root.ID, request)
			failure := errors.New("synthetic failure")
			if mode == "stop" {
				b.stopErr = failure
			} else {
				b.removeErr = failure
			}
			if e := s.StopTask(ctx, root.ID); !errors.Is(e, failure) {
				t.Fatal(e)
			}
			assertState(t, db, root, taskstore.Preparing)
			assertState(t, db, child, taskstore.Preparing)
			if len(b.removed) != 2 {
				t.Fatal("did not continue cleanup")
			}
			revoked, e := db.RootRevoked(ctx, scope(root))
			if e != nil || !revoked {
				t.Fatal("revocation lost")
			}
			b.stopErr = nil
			b.removeErr = nil
			if e = s.StopTask(ctx, root.ID); e != nil {
				t.Fatal(e)
			}
			assertState(t, db, root, taskstore.Stopped)
			assertState(t, db, child, taskstore.Stopped)
		})
	}
}
func TestStopAuthorizationAndMissingHandles(t *testing.T) {
	s, db, b := stopSetup(t)
	root, _ := s.CreateRoot(ctx, request)
	child, _ := s.CreateChild(ctx, root.ID, request)
	other, _ := New(db, b, "bob", policy())
	for _, call := range []func() error{func() error { return other.StopTask(ctx, root.ID) }, func() error { return s.StopTask(ctx, child.ID) }} {
		if call() == nil {
			t.Fatal("unauthorized stop")
		}
	}
	if len(b.removed) != 0 {
		t.Fatal("unauthorized backend access")
	}
	restarted, _ := New(db, b, "alice", policy())
	if e := restarted.StopTask(ctx, root.ID); !errors.Is(e, ErrRecoveryRequired) {
		t.Fatal(e)
	}
	assertState(t, db, root, taskstore.Preparing)
	if len(b.removed) != 0 {
		t.Fatal("invented handles")
	}
}

type revokeFailureStore struct {
	taskStore
	err error
}

func (f revokeFailureStore) RevokeRoot(context.Context, taskstore.Scope) error { return f.err }
func TestRevokeFailureDoesNotStop(t *testing.T) {
	s, db, b := stopSetup(t)
	root, _ := s.CreateRoot(ctx, request)
	failure := errors.New("durable revoke failed")
	s.store = revokeFailureStore{db, failure}
	if e := s.StopTask(ctx, root.ID); !errors.Is(e, failure) {
		t.Fatal(e)
	}
	assertState(t, db, root, taskstore.Preparing)
	if len(b.removed) != 0 {
		t.Fatal("cleanup before revoke")
	}
}
func TestStopPagination(t *testing.T) {
	s, db, b := stopSetup(t)
	root, _ := s.CreateRoot(ctx, request)
	for i := 0; i < 503; i++ {
		child, e := db.CreateChild(ctx, taskstore.CreateRequest{Scope: scope(root), ParentID: root.ID, Goal: "synthetic", IdempotencyKey: fmt.Sprint(i)})
		if e != nil {
			t.Fatal(e)
		}
		env := runtime.Environment{ID: fmt.Sprint(i), Name: "opaque"}
		s.handles[child.ID] = &ownedEnvironment{env: env}
	}
	if e := s.StopTask(ctx, root.ID); e != nil {
		t.Fatal(e)
	}
	if len(b.removed) != 504 {
		t.Fatalf("removed %d", len(b.removed))
	}
}
func TestStopCancelsSlowCreateAndRejectsLateAdoption(t *testing.T) {
	s, db, b := stopSetup(t)
	root, _ := s.CreateRoot(ctx, request)
	entered := make(chan struct{})
	release := make(chan struct{})
	cancelled := make(chan struct{})
	b.create = func(c context.Context, _ runtime.EnvironmentSpec) (runtime.Environment, error) {
		close(entered)
		<-c.Done()
		close(cancelled)
		<-release
		return runtime.Environment{ID: "late", Name: "do-not-guess"}, nil
	}
	createDone := make(chan error, 1)
	go func() { _, e := s.CreateChild(ctx, root.ID, request); createDone <- e }()
	<-entered
	stopCtx, cancel := context.WithCancel(ctx)
	stopDone := make(chan error, 1)
	go func() { stopDone <- s.StopTask(stopCtx, root.ID) }()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("create not cancelled")
	}
	cancel()
	select {
	case e := <-stopDone:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop wait not cancellable")
	}
	close(release)
	if e := <-createDone; e == nil {
		t.Fatal("late adoption succeeded")
	}
	if e := s.StopTask(ctx, root.ID); e != nil {
		t.Fatal(e)
	}
	page, e := db.ListTasksForRecovery(ctx, scope(root), "", 500)
	if e != nil {
		t.Fatal(e)
	}
	for _, task := range page.Tasks {
		if task.ParentID != "" && task.EnvironmentID != "" {
			t.Fatal("late handle adopted")
		}
	}
	if len(b.removed) != 2 {
		t.Fatalf("cleanup count %d", len(b.removed))
	}
}
