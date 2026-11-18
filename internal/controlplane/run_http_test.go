package controlplane

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
)

func decodeJob(t *testing.T, body []byte) job {
	t.Helper()
	var view job
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return view
}

func runRequest(key string) sources.RunRequest {
	return sources.RunRequest{Key: key, Preset: "default", Vars: map[string]string{"repo": "owner/repo"}, Prompt: "Add a retry to the upload client\n\nSee ENG-142."}
}

func (f *fixture) run(t *testing.T, key string) job {
	t.Helper()
	f.connect(t)
	response, body := f.call(t, http.MethodPost, "/machinist/run", f.token, runRequest(key))
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	return decodeJob(t, body)
}

func TestRunCreatesAQueuedJobAndAKeyRepeatsIt(t *testing.T) {
	f := newFixture(t)
	first := f.run(t, "linear:ENG-142")
	if first.State != JobQueued || first.Preset != "default" || first.Vars["repo"] != "owner/repo" || first.CreatedBy != "runner-1" {
		t.Fatalf("job = %+v", first)
	}
	if first.Title != "Add a retry to the upload client" {
		t.Fatalf("title = %q", first.Title)
	}
	entries, err := f.store.Entries(t.Context(), first.ID)
	if err != nil || len(entries) == 0 || !strings.Contains(entries[0].Body, "See ENG-142.") {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
	response, body := f.call(t, http.MethodPost, "/machinist/run", f.token, runRequest("linear:ENG-142"))
	if response.StatusCode != http.StatusOK || decodeJob(t, body).ID != first.ID {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
}

func TestRunRejectsBadRequests(t *testing.T) {
	f := newFixture(t)
	f.connect(t)
	for name, change := range map[string]func(*sources.RunRequest){
		"no key":         func(r *sources.RunRequest) { r.Key = "" },
		"long key":       func(r *sources.RunRequest) { r.Key = strings.Repeat("k", 201) },
		"unknown preset": func(r *sources.RunRequest) { r.Preset = "missing" },
		"no repo":        func(r *sources.RunRequest) { r.Vars = nil },
		"other repo":     func(r *sources.RunRequest) { r.Vars["repo"] = "owner/other" },
		"unknown var":    func(r *sources.RunRequest) { r.Vars["branch"] = "main" },
		"no prompt":      func(r *sources.RunRequest) { r.Prompt = "  \n" },
	} {
		request := runRequest("k")
		change(&request)
		if response, body := f.call(t, http.MethodPost, "/machinist/run", f.token, request); response.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status = %d: %s", name, response.StatusCode, body)
		}
	}
}

func TestJobStateFollowsTheTicket(t *testing.T) {
	running := []tickets.Run{{Status: tickets.RunRunning}}
	ended := []tickets.Run{{Status: tickets.RunSucceeded}}
	for _, test := range []struct {
		state string
		runs  []tickets.Run
		want  string
	}{
		{tickets.StateWorking, nil, JobQueued},
		{tickets.StateWorking, ended, JobQueued},
		{tickets.StateWorking, running, JobRunning},
		{tickets.StatePRReady, ended, JobDone},
		{tickets.StateDone, ended, JobDone},
		{tickets.StateFailed, ended, JobFailed},
		{tickets.StateCancelled, nil, JobCancelled},
	} {
		if got := jobState(tickets.Ticket{State: test.state}, test.runs); got != test.want {
			t.Fatalf("%s with %v = %s, want %s", test.state, test.runs, got, test.want)
		}
	}
}

func TestJobRoutesGetListAndCancel(t *testing.T) {
	f := newFixture(t)
	created := f.run(t, "k1")

	response, body := f.call(t, http.MethodGet, "/machinist/jobs/"+created.ID, f.token, nil)
	if response.StatusCode != http.StatusOK || decodeJob(t, body).ID != created.ID {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	if response, _ := f.call(t, http.MethodGet, "/machinist/jobs/t_missing", f.token, nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", response.StatusCode)
	}

	response, body = f.call(t, http.MethodGet, "/machinist/jobs", f.token, nil)
	var list struct {
		Jobs []job `json:"jobs"`
	}
	if err := json.Unmarshal(body, &list); err != nil || response.StatusCode != http.StatusOK || len(list.Jobs) != 1 {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}

	// Jobs never wait for a person, so there is nothing to answer.
	if response, _ := f.call(t, http.MethodPost, "/machinist/jobs/"+created.ID+"/input", f.token, map[string]string{"text": "Postgres"}); response.StatusCode == http.StatusOK {
		t.Fatalf("input: status = %d", response.StatusCode)
	}

	response, body = f.call(t, http.MethodPost, "/machinist/jobs/"+created.ID+"/cancel", f.token, nil)
	if cancelled := decodeJob(t, body); response.StatusCode != http.StatusOK || cancelled.State != JobCancelled {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
}
