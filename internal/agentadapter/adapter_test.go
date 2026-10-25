package agentadapter

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/Luo-root/pulsar-dock/internal/runtime"
)

func validRequest() Request {
	return Request{Goal: "synthetic task", MCPConfigPath: GuestMCPConfigPath, MaxBudgetUSD: 0.25}
}

func planner(t *testing.T, model string) *Claude {
	t.Helper()
	c, err := NewClaude(TrustedClaudeConfig{VersionOutput: "2.1.300 (Claude Code)", Model: model})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestVersionPin(t *testing.T) {
	for _, version := range []string{"2.1.300 (Claude Code)", "2.1.300 (Claude Code)\n", "2.1.300 (Claude Code)\r\n"} {
		if _, err := NewClaude(TrustedClaudeConfig{VersionOutput: version}); err != nil {
			t.Errorf("expected exact probe output accepted: %q", version)
		}
	}
	for _, version := range []string{"", "2.1.300", "2.1.30 (Claude Code)", "2.1.301 (Claude Code)", "x2.1.300 (Claude Code)", "2.1.300 (Claude Code) extra", "2.1.300 (Claude Code)\n\n", " 2.1.300 (Claude Code)", "2.1.300 (Claude Code)\r", "2.1.300 (Claude Code)\x00"} {
		if c, err := NewClaude(TrustedClaudeConfig{VersionOutput: version}); !errors.Is(err, ErrUnsupported) || c != nil {
			t.Errorf("unexpected version accepted: %q", version)
		}
	}
}

func TestFixedPlan(t *testing.T) {
	got, err := planner(t, "synthetic/model[1m]").Plan(validRequest())
	want := runtime.ExecRequest{User: "agent", Command: []string{
		"claude", "--bare", "--restricted", "--print",
		"--strict-mcp-config", "--mcp-config", "/tmp/pulsar-dock/mcp.json",
		"--permission-mode", "dontAsk", "--permission-prompts", "none",
		"--tools", "", "--allowedTools", "mcp__dock__*",
		"--disable-slash-commands", "--setting-sources", "",
		"--no-chrome", "--no-session-persistence",
		"--output-format", "stream-json", "--verbose",
		"--max-budget-usd", "0.25", "--model", "synthetic/model[1m]", "--", "synthetic task",
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %#v, %v; want %#v", got, err, want)
	}
}

func TestGoalIsSingleArgument(t *testing.T) {
	c := planner(t, "")
	for _, goal := range []string{"--dangerously-skip-permissions --model attacker", "--mcp-config /host/secret", "$(shell); & command; second clause", "非 ASCII 合法任务"} {
		r := validRequest()
		r.Goal = goal
		got, err := c.Plan(r)
		if err != nil || !reflect.DeepEqual(got.Command[len(got.Command)-2:], []string{"--", goal}) || len(got.Command) != 27 {
			t.Fatalf("goal changed argv structure: %#v, %v", got, err)
		}
	}
	first, _ := c.Plan(validRequest())
	first.Command[0] = "mutated"
	second, _ := c.Plan(validRequest())
	if second.Command[0] != "claude" {
		t.Fatal("plans share mutable argv")
	}
}

// Mirror SbxBackend.Exec's command/user and argv validation (sbx.go) without
// creating an environment or executing any process. This checks transport
// compatibility only, not the later guest identity and policy preflight.
func TestPlansSatisfySbxArgvValidation(t *testing.T) {
	for _, model := range []string{"", "synthetic/model[1m]"} {
		c := planner(t, model)
		for _, goal := range []string{"synthetic task", "--dangerously-skip-permissions", "$(shell); & command\targument", "非 ASCII 合法任务", strings.Repeat("a", MaxGoalBytes)} {
			r := validRequest()
			r.Goal = goal
			plan, err := c.Plan(r)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Command) == 0 || plan.Command[0] == "" || strings.HasPrefix(plan.Command[0], "-") ||
				plan.User != "" && plan.User != "agent" && plan.User != "root" {
				t.Fatalf("plan violates SbxBackend command/user validation: %#v", plan)
			}
			for i, arg := range plan.Command {
				if strings.ContainsAny(arg, "\x00\r\n") {
					t.Fatalf("argv[%d] violates SbxBackend transport validation", i)
				}
			}
			if plan.User != "agent" || !reflect.DeepEqual(plan.Command[len(plan.Command)-2:], []string{"--", goal}) {
				t.Fatal("transport-compatible plan lost agent identity or goal boundary")
			}
		}
	}
}

func TestInvalidRequests(t *testing.T) {
	c := planner(t, "")
	cases := map[string]Request{}
	for name, goal := range map[string]string{"empty": "", "blank": " \n\t", "nul": "a\x00b", "CR": "first\rsecond", "LF": "first\nsecond", "CRLF": "first\r\nsecond", "invalid utf8": string([]byte{0xff}), "too large": strings.Repeat("a", MaxGoalBytes+1), "unicode byte limit": strings.Repeat("界", MaxGoalBytes/3+1)} {
		r := validRequest()
		r.Goal = goal
		cases[name] = r
	}
	for _, p := range []string{"", "/work/mcp.json", "C:\\host\\mcp.json", "//host/mcp.json", "/tmp/../tmp/pulsar-dock/mcp.json", "/tmp//pulsar-dock/mcp.json", "/tmp/pulsar-dock/mcp.json/", "/tmp/pulsar-dock/mcp.json\n", "/tmp/pulsar-dock/mcp.json\x00", "{\"mcpServers\":{}}"} {
		r := validRequest()
		r.MCPConfigPath = p
		cases["path "+p] = r
	}
	for name, budget := range map[string]float64{"zero": 0, "negative": -1, "nan": math.NaN(), "infinity": math.Inf(1), "negative infinity": math.Inf(-1)} {
		r := validRequest()
		r.MaxBudgetUSD = budget
		cases["budget "+name] = r
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := c.Plan(r)
			if !errors.Is(err, ErrInvalid) || !reflect.DeepEqual(got, runtime.ExecRequest{}) {
				t.Fatalf("invalid request produced plan: %#v, %v", got, err)
			}
		})
	}
	for _, goal := range []string{strings.Repeat("a", MaxGoalBytes), strings.Repeat("界", MaxGoalBytes/3)} {
		r := validRequest()
		r.Goal = goal
		if _, err := c.Plan(r); err != nil {
			t.Fatalf("bounded valid goal rejected: %v", err)
		}
	}
}

