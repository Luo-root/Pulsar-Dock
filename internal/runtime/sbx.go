package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	SbxVersion  = "v0.49.2"
	SbxTemplate = "docker.io/docker/sandbox-templates@sha256:fd0013ca46d457583c9d68382e88b7c73472b4611a37012e7987d45c5ec19e3b"
	maxOutput   = 64 * 1024
	maxSnapshot = 4 * 1024 * 1024
)

var validAgents = map[string]bool{"claude": true, "codex": true, "shell": true, "copilot": true, "cursor": true, "devin": true, "docker-agent": true, "droid": true, "gemini": true, "kiro": true, "opencode": true}
var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{1,62}$`)
var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type ErrorKind string

const (
	ErrInvalid     ErrorKind = "invalid"
	ErrDenied      ErrorKind = "denied"
	ErrUnsupported ErrorKind = "unsupported"
	ErrNotFound    ErrorKind = "not_found"
	ErrRunner      ErrorKind = "runner"
)

type BackendError struct {
	Kind ErrorKind
	Op   string
	Err  error
}

func (e *BackendError) Error() string        { return e.Op + ": " + string(e.Kind) + ": " + e.Err.Error() }
func (e *BackendError) Unwrap() error        { return e.Err }
func berr(k ErrorKind, op, msg string) error { return &BackendError{k, op, errors.New(msg)} }

// CommandRunner returns stdout only. Implementations must not return stderr in errors.
type CommandRunner interface {
	Run(context.Context, ...string) ([]byte, error)
}
type OSRunner struct{ Bin string }
type commandFailure struct {
	cause error
	code  int
}

func (e *commandFailure) Error() string { return fmt.Sprintf("sbx command failed (exit %d)", e.code) }
func (e *commandFailure) Unwrap() error { return e.cause }
func (r OSRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	bin := r.Bin
	if bin == "" {
		bin = "sbx"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var out, stderr limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		code := -1
		var ex *exec.ExitError
		if errors.As(err, &ex) {
			code = ex.ExitCode()
		}
		return nil, &commandFailure{err, code}
	}
	if out.overflow {
		return nil, errors.New("sbx stdout exceeded limit")
	}
	return out.b.Bytes(), nil
}

type limitedBuffer struct {
	b        bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	left := maxOutput - b.b.Len()
	if len(p) > left {
		b.overflow = true
		p = p[:left]
	}
	_, _ = b.b.Write(p)
	return n, nil
}

type managed struct {
	env     Environment
	removed bool
	grants  map[string]bool
	// Lease metadata is protected by SbxBackend.mu; environment state/grants
	// are accessed only by the exclusive operation lease, never under a runner lock.
	quarantined    bool
	busy           bool
	controlPending bool
	cancel         context.CancelFunc
	done           chan struct{}
}
type SbxBackend struct {
	runner    CommandRunner
	mu        sync.Mutex
	owned     map[string]*managed
	outputDir string
}

// outputDir is a caller-owned private staging directory, required for copying.
// No default host temporary directory is used. Ownership is process-local only.
func NewSbxBackend(r CommandRunner, outputDir ...string) *SbxBackend {
	if r == nil {
		r = OSRunner{}
	}
	b := &SbxBackend{runner: r, owned: map[string]*managed{}}
	if len(outputDir) == 1 {
		b.outputDir = outputDir[0]
	}
	return b
}
func (b *SbxBackend) Name() string { return "sbx" }
func (b *SbxBackend) Capabilities(context.Context) CapabilitySet {
	return CapabilitySet{MicroVM: true, NetworkPolicy: true, WorkspaceModes: []WorkspaceMode{WorkspaceNone}, GuestExec: true}
}
func (b *SbxBackend) run(ctx context.Context, args ...string) ([]byte, error) {
	out, err := b.runner.Run(ctx, args...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &BackendError{ErrRunner, args[0], ctx.Err()}
		}
		return nil, &BackendError{ErrRunner, args[0], &commandFailure{err, exitCode(err)}}
	}
	if len(out) > maxOutput {
		return nil, berr(ErrRunner, args[0], "stdout exceeded limit")
	}
	return out, nil
}
func exitCode(err error) int {
	var ex *exec.ExitError
	if errors.As(err, &ex) {
		return ex.ExitCode()
	}
	return -1
}

type policyRule struct {
	ID        string   `json:"id"`
	Scope     string   `json:"scope"`
	Applies   string   `json:"applies_to"`
	Type      string   `json:"resource_type"`
	Decision  string   `json:"decision"`
	Resources []string `json:"resources"`
	Status    string   `json:"status"`
	Actions   []string `json:"actions"`
}

func (b *SbxBackend) policies(ctx context.Context, name string, allowed map[string]bool) error {
	args := []string{"policy", "ls"}
	if name != "" {
		args = append(args, name)
	}
	args = append(args, "--json")
	out, err := b.run(ctx, args...)
	if err != nil {
		return err
	}
	var doc struct {
		Rules []policyRule `json:"rules"`
	}
	if json.Unmarshal(out, &doc) != nil || len(doc.Rules) == 0 {
		return berr(ErrDenied, "policy", "missing or malformed rules")
	}
	deny := false
	seen := map[string]bool{}
	for _, r := range doc.Rules {
		if r.Type == "" || r.Status == "" || r.Decision == "" || len(r.Resources) == 0 || r.Scope == "" || r.Applies == "" {
			return berr(ErrDenied, "policy", "incomplete rule")
		}
		if r.Status != "active" {
			return berr(ErrDenied, "policy", "unrecognized rule status")
		}
		if strings.HasPrefix(r.Type, "filesystem:") {
			continue
		}
		if r.Type != "network" && r.Type != "http" {
			return berr(ErrDenied, "policy", "unknown policy resource type")
		}
		if r.Decision == "deny" {
			if r.Type == "network" && r.Scope == "global" && r.Applies == "all" {
				tcp, udp := false, false
				for _, a := range r.Actions {
					tcp = tcp || a == "net:connect:tcp"
					udp = udp || a == "net:connect:udp"
				}
				for _, v := range r.Resources {
					deny = deny || v == "**" && tcp && udp
				}
			}
			continue
		}
		if r.Decision != "allow" {
			return berr(ErrDenied, "policy", "unknown decision")
		}
		for _, v := range r.Resources {
			if name == "" || r.Scope != "sandbox:"+name || r.Applies != "sandbox:"+name || !allowed[v] || r.Type != "network" {
				return berr(ErrDenied, "policy", "unexpected allow rule")
			}
			seen[v] = true
		}
	}
	if !deny {
		return berr(ErrDenied, "policy", "global TCP/UDP default deny missing")
	}
	for v := range allowed {
		if !seen[v] {
			return berr(ErrDenied, "policy", "scoped allow was not observed")
		}
	}
	return nil
}
func (b *SbxBackend) preflight(ctx context.Context) error {
	out, err := b.run(ctx, "version")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(out))
	if len(fields) < 3 || fields[0] != "sbx" || fields[1] != "version" || fields[2] != SbxVersion {
		return berr(ErrDenied, "check", "exact sbx v0.49.2 required")
	}
	if err = b.policies(ctx, "", nil); err != nil {
		return err
	}
	out, err = b.run(ctx, "settings", "ls", "--json")
	if err != nil {
		return err
	}
	var rows []struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(out, &rows) != nil {
		return berr(ErrDenied, "settings", "invalid JSON")
	}
	required := map[string]string{"ssh.agentForwardingEnabled": "false", "browser.enabled": "false", "clipboard.imagePaste": "false", "skills.defaultMode": "\"off\"", "ssh.autoCreate": "false"}
	seen := map[string]bool{}
	for _, r := range rows {
		if want, ok := required[r.Key]; ok {
			if seen[r.Key] || string(r.Value) != want {
				return berr(ErrDenied, "settings", "unsafe or duplicate integration setting")
			}
			seen[r.Key] = true
		}
	}
	if len(seen) != len(required) {
		return berr(ErrDenied, "settings", "required settings missing")
	}
	// The local template index lists saved snapshots, not OCI pull availability.
	// Create validates the pinned OCI digest via --pull missing; no mutable tag fallback.
	_, err = b.list(ctx)
	return err
}
func (b *SbxBackend) Check(ctx context.Context) Diagnostics {
	d := Diagnostics{Backend: b.Name(), Capabilities: b.Capabilities(ctx)}
	if err := b.preflight(ctx); err != nil {
		d.Messages = []string{err.Error()}
		return d
	}
	d.Ready = true
	d.Messages = []string{"preflight passed; pinned OCI image pull is deferred to Create and remains unverified; safe Start and CopyOut are unsupported"}
	return d
}
func validRules(rules []NetworkRule) error {
	seen := map[int]bool{}
	for _, r := range rules {
		if r.Host != "localhost" || r.Action != "allow" || r.Port < 1 || r.Port > 65535 || seen[r.Port] {
			return berr(ErrDenied, "network", "only unique localhost exact-port allow rules supported")
		}
		seen[r.Port] = true
	}
	return nil
}
func validate(s EnvironmentSpec) error {
	if !nameRE.MatchString("pulsar-"+s.TaskID) || !validAgents[s.Agent] {
		return berr(ErrInvalid, "create", "invalid task or agent")
	}
	if s.TemplateRef != SbxTemplate || s.Workspace != WorkspaceNone || s.Skills != SkillsOff || s.ExpiresAfter != 0 || s.GatewayURL != "" {
		return berr(ErrUnsupported, "create", "unsupported template/workspace/skills/TTL/gateway")
	}
	m := regexp.MustCompile(`^([1-9][0-9]*)(m|g)$`).FindStringSubmatch(s.Memory)
	if len(m) != 3 {
		return berr(ErrInvalid, "create", "memory must use m or g")
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return berr(ErrInvalid, "create", "invalid memory")
	}
	if m[2] == "g" {
		if n > 32 {
			return berr(ErrInvalid, "create", "memory exceeds 32 GiB")
		}
		n *= 1024
	}
	if n < 512 || n > 32768 || s.CPUs < 1 || s.CPUs > 16 {
		return berr(ErrInvalid, "create", "CPU 1..16 and memory 512 MiB..32 GiB required")
	}
	return validRules(s.NetworkRules)
}
func (b *SbxBackend) list(ctx context.Context) ([]Environment, error) {
	out, err := b.run(ctx, "ls", "--json")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Agent  string `json:"agent"`
		CPUs   int    `json:"cpus"`
		Memory string `json:"memory"`
	}
	if json.Unmarshal(out, &rows) != nil || string(bytes.TrimSpace(out)) == "null" {
		return nil, berr(ErrRunner, "inspect", "invalid sandbox listing")
	}
	result := []Environment{}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		if !uuidRE.MatchString(r.ID) || !nameRE.MatchString(r.Name) || r.Status == "" || ids[r.ID] || names[r.Name] {
			return nil, berr(ErrDenied, "inspect", "invalid sandbox identity/state")
		}
		ids[r.ID] = true
		names[r.Name] = true
		result = append(result, Environment{ID: r.ID, Name: r.Name, Backend: b.Name(), State: r.Status, Agent: r.Agent, CPUs: r.CPUs, Memory: r.Memory})
	}
	return result, nil
}

// acquire serializes one environment without holding mu across I/O. A control
// operation cancels the current lease and reserves the next slot, preventing a
// new Exec from slipping between cancellation and Stop/Revoke/Remove.
func (b *SbxBackend) acquire(ctx context.Context, e Environment, control, allowUnsafe bool) (*managed, context.Context, func(), error) {
	reserved := false
	for {
		b.mu.Lock()
		m, err := b.require(e)
		if err != nil {
			b.mu.Unlock()
			return nil, nil, nil, err
		}
		if control && !reserved {
			if m.controlPending {
				b.mu.Unlock()
				return nil, nil, nil, berr(ErrDenied, "lease", "another control operation is pending")
			}
			m.controlPending = true
			reserved = true
		}
		if err = ctx.Err(); err != nil {
			if reserved {
				m.controlPending = false
			}
			b.mu.Unlock()
			return nil, nil, nil, err
		}
		if !allowUnsafe && m.quarantined {
			if reserved {
				m.controlPending = false
			}
			b.mu.Unlock()
			return nil, nil, nil, berr(ErrDenied, "lease", "environment quarantined; only inspection and cleanup allowed")
		}
		if !control && m.controlPending {
			b.mu.Unlock()
			return nil, nil, nil, berr(ErrDenied, "lease", "control operation pending")
		}
		if m.busy {
			done := m.done
			if control {
				m.cancel()
			}
			b.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				b.mu.Lock()
				if reserved {
					m.controlPending = false
				}
				b.mu.Unlock()
				return nil, nil, nil, ctx.Err()
			}
		}
		opCtx, cancel := context.WithCancel(ctx)
		m.busy = true
		m.cancel = cancel
		m.done = make(chan struct{})
		if reserved {
			m.controlPending = false
		}
		b.mu.Unlock()
		release := func() {
			cancel()
			b.mu.Lock()
			m.busy = false
			m.cancel = nil
			close(m.done)
			b.mu.Unlock()
		}
		return m, opCtx, release, nil
	}
}

// require is called only with mu held. Stored identity fields are immutable.
func (b *SbxBackend) require(e Environment) (*managed, error) {
	m, ok := b.owned[e.ID]
	if !ok || m.env.Name != e.Name || e.Backend != b.Name() {
		return nil, berr(ErrNotFound, "ownership", "unknown or forged environment")
	}
	return m, nil
}
func (b *SbxBackend) current(ctx context.Context, m *managed) (Environment, error) {
	if m.removed {
		return Environment{}, berr(ErrNotFound, "inspect", "environment removed")
	}
	rows, err := b.list(ctx)
	if err != nil {
		return Environment{}, err
	}
	for _, e := range rows {
		if e.ID == m.env.ID && e.Name == m.env.Name {
			return e, nil
		}
		if e.ID == m.env.ID || e.Name == m.env.Name {
			return Environment{}, berr(ErrDenied, "inspect", "sandbox identity replaced")
		}
	}
	return Environment{}, berr(ErrNotFound, "inspect", "sandbox missing")
}
func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}
func (b *SbxBackend) Create(ctx context.Context, s EnvironmentSpec) (Environment, error) {
	if err := validate(s); err != nil {
		return Environment{}, err
	}
	if err := b.preflight(ctx); err != nil {
		return Environment{}, err
	}
	rows, err := b.list(ctx)
	if err != nil {
		return Environment{}, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return Environment{}, berr(ErrRunner, "create", "random name generation failed")
	}
	name := "pulsar-" + hex.EncodeToString(nonce[:])
	before := map[string]bool{}
	for _, old := range rows {
		before[old.ID] = true
		if old.Name == name {
			return Environment{}, berr(ErrDenied, "create", "random name collision")
		}
	}
	_, createErr := b.run(ctx, "create", "--name", name, "--cpus", strconv.Itoa(s.CPUs), "--memory", s.Memory, "--skills", "off", "--template", SbxTemplate, "--pull", "missing", s.Agent)
	// stdout is human-readable. Only a new UUID with our unpredictable name in
	// the authoritative listing identifies a candidate for ownership/cleanup.
	c, cancel := cleanupContext()
	after, listErr := b.list(c)
	cancel()
	if listErr != nil {
		return Environment{}, errors.Join(createErr, listErr, berr(ErrRunner, "create", "new sandbox identity unconfirmed; no speculative removal"))
	}
	var e Environment
	for _, candidate := range after {
		if candidate.Name == name && !before[candidate.ID] {
			e = candidate
		}
	}
	if e.ID == "" {
		return Environment{}, errors.Join(createErr, berr(ErrDenied, "create", "new sandbox not found; no speculative removal"))
	}
	id := e.ID
	m := &managed{env: e, grants: map[string]bool{}}
	err = createErr
	if err == nil && (e.Agent != s.Agent || e.CPUs != s.CPUs || e.Memory != s.Memory) {
		err = berr(ErrDenied, "create", "created sandbox agent/resource mismatch")
	}
	if err == nil {
		_, err = b.current(ctx, m)
	}
	if err == nil {
		err = b.network(ctx, m, s.NetworkRules)
	}
	if err != nil {
		b.quarantine(m)
		c, cancel := cleanupContext()
		defer cancel()
		_, rollback := b.current(c, m)
		if rollback == nil {
			_, rollback = b.run(c, "rm", "--force", "--", name)
		}
		if rollback != nil {
			// Preserve the confirmed identity as cleanup-only and return it with
			// the error; callers must never treat this as successful creation.
			b.mu.Lock()
			b.owned[id] = m
			b.mu.Unlock()
			return e, b.failStop(m, errors.Join(err, berr(ErrRunner, "rollback", "new sandbox removal failed; environment quarantined")))
		}
		return Environment{}, err
	}
	m.env = e
	b.mu.Lock()
	b.owned[id] = m
	b.mu.Unlock()
	return e, nil
}
func (b *SbxBackend) Inspect(ctx context.Context, e Environment) (Environment, error) {
	m, opCtx, release, err := b.acquire(ctx, e, false, true)
	if err != nil {
		return Environment{}, err
	}
	defer release()
	return b.current(opCtx, m)
}
func (b *SbxBackend) Start(ctx context.Context, e Environment) error {
	m, opCtx, release, err := b.acquire(ctx, e, false, false)
	if err != nil {
		return err
	}
	defer release()
	ctx = opCtx
	if _, err = b.current(ctx, m); err != nil {
		return err
	}
	// v0.49.2 has no local start-existing-only primitive. run --name can
	// recreate a concurrently deleted sandbox with an implicit host workspace.
	return berr(ErrUnsupported, "start", "sbx lacks an atomic start-existing-only command; refusing implicit creation")
}
func (b *SbxBackend) Exec(ctx context.Context, e Environment, r ExecRequest) (string, error) {
	m, opCtx, release, err := b.acquire(ctx, e, false, false)
	if err != nil {
		return "", err
	}
	defer release()
	ctx = opCtx
	if len(r.Command) == 0 || r.Command[0] == "" || strings.HasPrefix(r.Command[0], "-") || r.User != "" && r.User != "agent" && r.User != "root" {
		return "", berr(ErrInvalid, "exec", "invalid command/user")
	}
	for _, a := range r.Command {
		if strings.ContainsAny(a, "\x00\r\n") {
			return "", berr(ErrInvalid, "exec", "invalid argument")
		}
	}
	if _, err = b.current(ctx, m); err != nil {
		return "", err
	}
	args := []string{"exec"}
	if r.User != "" {
		args = append(args, "-u", r.User)
	}
	args = append(args, "--", m.env.Name)
	args = append(args, r.Command...)
	out, err := b.run(ctx, args...)
	return string(out), err
}
func (b *SbxBackend) network(ctx context.Context, m *managed, rules []NetworkRule) error {
	if err := validRules(rules); err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, r := range rules {
		expected["localhost:"+strconv.Itoa(r.Port)] = true
	}
	if err := b.policies(ctx, m.env.Name, m.grants); err != nil {
		return b.failStop(m, err)
	}
	// Replace, never append: remove obsolete grants before admitting new ones.
	for old := range m.grants {
		if !expected[old] {
			if _, err := b.run(ctx, "policy", "rm", "network", "--force", "--sandbox", m.env.Name, "--resource", old); err != nil {
				return b.failStop(m, err)
			}
			delete(m.grants, old)
		}
	}
	added := []string{}
	var failure error
	for _, r := range rules {
		resource := "localhost:" + strconv.Itoa(r.Port)
		if m.grants[resource] {
			continue
		}
		added = append(added, resource)
		if _, failure = b.run(ctx, "policy", "allow", "network", "--sandbox", m.env.Name, "--", resource); failure != nil {
			break
		}
		expected[resource] = true
	}
	if failure == nil {
		failure = b.policies(ctx, m.env.Name, expected)
	}
	if failure != nil {
		b.quarantine(m)
		c, cancel := cleanupContext()
		defer cancel()
		if _, err := b.current(c, m); err != nil {
			return errors.Join(failure, err)
		}
		for _, v := range added {
			_, err := b.run(c, "policy", "rm", "network", "--force", "--sandbox", m.env.Name, "--resource", v)
			if err != nil {
				failure = errors.Join(failure, err)
			}
		}
		_, err := b.run(c, "stop", "--", m.env.Name)
		return errors.Join(failure, err)
	}
	m.grants = expected
	return nil
}
func (b *SbxBackend) SetNetworkPolicy(ctx context.Context, e Environment, rules []NetworkRule) error {
	if err := validRules(rules); err != nil {
		return err
	}
	m, opCtx, release, err := b.acquire(ctx, e, true, false)
	if err != nil {
		return err
	}
	defer release()
	ctx = opCtx
	if _, err = b.current(ctx, m); err != nil {
		return err
	}
	return b.network(ctx, m, rules)
}

// failStop never relies on a cancelled request context, and rechecks identity
// before name-based CLI operations. A same-user malicious host race remains outside this boundary.
func (b *SbxBackend) quarantine(m *managed) {
	b.mu.Lock()
	m.quarantined = true
	b.mu.Unlock()
}
func (b *SbxBackend) failStop(m *managed, cause error) error {
	// Quarantine persists even when cleanup fails or Stop succeeds: old grants
	// may still exist and a later Exec would implicitly restart the sandbox.
	b.quarantine(m)
	c, cancel := cleanupContext()
	defer cancel()
	if _, err := b.current(c, m); err != nil {
		return errors.Join(cause, err)
	}
	_, err := b.run(c, "stop", "--", m.env.Name)
	return errors.Join(cause, err)
}

// Revoke removes a recorded scoped network resource only. Controller must revoke application tokens.
func (b *SbxBackend) Revoke(ctx context.Context, e Environment, grant string) error {
	m, opCtx, release, err := b.acquire(ctx, e, true, true)
	if err != nil {
		return err
	}
	defer release()
	ctx = opCtx
	if !m.grants[grant] {
		return berr(ErrNotFound, "revoke", "unknown scoped grant")
	}
	if _, err = b.current(ctx, m); err != nil {
		return b.failStop(m, err)
	}
	if _, err = b.run(ctx, "policy", "rm", "network", "--force", "--sandbox", m.env.Name, "--resource", grant); err != nil {
		return b.failStop(m, err)
	}
	delete(m.grants, grant)
	if err = b.policies(ctx, m.env.Name, m.grants); err != nil {
		return b.failStop(m, err)
	}
	return nil
}
func (b *SbxBackend) Stop(ctx context.Context, e Environment) error {
	m, opCtx, release, err := b.acquire(ctx, e, true, true)
	if err != nil {
		return err
	}
	defer release()
	ctx = opCtx
	if m.removed {
		return nil
	}
	live, err := b.current(ctx, m)
	if err != nil {
		var be *BackendError
		if errors.As(err, &be) && be.Kind == ErrNotFound {
			m.removed = true
			return nil
		}
		return err
	}
	if live.State == "stopped" {
		return nil
	}
	_, err = b.run(ctx, "stop", "--", m.env.Name)
	if err != nil {
		b.quarantine(m)
	}
	return err
}
func (b *SbxBackend) Remove(ctx context.Context, e Environment) error {
	m, opCtx, release, err := b.acquire(ctx, e, true, true)
	if err != nil {
		return err
	}
	defer release()
	ctx = opCtx
	if m.removed {
		return nil
	}
	_, err = b.current(ctx, m)
	if err != nil {
		var be *BackendError
		if errors.As(err, &be) && be.Kind == ErrNotFound {
			m.removed = true
			return nil
		}
		return err
	}
	_, err = b.run(ctx, "rm", "--force", "--", m.env.Name)
	if err == nil {
		m.removed = true
	} else {
		return b.failStop(m, err)
	}
	return err
}
func validGuestPath(p string) bool {
	return path.IsAbs(p) && path.Clean(p) == p && !strings.ContainsAny(p, "\\\x00\r\n:") && (strings.HasPrefix(p, "/tmp/") || strings.HasPrefix(p, "/work/"))
}
func safeHost(p string, regular bool) error {
	if !filepath.IsAbs(p) {
		return berr(ErrInvalid, "copy", "absolute host path required")
	}
	p = filepath.Clean(p)
	for cur := p; ; cur = filepath.Dir(cur) {
		st, err := os.Lstat(cur)
		if err != nil {
			return berr(ErrDenied, "copy", "host path unavailable")
		}
		reparse := false
		v := reflect.ValueOf(st.Sys())
		if v.Kind() == reflect.Pointer && !v.IsNil() {
			v = v.Elem()
			if v.Kind() == reflect.Struct {
				a := v.FieldByName("FileAttributes")
				if a.IsValid() && a.CanUint() {
					reparse = a.Uint()&0x400 != 0
				}
			}
		}
		if reparse || st.Mode()&os.ModeSymlink != 0 || cur == p && regular && !st.Mode().IsRegular() {
			return berr(ErrDenied, "copy", "symlink/reparse/nonregular path denied")
		}
		next := filepath.Dir(cur)
		if next == cur {
			break
		}
	}
	return nil
}
func digest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func (b *SbxBackend) staging() (string, error) {
	if err := safeHost(b.outputDir, false); err != nil {
		return "", err
	}
	st, err := os.Stat(b.outputDir)
	if err != nil || !st.IsDir() {
		return "", berr(ErrInvalid, "copy", "caller staging directory required")
	}
	return os.MkdirTemp(b.outputDir, "sbx-copy-")
}
func readBounded(p string, limit int64) ([]byte, error) {
	if err := safeHost(p, true); err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, berr(ErrDenied, "copy", "snapshot exceeds bound")
	}
	return data, nil
}

// CopyIn accepts one bounded regular file, stages immutable bytes, and never passes -L.
func (b *SbxBackend) CopyIn(ctx context.Context, e Environment, src, dst string) (CopyReceipt, error) {
	m, opCtx, release, err := b.acquire(ctx, e, false, false)
	if err != nil {
		return CopyReceipt{}, err
	}
	defer release()
	ctx = opCtx
	if !validGuestPath(dst) {
		return CopyReceipt{}, berr(ErrInvalid, "copy", "canonical guest /tmp or /work file required")
	}
	data, err := readBounded(src, maxSnapshot)
	if err != nil {
		return CopyReceipt{}, err
	}
	dir, err := b.staging()
	if err != nil {
		return CopyReceipt{}, err
	}
	defer os.RemoveAll(dir)
	stage := filepath.Join(dir, "input")
	if err = os.WriteFile(stage, data, 0600); err != nil {
		return CopyReceipt{}, err
	}
	if _, err = b.current(ctx, m); err != nil {
		return CopyReceipt{}, err
	}
	if _, err = b.run(ctx, "cp", "--", stage, m.env.Name+":"+dst); err != nil {
		return CopyReceipt{}, err
	}
	return CopyReceipt{GuestPath: dst, Bytes: int64(len(data)), Digest: digest(data)}, nil
}

// CopyOut fails closed: sbx cp can consume unbounded host disk before returning.
// No verified bounded binary guest export primitive is available in this adapter.
func (b *SbxBackend) CopyOut(ctx context.Context, e Environment, s CopySelector) (ArtifactSnapshot, error) {
	_, _, release, err := b.acquire(ctx, e, false, false)
	if err != nil {
		return ArtifactSnapshot{}, err
	}
	defer release()
	return ArtifactSnapshot{}, berr(ErrUnsupported, "copy-out", "bounded guest export is not implemented; refusing unbounded host transfer")
}

var _ RuntimeBackend = (*SbxBackend)(nil)
