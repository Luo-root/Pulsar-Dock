package controller

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Luo-root/pulsar-dock/internal/agentadapter"
	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

const MaxExecutionOutputBytes = 64 * 1024

var ErrExecutionDenied = errors.New("task execution denied")

// Planner is injected by the trusted host, never selected by Agent arguments.
type Planner interface {
	Plan(agentadapter.Request) (runtime.ExecRequest, error)
}

// GuestVerifier must independently verify the selected guest binary/version,
// pinned PATH, MCP config content/ownership and isolation prerequisites in env.
// A caller-claimed version or a successful Plan is not verification. Implementations
// must honor ctx; this interface supplies no identity or gateway authorization.
type GuestVerifier interface {
	Verify(context.Context, runtime.Environment) error
}

// GuestCleanup is optional lifecycle cleanup for verifier-owned guest resources
// such as scoped model proxy listeners. It is invoked after execution admission
// ends and must be idempotent.
type GuestCleanup interface {
	Cleanup(context.Context, runtime.Environment) error
	Close() error
}

// ExecutionConfig is immutable host configuration. No execution is available
// through New alone. The host must provision and verify a compatible guest.
type ExecutionConfig struct {
	Planner      Planner
	Verifier     GuestVerifier
	MaxBudgetUSD float64
}

func NewWithExecution(store *taskstore.Store, backend runtime.RuntimeBackend, caller string, policy Policy, config ExecutionConfig) (*Service, error) {
	if config.Planner == nil || config.Verifier == nil || math.IsNaN(config.MaxBudgetUSD) || math.IsInf(config.MaxBudgetUSD, 0) || config.MaxBudgetUSD <= 0 {
		return nil, errors.New("trusted planner, guest verifier and positive fixed budget required")
	}
	s, err := New(store, backend, caller, policy)
	if err != nil {
		return nil, err
	}
	s.execution = &config
	return s, nil
}

// ExecutionResult reports command completion only, never Agent semantic success.
// Output is untrusted guest data, not an artifact or trusted terminal markup.
type ExecutionResult struct {
	Task             taskstore.Task
	CommandCompleted bool
	Output           string
}

