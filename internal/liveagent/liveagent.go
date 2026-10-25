// Package liveagent wires trusted desktop Agent execution plans to approved guests.
package liveagent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/agentadapter"
	"github.com/Luo-root/pulsar-dock/internal/modelproxy"
	"github.com/Luo-root/pulsar-dock/internal/runtime"
)

const (
	ClaudeID         = "claude"
	ClaudeVersion    = agentadapter.ClaudeVersion
	ClaudeCLIPath    = "/home/agent/.local/bin/claude"
	ClaudeConfig     = agentadapter.GuestMCPConfigPath
	ClaudeModel      = "auto/gpt-6.1-sol"
	OpenCodeID       = "opencode"
	OpenCodeVersion  = "1.19.4"
	OpenCodeCLIPath  = "/opt/pulsar-agents/opencode"
	OpenCodeConfig   = "/etc/pulsar-agents/opencode/config.json"
	OpenCodeLauncher = "/opt/pulsar-agents/opencode-launcher"
	OpenCodeModel    = "pulsar/deepseek-flash"
	CodexID          = "codex"
	CodexVersion     = "0.164.0"
	CodexCLIPath     = "/opt/pulsar-agents/codex"
	CodexConfigRoot  = "/etc/pulsar-agents/codex"
	CodexConfig      = CodexConfigRoot + "/config.toml"
	CodexHome        = "/tmp/pulsar-codex-home"
	CodexLauncher    = "/opt/pulsar-agents/codex-launcher"
	CodexModel       = "deepseek-flash"
	GateHost         = "127.0.0.1"
	GatePort         = 3425

	claudeLauncher = "/opt/pulsar-agents/claude-launcher"
	claudeToken    = "/etc/pulsar-agents/claude/token"
	opencodeToken  = "/etc/pulsar-agents/opencode/token"
	codexToken     = CodexConfigRoot + "/token"
)

var ErrUnsupported = errors.New("agent is not ready for live execution")

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

type Definition struct {
	ID, DisplayName, Version, CLIPath, ConfigPath, Model string
	Installed, CLIVerified, ModelVerified                bool
}

func Definitions() []Definition {
	return []Definition{
		{ID: ClaudeID, DisplayName: "Claude", Version: ClaudeVersion, CLIPath: ClaudeCLIPath, ConfigPath: ClaudeConfig, Model: ClaudeModel, Installed: true, CLIVerified: true, ModelVerified: false},
		{ID: CodexID, DisplayName: "Codex", Version: CodexVersion, CLIPath: CodexCLIPath, ConfigPath: CodexConfig, Model: CodexModel, Installed: true, CLIVerified: true, ModelVerified: false},
		{ID: OpenCodeID, DisplayName: "OpenCode", Version: OpenCodeVersion, CLIPath: OpenCodeCLIPath, ConfigPath: OpenCodeConfig, Model: OpenCodeModel, Installed: true, CLIVerified: true, ModelVerified: false},
	}
}

type ClaudePlanner struct{}

func (ClaudePlanner) Plan(r agentadapter.Request) (runtime.ExecRequest, error) {
	if !validRequest(r) {
		return runtime.ExecRequest{}, errors.New("invalid Claude execution request")
	}
	return runtime.ExecRequest{User: "agent", Command: []string{
		claudeLauncher, "--bare", "--restricted", "--print", "--strict-mcp-config", "--mcp-config", ClaudeConfig,
		"--permission-mode", "dontAsk", "--permission-prompts", "none", "--tools", "", "--allowedTools", "mcp__dock__*",
		"--disable-slash-commands", "--setting-sources", "", "--no-chrome", "--no-session-persistence",
		"--output-format", "stream-json", "--verbose", "--max-budget-usd", "1", "--", r.Goal,
	}}, nil
}

func validRequest(r agentadapter.Request) bool {
	return r.MCPConfigPath == ClaudeConfig && r.MaxBudgetUSD > 0 && r.MaxBudgetUSD < 100000 && strings.TrimSpace(r.Goal) != "" && len(r.Goal) <= agentadapter.MaxGoalBytes && !strings.ContainsAny(r.Goal, "\x00\r\n")
}

