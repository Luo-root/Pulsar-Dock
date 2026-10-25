//go:build sandboxsmoke

package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/modelproxy"
)

// TestRealModelRoute checks only the existing host model API behind the gate.
// It is not an Agent, sandbox, MCP or UI end-to-end acceptance test.
func TestRealModelRoute(t *testing.T) {
	if os.Getenv("PULSAR_REAL_MODEL_ROUTE") != "1" {
		t.Skip("explicit real model API opt-in required")
	}
	const model = "deepseek/deepseek-flash[1m]"
	proxy, err := modelproxy.New(modelproxy.Config{UpstreamURL: "http://127.0.0.1:3425", UpstreamToken: "magpie", Model: model, Timeout: 90 * time.Second, MaxResponseBytes: 1 << 20})
	if err != nil {
		t.Fatal("proxy configuration failed")
	}
	defer proxy.Close()
	server := httptest.NewServer(proxy)
	defer server.Close()
	for _, route := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"} {
		t.Run(route, func(t *testing.T) {
			payload := map[string]any{"model": model, "max_tokens": 32, "messages": []map[string]string{{"role": "user", "content": "Reply exactly PULSAR_ROUTE_OK and nothing else."}}}
			if route == "/v1/responses" {
				payload = map[string]any{"model": model, "input": "Reply exactly PULSAR_ROUTE_OK and nothing else.", "max_output_tokens": 32}
			}
			body, _ := json.Marshal(payload)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+route, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+proxy.GuestToken())
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal("model gate request failed; details withheld")
			}
			defer response.Body.Close()
			result, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
			if err != nil || len(result) > 1<<20 {
				t.Fatal("bounded model response failed")
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("model gate status=%d; upstream body withheld", response.StatusCode)
			}
			if !json.Valid(result) || !strings.Contains(string(result), "PULSAR_ROUTE_OK") {
				t.Fatal("expected synthetic marker absent; response withheld")
			}
			t.Log("real model API returned synthetic marker through restricted gate")
		})
	}
}
