package sources

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const testLinearSecret = "lin_wh_test_secret_0123456789"

func sign(body []byte) string {
	mac := hmac.New(sha256.New, []byte(testLinearSecret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// linearEvent is an issue event. assignee is the issue's assignee now, and
// updatedFrom holds the old values an update changed.
func linearEvent(t *testing.T, action string, at time.Time, assignee string, updatedFrom map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"action": action, "type": "Issue", "webhookTimestamp": at.UnixMilli(), "updatedFrom": updatedFrom,
		"actor": map[string]string{"id": "owain", "type": "user"},
		"data": map[string]any{
			"id": "issue-1", "identifier": "ENG-12", "title": "Fix the export button", "description": "It does nothing on Safari.",
			"url": "https://linear.app/team/issue/ENG-12", "assigneeId": assignee, "team": map[string]string{"key": "ENG"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestVerifyLinearAcceptsOnlyFreshSignedDeliveries(t *testing.T) {
	now := time.Now()
	body := linearEvent(t, "create", now, "", nil)
	if event, err := VerifyLinear(testLinearSecret, body, sign(body), now); err != nil || event.Data.Identifier != "ENG-12" {
		t.Fatalf("event = %+v, err = %v", event, err)
	}
	for name, check := range map[string]func() error{
		"no signature": func() error { _, err := VerifyLinear(testLinearSecret, body, "", now); return err },
		"wrong secret": func() error { _, err := VerifyLinear("another_secret_0123456789", body, sign(body), now); return err },
		"changed body": func() error {
			_, err := VerifyLinear(testLinearSecret, append([]byte(" "), body...), sign(body), now)
			return err
		},
		"replayed": func() error {
			_, err := VerifyLinear(testLinearSecret, body, sign(body), now.Add(2*time.Minute))
			return err
		},
		"no secret": func() error { _, err := VerifyLinear("", body, sign(body), now); return err },
	} {
		if err := check(); !errors.Is(err, ErrLinearUnsigned) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func decide(t *testing.T, rules LinearRules, body []byte) (LinearJob, string) {
	t.Helper()
	var event LinearEvent
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatal(err)
	}
	return rules.Decide(event)
}

func TestLinearStartsAJobWhenAnIssueIsAssignedToTheApp(t *testing.T) {
	rules := LinearRules{AppUserID: "app-user", AllowedUsers: []string{"owain"}, Preset: "coding", Teams: map[string]string{"eng": "owner/repo"}}
	now := time.Now()
	job, reason := decide(t, rules, linearEvent(t, "update", now, "app-user", map[string]any{"assigneeId": nil}))
	if reason != "" || job.Key != "linear:issue-1" || job.Repo != "owner/repo" || job.Preset != "coding" {
		t.Fatalf("job = %+v, reason = %q", job, reason)
	}
	if !strings.HasPrefix(job.Prompt, "Fix the export button\n\nIt does nothing on Safari.") || !strings.Contains(job.Prompt, "Linear issue ENG-12: https://linear.app/team/issue/ENG-12") {
		t.Fatalf("prompt = %q", job.Prompt)
	}
	started := map[string][]byte{
		"created assigned":    linearEvent(t, "create", now, "app-user", nil),
		"moved from a person": linearEvent(t, "update", now, "app-user", map[string]any{"assigneeId": "someone"}),
	}
	for name, body := range started {
		if _, reason := decide(t, rules, body); reason != "" {
			t.Errorf("%s: %s", name, reason)
		}
	}
	ignored := map[string][]byte{
		"assigned to a person":        linearEvent(t, "update", now, "someone", map[string]any{"assigneeId": nil}),
		"unassigned":                  linearEvent(t, "create", now, "", nil),
		"edited while the app has it": linearEvent(t, "update", now, "app-user", map[string]any{"title": "Old"}),
		"reassigned to the app again": linearEvent(t, "update", now, "app-user", map[string]any{"assigneeId": "app-user"}),
		"removed":                     linearEvent(t, "remove", now, "app-user", nil),
	}
	for name, body := range ignored {
		if _, reason := decide(t, rules, body); reason == "" {
			t.Errorf("%s started a job", name)
		}
	}
	other := LinearRules{AppUserID: "app-user", AllowedUsers: []string{"owain"}, Teams: map[string]string{"OPS": "owner/repo"}}
	if _, reason := decide(t, other, linearEvent(t, "create", now, "app-user", nil)); !strings.Contains(reason, "not connected") {
		t.Errorf("other team: reason = %q", reason)
	}
	stranger := LinearRules{AppUserID: "app-user", AllowedUsers: []string{"someone-else"}, Teams: map[string]string{"ENG": "owner/repo"}}
	if _, reason := decide(t, stranger, linearEvent(t, "create", now, "app-user", nil)); !strings.Contains(reason, "may not start jobs") {
		t.Errorf("unlisted user: reason = %q", reason)
	}
	var noActor map[string]any
	_ = json.Unmarshal(linearEvent(t, "create", now, "app-user", nil), &noActor)
	delete(noActor, "actor")
	body, _ := json.Marshal(noActor)
	if _, reason := decide(t, rules, body); !strings.Contains(reason, "may not start jobs") {
		t.Errorf("no actor: reason = %q", reason)
	}
	if _, reason := decide(t, LinearRules{Teams: map[string]string{"ENG": "owner/repo"}}, linearEvent(t, "create", now, "", nil)); reason == "" {
		t.Error("no app user started a job")
	}
}

func TestLinearJobIsCreatedOncePerIssue(t *testing.T) {
	intake, _ := newIntake(t)
	intake.Presets = func(name string) bool { return name == "coding" }
	job := LinearJob{Key: "linear:issue-1", Repo: "owner/repo", Preset: "coding", Prompt: "Fix it\n\nDetail"}
	first, created, err := intake.Linear(t.Context(), job)
	if err != nil || !created || first.Source != "linear" || first.CreatedBy != "linear" || first.Title != "Fix it" {
		t.Fatalf("ticket = %+v, created = %v, err = %v", first, created, err)
	}
	if again, found, err := intake.LinearExisting(t.Context(), job); err != nil || !found || again.ID != first.ID {
		t.Fatalf("again = %+v, found = %v, err = %v", again, found, err)
	}
}
