package acpclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func fake(t *testing.T, caps Capabilities) (*Fake, Session) {
	t.Helper()
	f := NewFake(InitializeResult{AgentName: "synthetic", AgentVersion: "1", Protocol: ProtocolVersion, Capabilities: caps})
	if _, err := f.Initialize(context.Background(), InitializeRequest{AgentName: "synthetic", AgentVersion: "1", Protocol: ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	s, err := f.NewSession(context.Background(), NewSessionRequest{Identity: SessionIdentity{TaskID: "task-1", EnvironmentID: "env-1"}})
	if err != nil {
		t.Fatal(err)
	}
	return f, s
}

func TestHandshakeAndCapabilitiesFailClosed(t *testing.T) {
	f := NewFake(InitializeResult{Protocol: "other"})
	if _, err := f.Initialize(context.Background(), InitializeRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("handshake = %v", err)
	}
	f, _ = fake(t, Capabilities{Streaming: true})
	if _, err := f.Prompt(context.Background(), Session{}, PromptRequest{Text: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid identity = %v", err)
	}
}

func TestPromptSequencingAndCancel(t *testing.T) {
	f, s := fake(t, Capabilities{Streaming: true})
	stream, err := f.Prompt(context.Background(), s, PromptRequest{Text: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Prompt(context.Background(), s, PromptRequest{Text: "second"}); !errors.Is(err, ErrPromptInFlight) {
		t.Fatalf("second prompt = %v", err)
	}
	if err := f.Cancel(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-stream; ok {
		t.Fatal("cancel did not close update stream")
	}
	if err := f.Emit(Update{Generation: s.Generation, Kind: UpdateText, Text: "late"}); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("late update = %v", err)
	}
}

func TestGenerationRejectsLateUpdate(t *testing.T) {
	f, s1 := fake(t, Capabilities{Streaming: true})
	if _, err := f.Prompt(context.Background(), s1, PromptRequest{Text: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Cancel(context.Background(), s1); err != nil {
		t.Fatal(err)
	}
	s2, err := f.NewSession(context.Background(), NewSessionRequest{Identity: s1.Identity})
	if err != nil {
		t.Fatal(err)
	}
	if s2.Generation == s1.Generation {
		t.Fatal("generation did not advance")
	}
	if _, err := f.Prompt(context.Background(), s2, PromptRequest{Text: "two"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Emit(Update{Generation: s1.Generation, Kind: UpdateText}); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale update = %v", err)
	}
}

func TestPermissionCorrelation(t *testing.T) {
	f, s := fake(t, Capabilities{Streaming: true, Permissions: true})
	if _, err := f.Prompt(context.Background(), s, PromptRequest{Text: "ask"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Emit(Update{Generation: s.Generation, Kind: UpdatePermission, RequestID: "p-1"}); err != nil {
		t.Fatal(err)
	}
	if err := f.PermissionResponse(context.Background(), s, PermissionResponse{RequestID: "wrong", Allowed: true}); !errors.Is(err, ErrPermissionNotFound) {
		t.Fatalf("unknown permission = %v", err)
	}
	if err := f.PermissionResponse(context.Background(), s, PermissionResponse{RequestID: "p-1", Allowed: true}); err != nil {
		t.Fatal(err)
	}
}

func TestProviderRegistryUnsupported(t *testing.T) {
	r := NewProviderRegistry()
	for _, p := range []ProviderConfig{{Name: "x", Command: "x", Version: "1", Protocol: ProtocolVersion}, {Name: "x", Command: "x", Version: "1", Protocol: "other", Verified: true}} {
		if err := r.Register(p); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unverified provider accepted: %v", err)
		}
	}
	if _, err := r.Lookup("x"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown provider = %v", err)
	}
	if err := r.Register(ProviderConfig{Name: "synthetic", Command: "agent", Version: "1", Protocol: ProtocolVersion, Verified: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Lookup("synthetic"); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityBinding(t *testing.T) {
	f, _ := fake(t, Capabilities{Streaming: true})
	if _, err := f.NewSession(context.Background(), NewSessionRequest{Identity: SessionIdentity{TaskID: "task-only"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbound identity = %v", err)
	}
}

func TestSecretShapedTextNotSerialized(t *testing.T) {
	secret := "synthetic-api-key-123"
	b, err := json.Marshal(Update{Kind: UpdateText, Text: secret, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatalf("text leaked: %s", b)
	}
	p, err := json.Marshal(PromptRequest{Text: secret})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(p), secret) {
		t.Fatalf("prompt leaked: %s", p)
	}
}
