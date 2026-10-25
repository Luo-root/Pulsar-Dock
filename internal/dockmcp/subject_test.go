package dockmcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/artifactstore"
	"github.com/Luo-root/pulsar-dock/internal/controller"
	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func subjectChild(t *testing.T, f publishFixture, key, env string) taskstore.Task {
	t.Helper()
	ctx := context.Background()
	scope := taskstore.Scope{CallerID: f.root.CallerID, RootID: f.root.ID}
	child, err := f.tasks.CreateChild(ctx, taskstore.CreateRequest{Scope: scope, ParentID: f.root.ID, Goal: key, IdempotencyKey: key, EnvironmentID: env})
	if err != nil {
		t.Fatal(err)
	}
	child, err = f.tasks.Transition(ctx, scope, child.ID, taskstore.Created, taskstore.Preparing)
	if err != nil {
		t.Fatal(err)
	}
	return child
}

func subjectGrant(f publishFixture, child taskstore.Task) CapabilityGrant {
	return CapabilityGrant{CallerID: f.root.CallerID, RootTaskID: f.root.ID, SubjectTaskID: child.ID, EnvironmentID: child.EnvironmentID, ExpiresAt: time.Now().Add(time.Hour), Actions: []string{ActionStatus, ActionPublish}}
}

func subjectIdentity(t *testing.T, s *Service, grant CapabilityGrant) RequestIdentity {
	t.Helper()
	token, err := s.IssueCapability(grant)
	if err != nil {
		t.Fatal(err)
	}
	return RequestIdentity{Token: token, CallerID: grant.CallerID, EnvironmentID: grant.EnvironmentID}
}

func TestChildSubjectIssuance(t *testing.T) {
	f := newPublishFixture(t, nil)
	child := subjectChild(t, f, "child", "child-env")
	sibling := subjectChild(t, f, "sibling", "sibling-env")
	unbound := subjectChild(t, f, "unbound", "")
	shared := subjectChild(t, f, "shared", f.root.EnvironmentID)
	good := subjectGrant(f, child)
	subjectIdentity(t, f.s, good)
	for _, mode := range []string{"caller", "root", "subject", "env", "root-env", "unbound", "shared", "create"} {
		t.Run(mode, func(t *testing.T) {
			g := good
			switch mode {
			case "caller":
				g.CallerID = "other"
			case "root":
				g.RootTaskID = child.ID
			case "subject":
				g.SubjectTaskID = "unknown"
			case "env":
				g.EnvironmentID = sibling.EnvironmentID
			case "root-env":
				g.EnvironmentID = f.root.EnvironmentID
			case "unbound":
				g.SubjectTaskID = unbound.ID
				g.EnvironmentID = "invented"
			case "shared":
				g.SubjectTaskID = shared.ID
				g.EnvironmentID = shared.EnvironmentID
			case "create":
				g.Actions = []string{ActionStatus, ActionCreateChild}
			}
			if _, err := f.s.IssueCapability(g); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("accepted %s: %v", mode, err)
			}
		})
	}
	// Explicit root subject and legacy empty subject have identical authority.
	g := good
	g.SubjectTaskID = f.root.ID
	g.EnvironmentID = f.root.EnvironmentID
	subjectIdentity(t, f.s, g)
}

