package desktopapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const DefaultMaxBodyBytes int64 = 64 << 10

type HTTPConfig struct {
	Token          string
	AllowedHosts   []string
	AllowedOrigins []string
	MaxBodyBytes   int64
}

// NewHTTPHandler returns a loopback-ready handler. It does not listen, inject
// credentials, or log request tokens/bodies. Each map entry is a fixed trusted
// facade; agentID in the URL only selects that entry and cannot change task scope.
func NewHTTPHandler(facades map[string]*Facade, cfg HTTPConfig) (http.Handler, error) {
	if len(facades) == 0 || strings.TrimSpace(cfg.Token) == "" || len(cfg.AllowedHosts) == 0 {
		return nil, ErrInvalid
	}
	for id, f := range facades {
		if !validID(id) || f == nil || f.agentID != id {
			return nil, ErrInvalid
		}
	}
	for _, h := range cfg.AllowedHosts {
		if strings.TrimSpace(h) == "" || h != strings.TrimSpace(h) || h == "*" {
			return nil, ErrInvalid
		}
	}
	for _, o := range cfg.AllowedOrigins {
		if strings.TrimSpace(o) == "" || o != strings.TrimSpace(o) || o == "*" {
			return nil, ErrInvalid
		}
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = DefaultMaxBodyBytes
	}
	copiedFacades := make(map[string]*Facade, len(facades))
	for id, f := range facades {
		copiedFacades[id] = f
	}
	cfg.AllowedHosts = append([]string(nil), cfg.AllowedHosts...)
	cfg.AllowedOrigins = append([]string(nil), cfg.AllowedOrigins...)
	return &httpHandler{facades: copiedFacades, cfg: cfg}, nil
}

type httpHandler struct {
	facades map[string]*Facade
	cfg     HTTPConfig
}

func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || r.URL.RawPath != "" || len(r.URL.Query()) != 0 {
		h.writeError(w, http.StatusBadRequest, ErrInvalid)
		return
	}
	if !h.hostAllowed(r.Host) || !h.originAllowed(r) {
		h.writeError(w, http.StatusForbidden, ErrUnauthorized)
		return
	}
	h.setCORS(w, r.Header.Get("Origin"))
	if r.Method == http.MethodOptions {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Allow", "GET, POST, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !h.authorized(r) {
		h.writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, ErrUnsupported)
		return
	}
	parts := pathParts(r.URL.Path)
	if len(parts) == 2 && parts[0] == "api" && parts[1] == "agents" && r.Method == http.MethodGet {
		h.agents(w, r)
		return
	}
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "agents" {
		h.writeError(w, http.StatusNotFound, ErrUnauthorized)
		return
	}
	f := h.facades[parts[2]]
	if f == nil {
		h.writeError(w, http.StatusNotFound, ErrUnauthorized)
		return
	}
	if len(parts) == 4 && parts[3] == "tasks" && r.Method == http.MethodPost {
		h.create(w, r, f)
		return
	}
	if len(parts) == 6 && parts[3] == "tasks" && r.Method == http.MethodGet {
		h.get(w, r, f, parts[4], parts[5])
		return
	}
	if len(parts) == 7 && parts[3] == "tasks" && parts[6] == "execute" && r.Method == http.MethodPost {
		h.execute(w, r, f, parts[4], parts[5])
		return
	}
	if len(parts) == 6 && parts[3] == "tasks" && parts[5] == "stop" && r.Method == http.MethodPost {
		h.stop(w, r, f, parts[4])
		return
	}
	h.writeError(w, http.StatusNotFound, ErrUnauthorized)
}

func (h *httpHandler) agents(w http.ResponseWriter, r *http.Request) {
	seen := make(map[string]bool)
	out := make([]AgentInfo, 0, len(h.facades))
	for _, f := range h.facades {
		a, err := f.Agents(r.Context())
		if err != nil {
			h.writeError(w, status(err), err)
			return
		}
		for _, x := range a {
			if !seen[x.ID] {
				seen[x.ID] = true
				out = append(out, x)
			}
		}
	}
	h.writeJSON(w, http.StatusOK, out)
}
func (h *httpHandler) create(w http.ResponseWriter, r *http.Request, f *Facade) {
	var req CreateTaskRequest
	if err := h.decode(r, &req); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	t, err := f.CreateTask(r.Context(), req)
	if err != nil {
		h.writeError(w, status(err), err)
		return
	}
	h.writeJSON(w, http.StatusCreated, t)
}
func (h *httpHandler) get(w http.ResponseWriter, r *http.Request, f *Facade, root, task string) {
	t, err := f.GetTask(r.Context(), root, task)
	if err != nil {
		h.writeError(w, status(err), err)
		return
	}
	h.writeJSON(w, http.StatusOK, t)
}
func (h *httpHandler) execute(w http.ResponseWriter, r *http.Request, f *Facade, root, task string) {
	x, err := f.ExecuteTask(r.Context(), root, task)
	if err != nil {
		h.writeError(w, status(err), err)
		return
	}
	h.writeJSON(w, http.StatusOK, x)
}
func (h *httpHandler) stop(w http.ResponseWriter, r *http.Request, f *Facade, root string) {
	if err := f.StopTask(r.Context(), root); err != nil {
		h.writeError(w, status(err), err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (h *httpHandler) decode(r *http.Request, dst any) error {
	if r.ContentLength > h.cfg.MaxBodyBytes {
		return ErrInvalid
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, h.cfg.MaxBodyBytes+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return ErrInvalid
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return ErrInvalid
	}
	return nil
}
func (h *httpHandler) authorized(r *http.Request) bool {
	if r.URL.Query().Get("token") != "" {
		return false
	}
	raw := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(raw, prefix) {
		return false
	}
	a, b := []byte(raw[len(prefix):]), []byte(h.cfg.Token)
	return len(a) == len(b) && subtle.ConstantTimeCompare(a, b) == 1
}
func (h *httpHandler) hostAllowed(host string) bool {
	for _, x := range h.cfg.AllowedHosts {
		if host == x {
			return true
		}
	}
	return false
}
func (h *httpHandler) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	for _, x := range h.cfg.AllowedOrigins {
		if origin == x {
			return true
		}
	}
	return false
}
func (h *httpHandler) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func (h *httpHandler) setCORS(w http.ResponseWriter, origin string) {
	// Origin is validated before dispatch; reflect only an exact configured origin.
	// Same-origin requests have no Origin and intentionally receive no wildcard.
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
}
func (h *httpHandler) writeError(w http.ResponseWriter, code int, err error) {
	e, ok := err.(*Error)
	if !ok {
		e = ErrUnavailable
	}
	h.writeJSON(w, code, e)
}
func status(err error) int {
	var e *Error
	if !errors.As(err, &e) {
		return http.StatusInternalServerError
	}
	switch e.Code {
	case "invalid_request":
		return http.StatusBadRequest
	case "not_found":
		return http.StatusNotFound
	case "conflict":
		return http.StatusConflict
	case "unsupported":
		return http.StatusNotImplemented
	case "canceled":
		return 499
	default:
		return http.StatusServiceUnavailable
	}
}
func pathParts(path string) []string {
	raw := strings.Split(strings.Trim(path, "/"), "/")
	if len(raw) == 1 && raw[0] == "" {
		return nil
	}
	for _, p := range raw {
		if p == "" || strings.ContainsAny(p, "?\\#") {
			return nil
		}
	}
	return raw
}
