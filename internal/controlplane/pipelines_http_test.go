package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestPipelineAPIListsSummariesAndUpdatesDetails(t *testing.T) {
	store := newTestStore(t)
	handler := NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	doJSON := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequestWithContext(context.Background(), method, "http://localhost"+path, bytes.NewReader(encoded))
		request.Host = "localhost"
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	createdResponse := doJSON(http.MethodPost, "/api/v1/pipelines", protocol.SavePipelineRequest{
		Name: "Plan and build",
		Stages: []protocol.PipelineStage{
			{Name: "Plan", Prompt: "Plan {{ task.prompt }}"},
			{Name: "Build", Prompt: "Build on {{ branch }}"},
		},
	})
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created protocol.Pipeline
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	listResponse := doJSON(http.MethodGet, "/api/v1/pipelines", nil)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", listResponse.Code, listResponse.Body.String())
	}
	var page protocol.PipelinePage
	if err := json.Unmarshal(listResponse.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	var summary protocol.Pipeline
	for _, pipeline := range page.Pipelines {
		if pipeline.ID == created.ID {
			summary = pipeline
		}
	}
	if len(summary.Stages) != 2 || summary.Stages[0].Prompt != "" {
		t.Fatalf("Pipeline summary = %#v", summary)
	}

	detailResponse := doJSON(http.MethodGet, "/api/v1/pipelines/"+created.ID, nil)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail status %d: %s", detailResponse.Code, detailResponse.Body.String())
	}
	var detail protocol.Pipeline
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Stages[0].Prompt != "Plan {{ task.prompt }}" {
		t.Fatalf("Pipeline detail = %#v", detail)
	}

	updatedResponse := doJSON(http.MethodPut, "/api/v1/pipelines/"+created.ID, protocol.SavePipelineRequest{
		Name: "Build once", ExpectedGeneration: created.Generation,
		Stages: []protocol.PipelineStage{{Name: "Build", Prompt: "{{ task.prompt }}"}},
	})
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("update status %d: %s", updatedResponse.Code, updatedResponse.Body.String())
	}
	if conflictResponse := doJSON(http.MethodPut, "/api/v1/pipelines/"+created.ID, protocol.SavePipelineRequest{
		Name: "Stale", ExpectedGeneration: created.Generation,
		Stages: []protocol.PipelineStage{{Name: "Build", Prompt: "{{ task.prompt }}"}},
	}); conflictResponse.Code != http.StatusConflict {
		t.Fatalf("stale update status %d: %s", conflictResponse.Code, conflictResponse.Body.String())
	}
	if defaultDelete := doJSON(http.MethodDelete, "/api/v1/pipelines/"+protocol.DefaultPipelineID, nil); defaultDelete.Code != http.StatusConflict {
		t.Fatalf("default delete status %d: %s", defaultDelete.Code, defaultDelete.Body.String())
	}
	crossOriginDelete := httptest.NewRequestWithContext(
		context.Background(), http.MethodDelete, "http://localhost/api/v1/pipelines/"+created.ID, nil,
	)
	crossOriginDelete.Host = "localhost"
	crossOriginDelete.Header.Set("Origin", "http://attacker.example")
	crossOriginResponse := httptest.NewRecorder()
	handler.ServeHTTP(crossOriginResponse, crossOriginDelete)
	if crossOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin delete status %d: %s", crossOriginResponse.Code, crossOriginResponse.Body.String())
	}
	if deleteResponse := doJSON(http.MethodDelete, "/api/v1/pipelines/"+created.ID, nil); deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("delete status %d: %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	if missingResponse := doJSON(http.MethodGet, "/api/v1/pipelines/"+created.ID, nil); missingResponse.Code != http.StatusNotFound {
		t.Fatalf("deleted detail status %d: %s", missingResponse.Code, missingResponse.Body.String())
	}
}

func TestCompleteStageHTTPReturnsMetadataWithinWorkerResponseLimit(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	pipeline, err := store.CreatePipeline(ctx, protocol.SavePipelineRequest{
		Name:   "Escaped output",
		Stages: []protocol.PipelineStage{{Name: "Build", Prompt: strings.Repeat("\x00", protocol.MaxTaskPromptBytes)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Bound response", Prompt: "Run it.", Runtime: protocol.RuntimeCodex,
		PipelineID: pipeline.ID, RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RunTask(ctx, task.ID, protocol.RunTaskRequest{RequestKey: "bounded-stage-response"}); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, worker.ID, protocol.ClaimRequest{RequestID: "bounded-stage-response", LeaseToken: tokenA})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, error %v", claim, err)
	}
	if _, err := store.StartAttempt(ctx, claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartStage(ctx, claim.Attempt.ID, 0, protocol.StartStageRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	input := protocol.CompleteStageRequest{
		LeaseToken: tokenA, State: protocol.StageSucceeded,
		Result: strings.Repeat("\x00", 100<<10), Error: strings.Repeat("\x00", 50<<10),
	}
	body, err := json.Marshal(input)
	if err != nil || len(body) >= protocol.MaxBodyBytes {
		t.Fatalf("completion request = %d bytes, error %v", len(body), err)
	}
	request := httptest.NewRequestWithContext(ctx, http.MethodPost,
		"http://localhost/api/v1/attempts/"+claim.Attempt.ID+"/stages/0/complete", bytes.NewReader(body))
	request.Host = "localhost"
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("complete status %d: %s", response.Code, response.Body.String())
	}
	if response.Body.Len() >= protocol.MaxBodyBytes {
		t.Fatalf("completion response = %d bytes", response.Body.Len())
	}
	var stage protocol.StageRun
	if err := json.Unmarshal(response.Body.Bytes(), &stage); err != nil {
		t.Fatal(err)
	}
	if stage.State != protocol.StageSucceeded || stage.Prompt != "" || stage.Result != "" || stage.Error != "" {
		t.Fatalf("completion response exposed stage payloads: %#v", stage)
	}
}
