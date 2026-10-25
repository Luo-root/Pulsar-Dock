// Package modelproxy provides an instance-scoped model HTTP gate, not MCP identity
// authentication or environment/channel binding. The owner must provide network
// isolation, token delivery, revocation, concurrency and usage budgets separately.
package modelproxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config is trusted host configuration. UpstreamURL is an HTTP(S) origin with a
// literal loopback IP (not a DNS name), without a path, credentials or query.
// UpstreamToken may be a Magpie sentinel; it is never taken from guest headers.
// GuestToken must be distinct and at least 32 bytes, or empty to generate one.
// Zero bounds default to 1 MiB request, 64 MiB response and two minutes total.
// Timeout includes streaming; callers must configure their server's read limits.
type Config struct {
	UpstreamURL      string
	UpstreamToken    string
	GuestToken       string
	Model            string
	UpstreamModel    string
	MaxBodyBytes     int64
	MaxResponseBytes int64
	Timeout          time.Duration
}

// Proxy only forwards fixed provider POST routes: Anthropic Messages, OpenAI-compatible Chat Completions, and Responses.
// It has no listener, credential discovery, logging or administration endpoints.
type Proxy struct {
	origin        string
	upstreamToken string
	guestToken    string
	guestHash     [32]byte
	model         string
	upstreamModel string
	maxBody       int64
	maxResponse   int64
	timeout       time.Duration
	client        *http.Client
}

