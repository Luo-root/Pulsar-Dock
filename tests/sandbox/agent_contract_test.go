//go:build sandboxsmoke

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/agentadapter"
	runtime "github.com/Luo-root/pulsar-dock/internal/runtime"
)

// TestAgentGuestContracts inventories only the pinned guest, never host CLIs.
// It does not authenticate, invoke a model, or establish ACP compatibility.
func TestAgentGuestContracts(t *testing.T) {
	if os.Getenv("PULSAR_REAL_AGENT_CONTRACTS") != "1" {
		t.Skip("set PULSAR_REAL_AGENT_CONTRACTS=1 for the real offline guest inventory")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	staging, err := os.MkdirTemp(root, ".agent-contract-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(staging); err != nil {
			t.Error(err)
		}
	})
	absolute, err := filepath.Abs(staging)
	if err != nil {
		t.Fatal(err)
	}
	backend := runtime.NewSbxBackend(nil, absolute)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if diagnostic := backend.Check(ctx); !diagnostic.Ready {
		t.Fatalf("preflight: %v", diagnostic.Messages)
	}
	guestAgent := "shell"
	if os.Getenv("PULSAR_AGENT_CONTRACT_CODEX") == "1" {
		guestAgent = "codex"
	}
	env, err := backend.Create(ctx, runtime.EnvironmentSpec{TaskID: "agent-contract-inventory", Agent: guestAgent, TemplateRef: runtime.SbxTemplate, Workspace: runtime.WorkspaceNone, Skills: runtime.SkillsOff, CPUs: 2, Memory: "2g"})
	if err != nil {
		if env.ID != "" {
			cleanup, done := context.WithTimeout(context.Background(), 45*time.Second)
			cleanupErr := backend.Remove(cleanup, env)
			done()
			if cleanupErr != nil {
				t.Errorf("cleanup confirmed failed-create handle: %v", cleanupErr)
			}
		}
		t.Fatalf("create: %v; unknown outcomes require independent inspection, not speculative removal", err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 45*time.Second)
		defer done()
		if err := backend.Remove(cleanup, env); err != nil {
			t.Errorf("remove owned guest: %v", err)
		}
	})
	inspected, err := backend.Inspect(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.ID != env.ID || inspected.Name != env.Name || inspected.Agent != guestAgent {
		t.Fatal("guest identity mismatch")
	}
	for _, binary := range []string{"claude", "codex", "copilot", "cursor-agent", "devin", "docker-agent", "droid", "gemini", "kiro-cli", "opencode", "dsh"} {
		t.Run(binary, func(t *testing.T) {
			probe, done := context.WithTimeout(ctx, 30*time.Second)
			defer done()
			// Binary names are a compile-time allowlist, not caller-controlled shell input.
			output, err := backend.Exec(probe, env, runtime.ExecRequest{User: "agent", Command: []string{"sh", "-c", "if command -v " + binary + " >/dev/null 2>&1; then printf PRESENT; else printf ABSENT; fi"}})
			if err != nil {
				t.Fatalf("availability probe: %v", err)
			}
			if output == "ABSENT" {
				t.Log("UNSUPPORTED: binary absent in pinned shell template")
				return
			}
			if output != "PRESENT" {
				t.Fatal("unexpected availability response; output withheld")
			}
			version, err := backend.Exec(probe, env, runtime.ExecRequest{User: "agent", Command: []string{binary, "--version"}})
			if err != nil {
				t.Fatalf("version probe: %v", err)
			}
			if len(version) > 512 || strings.ContainsAny(version, "\x00") {
				t.Fatal("invalid version response")
			}
			t.Logf("guest version: %s", strings.TrimSpace(version))
			if binary == "claude" {
				if _, err := agentadapter.NewClaude(agentadapter.TrustedClaudeConfig{VersionOutput: version}); err != nil {
					t.Fatalf("guest Claude does not match pinned planner: %v", err)
				}
			}
			help, err := backend.Exec(probe, env, runtime.ExecRequest{User: "agent", Command: []string{binary, "--help"}})
			if err != nil {
				t.Fatalf("help probe: %v", err)
			}
			if strings.TrimSpace(help) == "" {
				t.Fatal("empty help")
			}
			for _, flag := range []string{"--bare", "--restricted", "--print", "--strict-mcp-config", "--mcp-config", "--permission-mode", "--permission-prompts", "--tools", "--allowedTools", "--disable-slash-commands", "--setting-sources", "--no-chrome", "--no-session-persistence", "--output-format", "--max-budget-usd"} {
				if binary == "claude" && !strings.Contains(help, flag) {
					t.Errorf("planned Claude flag missing from guest help: %s", flag)
				}
			}
			t.Log("CLI help available; authentication/model/MCP/ACP not tested")
		})
	}
}