// ClaudeVerifier prepares one owned guest before every execution. Gate listeners
// and tokens are process-private; neither token nor upstream credential enters argv.
type ClaudeVerifier struct {
	Backend   runtime.RuntimeBackend
	Staging   string
	mu        sync.Mutex
	prepareMu sync.Mutex
	instances map[string]*modelInstance
}

type modelInstance struct {
	env      runtime.Environment
	server   *http.Server
	listener net.Listener
	proxy    *modelproxy.Proxy
}

func (v *ClaudeVerifier) Verify(ctx context.Context, env runtime.Environment) (err error) {
	if v == nil || v.Backend == nil || env.ID == "" || env.Backend != v.Backend.Name() || env.Agent != "shell" || filepath.Clean(v.Staging) != v.Staging || !filepath.IsAbs(v.Staging) {
		return ErrUnsupported
	}
	if err = v.prepare(ctx, env); err != nil {
		return err
	}
	verified := false
	defer func() {
		if err != nil && !verified {
			_ = v.Cleanup(context.Background(), env)
		}
	}()
	got, err := v.Backend.Inspect(ctx, env)
	if err != nil || got.ID != env.ID || got.Name != env.Name || got.State != "running" {
		return errors.Join(ErrUnsupported, err)
	}
	path, err := v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "agent", Command: []string{"sh", "-c", "command -v claude"}})
	if err != nil || strings.TrimSpace(path) != ClaudeCLIPath {
		return errors.Join(ErrUnsupported, err)
	}
	out, err := v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "agent", Command: []string{ClaudeCLIPath, "--version"}})
	if err != nil || strings.TrimSpace(out) != ClaudeVersion+" (Claude Code)" {
		return errors.Join(ErrUnsupported, err)
	}
	verified = true
	return nil
}

func (v *ClaudeVerifier) Cleanup(ctx context.Context, env runtime.Environment) error {
	if v == nil {
		return nil
	}
	v.prepareMu.Lock()
	defer v.prepareMu.Unlock()
	v.mu.Lock()
	in := v.instances[env.ID]
	v.mu.Unlock()
	if in == nil {
		return nil
	}
	if v.Backend != nil {
		if err := v.Backend.SetNetworkPolicy(ctx, env, nil); err != nil {
			return err
		}
	}
	err := in.server.Close()
	in.proxy.Close()
	v.mu.Lock()
	delete(v.instances, env.ID)
	v.mu.Unlock()
	return err
}

