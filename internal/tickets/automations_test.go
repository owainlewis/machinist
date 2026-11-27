package tickets

import (
	"errors"
	"slices"
	"testing"
)

func TestAutomationsCRUDAndTicketOverrides(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()

	list, err := store.Automations(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("empty list = %v, %v", list, err)
	}

	created, err := store.CreateAutomation(ctx, Automation{
		Name:      "triage-prs",
		Repo:      "owner/app",
		Preset:    "default",
		Prompt:    "Review open pull requests.",
		Schedule:  "0 9 * * 1-5",
		Timezone:  "UTC",
		Harness:   "codex",
		Model:     "gpt-5-codex",
		RunsOn:    []string{" GPU ", "linux", "gpu"},
		CreatedBy: "alice",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Name != "triage-prs" || created.Harness != "codex" || created.Model != "gpt-5-codex" || created.Schedule != "0 9 * * 1-5" || !slices.Equal(created.RunsOn, []string{"gpu", "linux"}) {
		t.Fatalf("created = %+v", created)
	}

	// Duplicate fails cleanly.
	if _, err := store.CreateAutomation(ctx, Automation{Name: "triage-prs", Repo: "owner/app", Preset: "default", Prompt: "Dup"}); !errors.Is(err, ErrAutomationExists) {
		t.Fatalf("duplicate err = %v, want ErrAutomationExists", err)
	}

	// Second manual-only automation sorts ahead of triage-prs.
	if _, err := store.CreateAutomation(ctx, Automation{
		Name:   "audit-deps",
		Repo:   "owner/api",
		Preset: "neon",
		Prompt: "Audit dependencies.",
	}); err != nil {
		t.Fatalf("create manual: %v", err)
	}

	list, err = store.Automations(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "audit-deps" || list[1].Name != "triage-prs" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list[0].Schedule != "" || list[0].Timezone != "" || len(list[0].RunsOn) != 0 {
		t.Fatalf("manual automation should have empty schedule and runs_on: %+v", list[0])
	}
	if !slices.Equal(list[1].RunsOn, []string{"gpu", "linux"}) {
		t.Fatalf("listed runs_on = %v, want [gpu linux]", list[1].RunsOn)
	}

	updated, err := store.UpdateAutomation(ctx, Automation{
		Name:    "triage-prs",
		Repo:    "owner/api",
		Preset:  "opus",
		Prompt:  "Updated prompt.",
		Harness: "claude",
		Model:   "opus",
		RunsOn:  []string{"MacOS", "arm64"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Repo != "owner/api" || updated.Preset != "opus" || updated.Schedule != "" || updated.Harness != "claude" || updated.Model != "opus" || !slices.Equal(updated.RunsOn, []string{"arm64", "macos"}) {
		t.Fatalf("updated = %+v", updated)
	}
	fetched, err := store.Automation(ctx, "triage-prs")
	if err != nil || !slices.Equal(fetched.RunsOn, []string{"arm64", "macos"}) {
		t.Fatalf("fetched automation = %+v, %v", fetched, err)
	}

	if _, err := store.UpdateAutomation(ctx, Automation{Name: "missing", Repo: "owner/app", Preset: "default", Prompt: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing update err = %v", err)
	}

	removed, err := store.DeleteAutomation(ctx, "triage-prs")
	if err != nil || !removed {
		t.Fatalf("delete = %v, %v", removed, err)
	}
	removed, err = store.DeleteAutomation(ctx, "triage-prs")
	if err != nil || removed {
		t.Fatalf("second delete = %v, %v", removed, err)
	}

	// Ticket harness, model and runs_on overrides round-trip.
	ticket, createdTicket, err := store.CreateTicket(ctx, NewTicket{
		Source:    "schedule:audit-deps",
		Origin:    OriginSchedule,
		Repo:      "owner/api",
		Title:     "audit-deps",
		Body:      "Audit dependencies.",
		Agent:     "neon",
		Harness:   "codex",
		Model:     "gpt-5-codex",
		RunsOn:    []string{"GPU", "linux", "gpu"},
		CreatedBy: "alice",
		SourceKey: "schedule:audit-deps:manual:1",
	})
	if err != nil || !createdTicket {
		t.Fatalf("create ticket = %+v, %v, %v", ticket, createdTicket, err)
	}
	if ticket.Harness != "codex" || ticket.Model != "gpt-5-codex" || len(ticket.RunsOn) != 2 || ticket.RunsOn[0] != "gpu" || ticket.RunsOn[1] != "linux" {
		t.Fatalf("ticket overrides = harness:%q model:%q runs_on:%v", ticket.Harness, ticket.Model, ticket.RunsOn)
	}
	reloaded, err := store.Ticket(ctx, ticket.ID)
	if err != nil || reloaded.Harness != "codex" || reloaded.Model != "gpt-5-codex" || len(reloaded.RunsOn) != 2 || reloaded.RunsOn[0] != "gpu" || reloaded.RunsOn[1] != "linux" {
		t.Fatalf("reloaded ticket = %+v, %v", reloaded, err)
	}
}
