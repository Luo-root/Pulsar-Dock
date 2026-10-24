package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

type validationProbe struct{ called bool }

func (p *validationProbe) Run(context.Context, ...string) ([]byte, error) {
	p.called = true
	return nil, errors.New("synthetic preflight stop")
}

// Exercise real SbxBackend validation, then stop at the first external command.
// This proves parameter compatibility, not sandbox readiness or gateway support.
func TestControllerSpecPassesSbxValidation(t *testing.T) {
	s, _, b, _ := setup(t)
	if _, err := s.CreateRoot(ctx, request); err != nil {
		t.Fatal(err)
	}
	probe := &validationProbe{}
	backend := runtime.NewSbxBackend(probe)
	_, err := backend.Create(ctx, b.specs[0])
	if err == nil || !probe.called {
		t.Fatalf("spec rejected before preflight: %v", err)
	}
	var backendErr *runtime.BackendError
	if !errors.As(err, &backendErr) || backendErr.Kind != runtime.ErrRunner {
		t.Fatalf("unexpected contract result: %v", err)
	}
}

func TestUnsupportedPolicyIsNotSilentlyDropped(t *testing.T) {
	_, store, b, _ := setup(t)
	for _, p := range []Policy{
		func() Policy { p := policy(); p.GatewayURL = "http://localhost:7777"; return p }(),
		func() Policy { p := policy(); p.ExpiresAfter = 300; return p }(),
		func() Policy { p := policy(); p.ExpiresAfter = -1; return p }(),
	} {
		if _, err := New(store, b, "alice", p); err == nil {
			t.Fatalf("unsupported security policy accepted: %+v", p)
		}
	}
}

func TestChildReplayAfterParentStateChange(t *testing.T) {
	for _, state := range []taskstore.State{taskstore.WaitingApproval, taskstore.Stopping, taskstore.Failed, taskstore.Expired} {
		t.Run(string(state), func(t *testing.T) {
			s, store, b, _ := setup(t)
			root, err := s.CreateRoot(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			child, err := s.CreateChild(ctx, root.ID, request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.Transition(ctx, scope(root), root.ID, taskstore.Preparing, state); err != nil {
				t.Fatal(err)
			}
			replay, err := s.CreateChild(ctx, root.ID, request)
			if err != nil || replay != child || len(b.specs) != 2 {
				t.Fatalf("replay changed: %+v %v", replay, err)
			}
			_, err = s.CreateChild(ctx, root.ID, Request{Goal: "changed", IdempotencyKey: request.IdempotencyKey})
			var conflict *taskstore.Error
			if !errors.As(err, &conflict) || conflict.Kind != taskstore.Conflict {
				t.Fatalf("expected replay conflict: %v", err)
			}
			if _, err = s.CreateChild(ctx, root.ID, Request{Goal: "new", IdempotencyKey: "new"}); err == nil {
				t.Fatal("new delegation accepted")
			}
			if len(b.specs) != 2 {
				t.Fatal("new environment created for inactive parent")
			}
		})
	}
}
