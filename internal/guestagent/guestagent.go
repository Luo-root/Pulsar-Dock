// Package guestagent contains offline, approved guest-agent provisioning and
// verification. It never executes an agent on the host and does not discover
// credentials or accept arbitrary guest argv.
package guestagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Luo-root/pulsar-dock/internal/runtime"
)

const ChunkBytes int64 = 3 * 1024 * 1024

var ErrRejected = errors.New("guest agent operation rejected")

// Package is a trusted immutable package record. ArchiveSHA256 is mandatory;
// BinarySHA256 may only be populated by a separately reviewed registry update.
type Package struct {
	AgentID, Version, ArchivePath, ArchiveSHA256 string
	Member, GuestBinaryPath, ConfigRoot          string
	BinarySHA256                                 string
	VersionCommand                               []string
	LauncherPath                                 string
}

var registry = map[string]Package{
	"codex":    {AgentID: "codex", Version: "0.164.0", ArchivePath: "openai-codex-0.164.0-linux-x64.tgz", ArchiveSHA256: "62659fefd9a8efa21a496402fde3c11c025bbeee735a582968f8ffd14cb5f79c", BinarySHA256: "b93f6811d3f653f0fb83be4050d857fb9abbb111dfa2ce160563327daff7f306", Member: "package/vendor/x86_64-unknown-linux-musl/bin/codex", GuestBinaryPath: "/opt/pulsar-agents/codex", ConfigRoot: "/etc/pulsar-agents/codex", VersionCommand: []string{"--version"}},
	"opencode": {AgentID: "opencode", Version: "1.19.4", ArchivePath: "opencode-linux-x64-1.19.4.tgz", ArchiveSHA256: "dca7b06bbb4e4a1ab3681b799cd328057b356d9399e8e47336cf53b7ae658d0d", BinarySHA256: "538fcb1b6408c8d4608822acee19f7ff5b3a3eb3383c0633c4f06170857312a7", Member: "package/bin/opencode", GuestBinaryPath: "/opt/pulsar-agents/opencode", ConfigRoot: "/etc/pulsar-agents/opencode", VersionCommand: []string{"--version"}, LauncherPath: "/opt/pulsar-agents/opencode-launcher"},
}

func Lookup(agent string) (Package, bool) {
	p, ok := registry[agent]
	p.VersionCommand = append([]string(nil), p.VersionCommand...)
	return p, ok
}

type Planner struct{ pkg Package }
type Plan struct {
	AgentID, Version, BinaryPath, ConfigRoot string
	VersionCommand                           []string
}

func NewPlanner(agent string) (Planner, error) {
	p, ok := Lookup(agent)
	if !ok {
		return Planner{}, ErrRejected
	}
	return Planner{pkg: p}, nil
}
func (p Planner) Plan() Plan {
	return Plan{p.pkg.AgentID, p.pkg.Version, p.pkg.GuestBinaryPath, p.pkg.ConfigRoot, append([]string{p.pkg.GuestBinaryPath}, p.pkg.VersionCommand...)}
}

func verifyArchive(path string, p Package) ([]byte, error) {
	if filepath.Clean(path) != path || !filepath.IsAbs(path) {
		return nil, ErrRejected
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrRejected
	}
	defer f.Close()
	h := sha256.New()
	data, err := io.ReadAll(io.LimitReader(io.TeeReader(f, h), 512<<20+1))
	if err != nil || len(data) > 512<<20 {
		return nil, ErrRejected
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), p.ArchiveSHA256) {
		return nil, ErrRejected
	}
	return data, nil
}

type CopyBackend interface{ runtime.RuntimeBackend }

