//go:build sandboxsmoke

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	runtime "github.com/Luo-root/pulsar-dock/internal/runtime"
)

const realSbxSmokeMarker = "PULSAR_REAL_SBX_SMOKE_MARKER"

func TestSbxBackendLifecycleSmoke(t *testing.T) {
	if os.Getenv("PULSAR_REAL_SBX_SMOKE") != "1" {
		t.Skip("run with -tags sandboxsmoke and PULSAR_REAL_SBX_SMOKE=1 to enable the real sbx lifecycle smoke test")
	}

	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	staging, err := os.MkdirTemp(root, ".sbx-real-smoke-")
	if err != nil {
		t.Fatal(err)
	}
	// Register staging cleanup before environment cleanup so the latter runs first.
	t.Cleanup(func() {
		if err := os.RemoveAll(staging); err != nil {
			t.Errorf("remove private staging directory: %v", err)
		}
	})

	backend := runtime.NewSbxBackend(nil, staging)
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 30*time.Second)
	check := backend.Check(checkCtx)
	checkCancel()
	if !check.Ready {
		t.Fatalf("real sbx runner preflight failed: %v", check.Messages)
	}

	spec := runtime.EnvironmentSpec{
		TaskID:       "real-backend-smoke",
		Agent:        "shell",
		TemplateRef:  runtime.SbxTemplate,
		Workspace:    runtime.WorkspaceNone,
		Skills:       runtime.SkillsOff,
		CPUs:         2,
		Memory:       "2g",
		NetworkRules: nil,
		GatewayURL:   "",
		ExpiresAfter: 0,
	}

	createCtx, createCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	environment, err := backend.Create(createCtx, spec)
	createCancel()
	if err != nil {
		t.Fatalf("create failed; no environment handle is owned for cleanup, and orphan identity must be checked independently (no name-guess deletion): %v", err)
	}
	t.Cleanup(func() {
		removeCtx, removeCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer removeCancel()
		if err := backend.Remove(removeCtx, environment); err != nil {
			t.Errorf("cleanup remove owned environment %s: %v", environment.ID, err)
		}
	})

	inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 30*time.Second)
	inspected, err := backend.Inspect(inspectCtx, environment)
	inspectCancel()
	if err != nil {
		t.Fatalf("inspect after create: %v", err)
	}
	if inspected.ID != environment.ID || inspected.Name != environment.Name || inspected.Agent != "shell" || inspected.CPUs != 2 || inspected.Memory != "2g" {
		t.Fatalf("created environment mismatch: got id=%q name=%q agent=%q cpus=%d memory=%q", inspected.ID, inspected.Name, inspected.Agent, inspected.CPUs, inspected.Memory)
	}

	execCtx, execCancel := context.WithTimeout(context.Background(), 30*time.Second)
	output, err := backend.Exec(execCtx, environment, runtime.ExecRequest{Command: []string{"printf", realSbxSmokeMarker}, User: "agent"})
	execCancel()
	if err != nil {
		t.Fatalf("exec marker: %v", err)
	}
	if output != realSbxSmokeMarker {
		t.Fatal("exec marker mismatch; guest output withheld")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = backend.Stop(stopCtx, environment)
	stopCancel()
	if err != nil {
		t.Fatalf("stop: %v", err)
	}

	stoppedCtx, stoppedCancel := context.WithTimeout(context.Background(), 30*time.Second)
	stopped, err := backend.Inspect(stoppedCtx, environment)
	stoppedCancel()
	if err != nil {
		t.Fatalf("inspect after stop: %v", err)
	}
	if stopped.ID != environment.ID || stopped.Name != environment.Name || stopped.State != "stopped" {
		t.Fatalf("stopped environment mismatch: got id=%q name=%q state=%q", stopped.ID, stopped.Name, stopped.State)
	}

	removeCtx, removeCancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = backend.Remove(removeCtx, environment)
	removeCancel()
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
}
