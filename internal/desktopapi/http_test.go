package desktopapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Luo-root/pulsar-dock/internal/agentadapter"
	"github.com/Luo-root/pulsar-dock/internal/controller"
	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

type execBackend struct{}

func (execBackend) Name() string { return "http-test" }
func (execBackend) Capabilities(context.Context) runtime.CapabilitySet {
	return runtime.CapabilitySet{GuestExec: true}
}
func (execBackend) Check(context.Context) runtime.Diagnostics {
	return runtime.Diagnostics{Backend: "http-test", Ready: true}
}
func (execBackend) Create(_ context.Context, s runtime.EnvironmentSpec) (runtime.Environment, error) {
	return runtime.Environment{ID: "env-" + s.TaskID, Name: "guest", Backend: "http-test", State: "running", Agent: s.Agent, CPUs: s.CPUs, Memory: s.Memory}, nil
}
func (execBackend) Start(context.Context, runtime.Environment) error { return nil }
func (execBackend) Exec(context.Context, runtime.Environment, runtime.ExecRequest) (string, error) {
	return "safe synthetic output", nil
}
func (execBackend) CopyIn(context.Context, runtime.Environment, string, string) (runtime.CopyReceipt, error) {
	return runtime.CopyReceipt{}, errors.New("unused")
}
func (execBackend) CopyOut(context.Context, runtime.Environment, runtime.CopySelector) (runtime.ArtifactSnapshot, error) {
	return runtime.ArtifactSnapshot{}, errors.New("unused")
}
func (execBackend) Stop(context.Context, runtime.Environment) error   { return nil }
func (execBackend) Remove(context.Context, runtime.Environment) error { return nil }
func (execBackend) SetNetworkPolicy(context.Context, runtime.Environment, []runtime.NetworkRule) error {
	return nil
}
func (execBackend) Revoke(context.Context, runtime.Environment, string) error { return nil }
func (execBackend) Inspect(_ context.Context, e runtime.Environment) (runtime.Environment, error) {
	return e, nil
}

type executionPlanner struct{}

func (executionPlanner) Plan(r agentadapter.Request) (runtime.ExecRequest, error) {
	return runtime.ExecRequest{Command: []string{"synthetic", "--", r.Goal}, User: "agent"}, nil
}

type executionVerifier struct{}

func (executionVerifier) Verify(context.Context, runtime.Environment) error { return nil }

