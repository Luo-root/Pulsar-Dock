package liveagent

import (
	"context"
	"strings"
	"testing"

	"github.com/Luo-root/pulsar-dock/internal/agentadapter"
	"github.com/Luo-root/pulsar-dock/internal/runtime"
)

type fakeBackend struct{}

func (fakeBackend) Name() string { return "fake" }
func (fakeBackend) Capabilities(context.Context) runtime.CapabilitySet {
	return runtime.CapabilitySet{}
}
func (fakeBackend) Check(context.Context) runtime.Diagnostics { return runtime.Diagnostics{} }
func (fakeBackend) Create(context.Context, runtime.EnvironmentSpec) (runtime.Environment, error) {
	return runtime.Environment{}, nil
}
func (fakeBackend) Start(context.Context, runtime.Environment) error { return nil }
func (fakeBackend) Exec(context.Context, runtime.Environment, runtime.ExecRequest) (string, error) {
	return "", nil
}
func (fakeBackend) CopyIn(context.Context, runtime.Environment, string, string) (runtime.CopyReceipt, error) {
	return runtime.CopyReceipt{}, nil
}
func (fakeBackend) CopyOut(context.Context, runtime.Environment, runtime.CopySelector) (runtime.ArtifactSnapshot, error) {
	return runtime.ArtifactSnapshot{}, nil
}
func (fakeBackend) Stop(context.Context, runtime.Environment) error   { return nil }
func (fakeBackend) Remove(context.Context, runtime.Environment) error { return nil }
func (fakeBackend) SetNetworkPolicy(context.Context, runtime.Environment, []runtime.NetworkRule) error {
	return nil
}
func (fakeBackend) Revoke(context.Context, runtime.Environment, string) error { return nil }
func (fakeBackend) Inspect(context.Context, runtime.Environment) (runtime.Environment, error) {
	return runtime.Environment{}, nil
}

func TestClaudePlannerFixedCommandAndGoal(t *testing.T) {
	p, _, err := Components(ClaudeID, fakeBackend{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Plan(agentadapter.Request{Goal: "check status", MCPConfigPath: ClaudeConfig, MaxBudgetUSD: 1})
	if err != nil {
		t.Fatal(err)
	}
	if r.User != "agent" || r.Command[0] != claudeLauncher {
		t.Fatalf("unexpected request: %#v", r)
	}
	for _, arg := range r.Command {
		if arg == "--unsafe" || arg == "--allow-all" {
			t.Fatal("unsafe flag injected")
		}
	}
	if r.Command[len(r.Command)-1] != "check status" {
		t.Fatal("goal not passed as final positional argument")
	}
}

func TestClaudePlannerRejectsInjectedGoalAndPath(t *testing.T) {
	p, _, _ := Components(ClaudeID, fakeBackend{}, t.TempDir())
	if _, err := p.Plan(agentadapter.Request{Goal: "x\n--unsafe", MCPConfigPath: ClaudeConfig, MaxBudgetUSD: 1}); err == nil {
		t.Fatal("newline goal accepted")
	}
	if _, err := p.Plan(agentadapter.Request{Goal: "x", MCPConfigPath: "/host/config", MaxBudgetUSD: 1}); err == nil {
		t.Fatal("host config accepted")
	}
}

func TestOpenCodePlannerFixedCommandAndGoal(t *testing.T) {
	p, _, err := Components(OpenCodeID, fakeBackend{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Plan(agentadapter.Request{Goal: "Reply exactly PULSAR_OPENCODE_OK", MCPConfigPath: OpenCodeConfig, MaxBudgetUSD: 1})
	if err != nil {
		t.Fatal(err)
	}
	if r.User != "agent" || len(r.Command) != 8 || r.Command[0] != OpenCodeLauncher || r.Command[1] != "run" || r.Command[2] != "--config" || r.Command[3] != OpenCodeConfig || r.Command[4] != "--model" || r.Command[5] != OpenCodeModel || r.Command[6] != "--" || r.Command[7] != "Reply exactly PULSAR_OPENCODE_OK" {
		t.Fatalf("unexpected request: %#v", r)
	}
	for _, arg := range r.Command {
		if arg == "--unsafe" || arg == "--allow-all" || strings.Contains(arg, "token") {
			t.Fatal("unsafe flag or credential injected")
		}
	}
}

func TestOpenCodePlannerRejectsInjectedGoalAndPath(t *testing.T) {
	p, _, _ := Components(OpenCodeID, fakeBackend{}, t.TempDir())
	if _, err := p.Plan(agentadapter.Request{Goal: "x\n--unsafe", MCPConfigPath: OpenCodeConfig, MaxBudgetUSD: 1}); err == nil {
		t.Fatal("newline goal accepted")
	}
	if _, err := p.Plan(agentadapter.Request{Goal: "x", MCPConfigPath: ClaudeConfig, MaxBudgetUSD: 1}); err == nil {
		t.Fatal("wrong config accepted")
	}
}

func TestUnsupportedAgentsFailClosed(t *testing.T) {
	for _, id := range []string{"codex"} {
		p, v, err := Components(id, fakeBackend{}, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Plan(agentadapter.Request{Goal: "x", MCPConfigPath: ClaudeConfig, MaxBudgetUSD: 1}); err == nil {
			t.Fatalf("%s planner unexpectedly ready", id)
		}
		if err := v.Verify(context.Background(), runtime.Environment{ID: "id", Backend: "fake", Agent: id}); err == nil {
			t.Fatalf("%s verifier unexpectedly ready", id)
		}
	}
}
