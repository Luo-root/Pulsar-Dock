//go:build sandboxfixture

package main

// Synthetic guest-side runtime probes. No personal files or real credentials
// are read. Results are observations; the host driver decides acceptance.
import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func output(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		panic(err)
	}
}

func main() {
	if len(os.Args) < 2 {
		panic("expected inventory, write, absent, request, or server")
	}
	switch os.Args[1] {
	case "inventory":
		boot, bootErr := os.ReadFile("/proc/sys/kernel/random/boot_id")
		mounts, mountsErr := os.ReadFile("/proc/self/mountinfo")
		filtered := []string{}
		if mountsErr == nil {
			scanner := bufio.NewScanner(strings.NewReader(string(mounts)))
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, "virtiofs") || strings.Contains(line, "/mnt/") || strings.Contains(line, "skills") || strings.Contains(line, "/run/sandbox/source") {
					filtered = append(filtered, line)
				}
			}
		}
		sshSock := os.Getenv("SSH_AUTH_SOCK")
		_, socketErr := os.Stat(sshSock)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ssh := exec.CommandContext(ctx, "ssh-add", "-l")
		sshOutput, sshErr := ssh.CombinedOutput()
		sshExit := 0
		if sshErr != nil {
			sshExit = -1
			if e, ok := sshErr.(*exec.ExitError); ok {
				sshExit = e.ExitCode()
			}
		}
		// Never print key identities if the boundary was unexpectedly open.
		sshDetail := strings.TrimSpace(string(sshOutput))
		if sshExit == 0 {
			sshDetail = "identities were returned; details intentionally redacted"
		}
		output(map[string]any{"goos": runtime.GOOS, "arch": runtime.GOARCH, "uid": os.Getuid(), "boot_id": strings.TrimSpace(string(boot)), "boot_error": fmt.Sprint(bootErr), "selected_mounts": filtered, "ssh_socket_set": sshSock != "", "ssh_socket_exists": socketErr == nil, "ssh_add_exit": sshExit, "ssh_add_detail": sshDetail, "docker_host_env_set": os.Getenv("DOCKER_HOST") != "", "host_canary_environment_present": strings.Contains(os.Getenv("PULSAR_HOST_CANARY"), "PULSAR_SYNTHETIC")})
	case "write":
		if len(os.Args) != 4 { panic("write PATH VALUE") }
		if err := os.MkdirAll(filepath.Dir(os.Args[2]), 0700); err != nil { panic(err) }
		if err := os.WriteFile(os.Args[2], []byte(os.Args[3]), 0600); err != nil { panic(err) }
		digest := sha256.Sum256([]byte(os.Args[3]))
		output(map[string]any{"path": os.Args[2], "written": true, "sha256": hex.EncodeToString(digest[:])})
	case "absent":
		observations := []map[string]any{}
		for _, path := range os.Args[2:] {
			_, err := os.Stat(path)
			observations = append(observations, map[string]any{"path": path, "exists": err == nil, "not_exist": os.IsNotExist(err), "error": fmt.Sprint(err)})
		}
		output(observations)
	case "request":
		flags := flag.NewFlagSet("request", flag.ExitOnError)
		target := flags.String("url", "", "Synthetic target")
		method := flags.String("method", "GET", "Request method")
		proxyMode := flags.String("proxy", "env", "env or none")
		token := flags.String("token", "", "Synthetic fixture token only")
		_ = flags.Parse(os.Args[2:])
		transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 4*time.Second}).DialContext, TLSHandshakeTimeout: 4*time.Second}
		if *proxyMode == "none" { transport.Proxy = nil }
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 8*time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
		req, err := http.NewRequest(*method, *target, nil)
		if err != nil { panic(err) }
		if *token != "" { req.Header.Set("Authorization", "Bearer " + *token) }
		started := time.Now()
		response, err := client.Do(req)
		if err != nil {
			output(map[string]any{"url": *target, "proxy": *proxyMode, "connected": false, "error": err.Error(), "duration_ms": time.Since(started).Milliseconds()})
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		output(map[string]any{"url": *target, "proxy": *proxyMode, "connected": true, "status": response.StatusCode, "body": string(body), "duration_ms": time.Since(started).Milliseconds()})
	case "server":
		flags := flag.NewFlagSet("server", flag.ExitOnError)
		port := flags.Int("port", 18181, "Guest test port")
		marker := flags.String("marker", "PULSAR_SYNTHETIC_SERVER", "Synthetic marker")
		readyFile := flags.String("ready", "/tmp/pulsar-probe/server-ready.json", "Readiness file")
		_ = flags.Parse(os.Args[2:])
		listener, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", *port))
		if err != nil { panic(err) }
		ips := []string{}
		addrs, _ := net.InterfaceAddrs()
		for _, addr := range addrs {
			n, ok := addr.(*net.IPNet)
			if ok && n.IP.To4() != nil && !n.IP.IsLoopback() { ips = append(ips, n.IP.String()) }
		}
		child := exec.Command(os.Args[0], "heartbeat", "/tmp/pulsar-probe/child-heartbeat.txt")
		if err := child.Start(); err != nil { panic(err) }
		defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
		boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
		ready, _ := json.Marshal(map[string]any{"port": *port, "ips": ips, "pid": os.Getpid(), "child_pid": child.Process.Pid, "boot_id": strings.TrimSpace(string(boot)), "heartbeat_file": "/tmp/pulsar-probe/child-heartbeat.txt"})
		if err := os.MkdirAll(filepath.Dir(*readyFile), 0700); err != nil { panic(err) }
		if err := os.WriteFile(*readyFile, ready, 0600); err != nil { panic(err) }
		output(json.RawMessage(ready))
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, *marker) })
		s := &http.Server{Handler: mux, ReadHeaderTimeout: 3*time.Second, WriteTimeout: 3*time.Second}
		go func() { time.Sleep(20*time.Minute); _ = s.Close() }()
		if err := s.Serve(listener); err != nil && err != http.ErrServerClosed { panic(err) }
	case "heartbeat":
		if len(os.Args) != 3 { panic("heartbeat PATH") }
		for n := 0; n < 1200; n++ {
			if err := os.WriteFile(os.Args[2], []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0600); err != nil { panic(err) }
			time.Sleep(time.Second)
		}
	case "lifecycle-check":
		boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
		heartbeat1, err1 := os.ReadFile("/tmp/pulsar-probe/child-heartbeat.txt")
		time.Sleep(2*time.Second)
		heartbeat2, err2 := os.ReadFile("/tmp/pulsar-probe/child-heartbeat.txt")
		conn, connectErr := net.DialTimeout("tcp4", "127.0.0.1:18181", 2*time.Second)
		if conn != nil { _ = conn.Close() }
		output(map[string]any{"boot_id": strings.TrimSpace(string(boot)), "heartbeat_exists": err1 == nil && err2 == nil, "heartbeat_unchanged": string(heartbeat1) == string(heartbeat2), "server_port_open": connectErr == nil})
	case "proxy":
		u, _ := url.Parse("http://host.docker.internal:27871/health")
		p, err := http.ProxyFromEnvironment(&http.Request{URL:u})
		// Print only location, never URL credentials or environment variable values.
		proxyHost := ""
		if p != nil { proxyHost = p.Host }
		output(map[string]any{"proxy_host":proxyHost,"error":fmt.Sprint(err)})
	default:
		panic("unknown probe")
	}
}
