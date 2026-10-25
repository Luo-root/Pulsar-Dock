// Package desktopapi exposes a small, transport-neutral API for trusted desktop UI code.
// It is not an HTTP, native-window, or Agent-facing API. The constructor binds all
// calls to a controller Service and a host-selected caller/agent.
package desktopapi

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Luo-root/pulsar-dock/internal/agentadapter"
	"github.com/Luo-root/pulsar-dock/internal/controller"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

const (
	MaxGoalBytes           = agentadapter.MaxGoalBytes
	MaxIdempotencyKeyBytes = 256
	MaxIdentifierBytes     = 256
)

type Operation string

const (
	OperationCreate  Operation = "create"
	OperationExecute Operation = "execute"
)

type AgentConfig struct {
	ID            string
	DisplayName   string
	Installed     bool
	CLIVerified   bool
	ModelVerified bool
	Operations    []Operation
}

type AgentInfo struct {
	ID            string      `json:"id"`
	DisplayName   string      `json:"displayName"`
	Installed     bool        `json:"installed"`
	CLIVerified   bool        `json:"cliVerified"`
	ModelVerified bool        `json:"modelVerified"`
	Operations    []Operation `json:"operations"`
}

type CreateTaskRequest struct {
	AgentID        string `json:"agentId,omitempty"`
	Goal           string `json:"goal"`
	IdempotencyKey string `json:"idempotencyKey"`
	ParentTaskID   string `json:"parentTaskId,omitempty"`
}

type Task struct {
	ID        string    `json:"id"`
	ParentID  string    `json:"parentId,omitempty"`
	RootID    string    `json:"rootId"`
	Goal      string    `json:"goal"`
	State     string    `json:"state"`
	CreatedAt timeValue `json:"createdAt"`
	UpdatedAt timeValue `json:"updatedAt"`
}

// timeValue is a JSON-safe RFC3339 timestamp without exposing store internals.
type timeValue string

