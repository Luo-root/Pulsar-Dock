package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

func TestStopRetainsCandidateAfterFailedBindingCleanup(t *testing.T) {
	s, db, b := stopSetup(t)
	failure := errors.New("binding failed")
	s.store = &faultStore{taskStore: db, bind: func(context.Context, taskstore.Scope, string, taskstore.State, string) (taskstore.Task, error) {
		return taskstore.Task{}, failure
	}}
	b.removeErr = errors.New("cleanup failed")
	root, e := s.CreateRoot(ctx, request)
	if !errors.Is(e, ErrRecoveryRequired) {
		t.Fatal(e)
	}
	s.store = db
	b.removeErr = nil
	if e = s.StopTask(ctx, root.ID); e != nil {
		t.Fatal(e)
	}
	if len(b.removed) != 2 || b.removed[0] != b.removed[1] {
		t.Fatal("lost full candidate handle")
	}
	assertState(t, db, root, taskstore.Failed)
}
func TestStopRetainsHandleAfterTransitionFailure(t *testing.T) {
	s, db, b := stopSetup(t)
	s.store = &faultStore{taskStore: db, transition: func(context.Context, taskstore.Scope, string, taskstore.State, taskstore.State) (taskstore.Task, error) {
		return taskstore.Task{}, errors.New("transition failed")
	}}
	root, e := s.CreateRoot(ctx, request)
	if !errors.Is(e, ErrRecoveryRequired) {
		t.Fatal(e)
	}
	s.store = db
	if e = s.StopTask(ctx, root.ID); e != nil {
		t.Fatal(e)
	}
	assertState(t, db, root, taskstore.Stopped)
	if len(b.removed) != 1 || b.removed[0] != b.handles[0] {
		t.Fatal("lost adopted handle")
	}
}
func TestStopUnknownEmptyBindingRequiresRecovery(t *testing.T) {
	s, db, b := stopSetup(t)
	root, e := db.CreateRoot(ctx, taskstore.CreateRequest{Scope: taskstore.Scope{CallerID: "alice"}, Goal: "synthetic", IdempotencyKey: "unknown"})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.StopTask(ctx, root.ID); !errors.Is(e, ErrRecoveryRequired) {
		t.Fatal(e)
	}
	assertState(t, db, root, taskstore.Created)
	if len(b.removed) != 0 {
		t.Fatal("invented cleanup capability")
	}
}
