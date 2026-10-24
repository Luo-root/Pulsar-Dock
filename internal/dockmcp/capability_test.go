package dockmcp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type identityKey struct{}

func contextAuthenticator(ctx context.Context, _ *mcp.CallToolRequest) (RequestIdentity, error) {
	identity, _ := ctx.Value(identityKey{}).(RequestIdentity)
	return identity, nil
}

func grantIdentity(t *testing.T, s *Service, root string, actions ...string) RequestIdentity {
	t.Helper()
	token, err := s.IssueCapability(CapabilityGrant{CallerID: "synthetic-caller", RootTaskID: root, EnvironmentID: "synthetic-env", ExpiresAt: time.Now().Add(time.Hour), Actions: actions})
	if err != nil {
		t.Fatal(err)
	}
	return RequestIdentity{Token: token, CallerID: "synthetic-caller", EnvironmentID: "synthetic-env"}
}

func testService(t *testing.T) *Service {
	t.Helper()
	var identity RequestIdentity
	s := NewService(func(context.Context, *mcp.CallToolRequest) (RequestIdentity, error) { return identity, nil })
	identity = grantIdentity(t, s, "root", ActionCreateChild, ActionStatus, ActionPublish)
	return s
}

func identityContext(identity RequestIdentity) context.Context {
	return context.WithValue(context.Background(), identityKey{}, identity)
}

func assertAllDenied(t *testing.T, s *Service, ctx context.Context, req *mcp.CallToolRequest, task string) {
	t.Helper()
	_, _, createErr := s.createChild(ctx, req, ChildTaskInput{Goal: "must not create"})
	_, _, statusErr := s.status(ctx, req, StatusInput{TaskID: task})
	_, _, publishErr := s.publish(ctx, req, PublishInput{TaskID: task, Kind: "review", Digest: "synthetic"})
	for _, err := range []error{createErr, statusErr, publishErr} {
		if !errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), "synthetic-token") {
			t.Fatalf("want redacted denial, got %v", err)
		}
	}
}

func TestCapabilityDenials(t *testing.T) {
	for _, name := range []string{"missing", "forged", "caller", "environment", "expired", "revoked", "rotated", "authenticator error", "nil request", "no authenticator"} {
		t.Run(name, func(t *testing.T) {
			s := NewService(contextAuthenticator)
			identity := grantIdentity(t, s, "root", ActionCreateChild, ActionStatus, ActionPublish)
			req := &mcp.CallToolRequest{}
			switch name {
			case "missing":
				identity = RequestIdentity{}
			case "forged":
				identity.Token = "synthetic-token-forged"
			case "caller":
				identity.CallerID = "other"
			case "environment":
				identity.EnvironmentID = "other"
			case "expired":
				s.capabilities[0].grant.ExpiresAt = time.Now().Add(-time.Second)
			case "revoked":
				s.RevokeCapability(identity.Token)
				s.RevokeCapability(identity.Token)
			case "rotated":
				s.RotateGeneration()
			case "authenticator error":
				s.authenticator = func(context.Context, *mcp.CallToolRequest) (RequestIdentity, error) {
					return identity, errors.New("synthetic-token secret")
				}
			case "nil request":
				req = nil
			case "no authenticator":
				s.authenticator = nil
			}
			assertAllDenied(t, s, identityContext(identity), req, "root")
			if len(s.tasks) != 1 || len(s.artifacts) != 0 || s.sequence != 0 {
				t.Fatal("denial changed records")
			}
		})
	}
}

