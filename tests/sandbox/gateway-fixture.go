//go:build sandboxfixture

package main

// This is a bounded synthetic validation fixture, not a production gateway.
// It has no connection to a real model, credential store, or external service.
import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type event struct {
	At       time.Time `json:"at"`
	Subject  string    `json:"subject"`
	Decision string    `json:"decision"`
	Path     string    `json:"path"`
}

type recorder struct {
	mu sync.Mutex
	f  *os.File
}

func (r *recorder) add(subject, decision, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = json.NewEncoder(r.f).Encode(event{time.Now().UTC(), subject, decision, path})
	_ = r.f.Sync()
}

type route struct {
	Subject string `json:"subject"`
	Port    int    `json:"port"`
}

func main() {
	out := flag.String("out", "", "Required directory for local-only evidence")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "-out is required; generated evidence is not part of the source repository")
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0700); err != nil {
		panic(err)
	}
	logFile, err := os.OpenFile(filepath.Join(*out, "gateway-events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		panic(err)
	}
	defer logFile.Close()
	record := &recorder{f: logFile}
	var servers []*http.Server
	var routes []route
	for _, subject := range []string{"a", "b"} {
		subject := subject
		expected := "Bearer PULSAR_SYNTHETIC_" + subject
		var mu sync.Mutex
		revoked := false
		mux := http.NewServeMux()
		mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"fixture": "pulsar-dock", "subject": subject})
		})
		mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
			// A separate fixture-only action, called by the host test driver.
			// Not a production admin API. A guest is allowed to revoke itself,
			// but cannot un-revoke or expand any access.
			if r.Header.Get("Authorization") != "Bearer PULSAR_HOST_FIXTURE_CONTROL" {
				record.add(subject, "deny-control", r.URL.Path)
				http.Error(w, "denied", http.StatusUnauthorized)
				return
			}
			mu.Lock()
			revoked = true
			mu.Unlock()
			record.add(subject, "revoked", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("POST /invoke", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			inactive := revoked
			mu.Unlock()
			if inactive || r.Header.Get("Authorization") != expected {
				record.add(subject, "deny", r.URL.Path)
				http.Error(w, "denied", http.StatusUnauthorized)
				return
			}
			record.add(subject, "allow", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"result": "synthetic-model-response", "subject": subject})
		})
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			panic(err)
		}
		server := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
		servers = append(servers, server)
		routes = append(routes, route{Subject: subject, Port: listener.Addr().(*net.TCPAddr).Port})
		go func() {
			if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
				fmt.Fprintln(os.Stderr, err)
			}
		}()
	}
	data, err := json.MarshalIndent(routes, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(*out, "gateway-routes.json"), data, 0600); err != nil {
		panic(err)
	}
	fmt.Println(string(data))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Minute):
	}
	for _, server := range servers {
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = server.Shutdown(shutdown)
		cancel()
	}
}
