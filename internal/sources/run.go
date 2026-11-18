package sources

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/owainlewis/machinist/internal/tickets"
)

const (
	maxTitleRunes = 80
	// repoVar is the one variable every preset takes until presets declare their own.
	repoVar = "repo"
)

// RunRequest starts a job. Any system can send one: a tracker, a schedule or curl.
type RunRequest struct {
	Key    string            `json:"key"`
	Preset string            `json:"preset"`
	Vars   map[string]string `json:"vars"`
	Prompt string            `json:"prompt"`
}

// Run returns the job for a request. A repeated key returns the first job, so
// a caller can retry safely. The bool reports whether this call created it.
func (i *Intake) Run(ctx context.Context, request RunRequest, by string) (tickets.Ticket, bool, error) {
	if request.Key == "" || len(request.Key) > maxRequestIDBytes {
		return tickets.Ticket{}, false, invalid("key is required and at most %d bytes", maxRequestIDBytes)
	}
	key := "run:" + request.Key
	if existing, err := i.Store.TicketBySourceKey(ctx, key); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, tickets.ErrNotFound) {
		return tickets.Ticket{}, false, err
	}
	// A one-off job names no preset and gets the default one.
	if request.Preset == "" {
		request.Preset = i.DefaultPreset
	}
	if i.Presets == nil || !i.Presets(request.Preset) {
		return tickets.Ticket{}, false, invalid("preset %q does not exist", request.Preset)
	}
	if extra := unknownVars(request.Vars); len(extra) > 0 {
		return tickets.Ticket{}, false, invalid("unknown vars: %s", strings.Join(extra, ", "))
	}
	repository, connected := i.repository(request.Vars[repoVar])
	if !connected {
		return tickets.Ticket{}, false, invalid("vars.repo %q is not connected", request.Vars[repoVar])
	}
	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" {
		return tickets.Ticket{}, false, invalid("prompt is required")
	}
	return i.Store.CreateTicket(ctx, tickets.NewTicket{
		Source:    "run",
		Repo:      repository,
		Title:     title(prompt),
		Body:      prompt,
		Agent:     request.Preset,
		CreatedBy: by,
		SourceKey: key,
	})
}

func unknownVars(vars map[string]string) []string {
	extra := []string{}
	for name := range vars {
		if name != repoVar {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return extra
}

// title is the prompt's first line, cut to a length a list can show.
func title(prompt string) string {
	line, _, _ := strings.Cut(prompt, "\n")
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) <= maxTitleRunes {
		return line
	}
	runes := []rune(line)
	return strings.TrimSpace(string(runes[:maxTitleRunes-1])) + "…"
}
