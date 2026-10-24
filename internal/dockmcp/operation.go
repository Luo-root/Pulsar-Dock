package dockmcp

import (
	"context"

	"github.com/Luo-root/pulsar-dock/internal/taskstore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// operationLease tracks authorization, not task facts. Controller owns all
// candidate cleanup and adoption. Cancellation never authorizes gateway Remove:
// an adopted environment must remain recorded for trusted recovery/stop.
type operationLease struct {
	scope  taskstore.Scope
	cancel context.CancelFunc
}

func (s *Service) beginOperation(ctx context.Context, id RequestIdentity, action string) (context.Context, CapabilityGrant, *operationLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, err := s.authorizeLocked(id, action, "")
	if err != nil {
		return nil, CapabilityGrant{}, nil, err
	}
	opctx, cancel := context.WithDeadline(ctx, grant.ExpiresAt)
	op := &operationLease{scope: taskstore.Scope{CallerID: grant.CallerID, RootID: grant.RootTaskID}, cancel: cancel}
	s.persistent.operations[op] = struct{}{}
	return opctx, grant, op, nil
}

func (s *Service) checkOperation(ctx context.Context, id RequestIdentity, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.authorizeLocked(id, action, ""); err != nil {
		return err
	}
	return ctx.Err()
}

// finishOperation is the response authorization linearization point, serialized
// with revoke/rotate. No Store or Controller I/O runs while this lock is held.
func (s *Service) finishOperation(ctx context.Context, id RequestIdentity, action string, op *operationLease, err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.persistent.operations, op)
	defer op.cancel()
	if _, authErr := s.authorizeLocked(id, action, ""); authErr != nil {
		return authErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func (s *Service) runPersistentCreate(ctx context.Context, id RequestIdentity, input ChildTaskInput) (*mcp.CallToolResult, ChildTaskOutput, error) {
	opctx, grant, op, err := s.beginOperation(ctx, id, ActionCreateChild)
	if err != nil {
		return nil, ChildTaskOutput{}, err
	}
	// Revalidate durable environment before admission, then capability immediately
	// before Controller side effects. Revocation racing admission cancels opctx.
	_, err = s.persistentRoot(opctx, grant)
	var out ChildTaskOutput
	if err == nil {
		err = s.checkOperation(opctx, id, ActionCreateChild)
	}
	if err == nil {
		_, out, err = s.createPersistentChild(opctx, grant, input)
	}
	if err == nil {
		_, err = s.persistentRoot(opctx, grant)
	}
	if err == nil {
		err = opctx.Err()
	}
	err = s.finishOperation(opctx, id, ActionCreateChild, op, err)
	if err != nil {
		return nil, ChildTaskOutput{}, err
	}
	return nil, out, nil
}

func (s *Service) runPersistentStatus(ctx context.Context, id RequestIdentity, input StatusInput) (*mcp.CallToolResult, StatusOutput, error) {
	opctx, grant, op, err := s.beginOperation(ctx, id, ActionStatus)
	if err != nil {
		return nil, StatusOutput{}, err
	}
	_, out, err := s.persistentStatus(opctx, grant, input.TaskID)
	if err == nil {
		_, err = s.persistentRoot(opctx, grant)
	}
	if err == nil {
		err = opctx.Err()
	}
	err = s.finishOperation(opctx, id, ActionStatus, op, err)
	if err != nil {
		return nil, StatusOutput{}, err
	}
	return nil, out, nil
}

func (s *Service) runPersistentPublish(ctx context.Context, id RequestIdentity) (*mcp.CallToolResult, PublishOutput, error) {
	opctx, grant, op, err := s.beginOperation(ctx, id, ActionPublish)
	if err != nil {
		return nil, PublishOutput{}, err
	}
	_, err = s.persistentRoot(opctx, grant)
	if err == nil {
		err = ErrArtifactUnsupported
	}
	return nil, PublishOutput{}, s.finishOperation(opctx, id, ActionPublish, op, err)
}
