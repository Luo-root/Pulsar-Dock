package dockmcp

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCreateChildAndStatus(t *testing.T) {
	service := NewService()
	result, output, err := service.createChild(context.Background(), &mcp.CallToolRequest{}, ChildTaskInput{Goal: "review the patch"})
	if err != nil || result != nil {
		t.Fatalf("createChild result=%v err=%v", result, err)
	}
	if output.TaskID != "task-0001" || output.State != "created" {
		t.Fatalf("unexpected output: %#v", output)
	}
	_, status, err := service.status(context.Background(), &mcp.CallToolRequest{}, StatusInput{TaskID: output.TaskID})
	if err != nil || status.State != "created" {
		t.Fatalf("status=%#v err=%v", status, err)
	}
}

func TestUnknownTaskDoesNotLeakState(t *testing.T) {
	service := NewService()
	_, _, err := service.status(context.Background(), &mcp.CallToolRequest{}, StatusInput{TaskID: "task-9999"})
	if err == nil {
		t.Fatal("expected unknown task to be rejected")
	}
}
