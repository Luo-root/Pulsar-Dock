package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

func TestCleanupFailureIsVisible(t *testing.T) {
	s, store, b, _ := setup(t)
	b.create = func(runtime.EnvironmentSpec) (runtime.Environment, error) {
		return runtime.Environment{ID: "partial"}, errors.New("failed create")
	}
	b.removeErr = errors.New("failed remove")
	task, err := s.CreateRoot(ctx, request)
	if !errors.Is(err, ErrRecoveryRequired) || !errors.Is(err, b.removeErr) || task.State != taskstore.Failed {
		t.Fatalf("%+v %v", task, err)
	}
	persisted, err := store.Get(ctx, scope(task), task.ID)
	if err != nil || persisted.State != taskstore.Failed {
		t.Fatalf("%+v %v", persisted, err)
	}
}

func TestEmptyBackendIdentityFailsClosed(t *testing.T) {
	s, _, b, _ := setup(t)
	b.create = func(runtime.EnvironmentSpec) (runtime.Environment, error) { return runtime.Environment{}, nil }
	task, err := s.CreateRoot(ctx, request)
	if err == nil || task.State != taskstore.Failed || len(b.removed) != 0 {
		t.Fatalf("%+v %v", task, err)
	}
}

func TestPolicyCannotBeMutatedByConfigOrBackend(t *testing.T) {
	_, store, b, _ := setup(t)
	p := policy()
	s, err := New(store, b, "alice", p)
	if err != nil {
		t.Fatal(err)
	}
	p.CPUs = 999
	p.GatewayURL = "http://attacker.test"
	b.create = func(spec runtime.EnvironmentSpec) (runtime.Environment, error) {
		if spec.CPUs != 2 || len(spec.NetworkRules) != 0 || spec.GatewayURL != "" || spec.ExpiresAfter != 0 {
			t.Fatalf("mutated policy: %+v", spec)
		}
		spec.NetworkRules = append(spec.NetworkRules, runtime.NetworkRule{Host: "attacker.test", Port: 80, Action: "allow"})
		return runtime.Environment{ID: "real-" + spec.TaskID}, nil
	}
	for _, key := range []string{"one", "two"} {
		if _, err = s.CreateRoot(context.Background(), Request{Goal: "synthetic", IdempotencyKey: key}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInvalidGatewayConfiguration(t *testing.T) {
	_, store, b, _ := setup(t)
	for _, gateway := range []string{"file:///host", "http://user:password@gateway", "http://gateway:0", "http://gateway:65536", "http://gateway/admin", "http://gateway?token=secret", "http://gateway#secret"} {
		p := policy()
		p.GatewayURL = gateway
		if _, err := New(store, b, "alice", p); err == nil {
			t.Fatalf("accepted %q", gateway)
		}
	}
}
