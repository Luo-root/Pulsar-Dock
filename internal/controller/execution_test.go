package controller

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/agentadapter"
	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

type verifyFunc func(context.Context, runtime.Environment) error

func (f verifyFunc) Verify(c context.Context, e runtime.Environment) error { return f(c, e) }

type executionBackend struct {
	*fakeBackend
	calls   atomic.Int32
	exec    func(context.Context, runtime.Environment, runtime.ExecRequest) (string, error)
	inspect func(runtime.Environment) runtime.Environment
}

func (b *executionBackend) Name() string { return "test" }
func (b *executionBackend) Inspect(_ context.Context, e runtime.Environment) (runtime.Environment, error) {
	if b.inspect != nil {
		e = b.inspect(e)
	}
	return e, nil
}
func (b *executionBackend) Exec(c context.Context, e runtime.Environment, r runtime.ExecRequest) (string, error) {
	b.calls.Add(1)
	if b.exec != nil {
		return b.exec(c, e, r)
	}
	return "synthetic output", nil
}
func executionSetup(t *testing.T) (*Service, *taskstore.Store, *executionBackend, taskstore.Task) {
	t.Helper()
	s, store, b, _ := setup(t)
	b.create = func(spec runtime.EnvironmentSpec) (runtime.Environment, error) {
		return runtime.Environment{ID: "env-" + spec.TaskID, Name: "guest-" + spec.TaskID, Backend: "test", State: "running", Agent: spec.Agent, CPUs: spec.CPUs, Memory: spec.Memory}, nil
	}
	eb := &executionBackend{fakeBackend: b}
	planner, err := agentadapter.NewClaude(agentadapter.TrustedClaudeConfig{VersionOutput: agentadapter.ClaudeVersion + " (Claude Code)"})
	if err != nil {
		t.Fatal(err)
	}
	s, err = NewWithExecution(store, eb, "alice", policy(), ExecutionConfig{Planner: planner, Verifier: verifyFunc(func(context.Context, runtime.Environment) error { return nil }), MaxBudgetUSD: 0.1})
	if err != nil {
		t.Fatal(err)
	}
	root, err := s.CreateRoot(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	return s, store, eb, root
}
func TestExecutionClaudePlanAndNoSemanticSuccess(t *testing.T) {
	s, store, b, root := executionSetup(t)
	b.exec = func(_ context.Context, e runtime.Environment, r runtime.ExecRequest) (string, error) {
		if e.ID != root.EnvironmentID || r.User != "agent" || r.Command[0] != "claude" || r.Command[len(r.Command)-1] != root.Goal || !strings.Contains(strings.Join(r.Command, " "), "--max-budget-usd 0.1") {
			t.Errorf("bad plan: %+v", r)
		}
		return "command only", nil
	}
	result, err := s.ExecuteTask(ctx, root.ID, root.ID)
	if err != nil || !result.CommandCompleted || result.Output != "command only" || result.Task.State != taskstore.Running {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err = s.ExecuteTask(ctx, root.ID, root.ID); err == nil {
		t.Fatal("repeated execution")
	}
	persisted, _ := store.Get(ctx, scope(root), root.ID)
	if persisted.State == taskstore.Succeeded || b.calls.Load() != 1 {
		t.Fatal(persisted)
	}
	child, err := s.CreateChild(ctx, root.ID, Request{Goal: "child", IdempotencyKey: "child"})
	if err != nil {
		t.Fatal(err)
	}
	b.exec = nil
	if _, err = s.ExecuteTask(ctx, root.ID, child.ID); err != nil {
		t.Fatal(err)
	}
}
func TestExecutionAdmission(t *testing.T) {
	for _, kind := range []string{"caller", "root", "missing", "partial", "mismatch", "removed", "revoked", "state", "inspect", "verifier", "plan", "restart"} {
		t.Run(kind, func(t *testing.T) {
			s, store, b, root := executionSetup(t)
			rootID := root.ID
			switch kind {
			case "caller":
				s.caller = "bob"
			case "root":
				rootID = "other"
			case "missing":
				delete(s.handles, root.ID)
			case "partial":
				s.handles[root.ID].env.Name = ""
			case "mismatch":
				s.handles[root.ID].env.ID = "other"
			case "removed":
				s.handles[root.ID].removed = true
			case "revoked":
				if err := store.RevokeRoot(ctx, scope(root)); err != nil {
					t.Fatal(err)
				}
			case "state":
				if _, err := store.Transition(ctx, scope(root), root.ID, taskstore.Preparing, taskstore.Failed); err != nil {
					t.Fatal(err)
				}
			case "inspect":
				b.inspect = func(e runtime.Environment) runtime.Environment { e.State = "stopped"; return e }
			case "verifier":
				s.execution.Verifier = verifyFunc(func(context.Context, runtime.Environment) error { return errors.New("guest not verified") })
			case "plan": // A trusted planner must still execute as the unprivileged user.
				s.execution.Planner = badPlanner{}
			case "restart":
				s.handles = nil
			}
			if _, err := s.ExecuteTask(ctx, rootID, root.ID); err == nil {
				t.Fatal("admitted")
			}
			if b.calls.Load() != 0 {
				t.Fatal("backend executed")
			}
		})
	}
}

type badPlanner struct{}

func (badPlanner) Plan(agentadapter.Request) (runtime.ExecRequest, error) {
	return runtime.ExecRequest{Command: []string{"unexpected"}, User: "root"}, nil
}
func TestExecutionFailures(t *testing.T) {
	for _, kind := range []string{"exec", "oversize", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			s, store, b, root := executionSetup(t)
			c, cancel := context.WithCancel(ctx)
			defer cancel()
			b.exec = func(context.Context, runtime.Environment, runtime.ExecRequest) (string, error) {
				switch kind {
				case "exec":
					return "secret partial", errors.New("failed")
				case "oversize":
					return strings.Repeat("x", MaxExecutionOutputBytes+1), nil
				default:
					cancel()
					return "late", nil
				}
			}
			r, err := s.ExecuteTask(c, root.ID, root.ID)
			if err == nil || r.CommandCompleted || r.Output != "" {
				t.Fatalf("%+v %v", r, err)
			}
			persisted, _ := store.Get(ctx, scope(root), root.ID)
			if persisted.State != taskstore.Failed {
				t.Fatal(persisted)
			}
		})
	}
}
func TestExecutionStopCancelsAndWaits(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		t.Run(map[bool]string{false: "honors", true: "ignores"}[ignore], func(t *testing.T) {
			s, store, b, root := executionSetup(t)
			entered := make(chan struct{})
			cancelled := make(chan struct{})
			finish := make(chan struct{})
			b.exec = func(c context.Context, _ runtime.Environment, _ runtime.ExecRequest) (string, error) {
				close(entered)
				<-c.Done()
				close(cancelled)
				if ignore {
					<-finish
					return "late output", nil
				}
				return "", c.Err()
			}
			result := make(chan error, 1)
			go func() {
				r, err := s.ExecuteTask(ctx, root.ID, root.ID)
				if r.Output != "" || r.CommandCompleted {
					result <- errors.New("late success")
					return
				}
				result <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("no Exec")
			}
			if _, err := s.ExecuteTask(ctx, root.ID, root.ID); err == nil {
				t.Fatal("concurrent admitted")
			}
			stopped := make(chan error, 1)
			go func() { stopped <- s.StopTask(ctx, root.ID) }()
			select {
			case <-cancelled:
			case <-time.After(5 * time.Second):
				t.Fatal("not cancelled")
			}
			if ignore {
				select {
				case err := <-stopped:
					t.Fatalf("Stop did not wait: %v", err)
				default:
				}
				close(finish)
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("late execution accepted")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("execute deadlock")
			}
			select {
			case err := <-stopped:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("stop deadlock")
			}
			persisted, _ := store.Get(ctx, scope(root), root.ID)
			if persisted.State != taskstore.Stopped {
				t.Fatal(persisted)
			}
			if _, err := s.ExecuteTask(ctx, root.ID, root.ID); err == nil || b.calls.Load() != 1 {
				t.Fatal("restarted stopped environment")
			}
		})
	}
}
