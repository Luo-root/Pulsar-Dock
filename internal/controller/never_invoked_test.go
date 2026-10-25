package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/Luo-root/pulsar-dock/internal/taskstore"
)

func TestNeverInvokedSurvivesRejectedChildStateWriteFailure(t *testing.T) {
	s, db, b := stopSetup(t)
	root, e := s.CreateRoot(ctx, request)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.RevokeRoot(ctx, scope(root)); e != nil {
		t.Fatal(e)
	}
	s.store = &faultStore{taskStore: db, transition: func(context.Context, taskstore.Scope, string, taskstore.State, taskstore.State) (taskstore.Task, error) {
		return taskstore.Task{}, errors.New("failed rejection state write")
	}}
	child, e := s.CreateChild(ctx, root.ID, request)
	if !errors.Is(e, ErrRootRevoked) {
		t.Fatal(e)
	}
	s.store = db
	if e = s.StopTask(ctx, root.ID); e != nil {
		t.Fatal(e)
	}
	assertState(t, db, child, taskstore.Stopped)
	if len(b.removed) != 1 {
		t.Fatal("backend invoked for rejected child")
	}
}