func executionHTTPFacade(t *testing.T) (*Facade, taskstore.Task) {
	t.Helper()
	store, err := taskstore.Open(filepath.Join(canonicalTestDir(t), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	svc, err := controller.NewWithExecution(store, execBackend{}, "alice", controller.Policy{Agent: "shell", TemplateRef: runtime.SbxTemplate, CPUs: 1, Memory: "1g"}, controller.ExecutionConfig{Planner: executionPlanner{}, Verifier: executionVerifier{}, MaxBudgetUSD: 1})
	if err != nil {
		t.Fatal(err)
	}
	f, err := New(svc, store, "alice", "claude", []AgentConfig{{ID: "claude", DisplayName: "Claude", Installed: true, CLIVerified: true, ModelVerified: true, Operations: []Operation{OperationCreate, OperationExecute}}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := f.CreateTask(context.Background(), CreateTaskRequest{AgentID: "claude", Goal: "execute synthetic", IdempotencyKey: "exec"})
	if err != nil {
		t.Fatal(err)
	}
	return f, taskstore.Task{ID: task.ID, RootID: task.RootID}
}

func httpReq(method, path, token string, body []byte) *http.Request {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Host = "127.0.0.1:4123"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if len(body) > 0 {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func TestHTTPRoutesAuthCSRFAndStrictJSON(t *testing.T) {
	f, _ := openFacade(t, "alice")
	h, err := NewHTTPHandler(map[string]*Facade{"claude": f}, HTTPConfig{Token: "test-random-token", AllowedHosts: []string{"127.0.0.1:4123"}, AllowedOrigins: []string{"http://127.0.0.1:3000"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		req  *http.Request
		code int
	}{
		{"missing token", httpReq("GET", "http://127.0.0.1:4123/api/agents", "", nil), 401},
		{"query token rejected", httpReq("GET", "http://127.0.0.1:4123/api/agents?token=test-random-token", "", nil), 400},
		{"malformed query rejected", httpReq("GET", "http://127.0.0.1:4123/api/agents?%ZZ", "test-random-token", nil), 400},
		{"empty query rejected", httpReq("GET", "http://127.0.0.1:4123/api/agents?", "test-random-token", nil), 400},
		{"bad host", func() *http.Request {
			r := httpReq("GET", "http://evil/api/agents", "test-random-token", nil)
			r.Host = "evil"
			return r
		}(), 403},
		{"bad origin", func() *http.Request {
			r := httpReq("GET", "http://127.0.0.1:4123/api/agents", "test-random-token", nil)
			r.Header.Set("Origin", "http://evil")
			return r
		}(), 403},
		{"unknown JSON", httpReq("POST", "http://127.0.0.1:4123/api/agents/claude/tasks", "test-random-token", []byte(`{"goal":"x","idempotencyKey":"x","extra":1}`)), 400},
		{"trailing JSON", httpReq("POST", "http://127.0.0.1:4123/api/agents/claude/tasks", "test-random-token", []byte(`{"goal":"x","idempotencyKey":"x"}{}`)), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, tc.req)
			if w.Code != tc.code {
				t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
			}
			if bytes.Contains(w.Body.Bytes(), []byte("test-random-token")) {
				t.Fatal("token leaked")
			}
		})
	}
	w := httptest.NewRecorder()
	r := httpReq("OPTIONS", "http://127.0.0.1:4123/api/agents", "", nil)
	r.Header.Set("Origin", "http://127.0.0.1:3000")
	h.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "http://127.0.0.1:3000" {
		t.Fatalf("options code=%d headers=%v", w.Code, w.Header())
	}
	w = httptest.NewRecorder()
	r = httpReq("GET", "http://127.0.0.1:4123/api/agents", "test-random-token", nil)
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("agents=%d", w.Code)
	}
}

func TestHTTPChunkedBodyLimitRejectsWithoutCreate(t *testing.T) {
	f, _ := openFacade(t, "alice")
	h, err := NewHTTPHandler(map[string]*Facade{"claude": f}, HTTPConfig{Token: "token", AllowedHosts: []string{"127.0.0.1:4123"}, MaxBodyBytes: 128})
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"goal":"chunked","idempotencyKey":"chunked"}`
	body := valid + strings.Repeat(" ", 128-len(valid)+1) + `{}`
	r := httpReq("POST", "http://127.0.0.1:4123/api/agents/claude/tasks", "token", []byte(body))
	r.ContentLength = -1
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized chunked code=%d body=%s", w.Code, w.Body.String())
	}
	r = httpReq("POST", "http://127.0.0.1:4123/api/agents/claude/tasks", "token", []byte(valid))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("oversized request created a task or valid retry failed: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHTTPExecuteSuccessAndCrossAgentRoute(t *testing.T) {
	f, task := executionHTTPFacade(t)
	h, err := NewHTTPHandler(map[string]*Facade{"claude": f}, HTTPConfig{Token: "token", AllowedHosts: []string{"127.0.0.1:4123"}})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httpReq("POST", "http://127.0.0.1:4123/api/agents/claude/tasks/"+task.RootID+"/"+task.ID+"/execute", "token", nil)
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("execute code=%d body=%s", w.Code, w.Body.String())
	}
	var result ExecuteResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !result.CommandCompleted || result.Output != "safe synthetic output" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	w = httptest.NewRecorder()
	r = httpReq("GET", "http://127.0.0.1:4123/api/agents/other/tasks/"+task.RootID+"/"+task.ID, "token", nil)
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("cross-agent code=%d body=%s", w.Code, w.Body.String())
	}
}
