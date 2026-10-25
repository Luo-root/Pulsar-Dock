package modelproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const syntheticGuest = "synthetic-guest-token-0123456789abcdef"
const syntheticUpstream = "synthetic-upstream-secret"

func config(origin string) Config {
	return Config{UpstreamURL: origin, UpstreamToken: syntheticUpstream, GuestToken: syntheticGuest, Model: "approved-model"}
}

func proxyFor(t *testing.T, cfg Config) *Proxy {
	t.Helper()
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func request(path, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+syntheticGuest)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestConfiguration(t *testing.T) {
	for _, origin := range []string{"https://example.com", "http://localhost:1234", "http://192.168.0.1", "http://127.0.0.1/admin", "http://user:pass@127.0.0.1", "http://127.0.0.1?", "http://127.0.0.1#fragment", "ftp://127.0.0.1", "http://127.0.0.1/%2f"} {
		t.Run(origin, func(t *testing.T) {
			if _, err := New(config(origin)); err == nil {
				t.Fatal("accepted unsafe origin")
			}
		})
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.GuestToken = c.UpstreamToken },
		func(c *Config) { c.UpstreamToken = "bad\r\nheader" },
		func(c *Config) { c.Model = "" },
		func(c *Config) { c.GuestToken = "short" },
		func(c *Config) { c.Timeout = -time.Second },
		func(c *Config) { c.MaxBodyBytes = -1 },
		func(c *Config) { c.MaxResponseBytes = -1 },
	} {
		cfg := config("http://127.0.0.1:1234")
		mutate(&cfg)
		if _, err := New(cfg); err == nil {
			t.Fatal("accepted unsafe config")
		}
	}
	cfg := config("http://[::1]:1234")
	cfg.GuestToken = ""
	p := proxyFor(t, cfg)
	q := proxyFor(t, cfg)
	if len(p.GuestToken()) != 64 || p.GuestToken() == q.GuestToken() {
		t.Fatal("tokens not independently generated")
	}
}

func TestForwardingAllowlist(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/chat/completions"} {
		t.Run(path, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.RequestURI() != path {
					t.Errorf("unexpected route: %s %s", r.Method, r.URL)
				}
				for _, header := range []string{"Cookie", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Arbitrary", "Anthropic-Beta"} {
					if r.Header.Get(header) != "" {
						t.Errorf("forwarded %s", header)
					}
				}
				if path == "/v1/messages" {
					if r.Header.Get("X-Api-Key") != syntheticUpstream || r.Header.Get("Authorization") != "" || r.Header.Get("Anthropic-Version") != "2023-06-01" {
						t.Error("incorrect anthropic credentials")
					}
				} else if r.Header.Get("Authorization") != "Bearer "+syntheticUpstream || r.Header.Get("X-Api-Key") != "" {
					t.Error("incorrect openai credentials")
				}
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), `"model":"approved-model"`) || strings.Contains(string(body), syntheticGuest) {
					t.Error("invalid forwarded body")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Set-Cookie", "secret=cookie")
				w.Header().Set("Location", "/admin")
				w.Header().Set("Server", "private-server")
				w.Header().Set("X-Secret", syntheticUpstream)
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			defer up.Close()
			p := proxyFor(t, config(up.URL))
			r := request(path, `{"model":"approved-model","messages":[]}`)
			for _, h := range []string{"Cookie", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Arbitrary", "Anthropic-Beta"} {
				r.Header.Set(h, "guest-controlled")
			}
			w := httptest.NewRecorder()
			p.ServeHTTP(w, r)
			if w.Code != 200 || w.Body.String() != `{"ok":true}` {
				t.Fatalf("response %d %s", w.Code, w.Body)
			}
			for _, h := range []string{"Set-Cookie", "Location", "Server", "X-Secret"} {
				if w.Header().Get(h) != "" {
					t.Errorf("leaked %s", h)
				}
			}
		})
	}
}

