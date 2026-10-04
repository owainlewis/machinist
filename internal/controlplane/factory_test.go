package controlplane

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestFactoryApprovalsRequireBrowserAuthority(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()
	defer server.factory.Close()
	status := getStatus(t, web.URL)
	for _, headers := range []map[string]string{
		nil,
		{"Authorization": "Bearer secret"},
		{"Authorization": "Bearer conversation-scoped"},
		{"Origin": "http://evil.example", "X-Machinist-CSRF": status.CSRFToken},
	} {
		response := postJSON(t, web.URL+"/api/factory/tasks/task_1/approve", map[string]any{"version": 1, "subject": "design"}, headers)
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("untrusted approval status %d", response.StatusCode)
		}
	}
	response := postJSON(t, web.URL+"/api/factory/tools/inspect_tasks", map[string]any{}, map[string]string{"Authorization": "Bearer secret"})
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("worker token must not become coordinator credential: %d", response.StatusCode)
	}
}

func TestFactoryDisabledKeepsLegacyStatus(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()
	defer server.factory.Close()
	response, err := http.Get(web.URL + "/api/factory/status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var factory struct {
		Enabled bool   `json:"enabled"`
		CSRF    string `json:"csrf_token"`
	}
	if err = json.NewDecoder(response.Body).Decode(&factory); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || factory.Enabled || factory.CSRF == "" {
		t.Fatalf("disabled factory status=%d body=%#v", response.StatusCode, factory)
	}
	status := getStatus(t, web.URL)
	if len(status.Commands) != 1 {
		t.Fatal("legacy catalog changed")
	}
}
