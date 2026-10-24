package dockmcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ChildTaskInput struct {
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"retry key scoped to the authenticated caller and parent"`
	Goal           string `json:"goal" jsonschema:"the bounded goal for the child Agent"`
	InputArtifact  string `json:"input_artifact,omitempty" jsonschema:"an immutable artifact reference"`
}

type ChildTaskOutput struct {
	TaskID string `json:"task_id"`
	State  string `json:"state"`
}

type StatusInput struct {
	TaskID string `json:"task_id" jsonschema:"the task identifier"`
}

type StatusOutput struct {
	TaskID string `json:"task_id"`
	State  string `json:"state"`
	Note   string `json:"note,omitempty"`
}

type PublishInput struct {
	TaskID string `json:"task_id" jsonschema:"the task publishing the artifact"`
	Kind   string `json:"kind" jsonschema:"artifact kind such as review or patch"`
	Digest string `json:"digest" jsonschema:"digest of an immutable artifact snapshot"`
}

type PublishOutput struct {
	ArtifactID string `json:"artifact_id"`
	State      string `json:"state"`
}

type taskRecord struct {
	ID        string
	ParentID  string
	Goal      string
	State     string
	CreatedAt time.Time
}

type Service struct {
	mu            sync.Mutex
	sequence      uint64
	tasks         map[string]taskRecord
	artifacts     map[string]PublishInput // In-memory references only; no artifact files are persisted.
	authenticator RequestAuthenticator
	capabilities  []capability
	generation    uint64
	childRequests map[childRequestKey]childRequest
}

// NewService fails closed without a trusted host authenticator. The optional
// argument preserves construction compatibility, not unauthenticated access.
func NewService(authenticator ...RequestAuthenticator) *Service {
	s := &Service{
		tasks:         make(map[string]taskRecord),
		artifacts:     make(map[string]PublishInput),
		childRequests: make(map[childRequestKey]childRequest),
	}
	if len(authenticator) == 1 {
		s.authenticator = authenticator[0]
	}
	return s
}

type childRequestKey struct{ CallerID, RootTaskID, EnvironmentID, Key string }
type childRequest struct {
	Input  ChildTaskInput
	Output ChildTaskOutput
}

func (s *Service) CreateServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "pulsar-dock-gateway", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "task.create_child",
		Description: "Request a bounded child Agent task through Pulsar Dock. The Dock policy service decides whether it may run.",
	}, s.createChild)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "task.status",
		Description: "Read the state of a task visible to the current Agent environment.",
	}, s.status)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "artifact.publish",
		Description: "Record an artifact reference in memory for an existing task. This prototype does not persist artifact files or implement an ArtifactStore, and never applies changes to the host project.",
	}, s.publish)
	return server
}

func (s *Service) createChild(ctx context.Context, req *mcp.CallToolRequest, input ChildTaskInput) (*mcp.CallToolResult, ChildTaskOutput, error) {
	identity, err := s.authenticate(ctx, req)
	if err != nil {
		return nil, ChildTaskOutput{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, err := s.authorizeLocked(identity, ActionCreateChild, "")
	if err != nil {
		return nil, ChildTaskOutput{}, err
	}
	if input.Goal == "" {
		return nil, ChildTaskOutput{}, fmt.Errorf("goal is required")
	}
	key := childRequestKey{grant.CallerID, grant.RootTaskID, grant.EnvironmentID, input.IdempotencyKey}
	if input.IdempotencyKey != "" {
		if previous, ok := s.childRequests[key]; ok {
			if previous.Input != input {
				return nil, ChildTaskOutput{}, fmt.Errorf("idempotency conflict")
			}
			return nil, previous.Output, nil
		}
	}
	var id string
	for {
		s.sequence++
		id = fmt.Sprintf("task-%04d", s.sequence)
		if _, exists := s.tasks[id]; !exists {
			break
		}
	}
	s.tasks[id] = taskRecord{ID: id, ParentID: grant.RootTaskID, Goal: input.Goal, State: "created", CreatedAt: time.Now().UTC()}
	output := ChildTaskOutput{TaskID: id, State: "created"}
	if input.IdempotencyKey != "" {
		s.childRequests[key] = childRequest{input, output}
	}
	return nil, output, nil
}

func (s *Service) status(ctx context.Context, req *mcp.CallToolRequest, input StatusInput) (*mcp.CallToolResult, StatusOutput, error) {
	identity, err := s.authenticate(ctx, req)
	if err != nil {
		return nil, StatusOutput{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.authorizeLocked(identity, ActionStatus, input.TaskID); err != nil {
		return nil, StatusOutput{}, err
	}
	task, ok := s.tasks[input.TaskID]
	if !ok {
		return nil, StatusOutput{}, fmt.Errorf("task %q is not visible", input.TaskID)
	}
	return nil, StatusOutput{TaskID: task.ID, State: task.State, Note: task.Goal}, nil
}

func (s *Service) publish(ctx context.Context, req *mcp.CallToolRequest, input PublishInput) (*mcp.CallToolResult, PublishOutput, error) {
	identity, err := s.authenticate(ctx, req)
	if err != nil {
		return nil, PublishOutput{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.authorizeLocked(identity, ActionPublish, input.TaskID); err != nil {
		return nil, PublishOutput{}, err
	}
	if input.TaskID == "" || input.Kind == "" || input.Digest == "" {
		return nil, PublishOutput{}, fmt.Errorf("task_id, kind, and digest are required")
	}
	if _, ok := s.tasks[input.TaskID]; !ok {
		return nil, PublishOutput{}, fmt.Errorf("task %q is not visible", input.TaskID)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, PublishOutput{}, fmt.Errorf("serialize artifact reference: %w", err)
	}
	id := fmt.Sprintf("artifact-%x", sha256.Sum256(payload))
	s.artifacts[id] = input
	return nil, PublishOutput{ArtifactID: id, State: "published"}, nil
}
