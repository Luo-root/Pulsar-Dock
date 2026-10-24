package dockmcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCreateChildAndStatus(t *testing.T) {
	service := testService(t)
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

func TestPublishReferences(t *testing.T) {
	service := testService(t)
	ctx := context.Background()
	_, first, err := service.createChild(ctx, &mcp.CallToolRequest{}, ChildTaskInput{Goal: "review the patch"})
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := service.createChild(ctx, &mcp.CallToolRequest{}, ChildTaskInput{Goal: "review another patch"})
	if err != nil {
		t.Fatal(err)
	}
	inputs := []PublishInput{
		{TaskID: first.TaskID, Kind: "review", Digest: "synthetic-digest-one"},
		{TaskID: first.TaskID, Kind: "review", Digest: "synthetic-digest-two"},
		{TaskID: first.TaskID, Kind: "patch", Digest: "synthetic-digest-one"},
		{TaskID: second.TaskID, Kind: "review", Digest: "synthetic-digest-one"},
	}
	seen := make(map[string]bool)
	for _, input := range inputs {
		result, output, err := service.publish(ctx, &mcp.CallToolRequest{}, input)
		if err != nil || result != nil || output.State != "published" {
			t.Fatalf("publish result=%v output=%#v err=%v", result, output, err)
		}
		payload, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("artifact-%x", sha256.Sum256(payload)); output.ArtifactID != want {
			t.Fatalf("artifact ID=%q, want full-input digest %q", output.ArtifactID, want)
		}
		if seen[output.ArtifactID] {
			t.Fatalf("distinct input reused artifact ID: %#v", input)
		}
		seen[output.ArtifactID] = true
		if stored := service.artifacts[output.ArtifactID]; stored != input {
			t.Fatalf("stored association=%#v, want %#v", stored, input)
		}
		_, repeated, err := service.publish(ctx, &mcp.CallToolRequest{}, input)
		if err != nil || repeated != output || len(service.artifacts) != len(seen) {
			t.Fatalf("repeat output=%#v err=%v records=%d", repeated, err, len(service.artifacts))
		}
	}
}

func TestPublishRejectsInvalidInputWithoutChangingRecords(t *testing.T) {
	service := testService(t)
	ctx := context.Background()
	_, task, err := service.createChild(ctx, &mcp.CallToolRequest{}, ChildTaskInput{Goal: "review the patch"})
	if err != nil {
		t.Fatal(err)
	}
	valid := PublishInput{TaskID: task.TaskID, Kind: "review", Digest: "synthetic-digest"}
	_, published, err := service.publish(ctx, &mcp.CallToolRequest{}, valid)
	if err != nil {
		t.Fatal(err)
	}
	wantTasks := map[string]taskRecord{task.TaskID: service.tasks[task.TaskID], "root": service.tasks["root"]}
	wantArtifacts := map[string]PublishInput{published.ArtifactID: valid}
	for _, tc := range []struct {
		name  string
		input PublishInput
	}{
		{name: "empty task", input: PublishInput{Kind: valid.Kind, Digest: valid.Digest}},
		{name: "empty kind", input: PublishInput{TaskID: task.TaskID, Digest: valid.Digest}},
		{name: "empty digest", input: PublishInput{TaskID: task.TaskID, Kind: valid.Kind}},
		{name: "unknown task", input: PublishInput{TaskID: "task-9999", Kind: valid.Kind, Digest: valid.Digest}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, output, err := service.publish(ctx, &mcp.CallToolRequest{}, tc.input)
			if err == nil || result != nil || output != (PublishOutput{}) {
				t.Fatalf("publish result=%v output=%#v err=%v", result, output, err)
			}
			if !reflect.DeepEqual(service.tasks, wantTasks) || !reflect.DeepEqual(service.artifacts, wantArtifacts) || service.sequence != 1 {
				t.Fatal("rejected publish changed task or artifact records")
			}
		})
	}
}

func TestUnknownTaskDoesNotLeakState(t *testing.T) {
	service := testService(t)
	_, _, err := service.status(context.Background(), &mcp.CallToolRequest{}, StatusInput{TaskID: "task-9999"})
	if err == nil {
		t.Fatal("expected unknown task to be rejected")
	}
}
