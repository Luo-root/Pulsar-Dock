// Package liveagent wires trusted desktop Agent execution plans to approved guests.
// It never executes an Agent on the host and never accepts UI-supplied argv, env,
// model, or credential values.
package liveagent

import (
	"context"
	"errors"
	"strings"

	"github.com/Luo-root/pulsar-dock/internal/agentadapter"
	"github.com/Luo-root/pulsar-dock/internal/runtime"
)

const (
	ClaudeID      = "claude"
	ClaudeVersion = agentadapter.ClaudeVersion
	ClaudeCLIPath = "/opt/pulsar-agents/claude"
	ClaudeConfig  = agentadapter.GuestMCPConfigPath
	ClaudeModel   = "deepseek/deepseek-flash[1m]"
	GateHost      = "127.0.0.1"
	GatePort      = 3425
)

var ErrUnsupported = errors.New("agent is not ready for live execution")

// StartupCheck validates the host/runtime prerequisites that can be checked
// without creating a guest or making a model request. ModelVerified remains
// false until the caller performs a real, controlled modelproxy self-test.
func StartupCheck(ctx context.Context, backend runtime.RuntimeBackend) error {
	if backend == nil {
		return ErrUnsupported
	}
	caps := backend.Capabilities(ctx)
	if !caps.GuestExec || !caps.NetworkPolicy {
		return errors.New("runtime lacks guest execution or network policy")
	}
	return nil
}

// Definition is immutable host-owned readiness information. A false readiness
// flag is intentional: it means no verified guest contract is available.
type Definition struct {
	ID, DisplayName, Version, CLIPath, ConfigPath, Model string
	Installed, CLIVerified, ModelVerified                bool
}

func Definitions() []Definition {
	return []Definition{
		{ID: ClaudeID, DisplayName: "Claude", Version: ClaudeVersion, CLIPath: ClaudeCLIPath, ConfigPath: ClaudeConfig, Model: ClaudeModel, Installed: true, CLIVerified: true, ModelVerified: false},
		{ID: "codex", DisplayName: "Codex", Version: "0.164.0"},
		{ID: "opencode", DisplayName: "OpenCode", Version: "1.19.4"},
	}
}

// ClaudePlanner is a trusted Planner. The goal is the only task input; all
// command paths and flags are fixed here.
type ClaudePlanner struct{}

func (ClaudePlanner) Plan(r agentadapter.Request) (runtime.ExecRequest, error) {
	if !validRequest(r) {
		return runtime.ExecRequest{}, errors.New("invalid Claude execution request")
	}
	return runtime.ExecRequest{User: "agent", Command: []string{
		ClaudeCLIPath, "--bare", "--restricted", "--print", "--strict-mcp-config", "--mcp-config", ClaudeConfig,
		"--permission-mode", "dontAsk", "--permission-prompts", "none", "--tools", "", "--allowedTools", "mcp__dock__*",
		"--disable-slash-commands", "--setting-sources", "", "--no-chrome", "--no-session-persistence",
		"--output-format", "stream-json", "--verbose", "--max-budget-usd", "1", "--model", ClaudeModel, "--", r.Goal,
	}}, nil
}

func validRequest(r agentadapter.Request) bool {
	return r.MCPConfigPath == ClaudeConfig && r.MaxBudgetUSD > 0 && r.MaxBudgetUSD < 100000 &&
		strings.TrimSpace(r.Goal) != "" && len(r.Goal) <= agentadapter.MaxGoalBytes && !strings.ContainsAny(r.Goal, "\x00\r\n")
}

// ClaudeVerifier performs the guest-side version attestation immediately before
// execution. It does not trust catalogue/readiness claims from the UI.
type ClaudeVerifier struct{ Backend runtime.RuntimeBackend }

func (v ClaudeVerifier) Verify(ctx context.Context, env runtime.Environment) error {
	if v.Backend == nil || env.ID == "" || env.Backend != v.Backend.Name() || env.Agent != ClaudeID {
		return ErrUnsupported
	}
	got, err := v.Backend.Inspect(ctx, env)
	if err != nil || got.ID != env.ID || got.Name != env.Name || got.State != "running" {
		return errors.Join(ErrUnsupported, err)
	}
	out, err := v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "agent", Command: []string{ClaudeCLIPath, "--version"}})
	if err != nil || strings.TrimSpace(out) != ClaudeVersion+" (Claude Code)" {
		return errors.Join(ErrUnsupported, err)
	}
	return nil
}

// UnsupportedVerifier is used for agents whose CLI/model protocol has not been
// independently verified. It fails closed rather than guessing flags.
type UnsupportedVerifier struct{}

func (UnsupportedVerifier) Verify(context.Context, runtime.Environment) error { return ErrUnsupported }

type unsupportedPlanner struct{}

func (unsupportedPlanner) Plan(agentadapter.Request) (runtime.ExecRequest, error) {
	return runtime.ExecRequest{}, ErrUnsupported
}

// Components returns only fixed, host-owned components for one logical Agent.
// Readiness is deliberately not inferred from a package existing on disk.
func Components(agent string, backend runtime.RuntimeBackend) (interface {
	Plan(agentadapter.Request) (runtime.ExecRequest, error)
}, interface {
	Verify(context.Context, runtime.Environment) error
}, error) {
	switch agent {
	case ClaudeID:
		if backend == nil {
			return nil, nil, ErrUnsupported
		}
		return ClaudePlanner{}, ClaudeVerifier{Backend: backend}, nil
	case "codex", "opencode":
		return unsupportedPlanner{}, UnsupportedVerifier{}, nil
	default:
		return nil, nil, ErrUnsupported
	}
}
