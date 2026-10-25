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

	"github.com/Luo-root/pulsar-dock/internal/agentadapter"
	"github.com/Luo-root/pulsar-dock/internal/modelproxy"
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

	claudeLauncher = "/opt/pulsar-agents/claude-launcher"
	claudeToken    = "/etc/pulsar-agents/claude/token"
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
		{ID: "codex", DisplayName: "Codex", Version: "0.164.0"},
		{ID: "opencode", DisplayName: "OpenCode", Version: "1.19.4"},
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
		"--output-format", "stream-json", "--verbose", "--max-budget-usd", "1", "--model", ClaudeModel, "--", r.Goal,
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
	instances map[string]*modelInstance
}

type modelInstance struct {
	server   *http.Server
	listener net.Listener
	proxy    *modelproxy.Proxy
}

func (v *ClaudeVerifier) Verify(ctx context.Context, env runtime.Environment) error {
	if v == nil || v.Backend == nil || env.ID == "" || env.Backend != v.Backend.Name() || env.Agent != ClaudeID || filepath.Clean(v.Staging) != v.Staging || !filepath.IsAbs(v.Staging) {
		return ErrUnsupported
	}
	if err := v.prepare(ctx, env); err != nil {
		return err
	}
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
	return nil
}

func (v *ClaudeVerifier) prepare(ctx context.Context, env runtime.Environment) error {
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
	proxy, err := modelproxy.New(modelproxy.Config{UpstreamURL: fmt.Sprintf("http://%s:%d", GateHost, GatePort), UpstreamToken: "magpie", Model: ClaudeModel})
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
	if err = v.Backend.SetNetworkPolicy(ctx, env, []runtime.NetworkRule{{Host: "localhost", Port: port, Action: "allow"}}); err != nil {
		_ = server.Close()
		proxy.Close()
		return errors.Join(ErrUnsupported, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = server.Close()
			proxy.Close()
		}
	}()
	if _, err = v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"mkdir", "-p", "--", "/tmp/pulsar-dock", "/etc/pulsar-agents", "/opt/pulsar-agents"}}); err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	token := proxy.GuestToken()
	files := map[string][]byte{
		"mcp.json": []byte("{}\n"),
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
	if _, err = v.Backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"mv", "--", "/tmp/pulsar-dock/mcp.json", ClaudeConfig}}); err != nil {
		return errors.Join(ErrUnsupported, err)
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
	v.instances[env.ID] = &modelInstance{server: server, listener: ln, proxy: proxy}
	v.mu.Unlock()
	committed = true
	return nil
}

func (v *ClaudeVerifier) Close() error {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	var err error
	for id, in := range v.instances {
		err = errors.Join(err, in.server.Close())
		in.proxy.Close()
		delete(v.instances, id)
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
	case "codex", "opencode":
		return unsupportedPlanner{}, UnsupportedVerifier{}, nil
	default:
		return nil, nil, ErrUnsupported
	}
}
