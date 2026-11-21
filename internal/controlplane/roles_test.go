package controlplane

import (
	"net/http"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/tickets"
)

func (f *fixture) newToken(t *testing.T, name, role string) string {
	t.Helper()
	token, err := f.store.CreateToken(t.Context(), name, role)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// A person's token cannot take runs, so it cannot get a run's GitHub token,
// and a runner's token cannot use the API or sign in.
func TestTokensOnlyDoWhatTheirRoleAllows(t *testing.T) {
	f := newFixture(t)
	operator := f.newToken(t, "alice", tickets.RoleOperator)
	runner := f.newToken(t, "runner-2", tickets.RoleRunner)
	for _, test := range []struct {
		name, method, path, token string
		body                      any
		want                      int
	}{
		{"operator polls", http.MethodPost, "/api/v1/runners/poll", operator, protocol.PollRequest{}, http.StatusForbidden},
		{"runner polls", http.MethodPost, "/api/v1/runners/poll", runner, protocol.PollRequest{}, http.StatusNoContent},
		{"operator lists jobs", http.MethodGet, "/api/v1/tickets", operator, nil, http.StatusOK},
		{"runner lists jobs", http.MethodGet, "/api/v1/tickets", runner, nil, http.StatusForbidden},
		{"runner adds a project", http.MethodPost, "/api/v1/projects", runner, map[string]string{"repository": "owner/other"}, http.StatusForbidden},
		{"runner signs in", http.MethodPost, "/api/v1/session", "", map[string]string{"token": runner}, http.StatusForbidden},
		{"operator signs in", http.MethodPost, "/api/v1/session", "", map[string]string{"token": operator}, http.StatusOK},
		// A token from before roles keeps working for both.
		{"old token polls", http.MethodPost, "/api/v1/runners/poll", f.token, protocol.PollRequest{}, http.StatusNoContent},
		{"old token lists jobs", http.MethodGet, "/api/v1/tickets", f.token, nil, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, body := f.call(t, test.method, test.path, test.token, test.body)
			if response.StatusCode != test.want {
				t.Fatalf("status = %d, want %d: %s", response.StatusCode, test.want, body)
			}
		})
	}
}

// One runner cannot keep alive, write into or finish a run another runner
// holds.
func TestARunnerCanOnlyReportOnItsOwnRuns(t *testing.T) {
	f := newFixture(t)
	run := f.queueRun(t)
	other := f.newToken(t, "runner-2", tickets.RoleRunner)
	for _, path := range []string{"/heartbeat", "/events", "/complete"} {
		var body any = struct{}{}
		switch path {
		case "/events":
			body = protocol.RunEventBatch{}
		case "/complete":
			body = protocol.Completion{State: tickets.RunFailed, Error: "forged"}
		}
		response, data := f.call(t, http.MethodPost, "/api/v1/runs/"+run.RunID+path, other, body)
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("%s by another runner = %d: %s", path, response.StatusCode, data)
		}
	}
	got, err := f.store.Run(t.Context(), run.RunID)
	if err != nil || got.Status != tickets.RunRunning {
		t.Fatalf("run = %+v, err = %v", got, err)
	}
	if response, data := f.call(t, http.MethodPost, "/api/v1/runs/"+run.RunID+"/heartbeat", f.token, struct{}{}); response.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat by its runner = %d: %s", response.StatusCode, data)
	}
}
