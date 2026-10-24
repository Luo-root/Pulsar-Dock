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

type failingRevocationStore struct{ TaskReader }

func (f failingRevocationStore) RevokeRoot(context.Context, taskstore.Scope) error {
	return errors.New("synthetic storage failure")
}

func TestDurableRevocationFailureIsVisible(t *testing.T) {
	root := taskstore.Task{ID: "root", RootID: "root", CallerID: "synthetic-caller", EnvironmentID: "synthetic-env", State: taskstore.Preparing}
	store := failingRevocationStore{TaskReader: readerFunc(func(context.Context, taskstore.Scope, string) (taskstore.Task, error) { return root, nil })}
	s, err := NewPersistentService(contextAuthenticator, root.CallerID, childFunc(func(context.Context, string, controller.Request) (taskstore.Task, error) {
		t.Fatal("revoked request reached controller")
		return taskstore.Task{}, nil
	}), store)
	if err != nil {
		t.Fatal(err)
	}
	id := grantIdentity(t, s, "root", ActionStatus, ActionCreateChild)
	if err = s.RevokeCapability(id.Token); err == nil {
		t.Fatal("failed durable write acknowledged")
	}
	if _, _, err = s.status(identityContext(id), &mcp.CallToolRequest{}, StatusInput{TaskID: "root"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("local failure opened access", err)
	}
}

func TestDurableRevocationReopen(t *testing.T) {
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
	root, err := ctl.CreateRoot(ctx, controller.Request{Goal: "root", IdempotencyKey: "root"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := ctl.CreateRoot(ctx, controller.Request{Goal: "other", IdempotencyKey: "other"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewPersistentService(contextAuthenticator, root.CallerID, ctl, store)
	if err != nil {
		t.Fatal(err)
	}
	grant := CapabilityGrant{CallerID: root.CallerID, RootTaskID: root.ID, EnvironmentID: root.EnvironmentID, ExpiresAt: time.Now().Add(time.Hour), Actions: []string{ActionStatus, ActionCreateChild}}
	token, err := s.IssueCapability(grant)
	if err != nil {
		t.Fatal(err)
	}
	// Another live gateway must also check the durable denial on each request.
	peer, err := NewPersistentService(contextAuthenticator, root.CallerID, ctl, store)
	if err != nil {
		t.Fatal(err)
	}
	peerToken, err := peer.IssueCapability(grant)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeCapability(token); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeCapability(token); err != nil {
		t.Fatal(err)
	}
	peerID := RequestIdentity{Token: peerToken, CallerID: root.CallerID, EnvironmentID: root.EnvironmentID}
	assertDenied := func(service *Service, id RequestIdentity) {
		t.Helper()
		req := &mcp.CallToolRequest{}
		if _, _, e := service.status(identityContext(id), req, StatusInput{TaskID: root.ID}); !errors.Is(e, ErrUnauthorized) {
			t.Fatalf("status=%v", e)
		}
		if _, _, e := service.createChild(identityContext(id), req, ChildTaskInput{Goal: "denied", IdempotencyKey: "denied"}); !errors.Is(e, ErrUnauthorized) {
			t.Fatalf("create=%v", e)
		}
	}
	assertDenied(peer, peerID)
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = taskstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctl, err = controller.New(store, backend, root.CallerID, controller.Policy{Agent: "shell", TemplateRef: runtime.SbxTemplate, CPUs: 1, Memory: "1g"})
	if err != nil {
		t.Fatal(err)
	}
	s, err = NewPersistentService(contextAuthenticator, root.CallerID, ctl, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.IssueCapability(grant); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked grant resurrected: %v", err)
	}
	// Even a stale host cache cannot bypass per-request durable checks.
	s.capabilities = append(s.capabilities, peer.capabilities...)
	assertDenied(s, peerID)
	grant.RootTaskID = other.ID
	grant.EnvironmentID = other.EnvironmentID
	otherToken, err := s.IssueCapability(grant)
	if err != nil {
		t.Fatal(err)
	}
	_, out, err := s.status(identityContext(RequestIdentity{Token: otherToken, CallerID: other.CallerID, EnvironmentID: other.EnvironmentID}), &mcp.CallToolRequest{}, StatusInput{TaskID: other.ID})
	if err != nil || out.TaskID != other.ID {
		t.Fatalf("unrelated root denied: %+v %v", out, err)
	}
	if backend.calls != 2 {
		t.Fatal("revoked request reached backend")
	}
	_, child, err := s.createChild(identityContext(RequestIdentity{Token: otherToken, CallerID: other.CallerID, EnvironmentID: other.EnvironmentID}), &mcp.CallToolRequest{}, ChildTaskInput{Goal: "allowed unrelated child", IdempotencyKey: "allowed"})
	if err != nil || child.TaskID == "" || backend.calls != 3 {
		t.Fatalf("unrelated create denied: %+v %v", child, err)
	}
}
