package liveagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/agentadapter"
	"github.com/Luo-root/pulsar-dock/internal/guestagent"
	"github.com/Luo-root/pulsar-dock/internal/runtime"
)

func TestRealOpenCodeGuest(t *testing.T) {
	if os.Getenv("PULSAR_REAL_OPENCODE_GUEST") != "1" {
		t.Skip("explicit real OpenCode opt-in required")
	}
	archive := os.Getenv("PULSAR_OPENCODE_ARCHIVE")
	if archive == "" {
		t.Skip("set PULSAR_OPENCODE_ARCHIVE to the approved archive")
	}
	archive, err := filepath.EvalSymlinks(mustAbs(t, archive))
	if err != nil {
		t.Fatal(err)
	}
	staging, err := filepath.EvalSymlinks(mustAbs(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	backend := runtime.NewSbxBackend(nil, staging)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if d := backend.Check(ctx); !d.Ready {
		t.Fatalf("preflight: %v", d.Messages)
	}
	env, err := backend.Create(ctx, runtime.EnvironmentSpec{TaskID: "real-opencode", Agent: "shell", TemplateRef: runtime.SbxTemplate, Workspace: runtime.WorkspaceNone, Skills: runtime.SkillsOff, CPUs: 2, Memory: "2g"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("owned guest name=%s id=%s", env.Name, env.ID)
	defer func() {
		c, d := context.WithTimeout(context.Background(), 45*time.Second)
		defer d()
		if e := backend.Remove(c, env); e != nil {
			t.Errorf("cleanup: %v", e)
		}
	}()
	if err := guestagent.Provision(ctx, backend, env, archive, staging, "opencode"); err != nil {
		t.Fatalf("OpenCode provisioning rejected: %v archive=%s staging=%s envAgent=%s envBackend=%s", err, archive, staging, env.Agent, env.Backend)
	}
	v, err := guestagent.NewGuestVerifier(backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Verify(ctx, env, "opencode"); err != nil {
		t.Fatal(err)
	}
	_, verifier, err := Components(OpenCodeID, backend, staging)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(ctx, env); err != nil {
		diag, _ := backend.Exec(ctx, env, runtime.ExecRequest{User: "agent", Command: []string{"sh", "-c", "/opt/pulsar-agents/opencode-launcher run --help 2>&1 | head -c 8192"}})
		t.Fatalf("OpenCode verifier: %v; guest launcher diagnostic=%s", err, diag)
	}
	planner, _, err := Components(OpenCodeID, backend, staging)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(agentadapter.Request{Goal: "Reply exactly PULSAR_OPENCODE_OK and nothing else.", MCPConfigPath: agentadapter.GuestMCPConfigPath, MaxBudgetUSD: 0.1})
	if err != nil {
		t.Fatal(err)
	}
	candidates := [][]string{
		plan.Command,
		{OpenCodeLauncher, "run", "--prompt", "Reply exactly PULSAR_OPENCODE_OK and nothing else."},
		{OpenCodeLauncher, "--prompt", "Reply exactly PULSAR_OPENCODE_OK and nothing else."},
		{OpenCodeLauncher, "run", "--model", OpenCodeModel, "Reply exactly PULSAR_OPENCODE_OK and nothing else."},
		{OpenCodeLauncher, "run", "--format", "json", "Reply exactly PULSAR_OPENCODE_OK and nothing else."},
	}
	for i, command := range candidates {
		wrapped := append([]string{"sh", "-c", "set +e; label=$1; shift; o=$(mktemp /tmp/opencode-out.XXXXXX); e=$(mktemp /tmp/opencode-err.XXXXXX); \"$@\" >\"$o\" 2>\"$e\"; rc=$?; marker=0; grep -q -- PULSAR_OPENCODE_OK \"$o\" && marker=1; printf 'CANDIDATE_%s_RC_%s_MARKER_%s\\n' \"$label\" \"$rc\" \"$marker\"; exit 0", "probe", fmt.Sprint(i)}, command...)
		out, err := backend.Exec(ctx, env, runtime.ExecRequest{User: "agent", Command: wrapped})
		if err != nil {
			t.Fatalf("OpenCode candidate %d diagnostic execution: %v", i, err)
		}
		if strings.Contains(out, "MARKER_1") {
			t.Logf("OpenCode candidate %d returned marker", i)

			t.Logf("OpenCode guest returned synthetic marker with candidate %d", i)
			return
		}
	}
	t.Fatal("marker absent for all OpenCode command candidates")
}
func TestRealCodexGuestCLI(t *testing.T) {
	if os.Getenv("PULSAR_REAL_CODEX_GUEST") != "1" {
		t.Skip("explicit real Codex opt-in required")
	}
	archive := os.Getenv("PULSAR_CODEX_ARCHIVE")
	if archive == "" {
		t.Skip("set PULSAR_CODEX_ARCHIVE to the approved archive")
	}
	archive, err := filepath.EvalSymlinks(mustAbs(t, archive))
	if err != nil {
		t.Fatal(err)
	}
	staging, err := filepath.EvalSymlinks(mustAbs(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	backend := runtime.NewSbxBackend(nil, staging)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if d := backend.Check(ctx); !d.Ready {
		t.Fatalf("preflight: %v", d.Messages)
	}
	env, err := backend.Create(ctx, runtime.EnvironmentSpec{TaskID: "real-codex", Agent: "shell", TemplateRef: runtime.SbxTemplate, Workspace: runtime.WorkspaceNone, Skills: runtime.SkillsOff, CPUs: 2, Memory: "2g"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("owned guest name=%s id=%s", env.Name, env.ID)
	defer func() {
		c, d := context.WithTimeout(context.Background(), 45*time.Second)
		defer d()
		if e := backend.Remove(c, env); e != nil {
			t.Errorf("cleanup: %v", e)
		}
	}()
	if err := guestagent.Provision(ctx, backend, env, archive, staging, "codex"); err != nil {
		t.Fatal(err)
	}
	for i, command := range [][]string{
		{"/opt/pulsar-agents/codex", "--version"},
		{"/opt/pulsar-agents/codex", "--help"},
		{"/opt/pulsar-agents/codex", "exec", "--help"},
	} {
		out, err := backend.Exec(ctx, env, runtime.ExecRequest{User: "agent", Command: command})
		if err != nil {
			t.Fatalf("candidate %d: %v", i, err)
		}
		if i == 0 && strings.Contains(out, "codex-cli "+CodexVersion) {
			t.Logf("Codex candidate %d returned pinned version", i)
		}
	}
	if os.Getenv("PULSAR_REAL_CODEX_MODEL") != "1" {
		t.Log("CLI only; no Codex model call")
		return
	}
	_, verifier, err := Components(CodexID, backend, staging)
	if err != nil {
		t.Fatal(err)
	}
	cv := verifier.(*CodexVerifier)
	defer cv.Close()
	if err := cv.Verify(ctx, env); err != nil {
		t.Fatalf("Codex preparation: %v", err)
	}
	planner, _, err := Components(CodexID, backend, staging)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(agentadapter.Request{Goal: "Reply exactly PULSAR_CODEX_OK and nothing else.", MCPConfigPath: agentadapter.GuestMCPConfigPath, MaxBudgetUSD: 0.1})
	if err != nil {
		t.Fatal(err)
	}
	candidates := [][]string{
		plan.Command,
		{CodexCLIPath, "exec", "--skip-git-repo-check", "--sandbox", "read-only", "--json", "-"},
	}
	for i, command := range candidates {
		wrapped := append([]string{"sh", "-c", "set -eu; export CODEX_HOME=" + CodexConfigRoot + "; export CODEX_GATE_TOKEN=$(cat " + codexToken + "); label=$1; shift; set +e; o=$(mktemp /tmp/codex-out.XXXXXX); e=$(mktemp /tmp/codex-err.XXXXXX); printf '%s' 'Reply exactly PULSAR_CODEX_OK and nothing else.' | \"$@\" >\"$o\" 2>\"$e\"; rc=$?; marker=0; grep -q -- PULSAR_CODEX_OK \"$o\" && marker=1; printf 'CANDIDATE_%s_RC_%s_MARKER_%s\\n' \"$label\" \"$rc\" \"$marker\"; exit 0", "probe", fmt.Sprint(i)}, command...)
		out, err := backend.Exec(ctx, env, runtime.ExecRequest{User: "agent", Command: wrapped})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "MARKER_1") {
			t.Logf("Codex candidate %d returned synthetic marker", i)
			t.Log("Codex guest returned synthetic model marker through scoped Responses proxy")
			return
		}
	}
	t.Fatal("Codex marker absent; output withheld")
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	x, e := filepath.Abs(p)
	if e != nil {
		t.Fatal(e)
	}
	return x
}

func TestRealClaudeGuest(t *testing.T) {
	if os.Getenv("PULSAR_REAL_CLAUDE_GUEST") != "1" {
		t.Skip("explicit real guest/model opt-in required")
	}
	staging, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	staging, err = filepath.EvalSymlinks(staging)
	if err != nil {
		t.Fatal(err)
	}
	backend := runtime.NewSbxBackend(nil, staging)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if d := backend.Check(ctx); !d.Ready {
		t.Fatalf("runtime preflight: %v", d.Messages)
	}
	env, err := backend.Create(ctx, runtime.EnvironmentSpec{TaskID: "real-claude-model", Agent: "shell", TemplateRef: runtime.SbxTemplate, Workspace: runtime.WorkspaceNone, Skills: runtime.SkillsOff, CPUs: 2, Memory: "2g"})
	if env.ID != "" {
		t.Logf("owned environment name=%s id=%s", env.Name, env.ID)
		defer func() {
			cleanup, done := context.WithTimeout(context.Background(), 45*time.Second)
			defer done()
			if e := backend.Remove(cleanup, env); e != nil {
				t.Errorf("owned cleanup: %v", e)
			}
		}()
	}
	if err != nil {
		t.Fatal(err)
	}
	path, err := backend.Exec(ctx, env, runtime.ExecRequest{User: "agent", Command: []string{"sh", "-c", "command -v claude"}})
	if err != nil {
		t.Fatal(err)
	}
	path = strings.TrimSpace(path)
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\x00\r\n ") {
		t.Fatal("invalid binary path")
	}
	t.Logf("preinstalled guest Claude path=%s", path)
	version, err := backend.Exec(ctx, env, runtime.ExecRequest{User: "agent", Command: []string{path, "--version"}})
	if err != nil || strings.TrimSpace(version) != ClaudeVersion+" (Claude Code)" {
		t.Fatal("guest version mismatch")
	}
	if os.Getenv("PULSAR_REAL_CLAUDE_MODEL") != "1" {
		t.Log("version/path only; no model call")
		return
	}
	v := &ClaudeVerifier{Backend: backend, Staging: staging}
	defer v.Close()
	if err := v.Verify(ctx, env); err != nil {
		t.Fatalf("guest preparation: %v", err)
	}
	plan, err := (ClaudePlanner{}).Plan(agentadapter.Request{Goal: "Reply exactly PULSAR_CLAUDE_GUEST_OK and nothing else. Do not call tools.", MCPConfigPath: ClaudeConfig, MaxBudgetUSD: 0.1})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := []string{"sh", "-c", `umask 077; o=$(mktemp /tmp/pulsar-out.XXXXXX); e=$(mktemp /tmp/pulsar-err.XXXXXX); "$@" >"$o" 2>"$e"; rc=$?; if [ "$rc" = 0 ]; then head -c 60000 "$o"; else printf 'CLI_EXIT_%s ' "$rc"; for phrase in 'unknown option' 'permission' 'restricted' 'bare' 'API' 'authentication' 'connection' 'mcp' 'Invalid' 'Error' 'error' 'not found' 'denied' 'Invalid permission mode' 'Invalid MCP configuration' 'Invalid API key' 'Permission denied' 'ENOENT' 'requires' 'not allowed' 'Failed to parse' 'EACCES' 'API Error: 401' 'API Error: 400' 'API Error: 404' 'unknown model'; do if grep -qi -- "$phrase" "$e" "$o"; then printf '%s ' "$phrase"; fi; done; token=$(cat /etc/pulsar-agents/claude/token); { cat "$e" "$o"; } | sed "s|$token|[REDACTED]|g" | sed -E 's|https?://[^[:space:]]+|[URL]|g;s/sk-[A-Za-z0-9._-]+/[KEY]/g' | head -c 4096; fi`, "probe"}
	wrapped = append(wrapped, plan.Command...)
	wrapped[2] = wrapped[2] + "; exit 0"
	output, err := backend.Exec(ctx, env, runtime.ExecRequest{User: "agent", Command: wrapped})
	if err != nil {
		t.Fatalf("guest model runner: %v", err)
	}
	if strings.HasPrefix(output, "CLI_EXIT_") {
		t.Fatalf("guest fixed diagnostic classes: %s", output)
	}
	if !strings.Contains(output, "PULSAR_CLAUDE_GUEST_OK") {
		t.Fatal("synthetic marker absent; output withheld")
	}
	t.Log("guest Claude returned synthetic model marker through scoped proxy")
}
