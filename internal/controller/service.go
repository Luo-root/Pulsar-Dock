// Package controller implements the trusted, single-writer task creation service.
// It is not an Agent-facing administration API. One Service must own all creation
// for a database; cross-process leases and orphan reconciliation are not provided.
package controller

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

var ErrRecoveryRequired = errors.New("controller recovery required")
var ErrStopUnsupported = errors.New("controller stop is not implemented: no revocation or tree cleanup")

// Policy is supplied only by trusted configuration, never by task input.
// Gateway and TTL are currently unsupported and must be empty/zero. Creation
// denies all network access; no authenticated gateway or expiry is provided.
type Policy struct {
	Agent, TemplateRef, Memory, GatewayURL string
	CPUs                                   int
	ExpiresAfter                           int64
}

// Request deliberately has no caller, scope, paths, runtime spec or policy fields.
type Request struct{ Goal, IdempotencyKey string }

type taskStore interface {
	CreateRoot(context.Context, taskstore.CreateRequest) (taskstore.Task, error)
	CreateChild(context.Context, taskstore.CreateRequest) (taskstore.Task, error)
	Get(context.Context, taskstore.Scope, string) (taskstore.Task, error)
	BindEnvironment(context.Context, taskstore.Scope, string, taskstore.State, string) (taskstore.Task, error)
	Transition(context.Context, taskstore.Scope, string, taskstore.State, taskstore.State) (taskstore.Task, error)
	ListRootsForRecovery(context.Context, string, string, int) (taskstore.RootPage, error)
	ListTasksForRecovery(context.Context, taskstore.Scope, string, int) (taskstore.TaskPage, error)
}

type Service struct {
	mu      sync.Mutex
	store   taskStore
	backend runtime.RuntimeBackend
	caller  string
	spec    runtime.EnvironmentSpec
}

func New(store *taskstore.Store, backend runtime.RuntimeBackend, caller string, policy Policy) (*Service, error) {
	if store == nil || backend == nil || strings.TrimSpace(caller) == "" {
		return nil, errors.New("store, backend and trusted caller required")
	}
	if policy.GatewayURL != "" || policy.ExpiresAfter != 0 {
		return nil, errors.New("gateway and TTL are unsupported; must be empty and zero")
	}
	if strings.TrimSpace(policy.Agent) == "" || !regexp.MustCompile(`^.+@sha256:[0-9a-fA-F]{64}$`).MatchString(policy.TemplateRef) || strings.TrimSpace(policy.Memory) == "" || policy.CPUs < 1 {
		return nil, errors.New("fixed agent, digest-pinned template and positive resource limits required")
	}
	return &Service{store: store, backend: backend, caller: caller, spec: runtime.EnvironmentSpec{Agent: policy.Agent, TemplateRef: policy.TemplateRef, CPUs: policy.CPUs, Memory: policy.Memory, Workspace: runtime.WorkspaceNone, Skills: runtime.SkillsOff}}, nil
}

func (s *Service) CreateRoot(ctx context.Context, r Request) (taskstore.Task, error) {
	return s.create(ctx, "", r)
}

// CreateChild accepts only a root owned by the fixed trusted caller. This API is
// for the trusted control plane; a future gateway must bind parentID to the
// authenticated environment, not forward arbitrary Agent-supplied parent IDs.
func (s *Service) CreateChild(ctx context.Context, parentID string, r Request) (taskstore.Task, error) {
	if strings.TrimSpace(parentID) == "" {
		return taskstore.Task{}, errors.New("parent required")
	}
	return s.create(ctx, parentID, r)
}

