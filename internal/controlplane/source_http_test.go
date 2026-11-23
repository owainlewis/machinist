package controlplane

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/owainlewis/machinist/internal/tickets"
)

// signedInRun starts a job the way the web app does: with the session cookie,
// not an Authorization header.
func (f *fixture) signedInRun(t *testing.T, key string) job {
	t.Helper()
	f.connect(t)
	encoded, err := json.Marshal(runRequest(key))
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, f.http.URL+"/machinist/run", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.token})
	response, err := f.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	return decodeJob(t, body)
}

func TestAJobStartedWithCurlIsFromTheAPIWithNoLink(t *testing.T) {
	f := newFixture(t)
	created := f.run(t, "deploy-42")
	if created.Source != tickets.OriginAPI || created.SourceURL != "" {
		t.Fatalf("job = %+v", created)
	}
	_, body := f.call(t, http.MethodGet, "/machinist/jobs/"+created.ID, f.token, nil)
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["source"] != "api" {
		t.Fatalf("source = %v: %s", raw["source"], body)
	}
	if _, found := raw["source_url"]; found {
		t.Fatalf("a job with no original has a link: %s", body)
	}
}

func TestAJobStartedInTheWebAppIsFromTheWeb(t *testing.T) {
	f := newFixture(t)
	if created := f.signedInRun(t, "ui-0123"); created.Source != tickets.OriginWeb || created.SourceURL != "" {
		t.Fatalf("job = %+v", created)
	}
}

func TestAJobFromLinearLinksToItsIssue(t *testing.T) {
	f := newFixture(t)
	created, _, err := f.store.CreateTicket(t.Context(), tickets.NewTicket{Source: "linear", Origin: "linear", SourceURL: "https://linear.app/x/issue/ENG-1", Repo: "owner/repo", Title: "Fix", Body: "Fix it", Agent: "default", CreatedBy: "linear", SourceKey: "linear:1"})
	if err != nil {
		t.Fatal(err)
	}
	_, body := f.call(t, http.MethodGet, "/machinist/jobs/"+created.ID, f.token, nil)
	if view := decodeJob(t, body); view.Source != "linear" || view.SourceURL != "https://linear.app/x/issue/ENG-1" {
		t.Fatalf("job = %+v", view)
	}
	_, body = f.call(t, http.MethodGet, "/api/v1/tickets/"+created.ID, f.token, nil)
	var detail struct {
		Ticket map[string]any `json:"ticket"`
	}
	if err := json.Unmarshal(body, &detail); err != nil || detail.Ticket["source"] != "linear" || detail.Ticket["source_url"] != "https://linear.app/x/issue/ENG-1" {
		t.Fatalf("ticket = %v, err = %v", detail.Ticket, err)
	}
}

func TestJobListsFilterBySource(t *testing.T) {
	f := newFixture(t)
	fromAPI := f.run(t, "k1")
	fromWeb := f.signedInRun(t, "ui-k2")

	_, body := f.call(t, http.MethodGet, "/machinist/jobs?source=web", f.token, nil)
	var jobs struct {
		Jobs []job `json:"jobs"`
	}
	if err := json.Unmarshal(body, &jobs); err != nil || len(jobs.Jobs) != 1 || jobs.Jobs[0].ID != fromWeb.ID {
		t.Fatalf("jobs = %s, err = %v", body, err)
	}

	_, body = f.call(t, http.MethodGet, "/api/v1/tickets?source=api", f.token, nil)
	var listed struct {
		Tickets []tickets.Ticket `json:"tickets"`
	}
	if err := json.Unmarshal(body, &listed); err != nil || len(listed.Tickets) != 1 || listed.Tickets[0].ID != fromAPI.ID {
		t.Fatalf("tickets = %s, err = %v", body, err)
	}

	_, body = f.call(t, http.MethodGet, "/machinist/jobs", f.token, nil)
	if err := json.Unmarshal(body, &jobs); err != nil || len(jobs.Jobs) != 2 {
		t.Fatalf("jobs = %s, err = %v", body, err)
	}
}
