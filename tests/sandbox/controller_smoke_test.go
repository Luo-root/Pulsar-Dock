//go:build sandboxsmoke

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/controller"
	runtime "github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

func TestControllerLifecycleSmoke(t *testing.T) {
	if os.Getenv("PULSAR_REAL_CONTROLLER_SMOKE") != "1" {
		t.Skip("run with -tags sandboxsmoke and PULSAR_REAL_CONTROLLER_SMOKE=1 to enable the real Controller lifecycle smoke test")
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	staging, err := os.MkdirTemp(cwd, ".controller-real-smoke-")
	if err != nil {
		t.Fatal(err)
	}
	// Cleanup is LIFO: stop real resources, close the DB, then remove staging.
	t.Cleanup(func() {
		if err := os.RemoveAll(staging); err != nil {
			t.Errorf("remove private staging directory: %v", err)
		}
	})
	store, err := taskstore.Open(filepath.Join(staging, "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close task database after resource cleanup: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	backend := runtime.NewSbxBackend(nil, staging)
	checkCtx, checkCancel := context.WithTimeout(ctx, 30*time.Second)
	check := backend.Check(checkCtx)
	checkCancel()
	if !check.Ready {
		t.Fatalf("real sbx runner preflight failed: %v", check.Messages)
	}

	const caller = "real-controller-smoke"
	svc, err := controller.New(store, backend, caller, controller.Policy{
		Agent: "shell", TemplateRef: runtime.SbxTemplate, CPUs: 2, Memory: "2g",
		GatewayURL: "", ExpiresAfter: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	var rootID string
	t.Cleanup(func() {
		if rootID == "" {
			return
		}
		// Never inherit an expired test/request context. Only Controller-owned
		// handles authorize removal; unknown outcomes must remain visible.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if err := svc.StopTask(cleanupCtx, rootID); err != nil {
			t.Errorf("cleanup Controller.StopTask(%s): %v; real resource cleanup is unconfirmed; independently reconcile unknown identities (no name-guess deletion)", rootID, err)
		}
	})

	createCtx, createCancel := context.WithTimeout(ctx, 2*time.Minute)
	root, err := svc.CreateRoot(createCtx, controller.Request{
		Goal: "synthetic Controller root smoke", IdempotencyKey: "controller-root-smoke",
	})
	createCancel()
	// A persisted task returned with an error still needs a StopTask attempt.
	rootID = root.ID
	if err != nil {
		t.Fatalf("CreateRoot (task %q): %v; cleanup will attempt StopTask when an ID was returned; unknown runtime identities must be reconciled independently, never guessed", rootID, err)
	}
	if rootID == "" {
		t.Fatal("CreateRoot returned no task ID; runtime outcome is unknown and cannot be cleaned up by guessing a name")
	}

	childCtx, childCancel := context.WithTimeout(ctx, 2*time.Minute)
	child, err := svc.CreateChild(childCtx, rootID, controller.Request{
		Goal: "synthetic Controller child smoke", IdempotencyKey: "controller-child-smoke",
	})
	childCancel()
	if err != nil {
		t.Fatalf("CreateChild (task %q): %v; root cleanup will attempt StopTask; unknown runtime identities must not be guessed", child.ID, err)
	}
	if child.ID == "" || child.ID == rootID || root.EnvironmentID == "" || child.EnvironmentID == "" || root.EnvironmentID == child.EnvironmentID {
		t.Fatalf("distinct task/environment identities required: root=%+v child=%+v", root, child)
	}

	scope := taskstore.Scope{CallerID: caller, RootID: rootID}
	assertPersisted := func(want taskstore.Task, parent string, state taskstore.State) taskstore.Task {
		t.Helper()
		readCtx, readCancel := context.WithTimeout(ctx, 10*time.Second)
		defer readCancel()
		got, err := store.Get(readCtx, scope, want.ID)
		if err != nil {
			t.Fatalf("read persisted task %s: %v", want.ID, err)
		}
		if got.ID != want.ID || got.CallerID != caller || got.ParentID != parent || got.RootID != rootID || got.EnvironmentID != want.EnvironmentID || got.State != state {
			t.Fatalf("persisted task mismatch: got=%+v want id=%s caller=%s parent=%s root=%s environment=%s state=%s", got, want.ID, caller, parent, rootID, want.EnvironmentID, state)
		}
		return got
	}
	assertPersisted(root, "", taskstore.Preparing)
	assertPersisted(child, rootID, taskstore.Preparing)

	stop := func() {
		t.Helper()
		stopCtx, stopCancel := context.WithTimeout(ctx, time.Minute)
		defer stopCancel()
		if err := svc.StopTask(stopCtx, rootID); err != nil {
			t.Fatalf("Controller.StopTask(%s): %v; cleanup will retry", rootID, err)
		}
	}
	assertRevoked := func() {
		t.Helper()
		readCtx, readCancel := context.WithTimeout(ctx, 10*time.Second)
		defer readCancel()
		revoked, err := store.RootRevoked(readCtx, scope)
		if err != nil || !revoked {
			t.Fatalf("persisted root revocation: revoked=%t err=%v", revoked, err)
		}
	}
	stop()
	assertRevoked()
	stoppedRoot := assertPersisted(root, "", taskstore.Stopped)
	stoppedChild := assertPersisted(child, rootID, taskstore.Stopped)

	stop()
	assertRevoked()
	retriedRoot := assertPersisted(root, "", taskstore.Stopped)
	retriedChild := assertPersisted(child, rootID, taskstore.Stopped)
	if retriedRoot != stoppedRoot || retriedChild != stoppedChild {
		t.Fatal("repeated StopTask changed terminal persisted task history")
	}
}
