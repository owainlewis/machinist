package protocol

import (
	"encoding/json"
	"testing"
)

func TestTaskSnapshotUnmarshalDefaultsLegacyPipeline(t *testing.T) {
	var snapshot TaskSnapshot
	if err := json.Unmarshal([]byte(`{
		"id":"task-1",
		"name":"Legacy task",
		"prompt":"Review the repository.",
		"runtime":"codex",
		"generation":1
	}`), &snapshot); err != nil {
		t.Fatal(err)
	}

	if snapshot.Pipeline.ID != DefaultPipelineID || snapshot.Pipeline.Name != "Single agent" ||
		snapshot.Pipeline.Generation != 1 || len(snapshot.Pipeline.Stages) != 1 {
		t.Fatalf("legacy Pipeline snapshot = %#v, want the single-agent default", snapshot.Pipeline)
	}
	stage := snapshot.Pipeline.Stages[0]
	if stage.Position != 0 || stage.Name != "Do the task" || stage.Prompt != snapshot.Prompt {
		t.Fatalf("legacy Pipeline stage = %#v, want the frozen task prompt", stage)
	}
}

func TestTaskSnapshotUnmarshalPreservesPipeline(t *testing.T) {
	var snapshot TaskSnapshot
	if err := json.Unmarshal([]byte(`{
		"id":"task-1",
		"name":"Current task",
		"runtime":"codex",
		"generation":1,
		"pipeline":{
			"id":"pipeline-1",
			"name":"Review",
			"generation":2,
			"stages":[{"position":0,"name":"Inspect","prompt":"Check the diff."}]
		}
	}`), &snapshot); err != nil {
		t.Fatal(err)
	}

	if snapshot.Pipeline.ID != "pipeline-1" || snapshot.Pipeline.Name != "Review" ||
		snapshot.Pipeline.Generation != 2 || len(snapshot.Pipeline.Stages) != 1 ||
		snapshot.Pipeline.Stages[0].Name != "Inspect" {
		t.Fatalf("current Pipeline snapshot changed during unmarshal: %#v", snapshot.Pipeline)
	}
}
