package dockmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ChildTaskInput struct {
	Goal          string `json:"goal" jsonschema:"the bounded goal for the child Agent"`
	InputArtifact string `json:"input_artifact,omitempty" jsonschema:"an immutable artifact reference"`
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
	mu       sync.Mutex
	sequence uint64
	tasks    map[string]taskRecord
}

func NewService() *Service {
	return &Service{tasks: make(map[string]taskRecord)}
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
		Description: "Publish an immutable artifact reference for later review. This never applies changes to the host project.",
	}, s.publish)
	return server
}

func (s *Service) createChild(_ context.Context, _ *mcp.CallToolRequest, input ChildTaskInput) (*mcp.CallToolResult, ChildTaskOutput, error) {
	if input.Goal == "" {
		return nil, ChildTaskOutput{}, fmt.Errorf("goal is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	id := fmt.Sprintf("task-%04d", s.sequence)
	s.tasks[id] = taskRecord{ID: id, Goal: input.Goal, State: "created", CreatedAt: time.Now().UTC()}
	return nil, ChildTaskOutput{TaskID: id, State: "created"}, nil
}

func (s *Service) status(_ context.Context, _ *mcp.CallToolRequest, input StatusInput) (*mcp.CallToolResult, StatusOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[input.TaskID]
	if !ok {
		return nil, StatusOutput{}, fmt.Errorf("task %q is not visible", input.TaskID)
	}
	return nil, StatusOutput{TaskID: task.ID, State: task.State, Note: task.Goal}, nil
}

func (s *Service) publish(_ context.Context, _ *mcp.CallToolRequest, input PublishInput) (*mcp.CallToolResult, PublishOutput, error) {
	if input.TaskID == "" || input.Kind == "" || input.Digest == "" {
		return nil, PublishOutput{}, fmt.Errorf("task_id, kind, and digest are required")
	}
	payload, _ := json.Marshal(input)
	return nil, PublishOutput{ArtifactID: fmt.Sprintf("artifact-%x", payload[:8]), State: "published"}, nil
}