func Provision(ctx context.Context, backend runtime.RuntimeBackend, env runtime.Environment, archivePath, stagingDir, agent string) error {
	p, ok := Lookup(agent)
	if !ok || backend == nil || env.ID == "" || (env.Agent != "shell" && env.Agent != agent) || env.Backend != backend.Name() || filepath.Clean(stagingDir) != stagingDir || !filepath.IsAbs(stagingDir) {
		return ErrRejected
	}
	if info, err := os.Stat(stagingDir); err != nil || !info.IsDir() {
		return ErrRejected
	}
	data, err := verifyArchive(archivePath, p)
	if err != nil {
		return err
	}
	if _, err := backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"mkdir", "-p", "--", "/tmp/pulsar-agent-staging"}}); err != nil {
		return ErrRejected
	}
	guestChunks := make([]string, 0, (len(data)+int(ChunkBytes)-1)/int(ChunkBytes))
	for i, off := 0, int64(0); off < int64(len(data)); i++ {
		end := off + ChunkBytes
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		tmp, err := os.CreateTemp(stagingDir, "guestagent-")
		if err != nil {
			return ErrRejected
		}
		name := tmp.Name()
		if _, err = tmp.Write(data[off:end]); err == nil {
			err = tmp.Close()
		}
		if err != nil {
			os.Remove(name)
			return ErrRejected
		}
		guest := fmt.Sprintf("/tmp/pulsar-agent-staging/%s.%06d", p.AgentID, i)
		receipt, err := backend.CopyIn(ctx, env, name, guest)
		os.Remove(name)
		if err != nil || receipt.GuestPath != guest || receipt.Bytes != end-off || receipt.Digest != digest(data[off:end]) {
			return ErrRejected
		}
		guestChunks = append(guestChunks, guest)
		off = end
	}
	// Assembly, archive hash, exact-member extraction, ownership and mode are
	// fixed operations. No caller-provided shell or argv is accepted.
	archiveGuest := "/tmp/pulsar-agent-staging/" + p.AgentID + ".tgz"
	if _, err := backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"sh", "-c", "cat -- " + strings.Join(guestChunks, " ") + " > " + archiveGuest}}); err != nil {
		return ErrRejected
	}
	archiveHash, err := backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"sha256sum", "--", archiveGuest}})
	if err != nil || !strings.HasPrefix(strings.TrimSpace(archiveHash), strings.ToLower(p.ArchiveSHA256)+" ") {
		return ErrRejected
	}
	if _, err := backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"mkdir", "-p", "--", "/opt/pulsar-agents", p.ConfigRoot}}); err != nil {
		return ErrRejected
	}
	extract := "tar -xOzf " + archiveGuest + " -- " + p.Member + " > " + p.GuestBinaryPath
	if _, err := backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"sh", "-c", extract}}); err != nil {
		return ErrRejected
	}
	if _, err := backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"chown", "root:root", "--", p.GuestBinaryPath}}); err != nil {
		return ErrRejected
	}
	if _, err := backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"chmod", "0555", "--", p.GuestBinaryPath}}); err != nil {
		return ErrRejected
	}
	if p.BinarySHA256 == "" {
		return ErrRejected
	}
	got, err := backend.Exec(ctx, env, runtime.ExecRequest{User: "root", Command: []string{"sha256sum", "--", p.GuestBinaryPath}})
	if err != nil || !strings.HasPrefix(strings.TrimSpace(got), strings.ToLower(p.BinarySHA256)+" ") {
		return ErrRejected
	}
	return nil
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// GuestVerifier uses only fixed commands from the registry. env must be an
// environment previously created and inspected by the trusted controller.
type GuestVerifier struct{ backend runtime.RuntimeBackend }

func NewGuestVerifier(b runtime.RuntimeBackend) (GuestVerifier, error) {
	if b == nil {
		return GuestVerifier{}, ErrRejected
	}
	return GuestVerifier{b}, nil
}
func (v GuestVerifier) Verify(ctx context.Context, env runtime.Environment, agent string) error {
	p, ok := Lookup(agent)
	if !ok || env.ID == "" || env.Backend != v.backend.Name() || (env.Agent != "shell" && env.Agent != agent) {
		return ErrRejected
	}
	got, err := v.backend.Inspect(ctx, env)
	if err != nil || got.ID != env.ID || got.Name != env.Name || got.Backend != env.Backend || got.Agent != env.Agent {
		return ErrRejected
	}
	out, err := v.backend.Exec(ctx, env, runtime.ExecRequest{User: "agent", Command: append([]string{p.GuestBinaryPath}, p.VersionCommand...)})
	line := strings.TrimSuffix(strings.TrimSuffix(out, "\n"), "\r")
	expected := p.Version
	if agent == "codex" {
		expected = "codex-cli " + p.Version
	}
	if err != nil || (line != expected && !(agent == "codex" && line == "codex "+p.Version)) {
		return ErrRejected
	}
	return nil
}
