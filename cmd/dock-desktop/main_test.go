package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Luo-root/pulsar-dock/internal/desktopapi"
)

func testHandlerFactory(facades map[string]*desktopapi.Facade, cfg desktopapi.HTTPConfig) (http.Handler, error) {
	if len(facades) != 3 || cfg.Token == "" || len(cfg.AllowedOrigins) != 1 {
		return nil, errors.New("unexpected handler config")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/catalogue" {
			json.NewEncoder(w).Encode(map[string]string{"transport": "available"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}), nil
}

func TestCloseAllowsSameDirectoryRestart(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		s, err := Start(context.Background(), dir, "http://127.0.0.1:15353", "127.0.0.1:0", testHandlerFactory)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(dir, bootstrapName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("bootstrap remained after close")
		}
	}
}

func TestWriteBootstrapIsRandomAndPrivate(t *testing.T) {
	dir := t.TempDir()
	path, b, err := writeBootstrap(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Token) != base64.RawURLEncoding.EncodedLen(32) {
		t.Fatalf("token length = %d", len(b.Token))
	}
	if strings.Contains(path, b.Token) {
		t.Fatal("token leaked into bootstrap path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Bootstrap
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got != b {
		t.Fatalf("bootstrap mismatch: %#v != %#v", got, b)
	}
	if _, _, err := writeBootstrap(dir); err == nil {
		t.Fatal("bootstrap overwrite must fail")
	}
}

func TestStartRejectsNonLoopback(t *testing.T) {
	_, err := Start(context.Background(), t.TempDir(), "http://127.0.0.1:15353", "0.0.0.0:0", func(map[string]*desktopapi.Facade, desktopapi.HTTPConfig) (http.Handler, error) {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("error = %v", err)
	}
}

func TestStartRealHTTPRequestUsesActualHost(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := Start(ctx, filepath.Clean(dir), "http://127.0.0.1:15353", "127.0.0.1:0", desktopapi.NewHTTPHandler)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()
	req := httptest.NewRequest(http.MethodGet, "http://"+s.Listener.Addr().String()+"/api/agents", nil)
	req.Host = s.Listener.Addr().String()
	req.Header.Set("Authorization", "Bearer "+readBootstrapToken(t, s.BootstrapPath))
	req.Header.Set("Origin", "http://127.0.0.1:15353")
	rr := httptest.NewRecorder()
	s.HTTP.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func readBootstrapToken(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var b Bootstrap
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatal(err)
	}
	return b.Token
}

func TestStartShutdownIsBounded(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := Start(ctx, filepath.Clean(dir), "http://127.0.0.1:15353", "127.0.0.1:0", func(map[string]*desktopapi.Facade, desktopapi.HTTPConfig) (http.Handler, error) {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()
	cancel()
	deadline := time.After(2 * time.Second)
	for {
		err := s.HTTP.Shutdown(context.Background())
		if err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("shutdown did not complete")
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
}