func (v *ClaudeVerifier) prepare(ctx context.Context, env runtime.Environment) error {
	v.prepareMu.Lock()
	defer v.prepareMu.Unlock()
	v.mu.Lock()
	if v.instances == nil {
		v.instances = map[string]*modelInstance{}
	}
	if _, ok := v.instances[env.ID]; ok {
		v.mu.Unlock()
		return nil
	}
	v.mu.Unlock()
	if err := os.MkdirAll(v.Staging, 0700); err != nil {
		return ErrUnsupported
	}
	proxy, err := modelproxy.New(modelproxy.Config{UpstreamURL: fmt.Sprintf("http://%s:%d", GateHost, GatePort), UpstreamToken: "magpie", Model: ClaudeModel, UpstreamModel: "auto/gpt-6.1-sol"})
	if err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		proxy.Close()
		return errors.Join(ErrUnsupported, err)
	}
	server := &http.Server{Handler: proxy}
	go func() { _ = server.Serve(ln) }()
	_, portText, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	_, _ = fmt.Sscan(portText, &port)
	if err = v.Backend.SetNetworkPolicy(ctx, env, []runtime.NetworkRule{
		{Host: "host.docker.internal", Port: port, Action: "allow"},
		{Host: "localhost", Port: port, Action: "allow"},
	}); err != nil {
		_ = server.Close()
		proxy.Close()
		return errors.Join(ErrUnsupported, err)
	}
	committed := false
	policySet := true
	defer func() {
		if !committed {
			if policySet && v.Backend != nil {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = v.Backend.SetNetworkPolicy(cleanupCtx, env, nil)
				cancel()
			}
			_ = server.Close()
			proxy.Close()
		}
	}()
	if _, err = v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"mkdir", "-p", "--", "/tmp/pulsar-dock", "/etc/pulsar-agents/claude", "/opt/pulsar-agents"}}); err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	token := proxy.GuestToken()
	files := map[string][]byte{
		"mcp.json": []byte("{\"mcpServers\":{}}\n"),
		"token":    []byte(token + "\n"),
		"launcher": []byte("#!/bin/sh\nset -eu\nexport ANTHROPIC_BASE_URL=http://host.docker.internal:" + portText + "\nexport ANTHROPIC_API_KEY=$(cat " + claudeToken + ")\nexec " + ClaudeCLIPath + " \"$@\"\n"),
	}
	for name, data := range files {
		tmp, e := os.CreateTemp(v.Staging, "claude-")
		if e != nil {
			_ = server.Close()
			proxy.Close()
			return ErrUnsupported
		}
		path := tmp.Name()
		_, e = tmp.Write(data)
		_ = tmp.Close()
		if e != nil {
			_ = os.Remove(path)
			_ = server.Close()
			proxy.Close()
			return ErrUnsupported
		}
		guest := "/tmp/pulsar-dock/mcp.json"
		if name == "token" {
			guest = "/tmp/pulsar-dock/claude-token"
		}
		if name == "launcher" {
			guest = "/tmp/pulsar-dock/claude-launcher"
		}
		_, e = v.Backend.CopyIn(ctx, env, path, guest)
		_ = os.Remove(path)
		if e != nil {
			_ = server.Close()
			proxy.Close()
			return errors.Join(ErrUnsupported, e)
		}
	}
	if _, err = v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"mv", "--", "/tmp/pulsar-dock/claude-token", claudeToken}}); err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	if _, err = v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"mv", "--", "/tmp/pulsar-dock/claude-launcher", claudeLauncher}}); err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	if _, err = v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"chown", "root:root", "--", ClaudeConfig, claudeToken, claudeLauncher}}); err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	if _, err = v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"chmod", "0644", "--", ClaudeConfig, claudeToken}}); err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	if _, err = v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"chmod", "0555", "--", claudeLauncher}}); err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	v.mu.Lock()
	v.instances[env.ID] = &modelInstance{env: env, server: server, listener: ln, proxy: proxy}
	v.mu.Unlock()
	committed = true
	return nil
}

func (v *ClaudeVerifier) Close() error {
	if v == nil {
		return nil
	}
	v.prepareMu.Lock()
	defer v.prepareMu.Unlock()
	v.mu.Lock()
	defer v.mu.Unlock()
	var err error
	for id, in := range v.instances {
		policyErr := error(nil)
		if v.Backend != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			policyErr = v.Backend.SetNetworkPolicy(cleanupCtx, in.env, nil)
			cancel()
		}
		err = errors.Join(err, policyErr, in.server.Close())
		in.proxy.Close()
		if policyErr == nil {
			delete(v.instances, id)
		}
	}
	return err
}

// OpenCodePlanner exposes only the fixed non-interactive runner contract. The
// goal is the sole caller-controlled value; config, model and credentials are
// provisioned by OpenCodeVerifier and never accepted from the request.
type OpenCodePlanner struct{}

func (OpenCodePlanner) Plan(r agentadapter.Request) (runtime.ExecRequest, error) {
	if r.MCPConfigPath != agentadapter.GuestMCPConfigPath || r.MaxBudgetUSD <= 0 || r.MaxBudgetUSD >= 100000 ||
		strings.TrimSpace(r.Goal) == "" || len(r.Goal) > agentadapter.MaxGoalBytes ||
		strings.ContainsAny(r.Goal, "\x00\r\n") {
		return runtime.ExecRequest{}, errors.New("invalid OpenCode execution request")
	}
	return runtime.ExecRequest{User: "agent", Command: []string{
		OpenCodeLauncher, "run", r.Goal,
	}}, nil
}

// OpenCodeVerifier provisions one fixed OpenAI-compatible config into an owned
// guest. The token is delivered only through a root-owned file and is never in
// argv or logs.
type OpenCodeVerifier struct {
	Backend   runtime.RuntimeBackend
	Staging   string
	mu        sync.Mutex
	prepareMu sync.Mutex
	instances map[string]*modelInstance
}

