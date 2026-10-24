package taskstore

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRootRevocationDurabilityAndScope(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(canonicalTempDir(t), "tasks.db")
	s := openTest(t, path)
	r := root(t, s)
	scope := Scope{CallerID: r.CallerID, RootID: r.ID}
	if revoked, e := s.RootRevoked(ctx, scope); e != nil || revoked {
		t.Fatalf("initial=%v %v", revoked, e)
	}
	otherReq := rootRequest()
	otherReq.IdempotencyKey = "other"
	other, e := s.CreateRoot(ctx, otherReq)
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range []Scope{{}, {CallerID: "other", RootID: r.ID}, {CallerID: r.CallerID, RootID: "unknown"}} {
		if e = s.RevokeRoot(ctx, bad); e == nil {
			t.Fatal("invalid scope revoked")
		}
		if _, e = s.RootRevoked(ctx, bad); e == nil {
			t.Fatal("invalid scope authorized")
		}
	}
	if e = s.RevokeRoot(ctx, scope); e != nil {
		t.Fatal(e)
	}
	if e = s.RevokeRoot(ctx, scope); e != nil {
		t.Fatal("not idempotent", e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s = openTest(t, path)
	if revoked, e := s.RootRevoked(ctx, scope); e != nil || !revoked {
		t.Fatalf("reopen=%v %v", revoked, e)
	}
	if revoked, e := s.RootRevoked(ctx, Scope{CallerID: other.CallerID, RootID: other.ID}); e != nil || revoked {
		t.Fatalf("unrelated=%v %v", revoked, e)
	}
	persisted, e := s.Get(ctx, scope, r.ID)
	if e != nil || persisted != r {
		t.Fatal("revocation changed task facts", e)
	}
	s.Close()
	if _, e = s.RootRevoked(ctx, scope); e == nil {
		t.Fatal("closed store authorized")
	}
}

func TestRevocationMigratesV1(t *testing.T) {
	path := filepath.Join(canonicalTempDir(t), "tasks.db")
	s := openTest(t, path)
	r := root(t, s)
	// Reconstruct the actual v1 schema from a populated database.
	if _, e := s.db.Exec("DROP TABLE root_revocations; PRAGMA user_version=1;"); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s = openTest(t, path)
	scope := Scope{CallerID: r.CallerID, RootID: r.ID}
	if e := s.RevokeRoot(context.Background(), scope); e != nil {
		t.Fatal(e)
	}
	persisted, e := s.Get(context.Background(), scope, r.ID)
	if e != nil || persisted != r {
		t.Fatal("migration lost task", e)
	}
	var version int
	if e = s.db.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version != 2 {
		t.Fatalf("version=%d %v", version, e)
	}
}
