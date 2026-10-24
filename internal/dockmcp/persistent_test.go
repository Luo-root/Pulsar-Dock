package dockmcp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/controller"
	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type persistentBackend struct {
	runtime.RuntimeBackend
	calls int
}

func (b *persistentBackend) Create(_ context.Context, spec runtime.EnvironmentSpec) (runtime.Environment, error) {
	b.calls++
	return runtime.Environment{ID: "synthetic-env-" + spec.TaskID}, nil
}
func (b *persistentBackend) Remove(context.Context, runtime.Environment) error { return nil }

type childFunc func(context.Context, string, controller.Request) (taskstore.Task, error)

func (f childFunc) CreateChild(ctx context.Context, id string, r controller.Request) (taskstore.Task, error) {
	return f(ctx, id, r)
}

type readerFunc func(context.Context, taskstore.Scope, string) (taskstore.Task, error)

func (f readerFunc) Get(ctx context.Context, s taskstore.Scope, id string) (taskstore.Task, error) {
	return f(ctx, s, id)
}

func TestPersistentControllerAndReopen(t *testing.T) {
	ctx := context.Background()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tasks.db")
	store, err := taskstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	backend := &persistentBackend{}
	ctl, err := controller.New(store, backend, "synthetic-caller", controller.Policy{Agent: "shell", TemplateRef: runtime.SbxTemplate, CPUs: 1, Memory: "1g"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := ctl.CreateRoot(ctx, controller.Request{Goal: "synthetic root", IdempotencyKey: "root"})
	if err != nil {
		t.Fatal(err)
	}
	var identity RequestIdentity
	auth := func(context.Context, *mcp.CallToolRequest) (RequestIdentity, error) { return identity, nil }
	makeService := func() *Service {
		s, e := NewPersistentService(auth, "synthetic-caller", ctl, store)
		if e != nil {
			t.Fatal(e)
		}
		token, e := s.IssueCapability(CapabilityGrant{CallerID: root.CallerID, RootTaskID: root.ID, EnvironmentID: root.EnvironmentID, ExpiresAt: time.Now().Add(time.Hour), Actions: []string{ActionCreateChild, ActionStatus, ActionPublish}})
		if e != nil {
			t.Fatal(e)
		}
		identity = RequestIdentity{Token: token, CallerID: root.CallerID, EnvironmentID: root.EnvironmentID}
		return s
	}
	s := makeService()
	req := &mcp.CallToolRequest{}
	input := ChildTaskInput{Goal: "synthetic child", IdempotencyKey: "child"}
	_, child, err := s.createChild(ctx, req, input)
	if err != nil || child.State != "preparing" {
		t.Fatalf("child=%+v err=%v", child, err)
	}
	_, again, err := s.createChild(ctx, req, input)
	if err != nil || again != child || backend.calls != 2 {
		t.Fatalf("repeat=%+v %v calls=%d", again, err, backend.calls)
	}
	if _, _, err = s.createChild(ctx, req, ChildTaskInput{Goal: "changed", IdempotencyKey: "child"}); err == nil {
		t.Fatal("accepted conflict")
	}
	other, err := ctl.CreateRoot(ctx, controller.Request{Goal: "other", IdempotencyKey: "other"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"forged-task", other.ID} {
		if _, _, err = s.status(ctx, req, StatusInput{TaskID: id}); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("outside scope: %v", err)
		}
	}
	// Child environment cannot claim the root identity (nor obtain a root grant).
	if _, err = s.IssueCapability(CapabilityGrant{CallerID: root.CallerID, RootTaskID: child.TaskID, EnvironmentID: "synthetic-env-" + child.TaskID, ExpiresAt: time.Now().Add(time.Hour), Actions: []string{ActionStatus}}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("child grant: %v", err)
	}
	identity.EnvironmentID = "forged-env"
	if _, _, err = s.status(ctx, req, StatusInput{TaskID: root.ID}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	identity.EnvironmentID = root.EnvironmentID
	if _, _, err = s.publish(ctx, req, PublishInput{TaskID: root.ID, Kind: "review", Digest: "synthetic"}); !errors.Is(err, ErrArtifactUnsupported) {
		t.Fatal(err)
	}
	if s.tasks != nil || s.childRequests != nil || s.artifacts != nil {
		t.Fatal("persistent service allocated prototype state")
	}
	oldToken := identity.Token
	s.RevokeCapability(oldToken)
	if _, _, err = s.status(ctx, req, StatusInput{TaskID: child.TaskID}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	if _, err = s.IssueCapability(CapabilityGrant{CallerID: root.CallerID, RootTaskID: root.ID, EnvironmentID: root.EnvironmentID, ExpiresAt: time.Now().Add(time.Hour), Actions: []string{ActionStatus}}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked tree reissued")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = taskstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctl, err = controller.New(store, backend, "synthetic-caller", controller.Policy{Agent: "shell", TemplateRef: runtime.SbxTemplate, CPUs: 1, Memory: "1g"})
	if err != nil {
		t.Fatal(err)
	}
	s = makeService() // fresh host authorization, no task map or restored token
	_, replay, err := s.createChild(ctx, req, input)
	if err != nil || replay != child || backend.calls != 3 {
		t.Fatalf("reopened replay=%+v err=%v calls=%d", replay, err, backend.calls)
	}
	_, status, err := s.status(ctx, req, StatusInput{TaskID: child.TaskID})
	if err != nil || status.State != "preparing" {
		t.Fatalf("reopen=%+v %v", status, err)
	}
	identity.Token = oldToken
	if _, _, err = s.status(ctx, req, StatusInput{TaskID: child.TaskID}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old token survived restart")
	}
}

func TestPersistentFailClosedAndControllerErrors(t *testing.T) {
	ctx := context.Background()
	req := &mcp.CallToolRequest{}
	root := taskstore.Task{ID: "root", RootID: "root", CallerID: "synthetic-caller", EnvironmentID: "synthetic-env", State: taskstore.Preparing}
	var identity RequestIdentity
	failure := errors.New("synthetic controller failure")
	calls := 0
	ctl := childFunc(func(_ context.Context, parent string, r controller.Request) (taskstore.Task, error) {
		calls++
		if parent != "root" {
			t.Fatal("untrusted parent")
		}
		return taskstore.Task{}, failure
	})
	store := readerFunc(func(_ context.Context, scope taskstore.Scope, id string) (taskstore.Task, error) {
		if scope != (taskstore.Scope{CallerID: "synthetic-caller", RootID: "root"}) || id != "root" {
			t.Fatal("unscoped read")
		}
		return root, nil
	})
	s, err := NewPersistentService(func(context.Context, *mcp.CallToolRequest) (RequestIdentity, error) { return identity, nil }, "synthetic-caller", ctl, store)
	if err != nil {
		t.Fatal(err)
	}
	identity = grantIdentity(t, s, "root", ActionCreateChild, ActionStatus)
	if _, _, err = s.createChild(ctx, req, ChildTaskInput{Goal: "goal", IdempotencyKey: "key"}); !errors.Is(err, failure) {
		t.Fatalf("lost controller error: %v", err)
	}
	root.EnvironmentID = "mismatched"
	if _, _, err = s.createChild(ctx, req, ChildTaskInput{Goal: "goal", IdempotencyKey: "key"}); !errors.Is(err, ErrUnauthorized) || calls != 1 {
		t.Fatal("mismatched env invoked controller")
	}
	root.EnvironmentID = ""
	if _, _, err = s.status(ctx, req, StatusInput{TaskID: "root"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("unbound env accepted")
	}
	root.EnvironmentID = "synthetic-env"
	root.State = taskstore.Stopped
	if _, _, err = s.status(ctx, req, StatusInput{TaskID: "root"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("stopped root accepted")
	}
}

func TestPersistentSDKTransport(t *testing.T) {
	ctx := context.Background()
	root := taskstore.Task{ID: "root", RootID: "root", CallerID: "synthetic-caller", EnvironmentID: "synthetic-env", State: taskstore.Preparing}
	var identity RequestIdentity
	s, err := NewPersistentService(func(context.Context, *mcp.CallToolRequest) (RequestIdentity, error) { return identity, nil }, "synthetic-caller", childFunc(func(context.Context, string, controller.Request) (taskstore.Task, error) {
		t.Fatal("unexpected creation")
		return taskstore.Task{}, nil
	}), readerFunc(func(_ context.Context, scope taskstore.Scope, id string) (taskstore.Task, error) {
		if scope.RootID != "root" || scope.CallerID != "synthetic-caller" || id != "root" {
			return taskstore.Task{}, errors.New("not found")
		}
		return root, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	identity = grantIdentity(t, s, "root", ActionStatus)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.CreateServer().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "synthetic-client", Version: "test"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: ActionStatus, Arguments: map[string]any{"task_id": "root"}})
	if err != nil || result.IsError {
		t.Fatalf("status=%+v %v", result, err)
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: ActionStatus, Arguments: map[string]any{"task_id": "forged"}})
	if err != nil || !result.IsError {
		t.Fatalf("forged=%+v %v", result, err)
	}
}
