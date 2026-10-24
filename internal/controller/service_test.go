package controller

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

type fakeBackend struct {
	runtime.RuntimeBackend
	specs     []runtime.EnvironmentSpec
	removed   []string
	create    func(runtime.EnvironmentSpec) (runtime.Environment, error)
	removeErr error
}

func (b *fakeBackend) Create(_ context.Context, spec runtime.EnvironmentSpec) (runtime.Environment, error) {
	b.specs = append(b.specs, spec)
	if b.create != nil {
		return b.create(spec)
	}
	return runtime.Environment{ID: "real-" + spec.TaskID}, nil
}
func (b *fakeBackend) Remove(_ context.Context, env runtime.Environment) error {
	b.removed = append(b.removed, env.ID)
	return b.removeErr
}
func policy() Policy {
	return Policy{Agent: "shell", TemplateRef: runtime.SbxTemplate, CPUs: 2, Memory: "1GiB", GatewayURL: "http://gateway.test:7777", ExpiresAfter: 300}
}
func setup(t *testing.T) (*Service, *taskstore.Store, *fakeBackend, string) {
	t.Helper()
	// Resolve only testing's trusted directory (macOS may use /var -> /private/var).
	// Production database inputs must retain taskstore's ancestor-link rejection.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tasks.db")
	store, err := taskstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	b := &fakeBackend{}
	s, err := New(store, b, "alice", policy())
	if err != nil {
		t.Fatal(err)
	}
	return s, store, b, path
}

var ctx = context.Background()
var request = Request{Goal: "synthetic goal", IdempotencyKey: "key"}

func scope(t taskstore.Task) taskstore.Scope {
	return taskstore.Scope{CallerID: t.CallerID, RootID: t.RootID}
}

