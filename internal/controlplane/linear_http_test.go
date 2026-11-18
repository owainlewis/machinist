package controlplane

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/fakegh"
	"github.com/owainlewis/machinist/internal/tickets"
)

const linearSecret = "lin_wh_test_secret_0123456789"

func linearFixture(t *testing.T, maxJobs int) *fixture {
	t.Helper()
	fakegh.Install(t, testConfig().GitHub.Handle)
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	machinistConfig := testConfig()
	machinistConfig.GitHub.Repositories = []string{"owner/repo"}
	machinistConfig.Linear.TriggerLabel = "machinist"
	machinistConfig.Linear.Teams = map[string]string{"ENG": "owner/repo"}
	machinistConfig.Linear.MaxJobsPerHour = maxJobs
	machinistConfig.Linear.Secret = linearSecret
	server, err := New(machinistConfig, store, testFactory(), "test", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(server.Handler())
	t.Cleanup(web.Close)
	return &fixture{server: server, store: store, http: web}
}

func issueEvent(t *testing.T, id string, labels ...string) []byte {
	t.Helper()
	list := []map[string]string{}
	for _, name := range labels {
		list = append(list, map[string]string{"id": "label-" + name, "name": name})
	}
	body, err := json.Marshal(map[string]any{
		"action": "create", "type": "Issue", "webhookTimestamp": time.Now().UnixMilli(),
		"data": map[string]any{"id": id, "identifier": "ENG-1", "title": "Fix it", "description": "Detail", "url": "https://linear.app/x/issue/ENG-1", "team": map[string]string{"key": "ENG"}, "labels": list},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func (f *fixture) deliver(t *testing.T, body []byte, signature string) (int, map[string]string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, f.http.URL+"/webhooks/linear", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if signature != "" {
		request.Header.Set("Linear-Signature", signature)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var reply map[string]string
	_ = json.NewDecoder(response.Body).Decode(&reply)
	return response.StatusCode, reply
}

func linearSign(body []byte) string {
	mac := hmac.New(sha256.New, []byte(linearSecret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestLinearWebhookStartsOneJobPerIssue(t *testing.T) {
	f := linearFixture(t, 5)
	body := issueEvent(t, "issue-1", "machinist")
	status, reply := f.deliver(t, body, linearSign(body))
	if status != http.StatusCreated || reply["job"] == "" {
		t.Fatalf("status = %d, reply = %v", status, reply)
	}
	ticket, err := f.store.Ticket(t.Context(), reply["job"])
	if err != nil || ticket.Source != "linear" || ticket.Repo != "owner/repo" || ticket.Agent != "default" || ticket.State != tickets.StateWorking {
		t.Fatalf("ticket = %+v, err = %v", ticket, err)
	}
	again := issueEvent(t, "issue-1", "machinist")
	if status, retry := f.deliver(t, again, linearSign(again)); status != http.StatusOK || retry["job"] != reply["job"] {
		t.Fatalf("retry: status = %d, reply = %v", status, retry)
	}
}

func TestLinearWebhookRejectsUnsignedRequests(t *testing.T) {
	f := linearFixture(t, 5)
	body := issueEvent(t, "issue-1", "machinist")
	for _, signature := range []string{"", "00", strings.Repeat("ab", 32)} {
		if status, _ := f.deliver(t, body, signature); status != http.StatusUnauthorized {
			t.Fatalf("signature %q: status = %d", signature, status)
		}
	}
	if listed, _ := f.store.ListTickets(t.Context(), "", ""); len(listed) != 0 {
		t.Fatalf("tickets = %+v", listed)
	}
	huge := bytes.Repeat([]byte("x"), maxLinearBodyBytes+1)
	if status, _ := f.deliver(t, huge, linearSign(huge)); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("huge: status = %d", status)
	}
}

func TestLinearWebhookIgnoresIssuesWithoutTheTrigger(t *testing.T) {
	f := linearFixture(t, 5)
	body := issueEvent(t, "issue-1", "bug")
	if status, reply := f.deliver(t, body, linearSign(body)); status != http.StatusOK || reply["ignored"] == "" {
		t.Fatalf("status = %d, reply = %v", status, reply)
	}
}

func TestLinearWebhookCapsJobsPerHour(t *testing.T) {
	f := linearFixture(t, 1)
	first := issueEvent(t, "issue-1", "machinist")
	if status, _ := f.deliver(t, first, linearSign(first)); status != http.StatusCreated {
		t.Fatalf("first: status = %d", status)
	}
	second := issueEvent(t, "issue-2", "machinist")
	if status, _ := f.deliver(t, second, linearSign(second)); status != http.StatusTooManyRequests {
		t.Fatalf("second: status = %d", status)
	}
	f.server.now = func() time.Time { return time.Now().Add(61 * time.Minute) }
	later := issueEvent(t, "issue-2", "machinist")
	var event map[string]any
	_ = json.Unmarshal(later, &event)
	event["webhookTimestamp"] = f.server.now().UnixMilli()
	later, _ = json.Marshal(event)
	if status, _ := f.deliver(t, later, linearSign(later)); status != http.StatusCreated {
		t.Fatalf("an hour later: status = %d", status)
	}
}

func TestLinearWebhookIsOffWithoutTeams(t *testing.T) {
	f := newFixture(t)
	body := issueEvent(t, "issue-1", "machinist")
	if status, _ := f.deliver(t, body, linearSign(body)); status == http.StatusCreated || status == http.StatusOK {
		t.Fatalf("status = %d", status)
	}
}

func TestLinearTeamsMustBeConnected(t *testing.T) {
	machinistConfig := testConfig()
	machinistConfig.Linear.TriggerLabel = "machinist"
	machinistConfig.Linear.Teams = map[string]string{"ENG": "owner/elsewhere"}
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := New(machinistConfig, store, testFactory(), "test", io.Discard); err == nil || !strings.Contains(err.Error(), "not in github.repositories") {
		t.Fatalf("error = %v", err)
	}
	machinistConfig.GitHub.Repositories = []string{"owner/elsewhere"}
	machinistConfig.Linear.Preset = "missing"
	if _, err := New(machinistConfig, store, testFactory(), "test", io.Discard); err == nil || !strings.Contains(err.Error(), "not in the factory") {
		t.Fatalf("error = %v", err)
	}
}
