package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/controller"
	"github.com/Luo-root/pulsar-dock/internal/desktopapi"
	"github.com/Luo-root/pulsar-dock/internal/runtime"
	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

const bootstrapName = "desktop-bootstrap.json"

// HTTPConfig is kept at the command boundary so transport policy is explicit.
// The transport package can map this to its concrete configuration when wired.
type HandlerFactory func(map[string]*desktopapi.Facade, desktopapi.HTTPConfig) (http.Handler, error)

var DefaultHandlerFactory HandlerFactory = desktopapi.NewHTTPHandler

type Bootstrap struct {
	Version int    `json:"version"`
	Token   string `json:"token"`
}

type Server struct {
	HTTP          *http.Server
	Listener      net.Listener
	BootstrapPath string
	Store         *taskstore.Store
	Backend       *runtime.SbxBackend
	closeOnce     sync.Once
	closeErr      error
	done          chan struct{}
	bootstrapInfo os.FileInfo
}

func main() {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	stateDir := fs.String("state-dir", "", "existing workspace state directory (required; no home/temp default)")
	origin := fs.String("origin", "", "explicit trusted UI origin, for example http://127.0.0.1:15353 (required)")
	addr := fs.String("listen", "127.0.0.1:0", "loopback listen address")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s --state-dir DIR --origin ORIGIN [--listen 127.0.0.1:0]\n", os.Args[0])
		fs.PrintDefaults()
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		return
	}
	if *stateDir == "" || *origin == "" {
		fs.Usage()
		fmt.Fprintln(os.Stderr, "error: --state-dir and --origin are required")
		os.Exit(2)
	}
	ctx, stop := signalContext()
	defer stop()
	server, err := Start(ctx, *stateDir, *origin, *addr, DefaultHandlerFactory)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: desktop service unavailable")
		os.Exit(1)
	}
	defer server.Close()
	fmt.Printf("desktop service listening at http://%s\nbootstrap file: %s\n", server.Listener.Addr().String(), server.BootstrapPath)
	if err := server.Serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		os.Exit(1)
	}
}

func Start(ctx context.Context, stateDir, origin, listenAddr string, factory HandlerFactory) (_ *Server, err error) {
	if ctx == nil || factory == nil || strings.TrimSpace(stateDir) == "" || !filepath.IsAbs(stateDir) || strings.TrimSpace(origin) == "" {
		return nil, errors.New("explicit absolute state directory, origin and handler required")
	}
	if !strings.HasPrefix(listenAddr, "127.0.0.1:") && !strings.HasPrefix(listenAddr, "localhost:") {
		return nil, errors.New("listen address must be loopback")
	}
	info, err := os.Stat(stateDir)
	if err != nil || !info.IsDir() {
		return nil, errors.New("state directory must already exist")
	}
	staging := filepath.Join(stateDir, "staging")
	if err := os.MkdirAll(staging, 0700); err != nil {
		return nil, errors.New("state directory unavailable")
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, errors.New("loopback listener unavailable")
	}
	keepListener := false
	defer func() {
		if !keepListener {
			_ = ln.Close()
		}
	}()
	if host, _, _ := net.SplitHostPort(ln.Addr().String()); host != "127.0.0.1" && host != "::1" {
		return nil, errors.New("listener escaped loopback")
	}
	bootstrapPath, bootstrap, err := writeBootstrap(stateDir)
	if err != nil {
		return nil, err
	}
	bootstrapCreated := true
	defer func() {
		if !keepListener && bootstrapCreated {
			_ = os.Remove(bootstrapPath)
		}
	}()
	store, err := taskstore.Open(filepath.Join(stateDir, "tasks.sqlite"))
	if err != nil {
		return nil, errors.New("task store unavailable")
	}
	keepStore := false
	defer func() {
		if !keepStore {
			_ = store.Close()
		}
	}()
	backend := runtime.NewSbxBackend(nil, staging)
	facades := make(map[string]*desktopapi.Facade, 3)
	for _, agent := range []string{"claude", "codex", "opencode"} {
		service, e := controller.New(store, backend, "desktop-"+agent, controller.Policy{
			Agent: agent, TemplateRef: runtime.SbxTemplate, Memory: "2g", CPUs: 2,
		})
		if e != nil {
			return nil, errors.New("controller unavailable")
		}
		f, e := desktopapi.New(service, store, "desktop-"+agent, agent, []desktopapi.AgentConfig{{ID: agent, DisplayName: agent, Installed: false, CLIVerified: false, ModelVerified: false, Operations: nil}})
		if e != nil {
			return nil, errors.New("desktop facade unavailable")
		}
		facades[agent] = f
	}
	h, err := factory(facades, desktopapi.HTTPConfig{Token: bootstrap.Token, AllowedHosts: []string{ln.Addr().String()}, AllowedOrigins: []string{origin}, MaxBodyBytes: 1 << 20})
	if err != nil || h == nil {
		return nil, errors.New("desktop HTTP transport unavailable")
	}
	s := &Server{HTTP: &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}, Listener: ln, BootstrapPath: bootstrapPath, Store: store, Backend: backend}
	s.bootstrapInfo, err = os.Lstat(bootstrapPath)
	if err != nil {
		return nil, errors.New("bootstrap identity unavailable")
	}
	s.done = make(chan struct{})
	keepListener = true
	keepStore = true
	bootstrapCreated = false
	go func() {
		select {
		case <-ctx.Done():
		case <-s.done:
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.HTTP.Shutdown(shutdownCtx)
		_ = s.Close()
	}()
	return s, nil
}

func (s *Server) Serve() error { return s.HTTP.Serve(s.Listener) }
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		if s.done != nil {
			close(s.done)
		}
		if s.HTTP != nil {
			s.closeErr = errors.Join(s.closeErr, s.HTTP.Close())
		}
		if s.Listener != nil {
			if err := s.Listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				s.closeErr = errors.Join(s.closeErr, err)
			}
		}
		if s.Store != nil {
			s.closeErr = errors.Join(s.closeErr, s.Store.Close())
		}
		if s.bootstrapInfo != nil {
			current, err := os.Lstat(s.BootstrapPath)
			if errors.Is(err, os.ErrNotExist) {
				return
			}
			if err != nil || !os.SameFile(s.bootstrapInfo, current) {
				s.closeErr = errors.Join(s.closeErr, errors.New("bootstrap identity changed; cleanup refused"))
				return
			}
			s.closeErr = errors.Join(s.closeErr, os.Remove(s.BootstrapPath))
		}
	})
	return s.closeErr
}

func writeBootstrap(dir string) (string, Bootstrap, error) {
	var raw [32]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", Bootstrap{}, errors.New("bootstrap generation failed")
	}
	b := Bootstrap{Version: 1, Token: base64.RawURLEncoding.EncodeToString(raw[:])}
	path := filepath.Join(dir, bootstrapName)
	data, _ := json.Marshal(b)
	data = append(data, '\n')
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", Bootstrap{}, errors.New("bootstrap file unavailable")
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return "", Bootstrap{}, errors.New("bootstrap file unavailable")
	}
	if err = f.Close(); err != nil {
		return "", Bootstrap{}, errors.New("bootstrap file unavailable")
	}
	return path, b, nil
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