func New(cfg Config) (*Proxy, error) {
	u, err := url.Parse(cfg.UpstreamURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return nil, errors.New("modelproxy: invalid upstream origin")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || u.Host == "" {
		return nil, errors.New("modelproxy: upstream must be a literal loopback IP")
	}
	if cfg.Model == "" || strings.TrimSpace(cfg.Model) != cfg.Model || len(cfg.Model) > 256 {
		return nil, errors.New("modelproxy: invalid model")
	}
	if !validToken(cfg.UpstreamToken) {
		return nil, errors.New("modelproxy: invalid upstream token")
	}
	if cfg.GuestToken == "" {
		var token [32]byte
		if _, err := rand.Read(token[:]); err != nil {
			return nil, errors.New("modelproxy: token generation failed")
		}
		cfg.GuestToken = hex.EncodeToString(token[:])
	}
	if len(cfg.GuestToken) < 32 || !validToken(cfg.GuestToken) || cfg.GuestToken == cfg.UpstreamToken {
		return nil, errors.New("modelproxy: invalid guest token")
	}
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = 1 << 20
	}
	if cfg.MaxResponseBytes == 0 {
		cfg.MaxResponseBytes = 64 << 20
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Minute
	}
	if cfg.MaxBodyBytes < 1 || cfg.MaxBodyBytes > 64<<20 || cfg.MaxResponseBytes < 1 || cfg.MaxResponseBytes > 1<<30 || cfg.Timeout < 0 {
		return nil, errors.New("modelproxy: invalid bounds")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Never use ambient HTTP_PROXY to send host credentials.
	transport.DisableCompression = true
	transport.ResponseHeaderTimeout = cfg.Timeout
	transport.MaxResponseHeaderBytes = 32 << 10
	return &Proxy{
		origin: u.Scheme + "://" + u.Host, upstreamToken: cfg.UpstreamToken,
		guestToken: cfg.GuestToken, guestHash: sha256.Sum256([]byte(cfg.GuestToken)), model: cfg.Model, upstreamModel: cfg.UpstreamModel,
		maxBody: cfg.MaxBodyBytes, maxResponse: cfg.MaxResponseBytes, timeout: cfg.Timeout,
		client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func validToken(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range []byte(s) {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

// GuestToken returns the scoped secret for trusted delivery to one instance.
// Do not log it or place it in artifacts. Destroy/restrict access when revoked.
func (p *Proxy) GuestToken() string { return p.guestToken }

// Close releases idle upstream connections; it does not revoke active calls.
func (p *Proxy) Close() { p.client.CloseIdleConnections() }

func reject(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"error":{"code":"`+code+`"}}`)
}

func validGuestAuth(r *http.Request, expected [32]byte) bool {
	auth := r.Header.Values("Authorization")
	apiKey := r.Header.Values("X-Api-Key")
	// Authorization and x-api-key are alternative provider-compatible guest
	// credentials, never a combined mode. Multiple values are ambiguous.
	if (len(auth) != 0) == (len(apiKey) != 0) || (len(auth) > 1) || (len(apiKey) > 1) {
		return false
	}
	candidate := ""
	if len(auth) == 1 && strings.HasPrefix(auth[0], "Bearer ") {
		candidate = strings.TrimPrefix(auth[0], "Bearer ")
	} else if len(apiKey) == 1 {
		candidate = apiKey[0]
	}
	hash := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(hash[:], expected[:]) == 1
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || (r.URL.Path != "/v1/messages" && r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/v1/responses") || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.Fragment != "" || r.URL.IsAbs() || r.URL.Host != "" || (r.RequestURI != "" && r.RequestURI != r.URL.Path) {
		reject(w, http.StatusNotFound, "route_not_allowed")
		return
	}
	if !validGuestAuth(r, p.guestHash) {
		reject(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || r.Header.Get("Content-Encoding") != "" {
		reject(w, http.StatusUnsupportedMediaType, "json_required")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, p.maxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			reject(w, http.StatusRequestEntityTooLarge, "request_too_large")
		} else {
			reject(w, http.StatusBadRequest, "invalid_body")
		}
		return
	}
	// Canonicalize the top-level object and reject duplicates, so routing model
	// validation cannot disagree with the upstream JSON parser.
	fields, err := object(body)
	var model string
	if err != nil || json.Unmarshal(fields["model"], &model) != nil || model != p.model {
		reject(w, http.StatusBadRequest, "model_not_allowed")
		return
	}
	if p.upstreamModel != "" {
		fields["model"] = json.RawMessage(strconv.Quote(p.upstreamModel))
	}
	body, err = json.Marshal(fields)
	if err != nil {
		reject(w, http.StatusBadRequest, "invalid_body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.origin+r.URL.Path, bytes.NewReader(body))
	if err != nil {
		reject(w, http.StatusBadGateway, "upstream_unavailable")
		return
	}
	// Allowlist rather than copying client authentication, forwarding, cookies,
	// beta features, destination or arbitrary provider headers.
	req.Header.Set("Content-Type", "application/json")
	if r.URL.Path == "/v1/messages" {
		req.Header.Set("x-api-key", p.upstreamToken)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+p.upstreamToken)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		reject(w, http.StatusBadGateway, "upstream_unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || resp.Header.Get("Content-Encoding") != "" {
		reject(w, http.StatusBadGateway, "upstream_rejected")
		return
	}
	contentType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || (contentType != "application/json" && contentType != "text/event-stream") {
		reject(w, http.StatusBadGateway, "upstream_invalid_response")
		return
	}
	if resp.ContentLength > p.maxResponse {
		reject(w, http.StatusBadGateway, "upstream_response_too_large")
		return
	}
	if contentType == "application/json" {
		data, err := io.ReadAll(io.LimitReader(resp.Body, p.maxResponse+1))
		if err != nil || int64(len(data)) > p.maxResponse {
			reject(w, http.StatusBadGateway, "upstream_invalid_response")
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(data)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	controller := http.NewResponseController(w)
	_ = controller.Flush()
	buf := make([]byte, 16<<10)
	remaining := p.maxResponse
	for remaining > 0 {
		n, readErr := resp.Body.Read(buf[:min(int64(len(buf)), remaining)])
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			remaining -= int64(n)
			if err := controller.Flush(); err != nil {
				return
			}
		}
		if readErr != nil {
			return
		}
	}
	// Once streaming headers are sent, failures/limits can only end the stream;
	// never append an upstream error body or secret-bearing diagnostics.
}

func object(body []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return nil, errors.New("object required")
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := t.(string)
		if !ok {
			return nil, errors.New("invalid key")
		}
		if _, exists := fields[key]; exists {
			return nil, errors.New("duplicate key")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("trailing data")
	}
	return fields, nil
}
