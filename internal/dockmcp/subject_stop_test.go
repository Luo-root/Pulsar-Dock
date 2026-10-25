package dockmcp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Luo-root/pulsar-dock/internal/controller"
	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type subjectStopBackend struct{ persistentBackend }

func (*subjectStopBackend) Stop(context.Context, runtime.Environment) error { return nil }

func TestChildSubjectControllerStop(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := taskstore.Open(filepath.Join(dir, "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tasks.Close()
	backend := &subjectStopBackend{}
	ctl, err := controller.New(tasks, backend, "synthetic-caller", controller.Policy{Agent: "shell", TemplateRef: runtime.SbxTemplate, CPUs: 1, Memory: "1g"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root, err := ctl.CreateRoot(ctx, controller.Request{Goal: "root", IdempotencyKey: "root"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := ctl.CreateChild(ctx, root.ID, controller.Request{Goal: "child", IdempotencyKey: "child"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewPersistentService(contextAuthenticator, root.CallerID, ctl, tasks)
	if err != nil {
		t.Fatal(err)
	}
	grant := subjectGrant(publishFixture{root: root}, child)
	id := subjectIdentity(t, s, grant)
	if _, _, err = s.status(identityContext(id), &mcp.CallToolRequest{}, StatusInput{TaskID: child.ID}); err != nil {
		t.Fatal(err)
	}
	if err = ctl.StopTask(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	for _, task := range []taskstore.Task{root, child} {
		stored, e := tasks.Get(ctx, taskstore.Scope{CallerID: root.CallerID, RootID: root.ID}, task.ID)
		if e != nil || stored.State != taskstore.Stopped {
			t.Fatalf("stop=%+v %v", stored, e)
		}
	}
	if _, _, err = s.status(identityContext(id), &mcp.CallToolRequest{}, StatusInput{TaskID: child.ID}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("child survived Stop", err)
	}
	if _, err = s.IssueCapability(grant); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("stopped child reissued", err)
	}
	if backend.calls != 2 {
		t.Fatal("unexpected runtime create", backend.calls)
	}
}
