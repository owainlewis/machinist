package tickets

import (
	"errors"
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
		CreatedBy: "alice",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Name != "triage-prs" || created.Harness != "codex" || created.Model != "gpt-5-codex" || created.Schedule != "0 9 * * 1-5" {
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
	if list[0].Schedule != "" || list[0].Timezone != "" {
		t.Fatalf("manual automation should have empty schedule: %+v", list[0])
	}

	updated, err := store.UpdateAutomation(ctx, Automation{
		Name:    "triage-prs",
		Repo:    "owner/api",
		Preset:  "opus",
		Prompt:  "Updated prompt.",
		Harness: "claude",
		Model:   "opus",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Repo != "owner/api" || updated.Preset != "opus" || updated.Schedule != "" || updated.Harness != "claude" || updated.Model != "opus" {
		t.Fatalf("updated = %+v", updated)
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

	// Ticket harness and model overrides round-trip.
	ticket, createdTicket, err := store.CreateTicket(ctx, NewTicket{
		Source:    "schedule:audit-deps",
		Origin:    OriginSchedule,
		Repo:      "owner/api",
		Title:     "audit-deps",
		Body:      "Audit dependencies.",
		Agent:     "neon",
		Harness:   "codex",
		Model:     "gpt-5-codex",
		CreatedBy: "alice",
		SourceKey: "schedule:audit-deps:manual:1",
	})
	if err != nil || !createdTicket {
		t.Fatalf("create ticket = %+v, %v, %v", ticket, createdTicket, err)
	}
	if ticket.Harness != "codex" || ticket.Model != "gpt-5-codex" {
		t.Fatalf("ticket overrides = harness:%q model:%q", ticket.Harness, ticket.Model)
	}
	reloaded, err := store.Ticket(ctx, ticket.ID)
	if err != nil || reloaded.Harness != "codex" || reloaded.Model != "gpt-5-codex" {
		t.Fatalf("reloaded ticket = %+v, %v", reloaded, err)
	}
}
