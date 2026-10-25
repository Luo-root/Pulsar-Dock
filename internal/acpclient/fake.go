package acpclient

import (
	"context"
	"sync"
)

type Fake struct {
	mu          sync.Mutex
	initialized bool
	closed      bool
	result      InitializeResult
	session     Session
	prompt      bool
	updates     chan Update
	permissions map[string]bool
}

func NewFake(result InitializeResult) *Fake {
	return &Fake{result: result, permissions: make(map[string]bool)}
}
func (f *Fake) Initialize(context.Context, InitializeRequest) (InitializeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return InitializeResult{}, ErrSessionClosed
	}
	if f.result.Protocol != ProtocolVersion {
		return InitializeResult{}, ErrUnsupported
	}
	f.initialized = true
	return f.result, nil
}
func (f *Fake) NewSession(_ context.Context, req NewSessionRequest) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return Session{}, ErrSessionClosed
	}
	if !f.initialized || req.Identity.TaskID == "" || req.Identity.EnvironmentID == "" {
		return Session{}, ErrInvalid
	}
	f.session = Session{ID: "fake-session", Identity: req.Identity, Generation: f.session.Generation + 1}
	return f.session, nil
}
func (f *Fake) Prompt(_ context.Context, s Session, req PromptRequest) (<-chan Update, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := requireSession(s); err != nil {
		return nil, err
	}
	if f.closed {
		return nil, ErrSessionClosed
	}
	if s != f.session || s.Generation != f.session.Generation {
		return nil, ErrStaleGeneration
	}
	if req.Text == "" {
		return nil, ErrInvalid
	}
	if f.prompt && !f.result.Capabilities.ConcurrentPrompts {
		return nil, ErrPromptInFlight
	}
	if !f.result.Capabilities.Streaming {
		return nil, ErrUnsupported
	}
	f.prompt = true
	f.updates = make(chan Update, 16)
	return f.updates, nil
}
func (f *Fake) Cancel(_ context.Context, s Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := requireSession(s); err != nil {
		return err
	}
	if f.closed {
		return ErrSessionClosed
	}
	if s != f.session || s.Generation != f.session.Generation {
		return ErrStaleGeneration
	}
	f.prompt = false
	if f.updates != nil {
		close(f.updates)
		f.updates = nil
	}
	return nil
}
func (f *Fake) PermissionResponse(_ context.Context, s Session, r PermissionResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := requireSession(s); err != nil {
		return err
	}
	if f.closed {
		return ErrSessionClosed
	}
	if s != f.session || !f.result.Capabilities.Permissions {
		return ErrUnsupported
	}
	if _, ok := f.permissions[r.RequestID]; !ok {
		return ErrPermissionNotFound
	}
	f.permissions[r.RequestID] = r.Allowed
	return nil
}
func (f *Fake) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	if f.updates != nil {
		close(f.updates)
		f.updates = nil
	}
	return nil
}
func (f *Fake) Emit(u Update) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.updates == nil {
		return ErrSessionClosed
	}
	if u.Generation != f.session.Generation {
		return ErrStaleGeneration
	}
	if !f.result.Capabilities.Supports(u.Kind) {
		return ErrUnsupported
	}
	if u.Kind == UpdatePermission && u.RequestID != "" {
		f.permissions[u.RequestID] = false
	}
	f.updates <- u
	if u.Kind == UpdateStatus && u.Status == "done" {
		f.prompt = false
	}
	return nil
}