func TestCreatePolicyIdempotencyAndRecovery(t *testing.T) {
	s, store, b, path := setup(t)
	root, err := s.CreateRoot(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if root.State != taskstore.Preparing || root.EnvironmentID != "real-"+root.ID {
		t.Fatalf("bad task: %+v", root)
	}
	again, err := s.CreateRoot(ctx, request)
	if err != nil || again != root || len(b.specs) != 1 {
		t.Fatalf("duplicate: %+v %v", again, err)
	}
	_, err = s.CreateRoot(ctx, Request{Goal: "changed", IdempotencyKey: request.IdempotencyKey})
	if err == nil {
		t.Fatal("expected conflict")
	}
	child, err := s.CreateChild(ctx, root.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentID != root.ID || child.RootID != root.ID {
		t.Fatal(child)
	}
	for _, spec := range b.specs {
		if spec.Workspace != runtime.WorkspaceNone || spec.Skills != runtime.SkillsOff || spec.TemplateRef != runtime.SbxTemplate || spec.CPUs != 2 || spec.Memory != "1GiB" || spec.ExpiresAfter != 300 || spec.GatewayURL != policy().GatewayURL || len(spec.NetworkRules) != 1 || spec.NetworkRules[0] != (runtime.NetworkRule{Host: "gateway.test", Port: 7777, Action: "allow"}) {
			t.Fatalf("policy changed: %+v", spec)
		}
	}
	if err = s.StopTask(ctx, root.ID); !errors.Is(err, ErrStopUnsupported) {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := taskstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	roots, err := reopened.ListRootsForRecovery(ctx, "alice", "", 1)
	if err != nil || len(roots.Tasks) != 1 || roots.Tasks[0].EnvironmentID != root.EnvironmentID {
		t.Fatalf("recovery: %+v %v", roots, err)
	}
	tasks, err := reopened.ListTasksForRecovery(ctx, scope(root), "", 10)
	if err != nil || len(tasks.Tasks) != 2 {
		t.Fatalf("tasks: %+v %v", tasks, err)
	}
	s2, _ := New(reopened, b, "alice", policy())
	if _, err = s2.CreateRoot(ctx, request); err != nil || len(b.specs) != 2 {
		t.Fatalf("replay after reopen: %v", err)
	}
}

func TestAuthorizationAndValidation(t *testing.T) {
	s, store, b, _ := setup(t)
	for _, r := range []Request{{Goal: " ", IdempotencyKey: "x"}, {Goal: "x", IdempotencyKey: " "}} {
		if _, err := s.CreateRoot(ctx, r); err == nil {
			t.Fatal("accepted invalid request")
		}
	}
	other, _ := New(store, b, "bob", policy())
	root, err := other.CreateRoot(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateChild(ctx, root.ID, request); err == nil {
		t.Fatal("cross caller parent accepted")
	}
	own, err := s.CreateRoot(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateChild(ctx, own.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateChild(ctx, child.ID, request); err == nil {
		t.Fatal("recursive child accepted")
	}
	if _, err = store.Transition(ctx, scope(own), own.ID, taskstore.Preparing, taskstore.Failed); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateChild(ctx, own.ID, Request{Goal: "new", IdempotencyKey: "new"}); err == nil {
		t.Fatal("terminal parent accepted")
	}
	if _, err = New(store, b, " ", policy()); err == nil {
		t.Fatal("empty identity")
	}
	for _, p := range []Policy{{}, {Agent: "shell", TemplateRef: "latest", CPUs: 1, Memory: "1GiB", GatewayURL: "http://gateway", ExpiresAfter: 1}} {
		if _, err = New(store, b, "alice", p); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
}

func TestBackendFailure(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "partial"}[partial], func(t *testing.T) {
			s, store, b, _ := setup(t)
			b.create = func(runtime.EnvironmentSpec) (runtime.Environment, error) {
				env := runtime.Environment{}
				if partial {
					env.ID = "partial"
				}
				return env, errors.New("create failed")
			}
			task, err := s.CreateRoot(ctx, request)
			if err == nil || task.State != taskstore.Failed {
				t.Fatalf("%+v %v", task, err)
			}
			persisted, e := store.Get(ctx, scope(task), task.ID)
			if e != nil || persisted.State != taskstore.Failed || persisted.EnvironmentID != "" {
				t.Fatalf("%+v %v", persisted, e)
			}
			if partial && len(b.removed) != 1 {
				t.Fatal("partial result leaked")
			}
			if _, err = s.CreateRoot(ctx, request); err != nil || len(b.specs) != 1 {
				t.Fatalf("failed retry created environment: %v", err)
			}
		})
	}
}

type faultStore struct {
	taskStore
	bind       func(context.Context, taskstore.Scope, string, taskstore.State, string) (taskstore.Task, error)
	transition func(context.Context, taskstore.Scope, string, taskstore.State, taskstore.State) (taskstore.Task, error)
}

func (f *faultStore) BindEnvironment(c context.Context, s taskstore.Scope, id string, state taskstore.State, env string) (taskstore.Task, error) {
	if f.bind != nil {
		return f.bind(c, s, id, state, env)
	}
	return f.taskStore.BindEnvironment(c, s, id, state, env)
}
func (f *faultStore) Transition(c context.Context, s taskstore.Scope, id string, from, to taskstore.State) (taskstore.Task, error) {
	if f.transition != nil {
		return f.transition(c, s, id, from, to)
	}
	return f.taskStore.Transition(c, s, id, from, to)
}

func TestBindingFailuresNeverRemoveAdoptedEnvironment(t *testing.T) {
	for _, mode := range []string{"unadopted", "adopted", "other", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, store, b, _ := setup(t)
			f := &faultStore{taskStore: store}
			f.bind = func(c context.Context, sc taskstore.Scope, id string, state taskstore.State, env string) (taskstore.Task, error) {
				switch mode {
				case "adopted":
					if _, e := store.BindEnvironment(c, sc, id, state, env); e != nil {
						t.Fatal(e)
					}
				case "other":
					if _, e := store.BindEnvironment(c, sc, id, state, "another"); e != nil {
						t.Fatal(e)
					}
				case "unknown":
					store.Close()
				}
				return taskstore.Task{}, errors.New("bind result lost")
			}
			s.store = f
			task, err := s.CreateRoot(ctx, request)
			if err == nil {
				t.Fatal("expected failure")
			}
			wantRemove := mode == "unadopted" || mode == "other"
			if (len(b.removed) == 1) != wantRemove {
				t.Fatalf("unsafe cleanup: %v", b.removed)
			}
			if mode == "adopted" && task.EnvironmentID != "real-"+task.ID {
				t.Fatal("adoption lost")
			}
			if mode == "unadopted" && task.State != taskstore.Failed {
				t.Fatal("candidate not sealed")
			}
		})
	}
}

func TestTransitionFailureRetainsRecoverableBinding(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[committed], func(t *testing.T) {
			s, store, b, _ := setup(t)
			f := &faultStore{taskStore: store}
			f.transition = func(c context.Context, sc taskstore.Scope, id string, from, to taskstore.State) (taskstore.Task, error) {
				if committed {
					if _, e := store.Transition(c, sc, id, from, to); e != nil {
						t.Fatal(e)
					}
				}
				return taskstore.Task{}, errors.New("transition failed")
			}
			s.store = f
			task, err := s.CreateRoot(ctx, request)
			if !errors.Is(err, ErrRecoveryRequired) || task.EnvironmentID == "" || len(b.removed) != 0 {
				t.Fatalf("%+v %v removed=%v", task, err, b.removed)
			}
			_, _ = s.CreateRoot(ctx, request)
			if len(b.specs) != 1 {
				t.Fatal("retried backend creation")
			}
		})
	}
}

func TestCancelledAfterCreateCleansOnlyUnboundCandidate(t *testing.T) {
	s, store, b, _ := setup(t)
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	b.create = func(runtime.EnvironmentSpec) (runtime.Environment, error) {
		cancel()
		return runtime.Environment{ID: "candidate"}, nil
	}
	task, err := s.CreateRoot(c, request)
	if err == nil || len(b.removed) != 1 || task.State != taskstore.Failed {
		t.Fatalf("%+v %v %v", task, err, b.removed)
	}
	persisted, e := store.Get(ctx, scope(task), task.ID)
	if e != nil || persisted.EnvironmentID != "" {
		t.Fatalf("%+v %v", persisted, e)
	}
}

func TestExistingUnboundRecordRequiresRecovery(t *testing.T) {
	s, store, b, _ := setup(t)
	_, err := store.CreateRoot(ctx, taskstore.CreateRequest{Scope: taskstore.Scope{CallerID: "alice"}, Goal: request.Goal, IdempotencyKey: request.IdempotencyKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateRoot(ctx, request); !errors.Is(err, ErrRecoveryRequired) || len(b.specs) != 0 {
		t.Fatalf("unsafe restart: %v", err)
	}
}

func TestConcurrentIdempotency(t *testing.T) {
	s, _, b, _ := setup(t)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if _, err := s.CreateRoot(ctx, request); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(b.specs) != 1 {
		t.Fatalf("created %d environments", len(b.specs))
	}
}
