package desktopapi

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Luo-root/pulsar-dock/internal/controller"
	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

type backend struct{}

func (backend) Name() string                                       { return "test" }
func (backend) Capabilities(context.Context) runtime.CapabilitySet { return runtime.CapabilitySet{} }
func (backend) Check(context.Context) runtime.Diagnostics {
	return runtime.Diagnostics{Backend: "test", Ready: true}
}
func (backend) Create(_ context.Context, s runtime.EnvironmentSpec) (runtime.Environment, error) {
	return runtime.Environment{ID: "env-" + s.TaskID, Name: "guest", Backend: "test", State: "running", Agent: s.Agent, CPUs: s.CPUs, Memory: s.Memory}, nil
}
func (backend) Start(context.Context, runtime.Environment) error { return nil }
func (backend) Exec(context.Context, runtime.Environment, runtime.ExecRequest) (string, error) {
	return "untrusted stdout", nil
}
func (backend) CopyIn(context.Context, runtime.Environment, string, string) (runtime.CopyReceipt, error) {
	return runtime.CopyReceipt{}, errors.New("unused")
}
func (backend) CopyOut(context.Context, runtime.Environment, runtime.CopySelector) (runtime.ArtifactSnapshot, error) {
	return runtime.ArtifactSnapshot{}, errors.New("unused")
}
func (backend) Stop(context.Context, runtime.Environment) error   { return nil }
func (backend) Remove(context.Context, runtime.Environment) error { return nil }
func (backend) SetNetworkPolicy(context.Context, runtime.Environment, []runtime.NetworkRule) error {
	return nil
}
func (backend) Revoke(context.Context, runtime.Environment, string) error { return nil }
func (backend) Inspect(_ context.Context, e runtime.Environment) (runtime.Environment, error) {
	return e, nil
}

func canonicalTestDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func openFacade(t *testing.T, caller string) (*Facade, *taskstore.Store) {
	t.Helper()
	path := filepath.Join(canonicalTestDir(t), "tasks.db")
	store, err := taskstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	svc, err := controller.New(store, backend{}, caller, controller.Policy{Agent: "shell", TemplateRef: runtime.SbxTemplate, CPUs: 1, Memory: "1g"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := New(svc, store, caller, "claude", []AgentConfig{
		{ID: "claude", DisplayName: "Claude", Installed: true, CLIVerified: true, ModelVerified: true, Operations: []Operation{OperationCreate}},
		{ID: "codex", DisplayName: "Codex", Installed: false},
		{ID: "review", DisplayName: "Review", Installed: true, CLIVerified: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, store
}

func TestFacadeScopeDTOAndCatalogue(t *testing.T) {
	f, _ := openFacade(t, "alice")
	ctx := context.Background()
	agents, err := f.Agents(ctx)
	if err != nil || len(agents) != 3 || agents[0].ID != "claude" || agents[1].ID != "codex" || agents[2].ID != "review" {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
	if _, err = f.CreateTask(ctx, CreateTaskRequest{AgentID: "codex", Goal: "x", IdempotencyKey: "codex"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unverified agent err=%v", err)
	}
	if _, err = f.CreateTask(ctx, CreateTaskRequest{AgentID: "other", Goal: "x", IdempotencyKey: "k"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("wrong agent err=%v", err)
	}
	task, err := f.CreateTask(ctx, CreateTaskRequest{AgentID: "claude", Goal: "inspect", IdempotencyKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if task.ID == "" || task.State != string(taskstore.Preparing) || task.CreatedAt == "" || task.RootID != task.ID {
		t.Fatalf("task=%+v", task)
	}
	if _, err = f.GetTask(ctx, task.RootID, task.ID); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(task)
	if string(payload) == "" || string(payload) == "{}" {
		t.Fatal("empty dto")
	}
	for _, secret := range []string{"alice", "env-", "credential", "argv"} {
		if string(payload) == secret {
			t.Fatalf("leaked %q", secret)
		}
	}
	if _, err = f.GetTask(ctx, task.RootID, "task-not-owned"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("not-found err=%v", err)
	}
}

func TestFacadeBoundsCancellationAndUnsupported(t *testing.T) {
	f, _ := openFacade(t, "alice")
	if _, err := f.CreateTask(context.Background(), CreateTaskRequest{Goal: "x\nunsafe", IdempotencyKey: "k"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("newline err=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.CreateTask(ctx, CreateTaskRequest{Goal: "x", IdempotencyKey: "k"}); !errors.Is(err, ErrCanceled) {
		t.Fatalf("cancel err=%v", err)
	}
	if _, err := f.CreateTask(context.Background(), CreateTaskRequest{Goal: "x", IdempotencyKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ExecuteTask(context.Background(), "bad", "bad"); !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrInvalid) {
		t.Fatalf("execute err=%v", err)
	}
}