func (v *OpenCodeVerifier) Verify(ctx context.Context, env runtime.Environment) (err error) {
	if v == nil || v.Backend == nil || env.ID == "" || env.Backend != v.Backend.Name() || env.Agent != "shell" || filepath.Clean(v.Staging) != v.Staging || !filepath.IsAbs(v.Staging) {
		return ErrUnsupported
	}
	if err = v.prepare(ctx, env); err != nil {
		return err
	}
	verified := false
	defer func() {
		if err != nil && !verified {
			_ = v.Cleanup(context.Background(), env)
		}
	}()
	got, err := v.Backend.Inspect(ctx, env)
	if err != nil || got.ID != env.ID || got.Name != env.Name || got.State != "running" {
		return errors.Join(ErrUnsupported, err)
	}
	path, err := v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "agent", Command: []string{"sh", "-c", "command -v " + OpenCodeCLIPath}})
	if err != nil || strings.TrimSpace(path) != OpenCodeCLIPath {
		return errors.Join(ErrUnsupported, err)
	}
	version, err := v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "agent", Command: []string{OpenCodeCLIPath, "--version"}})
	if err != nil || strings.TrimSpace(version) != OpenCodeVersion {
		return errors.Join(ErrUnsupported, err)
	}
	if _, err = v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "agent", Command: []string{OpenCodeCLIPath, "--help"}}); err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	verified = true
	return nil
}

func (v *OpenCodeVerifier) Cleanup(ctx context.Context, env runtime.Environment) error {
	v.prepareMu.Lock()
	defer v.prepareMu.Unlock()
	if v == nil {
		return nil
	}
	v.mu.Lock()
	in := v.instances[env.ID]
	v.mu.Unlock()
	if in == nil {
		return nil
	}
	if v.Backend != nil {
		if err := v.Backend.SetNetworkPolicy(ctx, env, nil); err != nil {
			return err
		}
	}
	err := in.server.Close()
	in.proxy.Close()
	v.mu.Lock()
	delete(v.instances, env.ID)
	v.mu.Unlock()
	return err
}

