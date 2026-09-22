package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubmitWorkflow(t *testing.T) {
	var submitted submitJobRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/catalog" {
			writeTestJSON(w, map[string]any{"commands": []string{"build"}, "workflows": []string{"deliver"}, "repositories": []string{"repo"}})
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
			t.Error(err)
		}
		writeTestJSON(w, map[string]string{"id": "job_workflow"})
	}))
	defer server.Close()
	path := writeSubmitWorkerConfig(t, server.URL, "token")
	var out, stderr bytes.Buffer
	code := Execute(t.Context(), []string{"submit", "--workflow=deliver", "--repo=repo", "--prompt=issue", "--config=" + path}, strings.NewReader(""), &out, &stderr, "test")
	if code != 0 || out.String() != "job_workflow\n" || submitted.Workflow != "deliver" || submitted.Command != "" {
		t.Fatalf("submission %#v code=%d error=%s", submitted, code, &stderr)
	}
	for _, args := range [][]string{{"submit", "--repo=repo", "--prompt=issue"}, {"submit", "--workflow=deliver", "--command=build", "--repo=repo", "--prompt=issue"}} {
		if Execute(t.Context(), args, strings.NewReader(""), &out, &stderr, "test") == 0 {
			t.Fatal("accepted ambiguous selection")
		}
	}
}
