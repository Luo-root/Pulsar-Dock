package dockmcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	ActionCreateChild = "task.create_child"
	ActionStatus      = "task.status"
	ActionPublish     = "artifact.publish"
)

var ErrUnauthorized = errors.New("MCP capability denied")

// RequestIdentity must come from a trusted, environment-bound transport, not
// tool arguments, client metadata, loopback origin, or an unverified header.
// The host authenticator must enforce channel binding and replay protection.
type RequestIdentity struct {
	Token         string
	CallerID      string
	EnvironmentID string
}

// RequestAuthenticator is installed only by the trusted host. SDK HTTP headers
// are available in Request.Extra, but are not authenticated environment identity.
// Implementations must be concurrency-safe. Errors are deliberately redacted.
type RequestAuthenticator func(context.Context, *mcp.CallToolRequest) (RequestIdentity, error)

type CapabilityGrant struct {
	CallerID      string
	RootTaskID    string
	EnvironmentID string
	ExpiresAt     time.Time
	Actions       []string
}

type capability struct {
	grant      CapabilityGrant
	tokenHash  [32]byte
	generation uint64
	revoked    bool
}

// IssueCapability is a trusted-host API, never an MCP tool. An absent root is
// registered as a task record only; this does not create an Agent environment.
// Tokens are returned once and only their hashes are retained.
func (s *Service) IssueCapability(grant CapabilityGrant) (string, error) {
	if grant.CallerID == "" || grant.RootTaskID == "" || grant.EnvironmentID == "" || !grant.ExpiresAt.After(time.Now()) || len(grant.Actions) == 0 {
		return "", errors.New("invalid capability grant")
	}
	for _, action := range grant.Actions {
		if action != ActionCreateChild && action != ActionStatus && action != ActionPublish {
			return "", errors.New("invalid capability action")
		}
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", errors.New("capability entropy unavailable")
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	grant.Actions = append([]string(nil), grant.Actions...)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.capabilities = append(s.capabilities, capability{grant: grant, tokenHash: sha256.Sum256([]byte(token)), generation: s.generation})
	if _, ok := s.tasks[grant.RootTaskID]; !ok {
		s.tasks[grant.RootTaskID] = taskRecord{ID: grant.RootTaskID, State: "created", CreatedAt: time.Now().UTC()}
	}
	return token, nil
}

// RevokeCapability is idempotent and does not reveal whether a token existed.
func (s *Service) RevokeCapability(token string) {
	hash := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.capabilities {
		if subtle.ConstantTimeCompare(hash[:], s.capabilities[i].tokenHash[:]) == 1 {
			s.capabilities[i].revoked = true
		}
	}
}

// RotateGeneration invalidates every previously issued capability atomically.
func (s *Service) RotateGeneration() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	// Drop old hashes as well, including at uint64 wraparound.
	s.capabilities = nil
}

func (s *Service) authenticate(ctx context.Context, req *mcp.CallToolRequest) (RequestIdentity, error) {
	if s.authenticator == nil || req == nil {
		return RequestIdentity{}, ErrUnauthorized
	}
	identity, err := s.authenticator(ctx, req)
	if err != nil || identity.Token == "" || identity.CallerID == "" || identity.EnvironmentID == "" {
		return RequestIdentity{}, ErrUnauthorized
	}
	return identity, nil
}

// authorizeLocked and each operation share the same lock as revocation, so a
// revocation completed before an operation always prevents that operation.
func (s *Service) authorizeLocked(identity RequestIdentity, action, taskID string) (CapabilityGrant, error) {
	hash := sha256.Sum256([]byte(identity.Token))
	var found *capability
	for i := range s.capabilities {
		if subtle.ConstantTimeCompare(hash[:], s.capabilities[i].tokenHash[:]) == 1 {
			found = &s.capabilities[i]
		}
	}
	if found == nil || found.revoked || found.generation != s.generation || !time.Now().Before(found.grant.ExpiresAt) || found.grant.CallerID != identity.CallerID || found.grant.EnvironmentID != identity.EnvironmentID {
		return CapabilityGrant{}, ErrUnauthorized
	}
	allowed := false
	for _, candidate := range found.grant.Actions {
		allowed = allowed || candidate == action
	}
	if !allowed {
		return CapabilityGrant{}, ErrUnauthorized
	}
	if taskID != "" && !s.inScopeLocked(taskID, found.grant.RootTaskID) {
		return CapabilityGrant{}, ErrUnauthorized
	}
	return found.grant, nil
}

func (s *Service) inScopeLocked(taskID, root string) bool {
	// Bound traversal even if an internal record is corrupt.
	for n := 0; taskID != "" && n <= len(s.tasks); n++ {
		task, ok := s.tasks[taskID]
		if !ok {
			return false
		}
		if taskID == root {
			return true
		}
		taskID = task.ParentID
	}
	return false
}
