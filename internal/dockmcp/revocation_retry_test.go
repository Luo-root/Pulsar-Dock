package dockmcp

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/controller"
	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type recoverableRevocationStore struct {
	TaskReader
	unavailable atomic.Bool
	writes      atomic.Int32
}

func (s *recoverableRevocationStore) RevokeRoot(ctx context.Context, scope taskstore.Scope) error {
	s.writes.Add(1)
	if s.unavailable.Load() {
		return errors.New("synthetic unavailable storage")
	}
	return s.TaskReader.RevokeRoot(ctx, scope)
}

func TestRevocationRetrySurvivesRotationsAndReopen(t *testing.T) {
	for _, rotateFirst := range []bool{false, true} {
		name := "revoke-before-rotate"
		if rotateFirst {
			name = "rotate-before-first-revoke"
		}
		for _, failFirst := range []bool{false, true} {
			variant := name + "/write-succeeds"
			if failFirst {
				variant = name + "/first-write-fails"
			}
			t.Run(variant, func(t *testing.T) { testRevocationRetryReopen(t, rotateFirst, failFirst) })
		}
	}
}

func testRevocationRetryReopen(t *testing.T, rotateFirst, failFirst bool) {
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
	unstable := &recoverableRevocationStore{TaskReader: store}
	s, err := NewPersistentService(contextAuthenticator, root.CallerID, ctl, unstable)
	if err != nil {
		t.Fatal(err)
	}
	grant := CapabilityGrant{CallerID: root.CallerID, RootTaskID: root.ID, EnvironmentID: root.EnvironmentID, ExpiresAt: time.Now().Add(time.Hour), Actions: []string{ActionStatus, ActionCreateChild}}
	token, err := s.IssueCapability(grant)
	if err != nil {
		t.Fatal(err)
	}
	if rotateFirst {
		s.RotateGeneration()
	}
	unstable.unavailable.Store(failFirst)
	if err = s.RevokeCapability(token); (err != nil) != failFirst {
		t.Fatalf("first revoke error=%v, failFirst=%v", err, failFirst)
	}
	s.RotateGeneration()
	s.RotateGeneration()
	if err = s.RevokeCapability(token); (err != nil) != failFirst {
		t.Fatalf("retry after rotations error=%v, failFirst=%v", err, failFirst)
	}
	if unstable.writes.Load() != 2 {
		t.Fatal("retry skipped durable write")
	}
	if _, err = s.IssueCapability(grant); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("pending revocation allowed reissue", err)
	}
	unstable.unavailable.Store(false)
	if err = s.RevokeCapability(token); err != nil {
		t.Fatal(err)
	}
	denied, err := store.RootRevoked(ctx, taskstore.Scope{CallerID: root.CallerID, RootID: root.ID})
	if err != nil || !denied {
		t.Fatal("retry did not persist denial", err)
	}
	// Concurrent duplicate retries and rotations cannot discard retry identity.
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.RotateGeneration()
			if e := s.RevokeCapability(token); e != nil {
				t.Errorf("concurrent retry: %v", e)
			}
		}()
	}
	wg.Wait()
	if unstable.writes.Load() != 15 {
		t.Fatalf("duplicate retries skipped persistence: %d", unstable.writes.Load())
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = taskstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err = NewPersistentService(contextAuthenticator, root.CallerID, ctl, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.IssueCapability(grant); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked root resurrected", err)
	}
	grant.RootTaskID = other.ID
	grant.EnvironmentID = other.EnvironmentID
	otherToken, err := s.IssueCapability(grant)
	if err != nil {
		t.Fatal(err)
	}
	_, out, err := s.status(identityContext(RequestIdentity{Token: otherToken, CallerID: other.CallerID, EnvironmentID: other.EnvironmentID}), &mcp.CallToolRequest{}, StatusInput{TaskID: other.ID})
	if err != nil || out.TaskID != other.ID {
		t.Fatalf("unrelated root unavailable: %+v %v", out, err)
	}
}