func (v *OpenCodeVerifier) prepare(ctx context.Context, env runtime.Environment) error {
	v.prepareMu.Lock()
	defer v.prepareMu.Unlock()
	v.mu.Lock()
	if v.instances == nil {
		v.instances = map[string]*modelInstance{}
	}
	if _, ok := v.instances[env.ID]; ok {
		v.mu.Unlock()
		return nil
	}
	v.mu.Unlock()
	if err := os.MkdirAll(v.Staging, 0700); err != nil {
		return ErrUnsupported
	}
	proxy, err := modelproxy.New(modelproxy.Config{UpstreamURL: fmt.Sprintf("http://%s:%d", GateHost, GatePort), UpstreamToken: "magpie", Model: OpenCodeModel, UpstreamModel: "deepseek/deepseek-flash"})
	if err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		proxy.Close()
		return errors.Join(ErrUnsupported, err)
	}
	server := &http.Server{Handler: proxy}
	go func() { _ = server.Serve(ln) }()
	_, portText, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	_, _ = fmt.Sscan(portText, &port)
	if err = v.Backend.SetNetworkPolicy(ctx, env, []runtime.NetworkRule{{Host: "host.docker.internal", Port: port, Action: "allow"}, {Host: "localhost", Port: port, Action: "allow"}}); err != nil {
		_ = server.Close()
		proxy.Close()
		return errors.Join(ErrUnsupported, err)
	}
	committed := false
	policySet := true
	defer func() {
		if !committed {
			if policySet && v.Backend != nil {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = v.Backend.SetNetworkPolicy(cleanupCtx, env, nil)
				cancel()
			}
			_ = server.Close()
			proxy.Close()
		}
	}()
	if _, err = v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"mkdir", "-p", "--", "/tmp/pulsar-dock", "/etc/pulsar-agents/opencode", "/opt/pulsar-agents"}}); err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	config := fmt.Sprintf("{\"$schema\":\"https://opencode.ai/config.json\",\"model\":%q,\"provider\":{\"pulsar\":{\"npm\":\"@ai-sdk/openai-compatible\",\"options\":{\"baseURL\":\"http://host.docker.internal:%s/v1\",\"apiKey\":\"{env:OPENCODE_GATE_TOKEN}\"},\"models\":{%q:{}}}}}\n", OpenCodeModel, portText, "deepseek-flash")
	launcher := []byte("#!/bin/sh\nset -eu\nexport OPENCODE_CONFIG=" + OpenCodeConfig + "\nexport OPENCODE_GATE_TOKEN=$(cat " + opencodeToken + ")\nexec " + OpenCodeCLIPath + " \\\"$@\\\"\n")
	files := map[string][]byte{"config": []byte(config), "token": []byte(proxy.GuestToken() + "\n"), "launcher": launcher}
	for name, data := range files {
		tmp, e := os.CreateTemp(v.Staging, "opencode-")
		if e != nil {
			return ErrUnsupported
		}
		path := tmp.Name()
		_, e = tmp.Write(data)
		_ = tmp.Close()
		if e != nil {
			_ = os.Remove(path)
			return ErrUnsupported
		}
		guest := "/tmp/pulsar-dock/opencode-config"
		if name == "token" {
			guest = "/tmp/pulsar-dock/opencode-token"
		}
		if name == "launcher" {
			guest = "/tmp/pulsar-dock/opencode-launcher"
		}
		if _, e = v.Backend.CopyIn(ctx, env, path, guest); e != nil {
			_ = os.Remove(path)
			return errors.Join(ErrUnsupported, e)
		}
		_ = os.Remove(path)
	}
	commands := [][]string{{"mv", "--", "/tmp/pulsar-dock/opencode-config", OpenCodeConfig}, {"mv", "--", "/tmp/pulsar-dock/opencode-token", opencodeToken}, {"mv", "--", "/tmp/pulsar-dock/opencode-launcher", OpenCodeLauncher}, {"chown", "root:root", "--", OpenCodeConfig, OpenCodeLauncher}, {"chown", "root:agent", "--", opencodeToken}, {"chmod", "0644", "--", OpenCodeConfig}, {"chmod", "0640", "--", opencodeToken}, {"chmod", "0555", "--", OpenCodeLauncher}}
	for _, command := range commands {
		if _, err = v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: command}); err != nil {
			return errors.Join(ErrUnsupported, err)
		}
	}
	v.mu.Lock()
	v.instances[env.ID] = &modelInstance{env: env, server: server, listener: ln, proxy: proxy}
	v.mu.Unlock()
	committed = true
	return nil
}

func (v *OpenCodeVerifier) Close() error {
	if v == nil {
		return nil
	}
	v.prepareMu.Lock()
	defer v.prepareMu.Unlock()
	v.mu.Lock()
	defer v.mu.Unlock()
	var err error
	for id, in := range v.instances {
		policyErr := error(nil)
		if v.Backend != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			policyErr = v.Backend.SetNetworkPolicy(cleanupCtx, in.env, nil)
			cancel()
		}
		err = errors.Join(err, policyErr, in.server.Close())
		in.proxy.Close()
		if policyErr == nil {
			delete(v.instances, id)
		}
	}
	return err
}

// CodexPlanner exposes only the fixed non-interactive Responses runner contract.
type CodexPlanner struct{}

func (CodexPlanner) Plan(r agentadapter.Request) (runtime.ExecRequest, error) {
	if r.MCPConfigPath != agentadapter.GuestMCPConfigPath || r.MaxBudgetUSD <= 0 || r.MaxBudgetUSD >= 100000 ||
		strings.TrimSpace(r.Goal) == "" || len(r.Goal) > agentadapter.MaxGoalBytes ||
		strings.ContainsAny(r.Goal, "\x00\r\n") {
		return runtime.ExecRequest{}, errors.New("invalid Codex execution request")
	}
	return runtime.ExecRequest{User: "agent", Command: []string{
		"sh", "-c", "printf '%s' \"$1\" | \"$2\" --no-daemon exec --ephemeral --skip-git-repo-check --sandbox read-only --json -", "pulsar-codex", r.Goal, CodexLauncher,
	}}, nil
}

// CodexVerifier provisions a fixed Responses-compatible provider config and a
// scoped token into the owned guest. It does not trust UI catalogue state.
type CodexVerifier struct {
	Backend   runtime.RuntimeBackend
	Staging   string
	mu        sync.Mutex
	prepareMu sync.Mutex
	instances map[string]*modelInstance
}

