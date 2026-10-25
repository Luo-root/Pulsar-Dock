package artifactstore

import (
	"bytes"
	"context"
	"fmt"
	"testing"
)

func TestMaximumPayloadPagination(t *testing.T) {
	s := openTest(t, privateDir(t))
	ctx := context.Background()
	r := req()
	r.Bytes = bytes.Repeat([]byte("x"), MaxBytes)
	for i := 0; i < MaxPageSize+1; i++ {
		r.IdempotencyKey = fmt.Sprint(i)
		if _, err := s.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.List(ctx, r.Scope, "", MaxPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 8 || first.Next == "" {
		t.Fatalf("first page: %d items, cursor %q", len(first.Items), first.Next)
	}
	seen := map[string]bool{}
	for _, m := range first.Items {
		if m.Size != MaxBytes || m.Digest != digest(r.Bytes) {
			t.Fatal("invalid metadata")
		}
		seen[m.ID] = true
	}
	second, err := s.List(ctx, r.Scope, first.Next, MaxPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Next != "" || seen[second.Items[0].ID] || second.Items[0].Size != MaxBytes {
		t.Fatal("invalid second page")
	}
	// The lookahead row is checked too, not silently trusted as a metadata-only copy.
	if _, err = s.db.Exec("UPDATE artifacts SET payload=x'00' WHERE id=?", second.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.List(ctx, r.Scope, "", MaxPageSize)
	wantKind(t, err, Corrupt)
}
