package dockmcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/taskstore"
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

// IssueCapability is a trusted-host API, never an MCP tool. Persistent mode
// requires an existing root with the matching environment; only the independent
// prototype registers absent roots as records (not Agent environments).
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
	if s.persistent != nil {
		if _, err := s.persistentRoot(context.Background(), grant); err != nil {
			return "", ErrUnauthorized
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persistent != nil && s.persistent.revokedRoots[taskstore.Scope{CallerID: grant.CallerID, RootID: grant.RootTaskID}] {
		return "", ErrUnauthorized
	}
	s.capabilities = append(s.capabilities, capability{grant: grant, tokenHash: sha256.Sum256([]byte(token)), generation: s.generation})
	if s.persistent == nil {
		if _, ok := s.tasks[grant.RootTaskID]; !ok {
			s.tasks[grant.RootTaskID] = taskRecord{ID: grant.RootTaskID, State: "created", CreatedAt: time.Now().UTC()}
		}
	}
	return token, nil
}

// RevokeCapability revokes the matching token and its root task's entire subtree,
// including independently issued descendant capabilities. It is idempotent and
// does not reveal whether a token existed. Other task trees remain unaffected.
// In persistent mode a nil result acknowledges durable revocation. On storage
// failure local denial remains active, but the host must retry before restart.
func (s *Service) RevokeCapability(token string) error {
	hash := sha256.Sum256([]byte(token))
	var scopes []taskstore.Scope
	s.mu.Lock()
	if s.persistent != nil {
		if scope, ok := s.persistent.revocationIntents[hash]; ok {
			scopes = append(scopes, scope)
		}
	}
	for i := range s.capabilities {
		if subtle.ConstantTimeCompare(hash[:], s.capabilities[i].tokenHash[:]) == 1 {
			s.capabilities[i].revoked = true
			root := s.capabilities[i].grant.RootTaskID
			if s.persistent != nil {
				scope := taskstore.Scope{CallerID: s.capabilities[i].grant.CallerID, RootID: root}
				s.persistent.revokedRoots[scope] = true
				if _, exists := s.persistent.revocationIntents[hash]; !exists {
					s.persistent.revocationIntents[hash] = scope
					scopes = append(scopes, scope)
				}
				for op := range s.persistent.operations {
					if op.scope == scope {
						op.cancel()
					}
				}
			}
			if task, ok := s.tasks[root]; ok {
				task.Revoked = true
				s.tasks[root] = task
			}
		}
	}
	s.mu.Unlock()
	if len(scopes) != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, scope := range scopes {
			if err := s.persistent.store.RevokeRoot(ctx, scope); err != nil {
				return errors.New("MCP durable revocation failed; retry required before restart")
			}
		}
	}
	return nil
}

// RotateGeneration invalidates every previously issued capability atomically.
func (s *Service) RotateGeneration() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persistent != nil {
		for op := range s.persistent.operations {
			op.cancel()
		}
	}
	s.generation++
	// Drop live authorization hashes, including at uint64 wraparound. Durable
	// revocation retry intents are separate and must survive every rotation.
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

// authorizeLocked serializes capability checks with revocation. Persistent I/O
// runs outside this lock under a cancellable lease and is rechecked at completion.
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
	if s.persistent != nil {
		allowed := false
		for _, candidate := range found.grant.Actions {
			allowed = allowed || candidate == action
		}
		if !allowed || s.persistent.revokedRoots[taskstore.Scope{CallerID: found.grant.CallerID, RootID: found.grant.RootTaskID}] {
			return CapabilityGrant{}, ErrUnauthorized
		}
		return found.grant, nil
	}
	if !s.taskActiveLocked(found.grant.RootTaskID) || (taskID != "" && !s.taskActiveLocked(taskID)) {
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

// taskActiveLocked checks the complete ancestry, not just the capability root.
// Missing records and cycles fail closed. Rotation never clears task revocation.
func (s *Service) taskActiveLocked(taskID string) bool {
	if taskID == "" {
		return false
	}
	for n := 0; n < len(s.tasks); n++ {
		task, ok := s.tasks[taskID]
		if !ok || task.Revoked {
			return false
		}
		if task.ParentID == "" {
			return true
		}
		taskID = task.ParentID
	}
	return false
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
