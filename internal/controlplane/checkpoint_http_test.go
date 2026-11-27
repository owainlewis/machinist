package controlplane

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/tickets"
)

func TestRerunFromStepHTTP(t *testing.T) {
	f := newFixture(t)
	loaded := &factory.Factory{
		Presets: map[string]factory.Preset{
			"default": {
				Name: "default",
				Steps: []runner.Step{
					{Name: "checkout", Run: "true"},
					{Name: "implement", Run: "true"},
					{Name: "review", Run: "true"},
					{Name: "publish", Run: "true"},
				},
			},
		},
		Files: map[string]string{},
	}
	f.server.factory = loaded
	f.server.engine.Factory = loaded

	ticket := f.assign(t, "resume-1")
	if err := f.server.engine.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	run1, err := f.server.engine.Assign(t.Context(), "runner-1", []string{protocol.StepsHarness}, false)
	if err != nil || run1 == nil {
		t.Fatalf("run1 = %+v, err = %v", run1, err)
	}

	// Complete run1 as failed at review with a checkpoint.
	completion := protocol.Completion{
		State:      tickets.RunFailed,
		Error:      "step review: exit 1",
		FailedStep: "review",
		Checkpoint: &protocol.Checkpoint{
			FailedStep:   "review",
			HeadSHA:      "1111111",
			Patch:        []byte("patch-data"),
			ControlFiles: map[string]string{"assumptions.md": "kept\n"},
		},
		Stages: []protocol.StageResult{
			{Name: "checkout", Kind: "step", State: "succeeded"},
			{Name: "implement", Kind: "step", State: "succeeded"},
			{Name: "review", Kind: "step", State: "failed", Error: "exit 1"},
			{Name: "publish", Kind: "step", State: protocol.StageSkipped},
		},
	}
	if resp, body := f.call(t, http.MethodPost, "/api/v1/runs/"+run1.RunID+"/complete", f.token, completion); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("complete status = %d: %s", resp.StatusCode, body)
	}

	// Ticket detail exposes failed_step and checkpoint=true on the run.
	resp, body := f.call(t, http.MethodGet, "/api/v1/tickets/"+ticket.ID, f.token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get ticket status = %d: %s", resp.StatusCode, body)
	}
	var detail struct {
		Runs []tickets.Run `json:"runs"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Runs) != 1 || detail.Runs[0].FailedStep != "review" || !detail.Runs[0].Checkpoint {
		t.Fatalf("runs = %+v", detail.Runs)
	}

	rerunPath := "/api/v1/tickets/" + ticket.ID + "/rerun"

	// Invalid from_step values return 400 Bad Request.
	for _, tc := range []struct {
		step string
		want string
	}{
		{"checkout", "cannot resume from step"},
		{"nonexistent", "is not in preset"},
		{"publish", "after failed step"},
	} {
		resp, body := f.call(t, http.MethodPost, rerunPath, f.token, map[string]string{"from_step": tc.step})
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), tc.want) {
			t.Fatalf("from_step=%q: status=%d body=%s", tc.step, resp.StatusCode, body)
		}
	}

	// Valid from_step="review" succeeds and queues a resumed run carrying the checkpoint.
	resp, body = f.call(t, http.MethodPost, rerunPath, f.token, map[string]string{"from_step": "review"})
	if resp.StatusCode != http.StatusOK || decodeTicket(t, body).State != tickets.StateWorking {
		t.Fatalf("rerun from_step status = %d: %s", resp.StatusCode, body)
	}
	if err := f.server.engine.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	run2, err := f.server.engine.Assign(t.Context(), "runner-1", []string{protocol.StepsHarness}, false)
	if err != nil || run2 == nil {
		t.Fatalf("run2 = %+v, err = %v", run2, err)
	}
	if run2.ResumeFromStep != "review" || run2.Checkpoint == nil || string(run2.Checkpoint.Patch) != "patch-data" {
		t.Fatalf("run2 = %+v", run2)
	}

	// Complete run2 as failed WITHOUT a checkpoint (e.g. checkout failed).
	if resp, body := f.call(t, http.MethodPost, "/api/v1/runs/"+run2.RunID+"/complete", f.token, protocol.Completion{
		State:      tickets.RunFailed,
		Error:      "step checkout: exit 1",
		FailedStep: "checkout",
	}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("complete run2 status = %d: %s", resp.StatusCode, body)
	}

	// Asking for from_step when no checkpoint exists returns 400.
	resp, body = f.call(t, http.MethodPost, rerunPath, f.token, map[string]string{"from_step": "review"})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "checkpoint") {
		t.Fatalf("expected 400 without checkpoint, got %d: %s", resp.StatusCode, body)
	}

	// Calling POST /rerun with NO body preserves full-from-start rerun behavior.
	resp, body = f.call(t, http.MethodPost, rerunPath, f.token, nil)
	if resp.StatusCode != http.StatusOK || decodeTicket(t, body).State != tickets.StateWorking {
		t.Fatalf("full rerun status = %d: %s", resp.StatusCode, body)
	}
	if err := f.server.engine.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	run3, err := f.server.engine.Assign(t.Context(), "runner-1", []string{protocol.StepsHarness}, false)
	if err != nil || run3 == nil {
		t.Fatalf("run3 = %+v, err = %v", run3, err)
	}
	if run3.ResumeFromStep != "" || run3.Checkpoint != nil {
		t.Fatalf("expected full rerun without resume state, got %+v", run3)
	}
}
