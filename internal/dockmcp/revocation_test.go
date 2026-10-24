package dockmcp

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func revocationTree(t *testing.T) (*Service, map[string]RequestIdentity, map[string]string) {
	t.Helper()
	s := NewService(contextAuthenticator)
	identities := map[string]RequestIdentity{}
	tasks := map[string]string{"root": "root", "unrelated": "unrelated"}
	actions := []string{ActionCreateChild, ActionStatus, ActionPublish}
	identities["root"] = grantIdentity(t, s, "root", actions...)
	identities["unrelated"] = grantIdentity(t, s, "unrelated", actions...)
	for _, pair := range [][2]string{{"root", "child"}, {"root", "sibling"}, {"child", "grandchild"}} {
		_, child, err := s.createChild(identityContext(identities[pair[0]]), &mcp.CallToolRequest{}, ChildTaskInput{Goal: pair[1]})
		if err != nil {
			t.Fatal(err)
		}
		tasks[pair[1]] = child.TaskID
		identities[pair[1]] = grantIdentity(t, s, child.TaskID, actions...)
	}
	return s, identities, tasks
}

func assertIdentityAllowed(t *testing.T, s *Service, identity RequestIdentity, task string) {
	t.Helper()
	ctx := identityContext(identity)
	req := &mcp.CallToolRequest{}
	if _, _, err := s.createChild(ctx, req, ChildTaskInput{Goal: "synthetic allowed"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.status(ctx, req, StatusInput{TaskID: task}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.publish(ctx, req, PublishInput{TaskID: task, Kind: "review", Digest: "synthetic"}); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilitySubtreeRevocation(t *testing.T) {
	for _, revoked := range []string{"root", "child", "grandchild"} {
		t.Run(revoked, func(t *testing.T) {
			s, identities, tasks := revocationTree(t)
			// A second capability for the same revoked task cannot bypass task revocation.
			second := grantIdentity(t, s, tasks[revoked], ActionStatus)
			s.RevokeCapability("synthetic-unknown-token")
			assertIdentityAllowed(t, s, identities[revoked], tasks[revoked])
			s.RevokeCapability(identities[revoked].Token)
			s.RevokeCapability(identities[revoked].Token)
			denied := map[string]bool{revoked: true}
			if revoked == "root" {
				denied["child"] = true
				denied["sibling"] = true
				denied["grandchild"] = true
			}
			if revoked == "child" {
				denied["grandchild"] = true
			}
			for name, identity := range identities {
				if denied[name] {
					assertAllDenied(t, s, identityContext(identity), &mcp.CallToolRequest{}, tasks[name])
				} else {
					assertIdentityAllowed(t, s, identity, tasks[name])
				}
			}
			if _, _, err := s.status(identityContext(second), &mcp.CallToolRequest{}, StatusInput{TaskID: tasks[revoked]}); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("second token bypass: %v", err)
			}
			// Even an active ancestor must not access the revoked target subtree.
			if revoked != "root" {
				for _, name := range []string{revoked, "grandchild"} {
					if _, _, err := s.status(identityContext(identities["root"]), &mcp.CallToolRequest{}, StatusInput{TaskID: tasks[name]}); !errors.Is(err, ErrUnauthorized) {
						t.Fatalf("ancestor read revoked task: %v", err)
					}
					if _, _, err := s.publish(identityContext(identities["root"]), &mcp.CallToolRequest{}, PublishInput{TaskID: tasks[name], Kind: "review", Digest: "synthetic"}); !errors.Is(err, ErrUnauthorized) {
						t.Fatalf("ancestor publish revoked task: %v", err)
					}
				}
			}
			// Global rotation still invalidates all tokens; fresh grants cannot resurrect
			// a revoked subtree, but work for an unrelated root.
			s.RotateGeneration()
			assertAllDenied(t, s, identityContext(identities["unrelated"]), &mcp.CallToolRequest{}, tasks["unrelated"])
			freshDenied := grantIdentity(t, s, tasks["grandchild"], ActionCreateChild, ActionStatus, ActionPublish)
			assertAllDenied(t, s, identityContext(freshDenied), &mcp.CallToolRequest{}, tasks["grandchild"])
			freshAllowed := grantIdentity(t, s, tasks["unrelated"], ActionCreateChild, ActionStatus, ActionPublish)
			assertIdentityAllowed(t, s, freshAllowed, tasks["unrelated"])
		})
	}
}

func TestSubtreeRevokedDuringAuthentication(t *testing.T) {
	s, identities, tasks := revocationTree(t)
	authenticated := make(chan struct{})
	resume := make(chan struct{})
	s.authenticator = func(ctx context.Context, req *mcp.CallToolRequest) (RequestIdentity, error) {
		identity, err := contextAuthenticator(ctx, req)
		close(authenticated)
		<-resume
		return identity, err
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := s.createChild(identityContext(identities["grandchild"]), &mcp.CallToolRequest{}, ChildTaskInput{Goal: "must not create"})
		done <- err
	}()
	<-authenticated
	s.RevokeCapability(identities["root"].Token)
	close(resume)
	if err := <-done; !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("in-flight authorization bypass: %v", err)
	}
	if len(s.tasks) != len(tasks) {
		t.Fatal("revoked in-flight request created a task")
	}
}

func TestConcurrentSubtreeRevokeAndCalls(t *testing.T) {
	s, identities, tasks := revocationTree(t)
	start := make(chan struct{})
	revoked := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ctx := identityContext(identities["grandchild"])
			req := &mcp.CallToolRequest{}
			// These may linearize before or after revocation, but no other error is valid.
			_, _, err := s.createChild(ctx, req, ChildTaskInput{Goal: "synthetic concurrent", IdempotencyKey: "retry"})
			if err != nil && !errors.Is(err, ErrUnauthorized) {
				t.Error(err)
			}
			_, _, err = s.status(ctx, req, StatusInput{TaskID: tasks["grandchild"]})
			if err != nil && !errors.Is(err, ErrUnauthorized) {
				t.Error(err)
			}
			_, _, err = s.publish(ctx, req, PublishInput{TaskID: tasks["grandchild"], Kind: "review", Digest: "synthetic"})
			if err != nil && !errors.Is(err, ErrUnauthorized) {
				t.Error(err)
			}
			<-revoked
			assertAllDenied(t, s, ctx, req, tasks["grandchild"])
			assertIdentityAllowed(t, s, identities["sibling"], tasks["sibling"])
			assertIdentityAllowed(t, s, identities["unrelated"], tasks["unrelated"])
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); <-start; s.RevokeCapability(identities["child"].Token); close(revoked) }()
	close(start)
	wg.Wait()
	// The child itself is also denied once revocation completes.
	assertAllDenied(t, s, identityContext(identities["child"]), &mcp.CallToolRequest{}, tasks["child"])
}
