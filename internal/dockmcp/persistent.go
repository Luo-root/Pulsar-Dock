package dockmcp

import (
	"context"
	"errors"
	"strings"

	"github.com/Luo-root/pulsar-dock/internal/controller"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var ErrArtifactUnsupported = errors.New("artifact.publish unsupported: persistent ArtifactStore unavailable")

// ChildController is trusted host wiring for one fixed caller. It must not be
// selected from Agent input. A mismatched caller fails the scoped readback.
type ChildController interface {
	CreateChild(context.Context, string, controller.Request) (taskstore.Task, error)
}

type TaskReader interface {
	Get(context.Context, taskstore.Scope, string) (taskstore.Task, error)
}

type persistentTasks struct {
	caller       string
	controller   ChildController
	store        TaskReader
	revokedRoots map[taskstore.Scope]bool
}

// NewPersistentService uses only the Controller/taskstore as task facts. The
// legacy NewService remains an independent in-memory prototype. Host must bind
// controller to callerID. Capabilities are ephemeral: reopen requires fresh
// host-authorized grants, never restoration of tokens from the task database.
func NewPersistentService(auth RequestAuthenticator, callerID string, ctl ChildController, store TaskReader) (*Service, error) {
	if auth == nil || strings.TrimSpace(callerID) == "" || ctl == nil || store == nil {
		return nil, errors.New("authenticator, trusted caller, controller and store required")
	}
	// Bind authenticator identity to the fixed Controller caller before any side effect.
	bound := func(ctx context.Context, req *mcp.CallToolRequest) (RequestIdentity, error) {
		id, err := auth(ctx, req)
		if err != nil || id.CallerID != callerID {
			return RequestIdentity{}, ErrUnauthorized
		}
		return id, nil
	}
	return &Service{authenticator: bound, persistent: &persistentTasks{caller: callerID, controller: ctl, store: store, revokedRoots: make(map[taskstore.Scope]bool)}}, nil
}

func (s *Service) persistentRoot(ctx context.Context, grant CapabilityGrant) (taskstore.Task, error) {
	scope := taskstore.Scope{CallerID: grant.CallerID, RootID: grant.RootTaskID}
	if grant.CallerID != s.persistent.caller || s.persistent.revokedRoots[scope] {
		return taskstore.Task{}, ErrUnauthorized
	}
	root, err := s.persistent.store.Get(ctx, scope, grant.RootTaskID)
	if err != nil || root.ID != grant.RootTaskID || root.RootID != grant.RootTaskID || root.CallerID != grant.CallerID || root.ParentID != "" || root.EnvironmentID == "" || root.EnvironmentID != grant.EnvironmentID {
		return taskstore.Task{}, ErrUnauthorized
	}
	// Stopped, expired, failed and stopping tasks may not keep using a capability.
	switch root.State {
	case taskstore.Preparing, taskstore.Running, taskstore.WaitingApproval, taskstore.Succeeded:
		return root, nil
	default:
		return taskstore.Task{}, ErrUnauthorized
	}
}

func (s *Service) createPersistentChild(ctx context.Context, grant CapabilityGrant, input ChildTaskInput) (*mcp.CallToolResult, ChildTaskOutput, error) {
	if _, err := s.persistentRoot(ctx, grant); err != nil {
		return nil, ChildTaskOutput{}, err
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" {
		return nil, ChildTaskOutput{}, errors.New("idempotency_key required")
	}
	if input.InputArtifact != "" {
		return nil, ChildTaskOutput{}, errors.New("input_artifact unsupported by Controller")
	}
	task, err := s.persistent.controller.CreateChild(ctx, grant.RootTaskID, controller.Request{Goal: input.Goal, IdempotencyKey: input.IdempotencyKey})
	if err != nil {
		return nil, ChildTaskOutput{}, err
	}
	// Return persisted facts, never fabricate a task or an environment. A child
	// has its own environment; it must NOT equal the root capability environment.
	persisted, err := s.persistent.store.Get(ctx, taskstore.Scope{CallerID: grant.CallerID, RootID: grant.RootTaskID}, task.ID)
	if err != nil {
		return nil, ChildTaskOutput{}, err
	}
	if persisted.ID != task.ID || persisted.CallerID != grant.CallerID || persisted.RootID != grant.RootTaskID || persisted.ParentID != grant.RootTaskID || persisted.EnvironmentID == "" || persisted.EnvironmentID == grant.EnvironmentID {
		return nil, ChildTaskOutput{}, ErrUnauthorized
	}
	return nil, ChildTaskOutput{TaskID: persisted.ID, State: string(persisted.State)}, nil
}

func (s *Service) persistentStatus(ctx context.Context, grant CapabilityGrant, id string) (*mcp.CallToolResult, StatusOutput, error) {
	if _, err := s.persistentRoot(ctx, grant); err != nil {
		return nil, StatusOutput{}, err
	}
	task, err := s.persistent.store.Get(ctx, taskstore.Scope{CallerID: grant.CallerID, RootID: grant.RootTaskID}, id)
	if err != nil {
		return nil, StatusOutput{}, ErrUnauthorized
	}
	if task.ID != id || task.CallerID != grant.CallerID || task.RootID != grant.RootTaskID || (id != grant.RootTaskID && task.ParentID != grant.RootTaskID) {
		return nil, StatusOutput{}, ErrUnauthorized
	}
	// Empty child bindings are genuine records (e.g. creation failure), not proof
	// of a running Agent. Their stored state is returned without invented env IDs.
	return nil, StatusOutput{TaskID: task.ID, State: string(task.State), Note: task.Goal}, nil
}
