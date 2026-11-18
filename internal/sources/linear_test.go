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

func linearEvent(t *testing.T, action string, at time.Time, labels []string, updatedFrom map[string]any) []byte {
	t.Helper()
	type label struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	list := []label{}
	for _, name := range labels {
		list = append(list, label{ID: "label-" + name, Name: name})
	}
	body, err := json.Marshal(map[string]any{
		"action": action, "type": "Issue", "webhookTimestamp": at.UnixMilli(), "updatedFrom": updatedFrom,
		"data": map[string]any{
			"id": "issue-1", "identifier": "ENG-12", "title": "Fix the export button", "description": "It does nothing on Safari.",
			"url": "https://linear.app/team/issue/ENG-12", "assigneeId": "", "team": map[string]string{"key": "ENG"}, "labels": list,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestVerifyLinearAcceptsOnlyFreshSignedDeliveries(t *testing.T) {
	now := time.Now()
	body := linearEvent(t, "create", now, nil, nil)
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

func TestLinearStartsAJobOnlyWhenTheTriggerIsNew(t *testing.T) {
	rules := LinearRules{TriggerLabel: "Machinist", AppUserID: "app-user", Preset: "coding", Teams: map[string]string{"eng": "owner/repo"}}
	now := time.Now()
	job, reason := decide(t, rules, linearEvent(t, "create", now, []string{"machinist"}, nil))
	if reason != "" || job.Key != "linear:issue-1" || job.Repo != "owner/repo" || job.Preset != "coding" {
		t.Fatalf("job = %+v, reason = %q", job, reason)
	}
	if !strings.HasPrefix(job.Prompt, "Fix the export button\n\nIt does nothing on Safari.") || !strings.Contains(job.Prompt, "Linear issue ENG-12: https://linear.app/team/issue/ENG-12") {
		t.Fatalf("prompt = %q", job.Prompt)
	}
	if _, reason := decide(t, rules, linearEvent(t, "update", now, []string{"machinist"}, map[string]any{"labelIds": []string{}})); reason != "" {
		t.Fatalf("adding the label: %s", reason)
	}
	ignored := map[string][]byte{
		"no label":           linearEvent(t, "create", now, []string{"bug"}, nil),
		"label already on":   linearEvent(t, "update", now, []string{"machinist"}, map[string]any{"labelIds": []string{"label-machinist"}}),
		"other field edited": linearEvent(t, "update", now, []string{"machinist"}, map[string]any{"title": "Old"}),
		"removed":            linearEvent(t, "remove", now, []string{"machinist"}, nil),
	}
	other := LinearRules{TriggerLabel: "machinist", Teams: map[string]string{"OPS": "owner/repo"}}
	if _, reason := decide(t, other, linearEvent(t, "create", now, []string{"machinist"}, nil)); !strings.Contains(reason, "not connected") {
		t.Errorf("other team: reason = %q", reason)
	}
	for name, body := range ignored {
		if _, reason := decide(t, rules, body); reason == "" {
			t.Errorf("%s started a job", name)
		}
	}
}

func TestLinearStartsAJobWhenAssignedToTheApp(t *testing.T) {
	rules := LinearRules{AppUserID: "app-user", Preset: "coding", Teams: map[string]string{"ENG": "owner/repo"}}
	assigned := func(before any) []byte {
		var event map[string]any
		_ = json.Unmarshal(linearEvent(t, "update", time.Now(), nil, map[string]any{"assigneeId": before}), &event)
		event["data"].(map[string]any)["assigneeId"] = "app-user"
		body, _ := json.Marshal(event)
		return body
	}
	if _, reason := decide(t, rules, assigned(nil)); reason != "" {
		t.Fatalf("from nobody: %s", reason)
	}
	if _, reason := decide(t, rules, assigned("someone")); reason != "" {
		t.Fatalf("from someone: %s", reason)
	}
	if _, reason := decide(t, rules, assigned("app-user")); reason == "" {
		t.Fatal("an unchanged assignee started a job")
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