type ExecuteResult struct {
	Task             Task   `json:"task"`
	CommandCompleted bool   `json:"commandCompleted"`
	Output           string `json:"output,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

var (
	ErrInvalid      = &Error{Code: "invalid_request", Message: "request is invalid"}
	ErrUnauthorized = &Error{Code: "not_found", Message: "task is not available"}
	ErrConflict     = &Error{Code: "conflict", Message: "task state conflicts with this operation"}
	ErrUnsupported  = &Error{Code: "unsupported", Message: "operation is not supported"}
	ErrCanceled     = &Error{Code: "canceled", Message: "operation was canceled"}
	ErrUnavailable  = &Error{Code: "unavailable", Message: "operation could not be completed"}
)

type Facade struct {
	service   *controller.Service
	store     *taskstore.Store
	caller    string
	agentID   string
	catalogue []AgentInfo
}

// New is a trusted host constructor. The caller must not be derived from UI or
// Agent input; the service itself also retains its own fixed caller scope.
func New(service *controller.Service, store *taskstore.Store, caller, agentID string, catalogue []AgentConfig) (*Facade, error) {
	if service == nil || store == nil || !validID(caller) || !validID(agentID) {
		return nil, ErrInvalid
	}
	infos := make([]AgentInfo, 0, len(catalogue))
	seen := make(map[string]bool, len(catalogue))
	for _, c := range catalogue {
		if !validID(c.ID) || strings.TrimSpace(c.DisplayName) == "" || seen[c.ID] {
			return nil, ErrInvalid
		}
		seen[c.ID] = true
		ops := append([]Operation(nil), c.Operations...)
		for _, op := range ops {
			if op != OperationCreate && op != OperationExecute {
				return nil, ErrInvalid
			}
		}
		infos = append(infos, AgentInfo{ID: c.ID, DisplayName: c.DisplayName, Installed: c.Installed, CLIVerified: c.CLIVerified, ModelVerified: c.ModelVerified, Operations: ops})
	}
	return &Facade{service: service, store: store, caller: caller, agentID: agentID, catalogue: infos}, nil
}

func (f *Facade) Agents(ctx context.Context) ([]AgentInfo, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	out := make([]AgentInfo, len(f.catalogue))
	copy(out, f.catalogue)
	for i := range out {
		out[i].Operations = append([]Operation(nil), out[i].Operations...)
	}
	return out, nil
}

func (f *Facade) CreateTask(ctx context.Context, req CreateTaskRequest) (Task, error) {
	if err := contextErr(ctx); err != nil {
		return Task{}, err
	}
	if !validGoal(req.Goal) || !validBound(req.IdempotencyKey, MaxIdempotencyKeyBytes) || (req.ParentTaskID != "" && !validID(req.ParentTaskID)) {
		return Task{}, ErrInvalid
	}
	if req.AgentID != "" && req.AgentID != f.agentID {
		return Task{}, ErrUnsupported
	}
	if !f.agentSupports(OperationCreate) {
		return Task{}, ErrUnsupported
	}
	var t taskstore.Task
	var err error
	cr := controller.Request{Goal: req.Goal, IdempotencyKey: req.IdempotencyKey}
	if req.ParentTaskID == "" {
		t, err = f.service.CreateRoot(ctx, cr)
	} else {
		t, err = f.service.CreateChild(ctx, req.ParentTaskID, cr)
	}
	if err != nil {
		return Task{}, mapError(err)
	}
	return taskDTO(t), nil
}

func (f *Facade) GetTask(ctx context.Context, rootID, taskID string) (Task, error) {
	if err := contextErr(ctx); err != nil {
		return Task{}, err
	}
	if !validID(rootID) || !validID(taskID) {
		return Task{}, ErrInvalid
	}
	t, err := f.store.Get(ctx, taskstore.Scope{CallerID: f.caller, RootID: rootID}, taskID)
	if err != nil {
		return Task{}, mapError(err)
	}
	return taskDTO(t), nil
}

func (f *Facade) ExecuteTask(ctx context.Context, rootID, taskID string) (ExecuteResult, error) {
	if err := contextErr(ctx); err != nil {
		return ExecuteResult{}, err
	}
	if !validID(rootID) || !validID(taskID) {
		return ExecuteResult{}, ErrInvalid
	}
	if !f.agentSupports(OperationExecute) {
		return ExecuteResult{}, ErrUnsupported
	}
	r, err := f.service.ExecuteTask(ctx, rootID, taskID)
	if err != nil {
		return ExecuteResult{Task: taskDTO(r.Task)}, mapError(err)
	}
	return ExecuteResult{Task: taskDTO(r.Task), CommandCompleted: r.CommandCompleted, Output: r.Output}, nil
}

func (f *Facade) StopTask(ctx context.Context, rootID string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if !validID(rootID) {
		return ErrInvalid
	}
	return mapError(f.service.StopTask(ctx, rootID))
}

// StartStreaming, RequestPermission, and ApplyArtifact are intentionally not
// implemented. They exist as explicit probes so a transport adapter cannot
// accidentally turn an absent capability into a successful no-op.
func (f *Facade) StartStreaming(context.Context, string, string) error    { return ErrUnsupported }
func (f *Facade) RequestPermission(context.Context, string, string) error { return ErrUnsupported }
func (f *Facade) ApplyArtifact(context.Context, string, string) error     { return ErrUnsupported }

func (f *Facade) agentSupports(op Operation) bool {
	for _, a := range f.catalogue {
		if a.ID == f.agentID {
			for _, x := range a.Operations {
				if x == op && a.Installed && a.CLIVerified && a.ModelVerified {
					return true
				}
			}
			return false
		}
	}
	return false
}

func validID(s string) bool {
	return validBound(s, MaxIdentifierBytes) && strings.TrimSpace(s) == s && s != ""
}
func validBound(s string, max int) bool {
	return utf8.ValidString(s) && len(s) > 0 && len(s) <= max && !strings.ContainsAny(s, "\x00\r\n")
}
func validGoal(s string) bool {
	return validBound(s, MaxGoalBytes) && strings.TrimSpace(s) != "" && strings.TrimSpace(s) == s
}
func contextErr(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return mapError(err)
	}
	return nil
}

func taskDTO(t taskstore.Task) Task {
	return Task{ID: t.ID, ParentID: t.ParentID, RootID: t.RootID, Goal: t.Goal, State: string(t.State), CreatedAt: timeValue(t.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")), UpdatedAt: timeValue(t.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"))}
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrCanceled
	}
	if errors.Is(err, controller.ErrRootRevoked) || errors.Is(err, controller.ErrExecutionDenied) || errors.Is(err, controller.ErrRecoveryRequired) {
		return ErrUnavailable
	}
	var e *taskstore.Error
	if errors.As(err, &e) {
		switch e.Kind {
		case taskstore.NotFound:
			return ErrUnauthorized
		case taskstore.Conflict:
			return ErrConflict
		case taskstore.Invalid:
			return ErrInvalid
		}
	}
	return ErrUnavailable
}
