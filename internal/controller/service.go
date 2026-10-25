// Package controller implements the trusted, single-writer task creation service.
// It is not an Agent-facing administration API. One Service must own all creation
// for a database; cross-process leases and orphan reconciliation are not provided.
package controller

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

var ErrRecoveryRequired = errors.New("controller recovery required")
var ErrRootRevoked = errors.New("root delegation revoked")

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
	RevokeRoot(context.Context, taskstore.Scope) error
	RootRevoked(context.Context, taskstore.Scope) (bool, error)
}

type Service struct {
	mu      sync.Mutex
	store   taskStore
	backend runtime.RuntimeBackend
	caller  string
	spec    runtime.EnvironmentSpec
	// Handles are runtime cleanup capabilities, never persisted task facts.
	handles map[string]*ownedEnvironment
	creates map[string]*creation
	stops   map[string]chan struct{}
	gates   map[string]chan struct{}
}

// gate serializes only one root's database admission/adoption. Waiting is
// cancellable; the global mutex protects maps only, never store/backend IO.
func (s *Service) gate(ctx context.Context, key string) (func(), error) {
	s.mu.Lock()
	if s.gates == nil {
		s.gates = make(map[string]chan struct{})
	}
	g := s.gates[key]
	if g == nil {
		g = make(chan struct{}, 1)
		s.gates[key] = g
	}
	s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case g <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-g
			return nil, err
		}
		return func() { <-g }, nil
	}
}

type ownedEnvironment struct {
	env runtime.Environment
	// neverInvoked is an admission rejection; removed requires successful Remove.
	// Neither set, with no ID, means an unknown Create outcome.
	neverInvoked bool
	removed      bool
}

type creation struct {
	root   string
	cancel context.CancelFunc
	done   chan struct{}
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
	key := parent
	if key == "" {
		key = "root-creation:" + r.IdempotencyKey
	}
	release, lockErr := s.gate(ctx, key)
	if lockErr != nil {
		return taskstore.Task{}, lockErr
	}
	unlock := func() {
		if release != nil {
			release()
			release = nil
		}
	}
	defer unlock()
	var op *creation
	var taskID string
	defer func() {
		if op != nil {
			op.cancel()
			s.mu.Lock()
			delete(s.creates, taskID)
			close(op.done)
			s.mu.Unlock()
		}
	}()
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
	if parent == "" {
		rootRelease, err := s.gate(ctx, t.RootID)
		if err != nil {
			return t, err
		}
		requestRelease := release
		release = func() { rootRelease(); requestRelease() }
		// Adoption may have completed while this replay waited for the root gate.
		if existing[t.ID] {
			t, err = s.store.Get(ctx, scope, t.ID)
			if err != nil {
				return t, err
			}
		}
	}
	if existing[t.ID] {
		s.mu.Lock()
		pending := s.creates[t.ID]
		s.mu.Unlock()
		if pending != nil {
			unlock()
			select {
			case <-ctx.Done():
				return t, ctx.Err()
			case <-pending.done:
			}
			persisted, readErr := s.store.Get(ctx, scope, t.ID)
			if readErr == nil && persisted.State == taskstore.Created {
				readErr = ErrRecoveryRequired
			}
			return persisted, readErr
		}
		if t.State == taskstore.Created {
			return t, ErrRecoveryRequired
		}
		return t, nil
	}
	if parent != "" {
		// Store resolves idempotency before checking parent state. Only a newly
		// inserted child needs the stricter Controller admission check.
		p, readErr := s.store.Get(ctx, scope, parent)
		revoked, revokeErr := s.store.RootRevoked(ctx, scope)
		if readErr != nil || revokeErr != nil || revoked || (p.State != taskstore.Preparing && p.State != taskstore.Running) {
			repair, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			failed, failErr := s.store.Transition(repair, scope, t.ID, taskstore.Created, taskstore.Failed)
			if failErr == nil {
				t = failed
			}
			s.mu.Lock()
			if s.handles == nil {
				s.handles = make(map[string]*ownedEnvironment)
			}
			s.handles[t.ID] = &ownedEnvironment{neverInvoked: true}
			s.mu.Unlock()
			var denied error
			if revoked {
				denied = ErrRootRevoked
			}
			return t, errors.Join(errors.New("parent must be preparing or running for new delegation"), denied, readErr, revokeErr, failErr)
		}
	}
	if revoked, err := s.store.RootRevoked(ctx, scope); err != nil || revoked {
		return t, errors.Join(err, ErrRootRevoked)
	}
	createCtx, createCancel := context.WithCancel(ctx)
	op = &creation{root: t.RootID, cancel: createCancel, done: make(chan struct{})}
	taskID = t.ID
	s.mu.Lock()
	if s.creates == nil {
		s.creates = make(map[string]*creation)
	}
	s.creates[t.ID] = op
	s.mu.Unlock()
	unlock()
	spec := s.spec
	spec.TaskID = t.ID
	spec.NetworkRules = append([]runtime.NetworkRule(nil), s.spec.NetworkRules...)
	env, createErr := s.backend.Create(createCtx, spec)
	s.mu.Lock()
	if s.handles == nil {
		s.handles = make(map[string]*ownedEnvironment)
	}
	s.handles[t.ID] = &ownedEnvironment{env: env}
	s.mu.Unlock()
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
	// Serialize adoption with durable stop admission, not with backend IO.
	release, lockErr = s.gate(createCtx, t.RootID)
	var revoked bool
	var revokeErr error
	if lockErr == nil {
		revoked, revokeErr = s.store.RootRevoked(repair, scope)
	}
	if lockErr != nil || createCtx.Err() != nil || revoked || revokeErr != nil {
		unlock()
		cleanupErr := s.discardCandidate(repair, scope, t.ID, env)
		persisted, readErr := s.store.Get(repair, scope, t.ID)
		if readErr == nil {
			t = persisted
		}
		return t, errors.Join(createCtx.Err(), revokeErr, ErrRootRevoked, cleanupErr, readErr)
	}
	bound, err := s.store.BindEnvironment(ctx, scope, t.ID, taskstore.Created, env.ID)
	if err != nil {
		unlock()
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
	if strings.TrimSpace(env.ID) == "" {
		// Create was invoked: missing identity is an unknown runtime outcome,
		// not evidence that no environment exists.
		return ErrRecoveryRequired
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
	s.mu.Lock()
	if h := s.handles[id]; h != nil && h.env == env {
		h.removed = true
	}
	s.mu.Unlock()
	return nil
}

// StopTask is a trusted root-only operation. Revocation survives cancellation,
// partial cleanup and restart. Only backend-returned handles authorize cleanup.
func (s *Service) StopTask(ctx context.Context, rootID string) error {
	scope := taskstore.Scope{CallerID: s.caller, RootID: rootID}
	for {
		release, err := s.gate(ctx, rootID)
		if err != nil {
			return err
		}
		if err := s.store.RevokeRoot(ctx, scope); err != nil {
			release()
			return err
		}
		s.mu.Lock()
		if done := s.stops[rootID]; done != nil {
			s.mu.Unlock()
			release()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-done:
			}
			continue
		}
		if s.stops == nil {
			s.stops = make(map[string]chan struct{})
		}
		done := make(chan struct{})
		s.stops[rootID] = done
		var pending []chan struct{}
		for _, op := range s.creates {
			if op.root == rootID {
				op.cancel()
				pending = append(pending, op.done)
			}
		}
		s.mu.Unlock()
		release()
		defer func() {
			s.mu.Lock()
			delete(s.stops, rootID)
			close(done)
			s.mu.Unlock()
		}()
		for _, wait := range pending {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wait:
			}
		}
		return s.stopTree(ctx, scope)
	}
}