func TestChildSubjectSDKPublishAndReopen(t *testing.T) {
	f := newPublishFixture(t, nil)
	child := subjectChild(t, f, "child", "child-env")
	sibling := subjectChild(t, f, "sibling", "sibling-env")
	grant := subjectGrant(f, child)
	id := subjectIdentity(t, f.s, grant)
	// Single synthetic environment-bound connection: tool arguments cannot replace id.
	f.s.authenticator = func(context.Context, *mcp.CallToolRequest) (RequestIdentity, error) { return id, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := f.s.CreateServer().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "synthetic-child", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(action string, args any, denied bool) *mcp.CallToolResult {
		t.Helper()
		result, e := cs.CallTool(ctx, &mcp.CallToolParams{Name: action, Arguments: args})
		if e != nil || result.IsError != denied {
			t.Fatalf("%s denied=%v result=%+v err=%v", action, denied, result, e)
		}
		return result
	}
	call(ActionStatus, StatusInput{TaskID: child.ID}, false)
	for _, target := range []string{f.root.ID, sibling.ID, "unknown", ""} {
		call(ActionStatus, map[string]any{"task_id": target, "subject_task_id": f.root.ID, "root_task_id": f.root.ID, "environment_id": f.root.EnvironmentID, "caller_id": f.root.CallerID, "token": f.id.Token}, true)
		in := f.input
		in.TaskID = target
		call(ActionPublish, in, true)
	}
	call(ActionCreateChild, ChildTaskInput{Goal: "recursive", IdempotencyKey: "recursive"}, true)
	in := f.input
	in.TaskID = child.ID
	result := call(ActionPublish, in, false)
	b, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out PublishOutput
	if err = json.Unmarshal(b, &out); err != nil || out.ArtifactID == "" {
		t.Fatalf("output=%s %v", b, err)
	}
	scope := artifactstore.Scope{CallerID: child.CallerID, RootID: f.root.ID, TaskID: child.ID}
	a, err := f.artifacts.Read(ctx, scope, out.ArtifactID)
	if err != nil || string(a.Bytes) != in.Payload || a.Metadata.EnvironmentID != child.EnvironmentID || a.Metadata.Scope != scope {
		t.Fatalf("child snapshot=%+v %v", a, err)
	}
	if _, err = f.artifacts.Read(ctx, artifactstore.Scope{CallerID: f.root.CallerID, RootID: f.root.ID, TaskID: f.root.ID}, out.ArtifactID); err == nil {
		t.Fatal("snapshot assigned to root")
	}
	for _, wrong := range []RequestIdentity{
		{Token: id.Token, CallerID: id.CallerID, EnvironmentID: f.root.EnvironmentID},
		{Token: id.Token, CallerID: "other", EnvironmentID: id.EnvironmentID},
	} {
		if _, _, err = f.s.runPersistentStatus(identityContext(wrong), wrong, StatusInput{TaskID: child.ID}); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("wrong child identity accepted", err)
		}
	}
	// Root may read child status but cannot publish as it.
	if _, _, err = f.s.runPersistentStatus(identityContext(f.id), f.id, StatusInput{TaskID: child.ID}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.runPersistentPublish(identityContext(f.id), f.id, in); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("root impersonated child", err)
	}
	if err = f.tasks.Close(); err != nil {
		t.Fatal(err)
	}
	tasks, err := taskstore.Open(filepath.Join(f.dir, "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tasks.Close()
	if err = f.artifacts.Close(); err != nil {
		t.Fatal(err)
	}
	artifacts, err := artifactstore.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer artifacts.Close()
	reopened, err := NewPersistentServiceWithArtifactStore(contextAuthenticator, f.root.CallerID, f.s.persistent.controller, tasks, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = reopened.status(identityContext(id), &mcp.CallToolRequest{}, StatusInput{TaskID: child.ID}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old token revived", err)
	}
	fresh := subjectIdentity(t, reopened, grant)
	if _, _, err = reopened.status(identityContext(fresh), &mcp.CallToolRequest{}, StatusInput{TaskID: child.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = artifacts.Read(ctx, scope, out.ArtifactID); err != nil {
		t.Fatal("snapshot missing after reopen", err)
	}
}

func TestChildSubjectRuntimeFacts(t *testing.T) {
	for _, mode := range []string{"caller", "root", "parent", "env", "empty-env", "shared-env", "stopped", "root-stopped", "create-defense"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublishFixture(t, nil)
			child := subjectChild(t, f, "child", "child-env")
			id := subjectIdentity(t, f.s, subjectGrant(f, child))
			root := f.root
			switch mode {
			case "caller":
				child.CallerID = "other"
			case "root":
				child.RootID = "other"
			case "parent":
				child.ParentID = "sibling"
			case "env":
				child.EnvironmentID = "other"
			case "empty-env":
				child.EnvironmentID = ""
			case "shared-env":
				child.EnvironmentID = root.EnvironmentID
			case "stopped":
				child.State = taskstore.Stopped
			case "root-stopped":
				root.State = taskstore.Stopped
			case "create-defense":
				f.s.capabilities[len(f.s.capabilities)-1].grant.Actions = append(f.s.capabilities[len(f.s.capabilities)-1].grant.Actions, ActionCreateChild)
				if _, _, err := f.s.createChild(identityContext(id), &mcp.CallToolRequest{}, ChildTaskInput{Goal: "recursive", IdempotencyKey: "recursive"}); !errors.Is(err, ErrUnauthorized) {
					t.Fatal(err)
				}
				return
			}
			f.s.persistent.store = readerFunc(func(_ context.Context, scope taskstore.Scope, task string) (taskstore.Task, error) {
				if scope != (taskstore.Scope{CallerID: f.root.CallerID, RootID: f.root.ID}) {
					t.Error("untrusted scope")
				}
				if task == root.ID {
					return root, nil
				}
				return child, nil
			})
			if _, _, err := f.s.status(identityContext(id), &mcp.CallToolRequest{}, StatusInput{TaskID: child.ID}); !errors.Is(err, ErrUnauthorized) {
				t.Fatal("stale subject authorized", err)
			}
			in := f.input
			in.TaskID = child.ID
			if _, _, err := f.s.publish(identityContext(id), &mcp.CallToolRequest{}, in); !errors.Is(err, ErrUnauthorized) {
				t.Fatal("stale publisher authorized", err)
			}
		})
	}
}

func TestChildSubjectTreeRevocation(t *testing.T) {
	for _, mode := range []string{"root-token", "child-token", "durable", "rotate"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublishFixture(t, nil)
			child := subjectChild(t, f, "child", "child-env")
			sibling := subjectChild(t, f, "sibling", "sibling-env")
			grant := subjectGrant(f, child)
			id := subjectIdentity(t, f.s, grant)
			sid := subjectIdentity(t, f.s, subjectGrant(f, sibling))
			switch mode {
			case "root-token":
				if err := f.s.RevokeCapability(f.id.Token); err != nil {
					t.Fatal(err)
				}
			case "child-token":
				f.s.RotateGeneration()
				if err := f.s.RevokeCapability(id.Token); err != nil {
					t.Fatal(err)
				}
			case "durable":
				if err := f.tasks.RevokeRoot(context.Background(), taskstore.Scope{CallerID: f.root.CallerID, RootID: f.root.ID}); err != nil {
					t.Fatal(err)
				}
			case "rotate":
				f.s.RotateGeneration()
			}
			for _, pair := range []struct {
				id   RequestIdentity
				task string
			}{{f.id, f.root.ID}, {id, child.ID}, {sid, sibling.ID}} {
				if _, _, err := f.s.status(identityContext(pair.id), &mcp.CallToolRequest{}, StatusInput{TaskID: pair.task}); !errors.Is(err, ErrUnauthorized) {
					t.Fatal("tree access survived", err)
				}
			}
			if mode == "rotate" {
				subjectIdentity(t, f.s, grant)
				return
			}
			if err := f.tasks.Close(); err != nil {
				t.Fatal(err)
			}
			tasks, err := taskstore.Open(filepath.Join(f.dir, "tasks.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer tasks.Close()
			reopened, err := NewPersistentService(contextAuthenticator, f.root.CallerID, f.s.persistent.controller, tasks)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = reopened.IssueCapability(grant); !errors.Is(err, ErrUnauthorized) {
				t.Fatal("revoked child revived", err)
			}
			grant.SubjectTaskID = f.root.ID
			grant.EnvironmentID = f.root.EnvironmentID
			if _, err = reopened.IssueCapability(grant); !errors.Is(err, ErrUnauthorized) {
				t.Fatal("revoked root revived", err)
			}
		})
	}
}

func TestChildSubjectLatePublish(t *testing.T) {
	for _, mode := range []string{"root-revoke", "child-revoke", "rotate", "durable", "cancel", "stop"} {
		t.Run(mode, func(t *testing.T) {
			var delayed *delayedArtifactStore
			f := newPublishFixture(t, func(s ArtifactStore) ArtifactStore {
				delayed = &delayedArtifactStore{s, make(chan artifactstore.Metadata, 1), make(chan struct{})}
				return delayed
			})
			defer close(delayed.release)
			child := subjectChild(t, f, "child", "child-env")
			id := subjectIdentity(t, f.s, subjectGrant(f, child))
			ctx, cancel := context.WithCancel(identityContext(id))
			defer cancel()
			in := f.input
			in.TaskID = child.ID
			result := make(chan error, 1)
			go func() {
				_, out, err := f.s.publish(ctx, &mcp.CallToolRequest{}, in)
				if out != (PublishOutput{}) {
					result <- errors.New("late success")
					return
				}
				result <- err
			}()
			var meta artifactstore.Metadata
			select {
			case meta = <-delayed.committed:
			case <-time.After(3 * time.Second):
				t.Fatal("not committed")
			}
			switch mode {
			case "root-revoke":
				if err := f.s.RevokeCapability(f.id.Token); err != nil {
					t.Fatal(err)
				}
			case "child-revoke":
				if err := f.s.RevokeCapability(id.Token); err != nil {
					t.Fatal(err)
				}
			case "rotate":
				f.s.RotateGeneration()
			case "durable":
				if err := f.tasks.RevokeRoot(context.Background(), taskstore.Scope{CallerID: f.root.CallerID, RootID: f.root.ID}); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				cancel()
			case "stop":
				ctl, err := controller.New(f.tasks, &persistentBackend{}, f.root.CallerID, controller.Policy{Agent: "shell", TemplateRef: runtime.SbxTemplate, CPUs: 1, Memory: "1g"})
				if err != nil {
					t.Fatal(err)
				}
				// This Controller did not create these fixture environments: stop must
				// persist denial but honestly report that runtime cleanup needs recovery.
				if err = ctl.StopTask(context.Background(), f.root.ID); !errors.Is(err, controller.ErrRecoveryRequired) {
					t.Fatalf("stop=%v", err)
				}
			}
			delayed.release <- struct{}{}
			select {
			case err := <-result:
				if mode == "cancel" {
					if err == nil {
						t.Fatal("cancel accepted")
					}
				} else if !errors.Is(err, ErrUnauthorized) {
					t.Fatal("late publish accepted", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("publish blocked")
			}
			if _, err := f.artifacts.Read(context.Background(), meta.Scope, meta.ID); err != nil {
				t.Fatal("committed snapshot removed", err)
			}
			f.s.mu.Lock()
			remaining := len(f.s.persistent.operations)
			f.s.mu.Unlock()
			if remaining != 0 {
				t.Fatal("lease leaked")
			}
		})
	}
}
