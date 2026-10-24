package dockmcp

import (
	"context"
	"errors"
	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"path/filepath"
	"testing"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/controller"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type blockingBackend struct {
	runtime.RuntimeBackend
	entered chan string
	removed chan string
}

func (b *blockingBackend) Create(ctx context.Context, spec runtime.EnvironmentSpec) (runtime.Environment, error) {
	b.entered <- spec.TaskID
	<-ctx.Done()
	return runtime.Environment{ID: "candidate-" + spec.TaskID}, ctx.Err()
}
func (b *blockingBackend) Remove(_ context.Context, env runtime.Environment) error {
	b.removed <- env.ID
	return nil
}

func TestLeaseCancellationControllerCandidateCleanup(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := taskstore.Open(filepath.Join(dir, "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	root, err := store.CreateRoot(ctx, taskstore.CreateRequest{Scope: taskstore.Scope{CallerID: "synthetic-caller"}, Goal: "root", IdempotencyKey: "root"})
	if err != nil {
		t.Fatal(err)
	}
	scope := taskstore.Scope{CallerID: root.CallerID, RootID: root.ID}
	if _, err = store.BindEnvironment(ctx, scope, root.ID, taskstore.Created, "synthetic-env"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Transition(ctx, scope, root.ID, taskstore.Created, taskstore.Preparing); err != nil {
		t.Fatal(err)
	}
	backend := &blockingBackend{entered: make(chan string, 1), removed: make(chan string, 1)}
	ctl, err := controller.New(store, backend, root.CallerID, controller.Policy{Agent: "shell", TemplateRef: runtime.SbxTemplate, CPUs: 1, Memory: "1g"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewPersistentService(contextAuthenticator, root.CallerID, ctl, store)
	if err != nil {
		t.Fatal(err)
	}
	id := grantIdentity(t, s, root.ID, ActionCreateChild, ActionStatus)
	result := make(chan error, 1)
	go func() {
		_, _, e := s.createChild(identityContext(id), &mcp.CallToolRequest{}, ChildTaskInput{Goal: "child", IdempotencyKey: "child"})
		result <- e
	}()
	var childID string
	select {
	case childID = <-backend.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("backend not entered")
	}
	promptly(t, func() { s.RevokeCapability(id.Token) })
	select {
	case e := <-result:
		if !errors.Is(e, ErrUnauthorized) {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backend did not cancel")
	}
	child, err := store.Get(ctx, scope, childID)
	if err != nil || child.State != taskstore.Failed || child.EnvironmentID != "" {
		t.Fatalf("candidate not sealed: %+v %v", child, err)
	}
	select {
	case env := <-backend.removed:
		if env != "candidate-"+childID {
			t.Fatal(env)
		}
	default:
		t.Fatal("candidate not removed")
	}
}

func TestLeaseRevocationPreservesAdoptedEnvironment(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := taskstore.Open(filepath.Join(dir, "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	backend := &persistentBackend{}
	ctl, err := controller.New(store, backend, "synthetic-caller", controller.Policy{Agent: "shell", TemplateRef: runtime.SbxTemplate, CPUs: 1, Memory: "1g"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := ctl.CreateRoot(ctx, controller.Request{Goal: "root", IdempotencyKey: "root"})
	if err != nil {
		t.Fatal(err)
	}
	adopted := make(chan taskstore.Task, 1)
	delayed := childFunc(func(ctx context.Context, parent string, r controller.Request) (taskstore.Task, error) {
		task, e := ctl.CreateChild(ctx, parent, r)
		if e != nil {
			return task, e
		}
		adopted <- task
		<-ctx.Done()
		return task, nil
	})
	s, err := NewPersistentService(contextAuthenticator, root.CallerID, delayed, store)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.IssueCapability(CapabilityGrant{CallerID: root.CallerID, RootTaskID: root.ID, EnvironmentID: root.EnvironmentID, ExpiresAt: time.Now().Add(time.Hour), Actions: []string{ActionCreateChild}})
	if err != nil {
		t.Fatal(err)
	}
	id := RequestIdentity{Token: token, CallerID: root.CallerID, EnvironmentID: root.EnvironmentID}
	result := make(chan error, 1)
	go func() {
		_, out, e := s.createChild(identityContext(id), &mcp.CallToolRequest{}, ChildTaskInput{Goal: "child", IdempotencyKey: "child"})
		if out != (ChildTaskOutput{}) {
			result <- errors.New("adopted task reported successful after revoke")
			return
		}
		result <- e
	}()
	var child taskstore.Task
	select {
	case child = <-adopted:
	case <-time.After(2 * time.Second):
		t.Fatal("child not adopted")
	}
	promptly(t, func() { s.RevokeCapability(token) })
	select {
	case e := <-result:
		if !errors.Is(e, ErrUnauthorized) {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request not cancelled")
	}
	persisted, err := store.Get(ctx, taskstore.Scope{CallerID: root.CallerID, RootID: root.ID}, child.ID)
	if err != nil || persisted.EnvironmentID != child.EnvironmentID || persisted.EnvironmentID == "" || persisted.State != taskstore.Preparing {
		t.Fatalf("adopted environment lost: %+v %v", persisted, err)
	}
}

func promptly(t *testing.T, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("operation blocked behind Controller I/O")
	}
}

func TestOperationLeaseSlowController(t *testing.T) {
	for _, mode := range []string{"revoke", "rotate", "cancel", "ignores cancellation"} {
		t.Run(mode, func(t *testing.T) {
			root := taskstore.Task{ID: "root", RootID: "root", CallerID: "synthetic-caller", EnvironmentID: "synthetic-env", State: taskstore.Preparing}
			child := taskstore.Task{ID: "child", RootID: "root", ParentID: "root", CallerID: root.CallerID, EnvironmentID: "child-env", State: taskstore.Preparing}
			entered := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			ctl := childFunc(func(ctx context.Context, _ string, _ controller.Request) (taskstore.Task, error) {
				close(entered)
				if mode == "ignores cancellation" {
					<-release
					return child, nil
				}
				<-ctx.Done()
				return taskstore.Task{}, ctx.Err()
			})
			store := readerFunc(func(_ context.Context, _ taskstore.Scope, id string) (taskstore.Task, error) {
				if id == "child" {
					return child, nil
				}
				return root, nil
			})
			var identity RequestIdentity
			s, err := NewPersistentService(contextAuthenticator, root.CallerID, ctl, store)
			if err != nil {
				t.Fatal(err)
			}
			identity = grantIdentity(t, s, "root", ActionCreateChild, ActionStatus)
			ctx, cancel := context.WithCancel(identityContext(identity))
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, out, e := s.createChild(ctx, &mcp.CallToolRequest{}, ChildTaskInput{Goal: "synthetic", IdempotencyKey: "key"})
				if out != (ChildTaskOutput{}) {
					result <- errors.New("returned child success after invalidation")
					return
				}
				result <- e
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("Controller not entered")
			}
			promptly(t, func() {
				_, _, e := s.status(identityContext(identity), &mcp.CallToolRequest{}, StatusInput{TaskID: "root"})
				if e != nil {
					t.Errorf("concurrent status: %v", e)
				}
			})
			switch mode {
			case "revoke", "ignores cancellation":
				promptly(t, func() { s.RevokeCapability(identity.Token) })
			case "rotate":
				promptly(t, s.RotateGeneration)
			case "cancel":
				cancel()
			}
			if mode == "ignores cancellation" {
				release <- struct{}{}
			}
			select {
			case e := <-result:
				if mode == "cancel" {
					if !errors.Is(e, context.Canceled) {
						t.Fatalf("cancel=%v", e)
					}
				} else if !errors.Is(e, ErrUnauthorized) {
					t.Fatalf("invalidation=%v", e)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation did not reach Controller")
			}
			s.mu.Lock()
			remaining := len(s.persistent.operations)
			s.mu.Unlock()
			if remaining != 0 {
				t.Fatal("leaked operation lease")
			}
		})
	}
}