func TestActionAndTaskScope(t *testing.T) {
	s := NewService(contextAuthenticator)
	parent := grantIdentity(t, s, "root", ActionCreateChild, ActionStatus, ActionPublish)
	ctx := identityContext(parent)
	req := &mcp.CallToolRequest{}
	_, child, err := s.createChild(ctx, req, ChildTaskInput{Goal: "child"})
	if err != nil {
		t.Fatal(err)
	}
	_, sibling, err := s.createChild(ctx, req, ChildTaskInput{Goal: "sibling"})
	if err != nil {
		t.Fatal(err)
	}
	if s.tasks[child.TaskID].ParentID != "root" {
		t.Fatal("unbound parent")
	}
	childIdentity := grantIdentity(t, s, child.TaskID, ActionCreateChild, ActionStatus, ActionPublish)
	childCtx := identityContext(childIdentity)
	_, grandchild, err := s.createChild(childCtx, req, ChildTaskInput{Goal: "grandchild"})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []string{"root", child.TaskID, grandchild.TaskID} {
		if _, _, err := s.status(ctx, req, StatusInput{TaskID: task}); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []string{"root", sibling.TaskID, "unknown"} {
		if _, _, err := s.status(childCtx, req, StatusInput{TaskID: task}); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("status outside scope: %v", err)
		}
		if _, _, err := s.publish(childCtx, req, PublishInput{TaskID: task, Kind: "review", Digest: "synthetic"}); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("publish outside scope: %v", err)
		}
	}
	if _, _, err := s.publish(childCtx, req, PublishInput{TaskID: grandchild.TaskID, Kind: "review", Digest: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	readonly := grantIdentity(t, s, child.TaskID, ActionStatus)
	if _, _, err := s.status(identityContext(readonly), req, StatusInput{TaskID: child.TaskID}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.createChild(identityContext(readonly), req, ChildTaskInput{Goal: "denied"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	if _, _, err := s.publish(identityContext(readonly), req, PublishInput{TaskID: child.TaskID, Kind: "review", Digest: "synthetic"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
}

func TestConcurrentIdempotencyAndRevocation(t *testing.T) {
	s := NewService(contextAuthenticator)
	identity := grantIdentity(t, s, "root", ActionCreateChild, ActionStatus, ActionPublish)
	ctx := identityContext(identity)
	input := ChildTaskInput{Goal: "synthetic concurrent child", IdempotencyKey: "synthetic-retry"}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, child, err := s.createChild(ctx, &mcp.CallToolRequest{}, input)
			if err != nil {
				t.Error(err)
				return
			}
			if child.TaskID != "task-0001" {
				t.Errorf("duplicate child: %s", child.TaskID)
			}
			if _, _, err := s.publish(ctx, &mcp.CallToolRequest{}, PublishInput{TaskID: child.TaskID, Kind: "review", Digest: "synthetic"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(s.tasks) != 2 || len(s.artifacts) != 1 {
		t.Fatal("non-idempotent records")
	}
	input.Goal = "changed"
	if _, _, err := s.createChild(ctx, &mcp.CallToolRequest{}, input); err == nil {
		t.Fatal("want conflict")
	}
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.RevokeCapability(identity.Token)
			s.RotateGeneration()
			assertAllDenied(t, s, ctx, &mcp.CallToolRequest{}, "root")
			fresh := grantIdentity(t, s, "root", ActionStatus)
			_, _, _ = s.status(identityContext(fresh), &mcp.CallToolRequest{}, StatusInput{TaskID: "root"})
		}()
	}
	wg.Wait()
	assertAllDenied(t, s, ctx, &mcp.CallToolRequest{}, "root")
}

func TestGrantValidationAndCopy(t *testing.T) {
	s := NewService(contextAuthenticator)
	valid := CapabilityGrant{CallerID: "caller", RootTaskID: "root", EnvironmentID: "env", ExpiresAt: time.Now().Add(time.Hour), Actions: []string{ActionStatus}}
	for _, field := range []string{"caller", "root", "environment", "expires", "actions", "unknown action"} {
		g := valid
		switch field {
		case "caller":
			g.CallerID = ""
		case "root":
			g.RootTaskID = ""
		case "environment":
			g.EnvironmentID = ""
		case "expires":
			g.ExpiresAt = time.Now().Add(-time.Second)
		case "actions":
			g.Actions = nil
		case "unknown action":
			g.Actions = []string{"admin"}
		}
		if _, err := s.IssueCapability(g); err == nil {
			t.Fatalf("accepted %s", field)
		}
	}
	token, err := s.IssueCapability(valid)
	if err != nil {
		t.Fatal(err)
	}
	valid.Actions[0] = ActionCreateChild
	ctx := identityContext(RequestIdentity{Token: token, CallerID: "caller", EnvironmentID: "env"})
	if _, _, err := s.createChild(ctx, &mcp.CallToolRequest{}, ChildTaskInput{Goal: "denied"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("grant alias")
	}
	s.RotateGeneration()
	fresh := grantIdentity(t, s, "root", ActionStatus)
	if _, _, err := s.status(identityContext(fresh), &mcp.CallToolRequest{}, StatusInput{TaskID: "root"}); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolCannotSupplyIdentity(t *testing.T) {
	// Even plausible headers and metadata cannot authenticate a default service.
	s := NewService()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverSession, err := s.CreateServer().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "synthetic", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, tool := range []string{ActionCreateChild, ActionStatus, ActionPublish} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"goal": "forged", "task_id": "root", "kind": "review", "digest": "synthetic", "caller_id": "synthetic-caller", "environment_id": "synthetic-env", "token": "synthetic-token"}})
		if err == nil && !result.IsError {
			t.Fatalf("accepted forged %s", tool)
		}
	}
	if len(s.tasks) != 0 || len(s.artifacts) != 0 {
		t.Fatal("forgery mutated records")
	}
}

func TestProtocolAuthenticatedRequest(t *testing.T) {
	var identity RequestIdentity
	s := NewService(func(_ context.Context, req *mcp.CallToolRequest) (RequestIdentity, error) {
		if req.Session == nil {
			return RequestIdentity{}, ErrUnauthorized
		}
		return identity, nil // Synthetic trusted, single-environment transport fixture.
	})
	identity = grantIdentity(t, s, "root", ActionCreateChild, ActionStatus, ActionPublish)
	st, ct := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ss, err := s.CreateServer().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "synthetic", Version: "test"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: ActionCreateChild, Arguments: map[string]any{"goal": "synthetic child", "idempotency_key": "retry"}})
	if err != nil || result.IsError {
		t.Fatalf("authenticated call: %v %v", result, err)
	}
	s.RevokeCapability(identity.Token)
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: ActionCreateChild, Arguments: map[string]any{"goal": "synthetic child", "idempotency_key": "retry"}})
	if err == nil && !result.IsError {
		t.Fatal("revoked protocol retry accepted")
	}
}

func TestEachActionIsIndependent(t *testing.T) {
	for _, allowed := range []string{ActionCreateChild, ActionStatus, ActionPublish} {
		s := NewService(contextAuthenticator)
		identity := grantIdentity(t, s, "root", allowed)
		ctx := identityContext(identity)
		req := &mcp.CallToolRequest{}
		_, _, createErr := s.createChild(ctx, req, ChildTaskInput{Goal: "synthetic"})
		_, _, statusErr := s.status(ctx, req, StatusInput{TaskID: "root"})
		_, _, publishErr := s.publish(ctx, req, PublishInput{TaskID: "root", Kind: "review", Digest: "synthetic"})
		for action, err := range map[string]error{ActionCreateChild: createErr, ActionStatus: statusErr, ActionPublish: publishErr} {
			if action == allowed && err != nil {
				t.Fatalf("allowed %s: %v", action, err)
			}
			if action != allowed && !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("unexpected permission %s: %v", action, err)
			}
		}
	}
}