func (v *CodexVerifier) Verify(ctx context.Context, env runtime.Environment) (err error) {
	if v == nil || v.Backend == nil || env.ID == "" || env.Backend != v.Backend.Name() || env.Agent != "shell" || filepath.Clean(v.Staging) != v.Staging || !filepath.IsAbs(v.Staging) {
		return ErrUnsupported
	}
	if err = v.prepare(ctx, env); err != nil {
		return err
	}
	verified := false
	defer func() {
		if err != nil && !verified {
			_ = v.Cleanup(context.Background(), env)
		}
	}()
	got, err := v.Backend.Inspect(ctx, env)
	if err != nil || got.ID != env.ID || got.Name != env.Name || got.State != "running" {
		return errors.Join(ErrUnsupported, err)
	}
	path, err := v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "agent", Command: []string{"sh", "-c", "command -v " + CodexCLIPath}})
	if err != nil || strings.TrimSpace(path) != CodexCLIPath {
		return errors.Join(ErrUnsupported, err)
	}
	version, err := v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "agent", Command: []string{CodexCLIPath, "--version"}})
	if err != nil || strings.TrimSpace(version) != "codex-cli "+CodexVersion {
		return errors.Join(ErrUnsupported, err)
	}
	verified = true
	return nil
}

func (v *CodexVerifier) Cleanup(ctx context.Context, env runtime.Environment) error {
	v.prepareMu.Lock()
	defer v.prepareMu.Unlock()
	if v == nil {
		return nil
	}
	v.mu.Lock()
	in := v.instances[env.ID]
	v.mu.Unlock()
	if in == nil {
		return nil
	}
	if v.Backend != nil {
		if err := v.Backend.SetNetworkPolicy(ctx, env, nil); err != nil {
			return err
		}
	}
	err := in.server.Close()
	in.proxy.Close()
	v.mu.Lock()
	delete(v.instances, env.ID)
	v.mu.Unlock()
	return err
}

func (v *CodexVerifier) prepare(ctx context.Context, env runtime.Environment) error {
	v.prepareMu.Lock()
	defer v.prepareMu.Unlock()
	v.mu.Lock()
	if v.instances == nil {
		v.instances = map[string]*modelInstance{}
	}
	if _, ok := v.instances[env.ID]; ok {
		v.mu.Unlock()
		return nil
	}
	v.mu.Unlock()
	if err := os.MkdirAll(v.Staging, 0700); err != nil {
		return ErrUnsupported
	}
	proxy, err := modelproxy.New(modelproxy.Config{UpstreamURL: fmt.Sprintf("http://%s:%d", GateHost, GatePort), UpstreamToken: "magpie", Model: CodexModel, UpstreamModel: "deepseek/deepseek-flash"})
	if err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		proxy.Close()
		return errors.Join(ErrUnsupported, err)
	}
	server := &http.Server{Handler: proxy}
	go func() { _ = server.Serve(ln) }()
	_, portText, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	_, _ = fmt.Sscan(portText, &port)
	if err = v.Backend.SetNetworkPolicy(ctx, env, []runtime.NetworkRule{{Host: "host.docker.internal", Port: port, Action: "allow"}, {Host: "localhost", Port: port, Action: "allow"}}); err != nil {
		_ = server.Close()
		proxy.Close()
		return errors.Join(ErrUnsupported, err)
	}
	committed := false
	policySet := true
	defer func() {
		if !committed {
			if policySet && v.Backend != nil {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = v.Backend.SetNetworkPolicy(cleanupCtx, env, nil)
				cancel()
			}
			_ = server.Close()
			proxy.Close()
		}
	}()
	if _, err = v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"mkdir", "-p", "--", "/tmp/pulsar-dock", CodexConfigRoot, "/opt/pulsar-agents"}}); err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	config := fmt.Sprintf("model_provider = \"pulsar\"\nmodel = %q\n\n[model_providers.pulsar]\nname = \"Pulsar\"\nbase_url = \"http://host.docker.internal:%s/v1\"\nenv_key = \"CODEX_GATE_TOKEN\"\nwire_api = \"responses\"\n", CodexModel, portText)
	launcher := []byte("#!/bin/sh\nset -eu\nrm -rf " + CodexHome + "\nmkdir -p " + CodexHome + "\ncp " + CodexConfig + " " + CodexHome + "/config.toml\nchmod 0644 " + CodexHome + "/config.toml\nexport CODEX_HOME=" + CodexHome + "\nexport CODEX_GATE_TOKEN=$(cat " + codexToken + ")\nexec " + CodexCLIPath + " \"$@\"\n")
	files := map[string][]byte{"config": []byte(config), "token": []byte(proxy.GuestToken() + "\n"), "launcher": launcher}
	for name, data := range files {
		tmp, e := os.CreateTemp(v.Staging, "codex-")
		if e != nil {
			return ErrUnsupported
		}
		path := tmp.Name()
		_, e = tmp.Write(data)
		_ = tmp.Close()
		if e != nil {
			_ = os.Remove(path)
			return ErrUnsupported
		}
		guest := "/tmp/pulsar-dock/codex-config"
		if name == "token" {
			guest = "/tmp/pulsar-dock/codex-token"
		}
		if name == "launcher" {
			guest = "/tmp/pulsar-dock/codex-launcher"
		}
		if _, e = v.Backend.CopyIn(ctx, env, path, guest); e != nil {
			_ = os.Remove(path)
			return errors.Join(ErrUnsupported, e)
		}
		_ = os.Remove(path)
	}
	commands := [][]string{{"mv", "--", "/tmp/pulsar-dock/codex-config", CodexConfig}, {"mv", "--", "/tmp/pulsar-dock/codex-token", codexToken}, {"mv", "--", "/tmp/pulsar-dock/codex-launcher", CodexLauncher}, {"chown", "root:root", "--", CodexConfig, CodexLauncher}, {"chown", "root:agent", "--", codexToken}, {"chmod", "0644", "--", CodexConfig}, {"chmod", "0640", "--", codexToken}, {"chmod", "0555", "--", CodexLauncher}}
	for _, command := range commands {
		if _, err = v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: command}); err != nil {
			return errors.Join(ErrUnsupported, err)
		}
	}
	v.mu.Lock()
	v.instances[env.ID] = &modelInstance{env: env, server: server, listener: ln, proxy: proxy}
	v.mu.Unlock()
	committed = true
	return nil
}

