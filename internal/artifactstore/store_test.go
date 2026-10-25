package artifactstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func privateDir(t *testing.T) string {
	t.Helper()
	// macOS TempDir commonly has /var as a symlink. Only the test fixture is
	// canonicalized; production Open must reject aliases rather than follow them.
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	return d
}
func openTest(t *testing.T, d string) *Store {
	t.Helper()
	s, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func req() PutRequest {
	return PutRequest{Scope: Scope{"caller", "root", "task"}, EnvironmentID: "env", Kind: "diff", BaselineDigest: "baseline", IdempotencyKey: "key", Bytes: []byte("synthetic payload")}
}
func wantKind(t *testing.T, err error, k ErrorKind) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Kind != k {
		t.Fatalf("got %v, want %s", err, k)
	}
}
func TestPutReadReopenImmutable(t *testing.T) {
	ctx := context.Background()
	d := privateDir(t)
	s := openTest(t, d)
	r := req()
	m, err := s.Put(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Digest) != 64 || m.Digest != digest(r.Bytes) || m.Sensitivity != "restricted-unscanned" {
		t.Fatal(m)
	}
	r.Bytes[0] = 'X'
	a, err := s.Read(ctx, r.Scope, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Bytes) != "synthetic payload" {
		t.Fatal("caller buffer retained")
	}
	a.Bytes[0] = 'Y'
	a.Metadata.Kind = "changed"
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, d)
	a, err = s.Read(ctx, r.Scope, m.ID)
	if err != nil || a.Metadata != m || string(a.Bytes) != "synthetic payload" {
		t.Fatalf("reopen: %+v %v", a, err)
	}
	again, err := s.Put(ctx, req())
	if err != nil || again != m {
		t.Fatalf("retry: %+v %v", again, err)
	}
}
func TestLimitsAndValidation(t *testing.T) {
	s := openTest(t, privateDir(t))
	ctx := context.Background()
	for _, n := range []int{0, 1, MaxBytes} {
		r := req()
		r.IdempotencyKey = fmt.Sprint(n)
		r.Bytes = bytes.Repeat([]byte("a"), n)
		m, err := s.Put(ctx, r)
		if err != nil {
			t.Fatal(n, err)
		}
		a, err := s.Read(ctx, r.Scope, m.ID)
		if err != nil || !bytes.Equal(a.Bytes, r.Bytes) {
			t.Fatal(n, err)
		}
	}
	r := req()
	r.Bytes = make([]byte, MaxBytes+1)
	_, err := s.Put(ctx, r)
	wantKind(t, err, Invalid)
	mutations := []func(*PutRequest){func(r *PutRequest) { r.Scope.CallerID = "" }, func(r *PutRequest) { r.Scope.RootID = "" }, func(r *PutRequest) { r.Scope.TaskID = "" }, func(r *PutRequest) { r.EnvironmentID = "" }, func(r *PutRequest) { r.Kind = "" }, func(r *PutRequest) { r.BaselineDigest = "" }, func(r *PutRequest) { r.IdempotencyKey = "" }, func(r *PutRequest) { r.Kind = strings.Repeat("k", 513) }, func(r *PutRequest) { r.Kind = "\xff" }, func(r *PutRequest) { r.Kind = "a\x00b" }}
	for i, f := range mutations {
		r := req()
		f(&r)
		_, err = s.Put(ctx, r)
		if err == nil {
			t.Fatal(i)
		}
		wantKind(t, err, Invalid)
	}
	r = req()
	r.Bytes = nil
	r.IdempotencyKey = "empty"
	m, err := s.Put(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	r.Bytes = []byte{}
	m2, err := s.Put(ctx, r)
	if err != nil || m != m2 {
		t.Fatal("empty semantics", err)
	}
}
func TestAllParameterIdempotency(t *testing.T) {
	s := openTest(t, privateDir(t))
	ctx := context.Background()
	m, err := s.Put(ctx, req())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []func(*PutRequest){func(r *PutRequest) { r.EnvironmentID = "env2" }, func(r *PutRequest) { r.Kind = "report" }, func(r *PutRequest) { r.BaselineDigest = "other" }, func(r *PutRequest) { r.Bytes = []byte("different") }} {
		r := req()
		f(&r)
		_, err = s.Put(ctx, r)
		wantKind(t, err, Conflict)
	}
	for _, f := range []func(*PutRequest){func(r *PutRequest) { r.Scope.CallerID = "other" }, func(r *PutRequest) { r.Scope.RootID = "other" }, func(r *PutRequest) { r.Scope.TaskID = "other" }} {
		r := req()
		f(&r)
		_, err = s.Read(ctx, r.Scope, m.ID)
		wantKind(t, err, NotFound)
		p, err := s.List(ctx, r.Scope, "", MaxPageSize)
		if err != nil || len(p.Items) != 0 {
			t.Fatal("scope leak", err)
		}
		other, err := s.Put(ctx, r)
		if err != nil || other.ID == m.ID {
			t.Fatal("separate namespace", err)
		}
	}
}
func TestPagination(t *testing.T) {
	s := openTest(t, privateDir(t))
	ctx := context.Background()
	r := req()
	for i := 0; i < 7; i++ {
		r.IdempotencyKey = fmt.Sprint(i)
		if _, err := s.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		p, err := s.List(ctx, r.Scope, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Items) > 2 {
			t.Fatal("unbounded")
		}
		for _, m := range p.Items {
			if seen[m.ID] {
				t.Fatal("duplicate")
			}
			seen[m.ID] = true
		}
		if p.Next == "" {
			break
		}
		cursor = p.Next
	}
	if len(seen) != 7 {
		t.Fatal(len(seen))
	}
	for _, n := range []int{-1, 0, 9, MaxPageSize + 1} {
		_, err := s.List(ctx, r.Scope, "", n)
		wantKind(t, err, Invalid)
	}
	_, err := s.List(ctx, Scope{}, "", 1)
	wantKind(t, err, Invalid)
	_, err = s.Read(ctx, Scope{}, "id")
	wantKind(t, err, Invalid)
}
func TestConcurrentInstances(t *testing.T) {
	d := privateDir(t)
	s := openTest(t, d)
	s2 := openTest(t, d)
	ctx := context.Background()
	var wg sync.WaitGroup
	ids := make(chan string, 24)
	errs := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := s
			if i%2 == 0 {
				store = s2
			}
			m, err := store.Put(ctx, req())
			if err != nil {
				errs <- err
			} else {
				ids <- m.ID
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		t.Error(err)
	}
	id := ""
	for v := range ids {
		if id != "" && id != v {
			t.Fatal("duplicate")
		}
		id = v
	}
	// Distinct keys must also survive concurrent writers without lost updates.
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := req()
			r.IdempotencyKey = fmt.Sprint(i)
			store := s
			if i%2 == 0 {
				store = s2
			}
			if _, err := store.Put(ctx, r); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	count, cursor := 0, ""
	for {
		p, err := s.List(ctx, req().Scope, cursor, MaxPageSize)
		if err != nil {
			t.Fatal(err)
		}
		count += len(p.Items)
		if p.Next == "" {
			break
		}
		cursor = p.Next
	}
	if count != 25 {
		t.Fatal(count)
	}
}
func TestCorruption(t *testing.T) {
	for _, change := range []string{"payload=x'00'", "record_digest='bad'", "metadata=x'7b7d'", "request_digest='bad'", "idem='changed'"} {
		t.Run(change, func(t *testing.T) {
			s := openTest(t, privateDir(t))
			ctx := context.Background()
			m, err := s.Put(ctx, req())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec("UPDATE artifacts SET " + change); err != nil {
				t.Fatal(err)
			}
			_, err = s.Read(ctx, m.Scope, m.ID)
			wantKind(t, err, Corrupt)
			_, err = s.List(ctx, m.Scope, "", 1)
			wantKind(t, err, Corrupt)
		})
	}
	s := openTest(t, privateDir(t))
	if _, err := s.Put(context.Background(), req()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE artifacts SET payload=x'00'"); err != nil {
		t.Fatal(err)
	}
	_, err := s.Put(context.Background(), req())
	wantKind(t, err, Corrupt)
}
func TestContextAndClose(t *testing.T) {
	s := openTest(t, privateDir(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Put(ctx, req())
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_, err = s.Read(ctx, req().Scope, "id")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_, err = s.List(ctx, req().Scope, "", 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = s.Put(context.Background(), req())
	wantKind(t, err, Closed)
	_, err = s.Read(context.Background(), req().Scope, "id")
	wantKind(t, err, Closed)
	_, err = s.List(context.Background(), req().Scope, "", 1)
	wantKind(t, err, Closed)
}
func TestFutureSchemaUnchanged(t *testing.T) {
	d := privateDir(t)
	s := openTest(t, d)
	if _, err := s.db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	path := filepath.Join(d, "artifacts.sqlite")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(d)
	wantKind(t, err, Invalid)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("future schema rewritten")
	}
}
func TestRejectPaths(t *testing.T) {
	for _, d := range []string{"", ".", "file:test", ":memory:", `\\server\share`, "//server/share", filepath.Join(privateDir(t), "missing")} {
		_, err := Open(d)
		wantKind(t, err, Invalid)
		if strings.Contains(err.Error(), d) && len(d) > 12 {
			t.Fatal("path leaked")
		}
	}
	d := privateDir(t)
	if err := os.WriteFile(filepath.Join(d, "artifacts.sqlite"), []byte("synthetic-secret-token"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(d)
	if err == nil || strings.Contains(err.Error(), "synthetic-secret-token") || strings.Contains(err.Error(), d) {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		d = privateDir(t)
		if err = os.Chmod(d, 0755); err != nil {
			t.Fatal(err)
		}
		_, err = Open(d)
		wantKind(t, err, Invalid)
	}
}
func TestRejectLinks(t *testing.T) {
	d := privateDir(t)
	target := privateDir(t)
	link := filepath.Join(d, "alias")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := Open(link)
	wantKind(t, err, Invalid)
	if err = os.Mkdir(filepath.Join(target, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = Open(filepath.Join(link, "child"))
	wantKind(t, err, Invalid)
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			dir := privateDir(t)
			f := filepath.Join(target, "file"+suffix)
			if err := os.WriteFile(f, []byte("synthetic"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(f, filepath.Join(dir, "artifacts.sqlite"+suffix)); err != nil {
				t.Fatal(err)
			}
			_, err := Open(dir)
			wantKind(t, err, Invalid)
		})
	}
}
func TestRejectHardLink(t *testing.T) {
	d := privateDir(t)
	other := filepath.Join(privateDir(t), "file")
	if err := os.WriteFile(other, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(other, filepath.Join(d, "artifacts.sqlite")); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	_, err := Open(d)
	wantKind(t, err, Invalid)
}
func TestUnversionedUnrelatedDatabase(t *testing.T) {
	d := privateDir(t)
	path := filepath.Join(d, "artifacts.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE unrelated(x TEXT)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = Open(d)
	wantKind(t, err, Invalid)
}