func TestModelAndConfigurationSurface(t *testing.T) {
	for _, model := range []string{"--model", "model --flag", "model\nflag", "model\x00", strings.Repeat("a", 129)} {
		if _, err := NewClaude(TrustedClaudeConfig{VersionOutput: "2.1.300 (Claude Code)", Model: model}); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted invalid model %q", model)
		}
	}
	// Keep configuration closed: adding credentials, environment or arbitrary
	// arguments here requires an intentional API/security review and test update.
	for _, tc := range []struct {
		value  any
		fields []string
	}{
		{TrustedClaudeConfig{}, []string{"VersionOutput", "Model"}},
		{Request{}, []string{"Goal", "MCPConfigPath", "MaxBudgetUSD"}},
	} {
		typ := reflect.TypeOf(tc.value)
		var fields []string
		for i := 0; i < typ.NumField(); i++ {
			fields = append(fields, typ.Field(i).Name)
		}
		if !reflect.DeepEqual(fields, tc.fields) {
			t.Errorf("unexpected configuration surface: %v", fields)
		}
	}
}

func TestUnsupportedNeverReturnsPlan(t *testing.T) {
	for _, c := range []*Claude{nil, {}} {
		got, err := c.Plan(validRequest())
		if !errors.Is(err, ErrUnsupported) || !reflect.DeepEqual(got, runtime.ExecRequest{}) {
			t.Fatalf("unverified planner returned plan: %#v, %v", got, err)
		}
	}
	got, err := PlanCodex(validRequest())
	if !errors.Is(err, ErrUnsupported) || !reflect.DeepEqual(got, runtime.ExecRequest{}) {
		t.Fatalf("Codex returned guessed plan: %#v, %v", got, err)
	}
}