func TestRejectGuestInputs(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer up.Close()
	cfg := config(up.URL)
	cfg.MaxBodyBytes = 100
	p := proxyFor(t, cfg)
	tests := []struct {
		name   string
		mutate func(*http.Request)
		status int
	}{
		{"get", func(r *http.Request) { r.Method = "GET" }, 404},
		{"get-admin", func(r *http.Request) { r.Method = "GET"; r.URL.Path = "/admin" }, 404},
		{"admin", func(r *http.Request) { r.URL.Path = "/admin" }, 404},
		{"query", func(r *http.Request) { r.URL.RawQuery = "url=http://attacker" }, 404},
		{"empty-query", func(r *http.Request) { r.URL.ForceQuery = true }, 404},
		{"encoded-path", func(r *http.Request) { r.URL.RawPath = "/v1/%6dessages" }, 404},
		{"absolute", func(r *http.Request) { r.URL.Scheme = "http"; r.URL.Host = "attacker" }, 404},
		{"responses", func(r *http.Request) { r.URL.Path = "/v1/responses" }, 404},
		{"missing-auth", func(r *http.Request) { r.Header.Del("Authorization") }, 401},
		{"api-key-auth", func(r *http.Request) { r.Header.Del("Authorization"); r.Header.Set("X-Api-Key", syntheticGuest) }, 200},
		{"upstream-auth", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+syntheticUpstream) }, 401},
		{"duplicate-auth", func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+syntheticGuest) }, 401},
		{"duplicate-api-key", func(r *http.Request) {
			r.Header.Del("Authorization")
			r.Header.Set("X-Api-Key", syntheticGuest)
			r.Header.Add("X-Api-Key", syntheticGuest)
		}, 401},
		{"conflicting-auth", func(r *http.Request) { r.Header.Set("X-Api-Key", syntheticGuest) }, 401},
		{"compressed", func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }, 415},
		{"not-json", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := request("/v1/messages", `{"model":"approved-model"}`)
			tc.mutate(r)
			w := httptest.NewRecorder()
			p.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d", w.Code, tc.status)
			}
		})
	}
	for _, body := range []string{`{"model":"other"}`, `{"Model":"approved-model"}`, `{"model":"approved-model","model":"other"}`, `{"model":"approved-model"} {}`, `[]`, `{"model":`, `{"model":null}`} {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, request("/v1/messages", body))
		if w.Code != 400 {
			t.Fatalf("body %q: status %d", body, w.Code)
		}
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, request("/v1/messages", strings.Repeat("x", 101)))
	if w.Code != 413 {
		t.Fatalf("body bound status %d", w.Code)
	}
	if calls.Load() != 1 {
		t.Fatalf("unexpected upstream calls: got %d, want one valid api-key call", calls.Load())
	}
}

func TestUpstreamFailuresAndBounds(t *testing.T) {
	var redirects atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects.Add(1) }))
	defer target.Close()
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		code    string
	}{
		{"error", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, syntheticUpstream)
		}, "upstream_rejected"},
		{"redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }, "upstream_rejected"},
		{"oversize", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, strings.Repeat("x", 65))
		}, "upstream_response_too_large"},
		{"chunked-oversize", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, strings.Repeat("x", 65))
		}, "upstream_invalid_response"},
		{"html", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, syntheticUpstream)
		}, "upstream_invalid_response"},
		{"timeout", func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		}, "upstream_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(tc.handler)
			defer up.Close()
			cfg := config(up.URL)
			cfg.MaxResponseBytes = 64
			cfg.Timeout = 100 * time.Millisecond
			p := proxyFor(t, cfg)
			w := httptest.NewRecorder()
			p.ServeHTTP(w, request("/v1/messages", `{"model":"approved-model"}`))
			if w.Code != 502 || !strings.Contains(w.Body.String(), tc.code) || strings.Contains(w.Body.String(), syntheticUpstream) {
				t.Fatalf("unsafe response %d %s", w.Code, w.Body)
			}
		})
	}
	if redirects.Load() != 0 {
		t.Fatal("followed redirect")
	}
}

func TestCancellationAndSSEFlush(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer up.Close()
	p := proxyFor(t, config(up.URL))
	gate := httptest.NewServer(p)
	defer gate.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, "POST", gate.URL+"/v1/messages", strings.NewReader(`{"model":"approved-model","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+syntheticGuest)
	r.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-started
	first := make([]byte, len("data: first\n\n"))
	if _, err := io.ReadFull(resp.Body, first); err != nil || string(first) != "data: first\n\n" {
		t.Fatalf("SSE not flushed: %q %v", first, err)
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream was not cancelled")
	}
}

func TestSSEBodyBound(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, strings.Repeat("x", 128))
	}))
	defer up.Close()
	cfg := config(up.URL)
	cfg.MaxResponseBytes = 32
	p := proxyFor(t, cfg)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, request("/v1/messages", `{"model":"approved-model"}`))
	if w.Code != 200 || w.Body.Len() != 32 || !w.Flushed {
		t.Fatalf("SSE bound failed: %d bytes %d flushed %v", w.Code, w.Body.Len(), w.Flushed)
	}
}