func (v *CodexVerifier) Close() error {
	if v == nil {
		return nil
	}
	v.prepareMu.Lock()
	defer v.prepareMu.Unlock()
	v.mu.Lock()
	defer v.mu.Unlock()
	var err error
	for id, in := range v.instances {
		policyErr := error(nil)
		if v.Backend != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			policyErr = v.Backend.SetNetworkPolicy(cleanupCtx, in.env, nil)
			cancel()
		}
		err = errors.Join(err, policyErr, in.server.Close())
		in.proxy.Close()
		if policyErr == nil {
			delete(v.instances, id)
		}
	}
	return err
}

type UnsupportedVerifier struct{}

func (UnsupportedVerifier) Verify(context.Context, runtime.Environment) error { return ErrUnsupported }

type unsupportedPlanner struct{}

func (unsupportedPlanner) Plan(agentadapter.Request) (runtime.ExecRequest, error) {
	return runtime.ExecRequest{}, ErrUnsupported
}

func Components(agent string, backend runtime.RuntimeBackend, staging ...string) (interface {
	Plan(agentadapter.Request) (runtime.ExecRequest, error)
}, interface {
	Verify(context.Context, runtime.Environment) error
}, error) {
	switch agent {
	case ClaudeID:
		if backend == nil || len(staging) != 1 {
			return nil, nil, ErrUnsupported
		}
		return ClaudePlanner{}, &ClaudeVerifier{Backend: backend, Staging: staging[0]}, nil
	case CodexID:
		if backend == nil || len(staging) != 1 {
			return nil, nil, ErrUnsupported
		}
		return CodexPlanner{}, &CodexVerifier{Backend: backend, Staging: staging[0]}, nil
	case OpenCodeID:
		if backend == nil || len(staging) != 1 {
			return nil, nil, ErrUnsupported
		}
		return OpenCodePlanner{}, &OpenCodeVerifier{Backend: backend, Staging: staging[0]}, nil
	default:
		return nil, nil, ErrUnsupported
	}
}
