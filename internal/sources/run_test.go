package sources

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTitleIsTheFirstLineCutToFit(t *testing.T) {
	if got := title("  Fix the login  \n\nDetails"); got != "Fix the login" {
		t.Fatalf("title = %q", got)
	}
	long := title(strings.Repeat("é", 200))
	if utf8.RuneCountInString(long) != maxTitleRunes || !strings.HasSuffix(long, "…") {
		t.Fatalf("title = %q", long)
	}
}

func TestRunValidatesAndStoresHarnessAndModel(t *testing.T) {
	intake, _ := newIntake(t)
	intake.Presets = func(name string) bool { return name == "default" }

	// Valid harness and model are trimmed and stored on the ticket.
	ticket, created, err := intake.Run(t.Context(), RunRequest{
		Key:     "k-override",
		Preset:  "default",
		Vars:    map[string]string{"repo": "owner/repo"},
		Prompt:  "Use codex for this task",
		Harness: "  codex ",
		Model:   "  gpt-5-codex  ",
	}, "alice")
	if err != nil || !created {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	if ticket.Harness != "codex" || ticket.Model != "gpt-5-codex" {
		t.Fatalf("ticket harness/model = (%q, %q), want (codex, gpt-5-codex)", ticket.Harness, ticket.Model)
	}

	// Empty harness and model use preset defaults.
	plain, created, err := intake.Run(t.Context(), RunRequest{
		Key:    "k-default",
		Preset: "default",
		Vars:   map[string]string{"repo": "owner/repo"},
		Prompt: "Use preset defaults",
	}, "alice")
	if err != nil || !created {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	if plain.Harness != "" || plain.Model != "" {
		t.Fatalf("plain ticket harness/model = (%q, %q), want empty", plain.Harness, plain.Model)
	}

	// Model-only override without harness is accepted.
	modelOnly, created, err := intake.Run(t.Context(), RunRequest{
		Key:    "k-model-only",
		Preset: "default",
		Vars:   map[string]string{"repo": "owner/repo"},
		Prompt: "Override model only",
		Model:  " opus ",
	}, "alice")
	if err != nil || !created || modelOnly.Harness != "" || modelOnly.Model != "opus" {
		t.Fatalf("modelOnly = %+v, created = %v, err = %v", modelOnly, created, err)
	}

	// Invalid harness is rejected as ErrInvalidRequest.
	_, _, err = intake.Run(t.Context(), RunRequest{
		Key:     "k-bad-harness",
		Preset:  "default",
		Vars:    map[string]string{"repo": "owner/repo"},
		Prompt:  "Try unknown harness",
		Harness: "cursor",
	}, "alice")
	if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "harness must be one of") {
		t.Fatalf("invalid harness err = %v, want ErrInvalidRequest", err)
	}
}
