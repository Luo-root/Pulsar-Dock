package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const fixtureID = "12345678-1234-4234-8234-123456789abc"

func spec() EnvironmentSpec {
	return EnvironmentSpec{TaskID: "task1", Agent: "shell", TemplateRef: SbxTemplate, Workspace: WorkspaceNone, Skills: SkillsOff, CPUs: 2, Memory: "2g"}
}

type fakeRun struct {
	t                                                          *testing.T
	name                                                       string
	calls                                                      []string
	overrides                                                  map[string]string
	fail                                                       map[string]bool
	grants                                                     map[string]bool
	live, stopped, replaced, badResources, hideRules, deadline bool
	cancelRevoke                                               context.CancelFunc
}

func readyFake(t *testing.T) *fakeRun {
	return &fakeRun{t: t, overrides: map[string]string{}, fail: map[string]bool{}, grants: map[string]bool{}}
}
func (f *fakeRun) Run(ctx context.Context, args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if strings.HasPrefix(key, "stop ") {
		_, f.deadline = ctx.Deadline()
	}
	if f.cancelRevoke != nil && strings.HasPrefix(key, "policy rm ") {
		f.cancelRevoke()
		f.cancelRevoke = nil
		return nil, context.Canceled
	}
	if f.fail[key] {
		return nil, errors.New("credential=secret")
	}
	if v, ok := f.overrides[key]; ok {
		return []byte(v), nil
	}
	switch {
	case key == "version":
		return []byte("sbx version v0.49.2 deadbeef"), nil
	case key == "settings ls --json":
		return []byte(`[{"key":"ssh.agentForwardingEnabled","value":false},{"key":"browser.enabled","value":false},{"key":"clipboard.imagePaste","value":false},{"key":"skills.defaultMode","value":"off"},{"key":"ssh.autoCreate","value":false}]`), nil
	case key == "policy ls --json" || key == "policy ls "+f.name+" --json":
		rules := []policyRule{{ID: "deny", Scope: "global", Applies: "all", Type: "network", Decision: "deny", Resources: []string{"**"}, Status: "active", Actions: []string{"net:connect:tcp", "net:connect:udp"}}}
		if key != "policy ls --json" && !f.hideRules {
			for v := range f.grants {
				rules = append(rules, policyRule{ID: v, Scope: "sandbox", Applies: f.name, Type: "network", Decision: "allow", Resources: []string{v}, Status: "active"})
			}
		}
		v, _ := json.Marshal(map[string]any{"rules": rules})
		return v, nil
	case key == "ls --json":
		if !f.live {
			return []byte("[]"), nil
		}
		id := fixtureID
		if f.replaced {
			id = "87654321-1234-4234-8234-123456789abc"
		}
		state := "running"
		if f.stopped {
			state = "stopped"
		}
		cpus := 2
		if f.badResources {
			cpus = 4
		}
		return []byte(fmt.Sprintf(`[{"id":%q,"name":%q,"status":%q,"agent":"shell","cpus":%d,"memory":"2g"}]`, id, f.name, state, cpus)), nil
	case strings.HasPrefix(key, "create "):
		f.name = args[2]
		if !nameRE.MatchString(f.name) || len(f.name) != 39 {
			f.t.Fatal("not unpredictable name", f.name)
		}
		want := []string{"create", "--name", f.name, "--cpus", "2", "--memory", "2g", "--skills", "off", "--template", SbxTemplate, "--pull", "missing", "--", "shell"}
		if !reflect.DeepEqual(args, want) {
			f.t.Fatalf("create flags=%q", args)
		}
		f.live = true
		return []byte("SANDBOX IMAGE\n✓ Created sandbox " + f.name), nil
	case key == "stop -- "+f.name:
		f.stopped = true
		return nil, nil
	case key == "rm --force -- "+f.name:
		f.live = false
		return nil, nil
	case strings.HasPrefix(key, "policy allow network --sandbox "+f.name+" -- localhost:"):
		f.grants[args[len(args)-1]] = true
		return nil, nil
	case strings.HasPrefix(key, "policy rm network --force --sandbox "+f.name+" --resource localhost:"):
		delete(f.grants, args[len(args)-1])
		return nil, nil
	case key == "exec -u agent -- "+f.name+" printf --help":
		return []byte("ok"), nil
	case len(args) == 4 && args[0] == "cp" && args[1] == "--" && strings.HasPrefix(args[3], f.name+":"):
		return nil, nil
	default:
		f.t.Fatalf("unexpected CLI: %q", args)
		return nil, nil
	}
}
func created(t *testing.T) (*SbxBackend, *fakeRun, Environment) {
	t.Helper()
	f := readyFake(t)
	b := NewSbxBackend(f, t.TempDir())
	e, err := b.Create(context.Background(), spec())
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != fixtureID || e.Name == e.ID {
		t.Fatal("identity", e)
	}
	return b, f, e
}
func assertKind(t *testing.T, err error, k ErrorKind) {
	t.Helper()
	var be *BackendError
	if !errors.As(err, &be) || be.Kind != k {
		t.Fatalf("want %s got %v", k, err)
	}
}
func TestOwnershipCLIAndFailClosedStart(t *testing.T) {
	b, f, e := created(t)
	ctx := context.Background()
	assertKind(t, b.Start(ctx, e), ErrUnsupported)
	for _, c := range f.calls {
		if strings.HasPrefix(c, "run ") {
			t.Fatal("implicit create possible")
		}
	}
	out, err := b.Exec(ctx, e, ExecRequest{Command: []string{"printf", "--help"}, User: "agent"})
	if err != nil || out != "ok" {
		t.Fatal(out, err)
	}
	for _, forged := range []Environment{{ID: e.ID, Name: "other", Backend: "sbx"}, {ID: e.ID, Name: e.Name, Backend: "other"}, {ID: "unknown", Name: e.Name, Backend: "sbx"}} {
		n := len(f.calls)
		if _, err = b.Inspect(ctx, forged); err == nil || len(f.calls) != n {
			t.Fatal("forged handle used")
		}
	}
	f.replaced = true
	if b.Remove(ctx, e) == nil {
		t.Fatal("replacement removed")
	}
}
func TestCleanupIdempotentAndRealState(t *testing.T) {
	b, f, e := created(t)
	ctx := context.Background()
	if b.Stop(ctx, e) != nil {
		t.Fatal("stop")
	}
	live, err := b.Inspect(ctx, e)
	if err != nil || live.State != "stopped" {
		t.Fatal(live, err)
	}
	if b.Stop(ctx, e) != nil || b.Remove(ctx, e) != nil {
		t.Fatal("cleanup")
	}
	n := len(f.calls)
	if b.Remove(ctx, e) != nil || b.Stop(ctx, e) != nil || len(f.calls) != n {
		t.Fatal("not idempotent")
	}
	if b.Remove(ctx, Environment{ID: "unknown"}) == nil {
		t.Fatal("unknown removed")
	}
}
func TestPreflightStructuredFailClosed(t *testing.T) {
	cases := map[string]map[string]string{"version": {"version": "sbx version v0.49.20"}, "empty": {"policy ls --json": ""}, "null": {"policy ls --json": "null"}, "rules-missing": {"policy ls --json": "{}"}, "settings-missing": {"settings ls --json": "[]"}, "unsafe": {"settings ls --json": `[{"key":"browser.enabled","value":true}]`}, "broad": {"policy ls --json": `{"rules":[{"scope":"global","applies_to":"all","resource_type":"network","decision":"deny","resources":["**"],"status":"active","actions":["net:connect:tcp","net:connect:udp"]},{"scope":"global","applies_to":"all","resource_type":"network","decision":"allow","resources":["**"],"status":"active"}]}`}, "login-list": {"ls --json": "not JSON"}}
	for name, over := range cases {
		t.Run(name, func(t *testing.T) {
			f := readyFake(t)
			f.overrides = over
			b := NewSbxBackend(f)
			if b.Check(context.Background()).Ready {
				t.Fatal("unsafe ready")
			}
			if _, err := b.Create(context.Background(), spec()); err == nil {
				t.Fatal("unsafe create")
			}
			if f.live {
				t.Fatal("create invoked")
			}
		})
	}
	f := readyFake(t)
	d := NewSbxBackend(f).Check(context.Background())
	if !d.Ready || len(d.Messages) == 0 {
		t.Fatal("missing deferred pull diagnostic")
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "template ") {
			t.Fatal("saved snapshots used as OCI cache")
		}
	}
}
func TestSpecValidation(t *testing.T) {
	for _, mut := range []func(*EnvironmentSpec){func(s *EnvironmentSpec) { s.Memory = "33g" }, func(s *EnvironmentSpec) { s.Memory = "garbage" }, func(s *EnvironmentSpec) { s.Memory = "1m" }, func(s *EnvironmentSpec) { s.CPUs = 0 }, func(s *EnvironmentSpec) { s.Workspace = WorkspaceClone }, func(s *EnvironmentSpec) { s.Skills = SkillsReadonly }, func(s *EnvironmentSpec) { s.Agent = "./kit" }, func(s *EnvironmentSpec) { s.TaskID = "x\nsh" }, func(s *EnvironmentSpec) { s.GatewayURL = "http://localhost" }, func(s *EnvironmentSpec) { s.ExpiresAfter = 1 }} {
		s := spec()
		mut(&s)
		f := readyFake(t)
		if _, err := NewSbxBackend(f).Create(context.Background(), s); err == nil || len(f.calls) != 0 {
			t.Fatal("unsafe spec executed")
		}
	}
}
func TestCreateRandomNamesAndResourceRollback(t *testing.T) {
	_, f, e := created(t)
	_, g, other := created(t)
	if e.Name == other.Name {
		t.Fatal("names repeated")
	}
	f.badResources = true
	f.live = false
	_, err := NewSbxBackend(f).Create(context.Background(), spec())
	if err == nil || f.live {
		t.Fatal("mismatched resources not rolled back", err)
	}
	_ = g
}
func rule(port int) NetworkRule { return NetworkRule{Host: "localhost", Port: port, Action: "allow"} }
func TestNetworkReplaceEmptyAndRevoke(t *testing.T) {
	b, f, e := created(t)
	ctx := context.Background()
	if err := b.SetNetworkPolicy(ctx, e, []NetworkRule{rule(1), rule(2)}); err != nil {
		t.Fatal(err)
	}
	if err := b.SetNetworkPolicy(ctx, e, []NetworkRule{rule(2)}); err != nil || f.grants["localhost:1"] {
		t.Fatal("not replace", err)
	}
	if err := b.SetNetworkPolicy(ctx, e, nil); err != nil || len(f.grants) != 0 {
		t.Fatal("empty did not revoke", err)
	}
	if err := b.SetNetworkPolicy(ctx, e, []NetworkRule{rule(3)}); err != nil {
		t.Fatal(err)
	}
	if err := b.Revoke(ctx, e, "localhost:3"); err != nil || len(f.grants) != 0 {
		t.Fatal(err)
	}
}
func TestCreateAppliesNetwork(t *testing.T) {
	f := readyFake(t)
	s := spec()
	s.NetworkRules = []NetworkRule{rule(4)}
	if _, err := NewSbxBackend(f).Create(context.Background(), s); err != nil || !f.grants["localhost:4"] {
		t.Fatal("create rules ignored", err)
	}
}
func TestNetworkPartialRollbackAndInvalidTail(t *testing.T) {
	b, f, e := created(t)
	f.fail["policy allow network --sandbox "+e.Name+" -- localhost:2"] = true
	err := b.SetNetworkPolicy(context.Background(), e, []NetworkRule{rule(1), rule(2)})
	if err == nil || len(f.grants) != 0 || !f.stopped || !f.deadline || strings.Contains(err.Error(), "secret") {
		t.Fatal("partial rollback", err)
	}
	n := len(f.calls)
	if b.SetNetworkPolicy(context.Background(), e, []NetworkRule{rule(1), {Host: "*", Port: 2, Action: "allow"}}) == nil || len(f.calls) != n {
		t.Fatal("invalid tail executed")
	}
}
func TestNetworkMustBeObserved(t *testing.T) {
	b, f, e := created(t)
	f.hideRules = true
	if b.SetNetworkPolicy(context.Background(), e, []NetworkRule{rule(1)}) == nil || !f.stopped || len(f.grants) != 0 {
		t.Fatal("unobserved rule accepted")
	}
}
func TestRevokeFailureAndCancellationStops(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		b, f, e := created(t)
		if err := b.SetNetworkPolicy(context.Background(), e, []NetworkRule{rule(1)}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		if cancelled {
			f.cancelRevoke = cancel
		} else {
			f.fail["policy rm network --force --sandbox "+e.Name+" --resource localhost:1"] = true
		}
		err := b.Revoke(ctx, e, "localhost:1")
		cancel()
		if err == nil || !f.stopped || !f.deadline {
			t.Fatal("revoke failure left running", err)
		}
	}
}
func TestCopyInAndUnsupportedOut(t *testing.T) {
	b, f, e := created(t)
	src := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(src, []byte{0, 255, 1}, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := b.CopyIn(context.Background(), e, src, "/work/input")
	if err != nil || r.Bytes != 3 || r.Digest == "" {
		t.Fatal(r, err)
	}
	n := len(f.calls)
	_, err = b.CopyOut(context.Background(), e, CopySelector{GuestPath: "/tmp/malicious-dir", MaxBytes: 4 * 1024 * 1024})
	assertKind(t, err, ErrUnsupported)
	if len(f.calls) != n {
		t.Fatal("unbounded transfer attempted")
	}
	for _, p := range []string{"/tmp/a", "/work/a"} {
		if !validGuestPath(p) {
			t.Fatal(p)
		}
	}
	for _, p := range []string{"/tmp/../etc/a", "/tmp//a", "C:\\tmp\\a", "/tmp/"} {
		if validGuestPath(p) {
			t.Fatal(p)
		}
	}
	if _, err = readBounded(src, 2); err == nil {
		t.Fatal("read bound")
	}
}
func TestSymlinkParents(t *testing.T) {
	d := t.TempDir()
	target := filepath.Join(d, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "x"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlink creation unavailable")
	}
	if safeHost(filepath.Join(link, "x"), true) == nil {
		t.Fatal("symlink followed")
	}
}
func TestContextWriterAndExecValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewSbxBackend(readyFake(t)).Create(ctx, spec())
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var buf limitedBuffer
	data := []byte(strings.Repeat("x", maxOutput+1))
	n, err := buf.Write(data)
	if n != len(data) || err != nil || !buf.overflow || buf.b.Len() != maxOutput {
		t.Fatal("shortwrite")
	}
	b, f, e := created(t)
	for _, r := range []ExecRequest{{Command: []string{"--privileged"}}, {Command: []string{"sh\n"}}, {Command: []string{"echo"}, User: "-u root"}, {}} {
		n := len(f.calls)
		if _, err = b.Exec(context.Background(), e, r); err == nil || len(f.calls) != n {
			t.Fatal("unsafe exec")
		}
	}
}
func TestOSRunnerHelper(t *testing.T) {
	for i, a := range os.Args {
		if a == "--sbx-test-helper" {
			fmt.Fprint(os.Stdout, "{\"ok\":true}")
			fmt.Fprint(os.Stderr, "credential=secret")
			if os.Args[i+1] == "fail" {
				os.Exit(7)
			}
			if os.Args[i+1] == "wait" {
				time.Sleep(10 * time.Second)
			}
			os.Exit(0)
		}
	}
}
func TestOSRunner(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := OSRunner{Bin: exe}
	args := []string{"-test.run=TestOSRunnerHelper", "--", "--sbx-test-helper", "ok"}
	out, err := r.Run(context.Background(), args...)
	if err != nil || string(out) != `{"ok":true}` {
		t.Fatal(string(out), err)
	}
	args[len(args)-1] = "fail"
	out, err = r.Run(context.Background(), args...)
	var ex *exec.ExitError
	if !errors.As(err, &ex) || ex.ExitCode() != 7 || strings.Contains(err.Error(), "secret") || len(out) != 0 {
		t.Fatal(err)
	}
	args[len(args)-1] = "wait"
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = r.Run(ctx, args...)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
