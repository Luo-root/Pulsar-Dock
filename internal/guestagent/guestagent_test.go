package guestagent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Luo-root/pulsar-dock/internal/runtime"
)

type fakeBackend struct {
	commands                []runtime.ExecRequest
	badReceipt              bool
	inspect                 runtime.Environment
	archiveHash, binaryHash string
	versionOutput           string
}

func (f *fakeBackend) Name() string { return "fake" }
func (f *fakeBackend) Capabilities(context.Context) runtime.CapabilitySet {
	return runtime.CapabilitySet{}
}
func (f *fakeBackend) Check(context.Context) runtime.Diagnostics { return runtime.Diagnostics{} }
func (f *fakeBackend) Create(context.Context, runtime.EnvironmentSpec) (runtime.Environment, error) {
	return runtime.Environment{}, nil
}
func (f *fakeBackend) Start(context.Context, runtime.Environment) error { return nil }
func (f *fakeBackend) Exec(_ context.Context, e runtime.Environment, r runtime.ExecRequest) (string, error) {
	f.commands = append(f.commands, r)
	joined := strings.Join(r.Command, " ")
	if strings.HasPrefix(joined, "sha256sum") {
		if strings.Contains(joined, ".tgz") {
			return f.archiveHash + "  file\n", nil
		}
		return f.binaryHash + "  file\n", nil
	}
	if len(r.Command) == 2 && r.Command[1] == "--version" {
		return f.versionOutput, nil
	}
	return "", nil
}
func (f *fakeBackend) CopyIn(_ context.Context, e runtime.Environment, src, dst string) (runtime.CopyReceipt, error) {
	b, err := os.ReadFile(src)
	if err != nil {
		return runtime.CopyReceipt{}, err
	}
	d := sha256.Sum256(b)
	if f.badReceipt {
		return runtime.CopyReceipt{GuestPath: dst, Bytes: int64(len(b)), Digest: "bad"}, nil
	}
	return runtime.CopyReceipt{GuestPath: dst, Bytes: int64(len(b)), Digest: hex.EncodeToString(d[:])}, nil
}
func (f *fakeBackend) CopyOut(context.Context, runtime.Environment, runtime.CopySelector) (runtime.ArtifactSnapshot, error) {
	return runtime.ArtifactSnapshot{}, nil
}
func (f *fakeBackend) Stop(context.Context, runtime.Environment) error   { return nil }
func (f *fakeBackend) Remove(context.Context, runtime.Environment) error { return nil }
func (f *fakeBackend) SetNetworkPolicy(context.Context, runtime.Environment, []runtime.NetworkRule) error {
	return nil
}
func (f *fakeBackend) Revoke(context.Context, runtime.Environment, string) error { return nil }
func (f *fakeBackend) Inspect(context.Context, runtime.Environment) (runtime.Environment, error) {
	return f.inspect, nil
}
func syntheticArchive(t *testing.T, member string, payload []byte) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "pkg.tgz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	g := gzip.NewWriter(f)
	tr := tar.NewWriter(g)
	if err = tr.WriteHeader(&tar.Header{Name: member, Mode: 0755, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err = tr.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = tr.Close()
	_ = g.Close()
	_ = f.Close()
	data, _ := os.ReadFile(path)
	ah := sha256.Sum256(data)
	bh := sha256.Sum256(payload)
	return path, hex.EncodeToString(ah[:]), hex.EncodeToString(bh[:])
}
func withSyntheticRegistry(t *testing.T, p Package) {
	old := registry[p.AgentID]
	registry[p.AgentID] = p
	t.Cleanup(func() { registry[p.AgentID] = old })
}
func TestProvisionPositiveFixedSteps(t *testing.T) {
	member := "package/bin/codex"
	payload := []byte("synthetic-codex-binary")
	path, ah, bh := syntheticArchive(t, member, payload)
	withSyntheticRegistry(t, Package{AgentID: "codex", Version: "0.164.0", ArchivePath: "pkg.tgz", ArchiveSHA256: ah, BinarySHA256: bh, Member: member, GuestBinaryPath: "/opt/pulsar-agents/codex", ConfigRoot: "/etc/pulsar-agents/codex", VersionCommand: []string{"--version"}})
	f := &fakeBackend{archiveHash: ah, binaryHash: bh}
	err := Provision(context.Background(), f, runtime.Environment{ID: "e", Backend: "fake", Agent: "codex"}, path, t.TempDir(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.commands) == 0 || len(f.commands[0].Command) < 3 || f.commands[0].Command[0] != "mkdir" {
		t.Fatalf("first command was not staging mkdir: %+v", f.commands)
	}
}
func TestProvisionRejectsBadReceipt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pkg")
	if err := os.WriteFile(path, []byte("not-an-approved-package"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &fakeBackend{badReceipt: true}
	err := Provision(context.Background(), f, runtime.Environment{ID: "e", Backend: "fake", Agent: "codex"}, path, dir, "codex")
	if err == nil {
		t.Fatal("accepted bad archive")
	}
}
func TestVerifierRequiresOwnedEnvironmentAndVersion(t *testing.T) {
	f := &fakeBackend{inspect: runtime.Environment{ID: "owned", Backend: "fake", Agent: "codex"}}
	v, _ := NewGuestVerifier(f)
	if err := v.Verify(context.Background(), runtime.Environment{ID: "other", Backend: "fake", Agent: "codex"}, "codex"); err == nil {
		t.Fatal("accepted foreign environment")
	}
	if err := v.Verify(context.Background(), runtime.Environment{ID: "owned", Backend: "fake", Agent: "codex"}, "codex"); err == nil {
		t.Fatal("accepted without version output")
	}
	f.versionOutput = "codex 0.164.0\n"
	if err := v.Verify(context.Background(), runtime.Environment{ID: "owned", Backend: "fake", Agent: "codex"}, "codex"); err != nil {
		t.Fatalf("accepted fixed version output: %v", err)
	}
}