func (s *Service) create(ctx context.Context, parent string, r Request) (taskstore.Task, error) {
	if strings.TrimSpace(r.Goal) == "" || strings.TrimSpace(r.IdempotencyKey) == "" {
		return taskstore.Task{}, errors.New("goal and idempotency key required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := taskstore.Scope{CallerID: s.caller, RootID: parent}
	// Capture existing IDs before insertion: Store intentionally returns no
	// inserted flag. Never retry Create for a persisted, unbound task after restart.
	existing := map[string]bool{}
	cursor := ""
	for {
		if parent == "" {
			page, err := s.store.ListRootsForRecovery(ctx, s.caller, cursor, 500)
			if err != nil {
				return taskstore.Task{}, err
			}
			for _, t := range page.Tasks {
				existing[t.ID] = true
			}
			cursor = page.NextCursor
		} else {
			p, err := s.store.Get(ctx, scope, parent)
			if err != nil {
				return taskstore.Task{}, err
			}
			if p.ParentID != "" {
				return taskstore.Task{}, errors.New("parent must be an owned root")
			}
			page, err := s.store.ListTasksForRecovery(ctx, scope, cursor, 500)
			if err != nil {
				return taskstore.Task{}, err
			}
			for _, t := range page.Tasks {
				existing[t.ID] = true
			}
			cursor = page.NextCursor
		}
		if cursor == "" {
			break
		}
	}
	req := taskstore.CreateRequest{Scope: scope, ParentID: parent, Goal: r.Goal, IdempotencyKey: r.IdempotencyKey}
	var t taskstore.Task
	var err error
	if parent == "" {
		t, err = s.store.CreateRoot(ctx, req)
	} else {
		t, err = s.store.CreateChild(ctx, req)
	}
	if err != nil {
		return t, err
	}
	scope.RootID = t.RootID
	if existing[t.ID] {
		if t.State == taskstore.Created {
			return t, ErrRecoveryRequired
		}
		return t, nil
	}
	if parent != "" {
		// Store resolves idempotency before checking parent state. Only a newly
		// inserted child needs the stricter Controller admission check.
		p, readErr := s.store.Get(ctx, scope, parent)
		if readErr != nil || (p.State != taskstore.Preparing && p.State != taskstore.Running) {
			repair, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			failed, failErr := s.store.Transition(repair, scope, t.ID, taskstore.Created, taskstore.Failed)
			if failErr == nil {
				t = failed
			}
			return t, errors.Join(errors.New("parent must be preparing or running for new delegation"), readErr, failErr)
		}
	}
	spec := s.spec
	spec.TaskID = t.ID
	spec.NetworkRules = append([]runtime.NetworkRule(nil), s.spec.NetworkRules...)
	env, createErr := s.backend.Create(ctx, spec)
	// Persistence and cleanup must not inherit a cancelled request context.
	repair, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if createErr != nil || strings.TrimSpace(env.ID) == "" {
		if createErr == nil {
			createErr = errors.New("backend returned empty environment ID")
		}
		failed, failErr := s.store.Transition(repair, scope, t.ID, taskstore.Created, taskstore.Failed)
		if failErr == nil {
			t = failed
		}
		// A nonempty result accompanying an error is still an owned candidate.
		cleanupErr := s.discardCandidate(repair, scope, t.ID, env)
		return t, errors.Join(createErr, failErr, cleanupErr)
	}
	bound, err := s.store.BindEnvironment(ctx, scope, t.ID, taskstore.Created, env.ID)
	if err != nil {
		cleanupErr := s.discardCandidate(repair, scope, t.ID, env)
		persisted, readErr := s.store.Get(repair, scope, t.ID)
		if readErr == nil {
			t = persisted
		}
		return t, errors.Join(err, cleanupErr, readErr)
	}
	t = bound
	prepared, err := s.store.Transition(ctx, scope, t.ID, taskstore.Created, taskstore.Preparing)
	if err != nil {
		// The environment is adopted. Never delete it on a state/commit error;
		// persisted ID remains available to trusted recovery even if state is Created.
		persisted, readErr := s.store.Get(repair, scope, t.ID)
		if readErr == nil {
			t = persisted
		}
		return t, errors.Join(err, readErr, ErrRecoveryRequired)
	}
	return prepared, nil
}

func (s *Service) discardCandidate(ctx context.Context, scope taskstore.Scope, id string, env runtime.Environment) error {
	if env.ID == "" {
		return nil
	}
	t, err := s.store.Get(ctx, scope, id)
	if err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}
	if t.EnvironmentID == env.ID {
		return ErrRecoveryRequired
	}
	if t.EnvironmentID == "" && t.State == taskstore.Created {
		// Seal the empty binding before removal so it cannot subsequently be adopted.
		if _, err = s.store.Transition(ctx, scope, id, taskstore.Created, taskstore.Failed); err != nil {
			return errors.Join(ErrRecoveryRequired, err)
		}
	}
	if err = s.backend.Remove(ctx, env); err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}
	return nil
}

// StopTask deliberately performs no state transition: marking Stopped without
// gateway revocation and descendant/process-tree cleanup would be misleading.
func (s *Service) StopTask(context.Context, string) error { return ErrStopUnsupported }
