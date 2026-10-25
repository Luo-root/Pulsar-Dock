package dockmcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/artifactstore"
	"github.com/Luo-root/pulsar-dock/internal/controller"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type publishFixture struct {
	s         *Service
	id        RequestIdentity
	root      taskstore.Task
	tasks     *taskstore.Store
	artifacts *artifactstore.Store
	dir       string
	input     PublishInput
}

func newPublishFixture(t *testing.T, wrap func(ArtifactStore) ArtifactStore) publishFixture {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	tasks, err := taskstore.Open(filepath.Join(dir, "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tasks.Close() })
	ctx := context.Background()
	root, err := tasks.CreateRoot(ctx, taskstore.CreateRequest{Scope: taskstore.Scope{CallerID: "synthetic-caller"}, Goal: "synthetic root", IdempotencyKey: "root"})
	if err != nil {
		t.Fatal(err)
	}
	scope := taskstore.Scope{CallerID: root.CallerID, RootID: root.ID}
	if _, err = tasks.BindEnvironment(ctx, scope, root.ID, taskstore.Created, "synthetic-env"); err != nil {
		t.Fatal(err)
	}
	root, err = tasks.Transition(ctx, scope, root.ID, taskstore.Created, taskstore.Preparing)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := artifactstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { artifacts.Close() })
	var publisher ArtifactStore = artifacts
	if wrap != nil {
		publisher = wrap(publisher)
	}
	s, err := NewPersistentServiceWithArtifactStore(contextAuthenticator, root.CallerID, childFunc(func(context.Context, string, controller.Request) (taskstore.Task, error) {
		return taskstore.Task{}, errors.New("unexpected child creation")
	}), tasks, publisher)
	if err != nil {
		t.Fatal(err)
	}
	id := grantIdentity(t, s, root.ID, ActionPublish, ActionStatus)
	return publishFixture{s, id, root, tasks, artifacts, dir, PublishInput{TaskID: root.ID, Kind: "review", Payload: "synthetic UTF-8 审查\n<script>never execute</script>", BaselineDigest: "synthetic-baseline", IdempotencyKey: "publish"}}
}

func (f publishFixture) publish(input PublishInput) (PublishOutput, error) {
	_, out, err := f.s.publish(identityContext(f.id), &mcp.CallToolRequest{}, input)
	return out, err
}

func TestPersistentPublishSDKAndReopen(t *testing.T) {
	f := newPublishFixture(t, nil)
	ctx := identityContext(f.id)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := f.s.CreateServer().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "synthetic", Version: "test"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: ActionPublish, Arguments: f.input})
	if err != nil || result.IsError {
		t.Fatalf("publish=%+v %v", result, err)
	}
	b, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out PublishOutput
	if err = json.Unmarshal(b, &out); err != nil || out.ArtifactID == "" || out.State != "published" {
		t.Fatalf("output=%s %v", b, err)
	}
	again, err := f.publish(f.input)
	if err != nil || again != out {
		t.Fatalf("retry=%+v %v", again, err)
	}
	d := sha256.Sum256([]byte(f.input.Payload))
	withDigest := f.input
	withDigest.Digest = hex.EncodeToString(d[:])
	again, err = f.publish(withDigest)
	if err != nil || again != out {
		t.Fatalf("digest retry=%+v %v", again, err)
	}
	if err = f.artifacts.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := artifactstore.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	// Scope comes from trusted fixture facts, never from Agent response metadata.
	a, err := reopened.Read(context.Background(), artifactstore.Scope{CallerID: f.root.CallerID, RootID: f.root.ID, TaskID: f.root.ID}, out.ArtifactID)
	if err != nil || string(a.Bytes) != f.input.Payload || a.Metadata.Digest != withDigest.Digest || a.Metadata.EnvironmentID != f.root.EnvironmentID || a.Metadata.Sensitivity != "restricted-unscanned" || a.Metadata.BaselineDigest != f.input.BaselineDigest {
		t.Fatalf("read=%+v %v", a, err)
	}
}

func TestPersistentPublishValidation(t *testing.T) {
	f := newPublishFixture(t, nil)
	if _, err := f.publish(f.input); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"payload", "kind", "baseline"} {
		t.Run("conflict-"+field, func(t *testing.T) {
			in := f.input
			switch field {
			case "payload":
				in.Payload += "changed"
			case "kind":
				in.Kind = "patch"
			case "baseline":
				in.BaselineDigest = "other"
			}
			_, err := f.publish(in)
			var e *artifactstore.Error
			if !errors.As(err, &e) || e.Kind != artifactstore.Conflict {
				t.Fatalf("conflict=%v", err)
			}
		})
	}
	for _, name := range []string{"digest", "oversize", "empty", "utf8", "baseline", "key", "kind", "long-key", "nul"} {
		t.Run(name, func(t *testing.T) {
			in := f.input
			in.IdempotencyKey = name
			switch name {
			case "digest":
				in.Digest = "wrong"
			case "oversize":
				in.Payload = strings.Repeat("界", artifactstore.MaxBytes/3+1)
			case "empty":
				in.Payload = ""
			case "utf8":
				in.Payload = string([]byte{255})
			case "baseline":
				in.BaselineDigest = " "
			case "key":
				in.IdempotencyKey = ""
			case "kind":
				in.Kind = ""
			case "long-key":
				in.IdempotencyKey = strings.Repeat("k", 513)
			case "nul":
				in.Kind = "review\x00"
			}
			out, err := f.publish(in)
			if err == nil || out != (PublishOutput{}) {
				t.Fatalf("accepted invalid input: %+v %v", out, err)
			}
		})
	}
	in := f.input
	in.Payload = strings.Repeat("a", artifactstore.MaxBytes)
	in.IdempotencyKey = "max"
	if _, err := f.publish(in); err != nil {
		t.Fatalf("exact limit: %v", err)
	}
}

