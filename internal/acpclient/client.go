// Package acpclient defines the transport-neutral seam between the trusted
// Controller and an Agent Client Protocol session. It does not implement ACP
// framing, process launch, credentials, or Controller policy.
package acpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	ErrUnsupported        = errors.New("acp capability or provider is unsupported")
	ErrInvalid            = errors.New("invalid acp request")
	ErrPromptInFlight     = errors.New("a prompt is already in flight")
	ErrSessionClosed      = errors.New("acp session is closed")
	ErrStaleGeneration    = errors.New("acp update belongs to a stale session generation")
	ErrPermissionNotFound = errors.New("permission request is not pending")
)

const ProtocolVersion = "acp/1"

type Capabilities struct {
	Streaming         bool
	Permissions       bool
	FileUpdates       bool
	DiffUpdates       bool
	ToolUpdates       bool
	ConcurrentPrompts bool
}

func (c Capabilities) Supports(u UpdateKind) bool {
	switch u {
	case UpdateText, UpdateReasoning, UpdateStatus, UpdateError:
		return true
	case UpdateToolCall:
		return c.ToolUpdates
	case UpdatePermission:
		return c.Permissions
	case UpdateArtifact:
		return c.FileUpdates || c.DiffUpdates
	case UpdateChildSession:
		return false
	default:
		return false
	}
}

type InitializeRequest struct {
	AgentName    string
	AgentVersion string
	Protocol     string
}
type InitializeResult struct {
	AgentName    string
	AgentVersion string
	Protocol     string
	Capabilities Capabilities
}

type SessionIdentity struct {
	TaskID        string
	EnvironmentID string
}
type NewSessionRequest struct{ Identity SessionIdentity }
type Session struct {
	ID         string
	Identity   SessionIdentity
	Generation uint64
}
type PromptRequest struct{ Text string }
type PermissionResponse struct {
	RequestID string
	Allowed   bool
}

type UpdateKind string

const (
	UpdateText         UpdateKind = "text"
	UpdateReasoning    UpdateKind = "reasoning"
	UpdateToolCall     UpdateKind = "tool_call"
	UpdatePermission   UpdateKind = "permission"
	UpdateArtifact     UpdateKind = "artifact_ref"
	UpdateChildSession UpdateKind = "child_session"
	UpdateStatus       UpdateKind = "status"
	UpdateError        UpdateKind = "error"
)

type Update struct {
	Kind       UpdateKind
	Generation uint64
	RequestID  string
	Text       string
	ToolName   string
	ArtifactID string
	ChildID    string
	Status     string
	Code       string
}

// MarshalJSON intentionally excludes untrusted text and tool details. The
// Controller may retain text in memory for a trusted renderer, but ordinary
// event serialization must not create a credential/logging sink.
func (u Update) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Kind       UpdateKind `json:"kind"`
		Generation uint64     `json:"generation"`
		RequestID  string     `json:"request_id,omitempty"`
		ArtifactID string     `json:"artifact_id,omitempty"`
		ChildID    string     `json:"child_id,omitempty"`
		Status     string     `json:"status,omitempty"`
		Code       string     `json:"code,omitempty"`
	}{u.Kind, u.Generation, u.RequestID, u.ArtifactID, u.ChildID, u.Status, u.Code})
}

// MarshalJSON prevents prompt content from entering generic logs or event
// payloads. Prompt text is an untrusted input and must use protected storage.
func (p PromptRequest) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

type Client interface {
	Initialize(context.Context, InitializeRequest) (InitializeResult, error)
	NewSession(context.Context, NewSessionRequest) (Session, error)
	Prompt(context.Context, Session, PromptRequest) (<-chan Update, error)
	Cancel(context.Context, Session) error
	PermissionResponse(context.Context, Session, PermissionResponse) error
	Close(context.Context) error
}

type ProviderConfig struct {
	Name     string
	Command  string
	Version  string
	Protocol string
	Verified bool
}
type ProviderRegistry struct{ providers map[string]ProviderConfig }

func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{providers: make(map[string]ProviderConfig)}
}
func (r *ProviderRegistry) Register(p ProviderConfig) error {
	if r == nil || p.Name == "" || p.Command == "" || p.Version == "" || p.Protocol != ProtocolVersion || !p.Verified {
		return ErrUnsupported
	}
	if r.providers == nil {
		r.providers = make(map[string]ProviderConfig)
	}
	r.providers[p.Name] = p
	return nil
}
func (r *ProviderRegistry) Lookup(name string) (ProviderConfig, error) {
	if r == nil {
		return ProviderConfig{}, ErrUnsupported
	}
	p, ok := r.providers[name]
	if !ok || !p.Verified || p.Protocol != ProtocolVersion {
		return ProviderConfig{}, ErrUnsupported
	}
	return p, nil
}

func requireSession(s Session) error {
	if s.ID == "" || s.Identity.TaskID == "" || s.Identity.EnvironmentID == "" || s.Generation == 0 {
		return fmt.Errorf("%w: session identity", ErrInvalid)
	}
	return nil
}