// ExecuteTask is control-plane only: rootID/taskID are trusted UI selections,
// not authenticated identities. Scope always uses the Service's fixed caller.
// Only Preparing tasks execute; concurrent/repeated calls are rejected, with no
// automatic retries even on ambiguous backend failure or after host restart.
func (s *Service) ExecuteTask(ctx context.Context, rootID, taskID string) (ExecutionResult, error) {
	scope := taskstore.Scope{CallerID: s.caller, RootID: rootID}
	release, err := s.gate(ctx, rootID)
	if err != nil {
		return ExecutionResult{}, err
	}
	t, err := s.store.Get(ctx, scope, taskID)
	if err != nil {
		release()
		return ExecutionResult{}, err
	}
	result := ExecutionResult{Task: t}
	revoked, err := s.store.RootRevoked(ctx, scope)
	if err != nil || revoked {
		release()
		return result, errors.Join(ErrRootRevoked, err)
	}
	if s.execution == nil || t.State != taskstore.Preparing || !utf8.ValidString(t.Goal) || len(t.Goal) > agentadapter.MaxGoalBytes || strings.TrimSpace(t.Goal) == "" || strings.ContainsAny(t.Goal, "\x00\r\n") {
		release()
		return result, ErrExecutionDenied
	}
	backendName := s.backend.Name()
	s.mu.Lock()
	h := s.handles[taskID]
	var env runtime.Environment
	if h != nil {
		env = h.env
	}
	valid := h != nil && !h.removed && !h.neverInvoked && env.ID != "" && env.ID == t.EnvironmentID && env.Name != "" && env.Backend == backendName && env.Agent == s.spec.Agent && env.CPUs == s.spec.CPUs && env.Memory == s.spec.Memory
	if !valid || s.executions[taskID] != nil {
		s.mu.Unlock()
		release()
		return result, errors.Join(ErrExecutionDenied, ErrRecoveryRequired)
	}
	execCtx, cancel := context.WithCancel(ctx)
	op := &creation{root: rootID, cancel: cancel, done: make(chan struct{})}
	if s.executions == nil {
		s.executions = make(map[string]*creation)
	}
	s.executions[taskID] = op
	s.mu.Unlock()
	release()
	defer func() {
		cancel()
		if cleanup, ok := s.execution.Verifier.(GuestCleanup); ok {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = cleanup.Cleanup(cleanupCtx, env)
			cleanupCancel()
		}
		s.mu.Lock()
		delete(s.executions, taskID)
		close(op.done)
		s.mu.Unlock()
	}()

	// No root gate or global mutex across any backend/verifier/planner IO.
	inspected, runErr := s.backend.Inspect(execCtx, env)
	if runErr == nil && (inspected.ID != env.ID || inspected.Name != env.Name || inspected.Backend != env.Backend || inspected.Agent != env.Agent || inspected.CPUs != env.CPUs || inspected.Memory != env.Memory || inspected.State != "running") {
		runErr = ErrExecutionDenied
	}
	if runErr == nil {
		runErr = s.execution.Verifier.Verify(execCtx, env)
	}
	var plan runtime.ExecRequest
	if runErr == nil {
		plan, runErr = s.execution.Planner.Plan(agentadapter.Request{Goal: t.Goal, MCPConfigPath: agentadapter.GuestMCPConfigPath, MaxBudgetUSD: s.execution.MaxBudgetUSD})
		if runErr == nil && (plan.User != "agent" || len(plan.Command) == 0) {
			runErr = ErrExecutionDenied
		}
	}
	if runErr == nil {
		// Serialize final admission against Stop; Stop waits for this registration
		// before touching the runtime, so Exec cannot restart an already stopped env.
		release, runErr = s.gate(execCtx, rootID)
		if runErr == nil {
			revoked, runErr = s.store.RootRevoked(execCtx, scope)
			if revoked {
				runErr = errors.Join(runErr, ErrRootRevoked)
			}
			if runErr == nil {
				result.Task, runErr = s.store.Transition(execCtx, scope, taskID, taskstore.Preparing, taskstore.Running)
			}
			release()
		}
	}
	var output string
	if runErr == nil {
		runErr = execCtx.Err()
	}
	if runErr == nil {
		output, runErr = s.backend.Exec(execCtx, env, plan)
	}
	if len(output) > MaxExecutionOutputBytes {
		runErr = errors.Join(runErr, errors.New("execution output exceeds 64 KiB"))
	}
	runErr = errors.Join(runErr, execCtx.Err())

	// Ignore a late successful return from a backend that ignored cancellation.
	// Detached, bounded persistence permits failure recording on caller cancellation.
	repair, repairCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer repairCancel()
	release, err = s.gate(repair, rootID)
	if err != nil {
		return ExecutionResult{Task: result.Task}, errors.Join(runErr, err)
	}
	defer release()
	current, readErr := s.store.Get(repair, scope, taskID)
	revoked, revokeErr := s.store.RootRevoked(repair, scope)
	if readErr != nil || revokeErr != nil || revoked || current.EnvironmentID != env.ID || (current.State != taskstore.Preparing && current.State != taskstore.Running) {
		return ExecutionResult{Task: current}, errors.Join(ErrExecutionDenied, runErr, readErr, revokeErr)
	}
	result.Task = current
	runErr = errors.Join(runErr, execCtx.Err())
	if runErr != nil {
		failed, failErr := s.store.Transition(repair, scope, taskID, current.State, taskstore.Failed)
		if failErr == nil {
			result.Task = failed
		}
		return result, errors.Join(runErr, failErr)
	}
	result.CommandCompleted = true
	result.Output = output
	return result, nil
}