func TestPersistentPublishAuthorization(t *testing.T) {
	for _, mode := range []string{"cross-task", "child", "caller", "env", "action", "revoked", "durable-revoke", "persistent-env", "closed"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublishFixture(t, nil)
			in := f.input
			switch mode {
			case "cross-task":
				in.TaskID = "other-root"
			case "child":
				child, err := f.tasks.CreateChild(context.Background(), taskstore.CreateRequest{Scope: taskstore.Scope{CallerID: f.root.CallerID, RootID: f.root.ID}, ParentID: f.root.ID, Goal: "child", IdempotencyKey: "child"})
				if err != nil {
					t.Fatal(err)
				}
				in.TaskID = child.ID
			case "caller":
				f.id.CallerID = "other"
			case "env":
				f.id.EnvironmentID = "other"
			case "action":
				f.id = grantIdentity(t, f.s, f.root.ID, ActionStatus)
			case "revoked":
				if err := f.s.RevokeCapability(f.id.Token); err != nil {
					t.Fatal(err)
				}
			case "durable-revoke":
				if err := f.tasks.RevokeRoot(context.Background(), taskstore.Scope{CallerID: f.root.CallerID, RootID: f.root.ID}); err != nil {
					t.Fatal(err)
				}
			case "persistent-env":
				f.s.persistent.store = readerFunc(func(context.Context, taskstore.Scope, string) (taskstore.Task, error) {
					r := f.root
					r.EnvironmentID = "other"
					return r, nil
				})
			case "closed":
				f.artifacts.Close()
			}
			out, err := f.publish(in)
			if err == nil || out != (PublishOutput{}) {
				t.Fatalf("accepted %s: %+v %v", mode, out, err)
			}
			if mode != "closed" && !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("authorization=%v", err)
			}
		})
	}
}

type delayedArtifactStore struct {
	ArtifactStore
	committed chan artifactstore.Metadata
	release   chan struct{}
}

func (s *delayedArtifactStore) Put(ctx context.Context, r artifactstore.PutRequest) (artifactstore.Metadata, error) {
	m, err := s.ArtifactStore.Put(ctx, r)
	if err != nil {
		return m, err
	}
	s.committed <- m
	<-s.release // Deliberately ignore cancellation after a successful commit.
	return m, nil
}
func TestPersistentPublishLateCommitInvalidation(t *testing.T) {
	for _, mode := range []string{"revoke", "rotate", "durable-revoke"} {
		t.Run(mode, func(t *testing.T) {
			var delayed *delayedArtifactStore
			f := newPublishFixture(t, func(s ArtifactStore) ArtifactStore {
				delayed = &delayedArtifactStore{s, make(chan artifactstore.Metadata, 1), make(chan struct{})}
				return delayed
			})
			defer close(delayed.release)
			result := make(chan error, 1)
			go func() {
				out, err := f.publish(f.input)
				if out != (PublishOutput{}) {
					result <- errors.New("late success returned")
					return
				}
				result <- err
			}()
			var meta artifactstore.Metadata
			select {
			case meta = <-delayed.committed:
			case <-time.After(3 * time.Second):
				t.Fatal("put not committed")
			}
			promptly(t, func() {
				_, _, err := f.s.status(identityContext(f.id), &mcp.CallToolRequest{}, StatusInput{TaskID: f.root.ID})
				if err != nil {
					t.Error(err)
				}
			})
			promptly(t, func() {
				switch mode {
				case "revoke":
					if err := f.s.RevokeCapability(f.id.Token); err != nil {
						t.Error(err)
					}
				case "rotate":
					f.s.RotateGeneration()
				case "durable-revoke":
					if err := f.tasks.RevokeRoot(context.Background(), taskstore.Scope{CallerID: f.root.CallerID, RootID: f.root.ID}); err != nil {
						t.Error(err)
					}
				}
			})
			delayed.release <- struct{}{}
			select {
			case err := <-result:
				if !errors.Is(err, ErrUnauthorized) {
					t.Fatalf("late result: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("publish blocked")
			}
			if _, err := f.artifacts.Read(context.Background(), meta.Scope, meta.ID); err != nil {
				t.Fatalf("committed snapshot lost: %v", err)
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
