package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/agentadapter"
	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

func TestExecutionDurableRevokeSuppressesLateOutput(t *testing.T) {
	s, store, b, root := executionSetup(t)
	b.exec = func(context.Context, runtime.Environment, runtime.ExecRequest) (string, error) {
		return "late", store.RevokeRoot(ctx, scope(root))
	}
	r, err := s.ExecuteTask(ctx, root.ID, root.ID)
	if err == nil || r.CommandCompleted || r.Output != "" {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestExecutionVerifierCancellationIsTracked(t *testing.T) {
	s, _, b, root := executionSetup(t)
	entered := make(chan struct{})
	s.execution.Verifier = verifyFunc(func(c context.Context, _ runtime.Environment) error { close(entered); <-c.Done(); return c.Err() })
	done := make(chan error, 1)
	go func() { _, err := s.ExecuteTask(ctx, root.ID, root.ID); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("verifier not entered")
	}
	if err := s.StopTask(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("cancel ignored")
	}
	if b.calls.Load() != 0 {
		t.Fatal("executed after stop")
	}
}
func TestExecutionGoalAndOutputBounds(t *testing.T) {
	for _, goal := range []string{strings.Repeat("x", agentadapter.MaxGoalBytes+1), "line\nbreak"} {
		t.Run("invalid goal", func(t *testing.T) {
			s, _, b, _ := executionSetup(t)
			root, err := s.CreateRoot(ctx, Request{Goal: goal, IdempotencyKey: "bounded"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ExecuteTask(ctx, root.ID, root.ID); err == nil || b.calls.Load() != 0 {
				t.Fatal("unbounded goal executed")
			}
		})
	}
	s, _, b, root := executionSetup(t)
	b.exec = func(context.Context, runtime.Environment, runtime.ExecRequest) (string, error) {
		return strings.Repeat("x", MaxExecutionOutputBytes), nil
	}
	r, err := s.ExecuteTask(ctx, root.ID, root.ID)
	if err != nil || len(r.Output) != MaxExecutionOutputBytes {
		t.Fatalf("output boundary: %d %v", len(r.Output), err)
	}
}
func TestExecutionRootGateNotHeldAcrossExec(t *testing.T) {
	s, _, b, root := executionSetup(t)
	entered := make(chan struct{})
	finish := make(chan struct{})
	b.exec = func(context.Context, runtime.Environment, runtime.ExecRequest) (string, error) {
		close(entered)
		<-finish
		return "done", nil
	}
	done := make(chan error, 1)
	go func() { _, err := s.ExecuteTask(ctx, root.ID, root.ID); done <- err }()
	<-entered
	// Same-root delegation must remain possible during a long model command.
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	child, err := s.CreateChild(c, root.ID, Request{Goal: "parallel child", IdempotencyKey: "parallel"})
	close(finish)
	if runErr := <-done; runErr != nil {
		t.Fatal(runErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExecuteTask(ctx, child.ID, child.ID); err == nil {
		t.Fatal("child accepted as root scope")
	}
}

func TestExecutionNoConfigurationAndCancelledContext(t *testing.T) {
	s, store, b, root := executionSetup(t)
	disabled, err := New(store, b, "alice", policy())
	if err != nil {
		t.Fatal(err)
	}
	disabled.handles = s.handles
	if _, err = disabled.ExecuteTask(ctx, root.ID, root.ID); !errors.Is(err, ErrExecutionDenied) {
		t.Fatal(err)
	}
	c, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.ExecuteTask(c, root.ID, root.ID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	persisted, _ := store.Get(ctx, scope(root), root.ID)
	if persisted.State != taskstore.Preparing || b.calls.Load() != 0 {
		t.Fatal(persisted)
	}
}