func (s *Service) stopTree(ctx context.Context, scope taskstore.Scope) error {
	var result error
	cursor := ""
	for {
		page, err := s.store.ListTasksForRecovery(ctx, scope, cursor, 500)
		if err != nil {
			return errors.Join(result, err)
		}
		for _, t := range page.Tasks {
			if err := s.stopEnvironment(ctx, scope, t); err != nil {
				result = errors.Join(result, fmt.Errorf("task %s: %w", t.ID, err))
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			return result
		}
	}
}

func (s *Service) stopEnvironment(ctx context.Context, scope taskstore.Scope, t taskstore.Task) error {
	s.mu.Lock()
	h := s.handles[t.ID]
	s.mu.Unlock()
	// Even an empty persisted binding may hide a create interrupted by restart.
	if h == nil || (!h.neverInvoked && !h.removed && strings.TrimSpace(h.env.ID) == "") {
		return ErrRecoveryRequired
	}
	if !h.removed && !h.neverInvoked {
		stopErr := s.backend.Stop(ctx, h.env)
		removeErr := s.backend.Remove(ctx, h.env)
		if removeErr == nil {
			s.mu.Lock()
			h.removed = true
			s.mu.Unlock()
		}
		if stopErr != nil || removeErr != nil {
			return errors.Join(stopErr, removeErr)
		}
	}
	if t.EnvironmentID != "" && t.EnvironmentID != h.env.ID {
		// Reclaim our candidate, but never claim the unknown persisted environment.
		return ErrRecoveryRequired
	}
	if !taskstore.AllowedTransition(t.State, taskstore.Stopping) && t.State != taskstore.Stopping {
		return nil // terminal history is immutable
	}
	if t.State != taskstore.Stopping {
		var err error
		t, err = s.store.Transition(ctx, scope, t.ID, t.State, taskstore.Stopping)
		if err != nil {
			return err
		}
	}
	_, err := s.store.Transition(ctx, scope, t.ID, taskstore.Stopping, taskstore.Stopped)
	return err
}
