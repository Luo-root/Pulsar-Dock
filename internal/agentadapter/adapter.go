// Package agentadapter builds guest execution plans. It never probes binaries,
// starts processes, supplies credentials, or falls back to host execution.
package agentadapter

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Luo-root/pulsar-dock/internal/runtime"
)

const (
	ClaudeVersion = "2.1.300"
	// GuestMCPConfigPath is a fixed, canonical Linux guest path, not a host path.
	GuestMCPConfigPath = "/tmp/pulsar-dock/mcp.json"
	MaxGoalBytes       = 16 * 1024
)

var (
	ErrInvalid     = errors.New("invalid agent plan input")
	ErrUnsupported = errors.New("unsupported agent or version")
	modelPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,127}$`)
)

// TrustedClaudeConfig must come from the Controller's trusted configuration,
// never Agent tool arguments. VersionOutput must be stdout of a trusted probe
// of the guest binary selected for execution, not a caller-claimed version.
// This package cannot establish that provenance. No credentials belong here.
// Model is optional; empty leaves model selection to trusted guest provisioning.
// A model name is a selector, not a place for provider secrets or configuration.
type TrustedClaudeConfig struct {
	VersionOutput string
	Model         string
}

// Request contains only bounded task input and execution limits, not arbitrary
// flags, model overrides, environment variables, or credential configuration.
// Goal must be single-line: the current SbxBackend argv transport rejects CR/LF.
// Multiline input is rejected, never silently normalized.
type Request struct {
	Goal          string
	MCPConfigPath string
	MaxBudgetUSD  float64
}

// Claude is an immutable planner with a pinned CLI contract.
type Claude struct {
	model    string
	verified bool
}

func NewClaude(config TrustedClaudeConfig) (*Claude, error) {
	// Accept only the complete expected version line and its normal line ending.
	version := ClaudeVersion + " (Claude Code)"
	if config.VersionOutput != version && config.VersionOutput != version+"\n" && config.VersionOutput != version+"\r\n" {
		return nil, ErrUnsupported
	}
	if config.Model != "" && !modelPattern.MatchString(config.Model) {
		return nil, ErrInvalid
	}
	return &Claude{model: config.Model, verified: true}, nil
}

// Plan builds an argv vector for RuntimeBackend.Exec as the unprivileged agent
// user. It does not authorize execution or attest isolation, identity, installed
// version, MCP file content/ownership, network policy, or credential injection.
// The caller must verify those in an approved guest; never run this on the host.
// The guest must resolve "claude" through a trusted, pinned PATH and provision
// only the intended dock MCP server at GuestMCPConfigPath. CLI allowlists are
// defense in depth, not a replacement for gateway authorization.
func (c *Claude) Plan(request Request) (runtime.ExecRequest, error) {
	if c == nil || !c.verified {
		return runtime.ExecRequest{}, ErrUnsupported
	}
	if !utf8.ValidString(request.Goal) || len(request.Goal) > MaxGoalBytes ||
		strings.TrimSpace(request.Goal) == "" || strings.ContainsAny(request.Goal, "\x00\r\n") ||
		request.MCPConfigPath != GuestMCPConfigPath ||
		math.IsNaN(request.MaxBudgetUSD) || math.IsInf(request.MaxBudgetUSD, 0) || request.MaxBudgetUSD <= 0 {
		return runtime.ExecRequest{}, ErrInvalid
	}
	args := []string{
		"claude", "--bare", "--restricted", "--print",
		"--strict-mcp-config", "--mcp-config", GuestMCPConfigPath,
		"--permission-mode", "dontAsk", "--permission-prompts", "none",
		"--tools", "", "--allowedTools", "mcp__dock__*",
		"--disable-slash-commands", "--setting-sources", "",
		"--no-chrome", "--no-session-persistence",
		"--output-format", "stream-json", "--verbose",
		"--max-budget-usd", strconv.FormatFloat(request.MaxBudgetUSD, 'f', -1, 64),
	}
	if c.model != "" {
		args = append(args, "--model", c.model)
	}
	args = append(args, "--", request.Goal)
	return runtime.ExecRequest{Command: args, User: "agent"}, nil
}

// PlanCodex fails closed: no executable/version/help contract was verified.
// It intentionally offers neither guessed argv nor a Claude/host fallback.
func PlanCodex(Request) (runtime.ExecRequest, error) {
	return runtime.ExecRequest{}, ErrUnsupported
}
