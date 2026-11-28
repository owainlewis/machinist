package sources

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/tickets"
)

const (
	maxTitleRunes = 80
	// repoVar is the built-in repository variable required on every run request.
	repoVar = "repo"
)

// RunRequest starts a job. Any system can send one: a tracker, a schedule or curl.
type RunRequest struct {
	Key     string            `json:"key"`
	Preset  string            `json:"preset"`
	Vars    map[string]string `json:"vars"`
	Prompt  string            `json:"prompt"`
	Harness string            `json:"harness,omitempty"`
	Model   string            `json:"model,omitempty"`
	RunsOn  []string          `json:"runs_on,omitempty"`
	// Origin is where the request came from: tickets.OriginWeb for the web
	// app, and tickets.OriginAPI otherwise. The caller sets it, not the body.
	Origin string `json:"-"`
}

// Run returns the job for a request. A repeated key returns the first job, so
// a caller can retry safely. The bool reports whether this call created it.
func (i *Intake) Run(ctx context.Context, request RunRequest, by string) (tickets.Ticket, bool, error) {
	if request.Key == "" || len(request.Key) > maxRequestIDBytes {
		return tickets.Ticket{}, false, invalid("key is required and at most %d bytes", maxRequestIDBytes)
	}
	harnessName := strings.TrimSpace(request.Harness)
	if err := factory.ValidateHarnessOverride(harnessName); err != nil {
		return tickets.Ticket{}, false, invalid("%v", err)
	}
	model := strings.TrimSpace(request.Model)
	runsOn, err := config.NormalizeLabels(request.RunsOn)
	if err != nil {
		return tickets.Ticket{}, false, invalid("runs_on: %v", err)
	}
	origin := request.Origin
	if origin == "" {
		origin = tickets.OriginAPI
	}
	return i.start(ctx, start{
		source:  "run",
		origin:  origin,
		key:     "run:" + request.Key,
		preset:  request.Preset,
		repo:    request.Vars[repoVar],
		vars:    request.Vars,
		prompt:  request.Prompt,
		harness: harnessName,
		model:   model,
		runsOn:  runsOn,
		by:      by,
	})
}

// start is a job from any source that names its preset, repository and prompt.
// origin is where it came from, such as web or linear, and link is the URL
// of the original issue or message, if there is one.
type start struct {
	source, origin, link, key, preset, repo, prompt, harness, model, by string
	vars                                                                map[string]string
	runsOn                                                              []string
}

func (i *Intake) start(ctx context.Context, job start) (tickets.Ticket, bool, error) {
	if existing, err := i.Store.TicketBySourceKey(ctx, job.key); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, tickets.ErrNotFound) {
		return tickets.Ticket{}, false, err
	}
	// A one-off job names no preset and gets the default one.
	defaultPreset, presets := i.presetConfig()
	if job.preset == "" {
		job.preset = defaultPreset
	}
	if presets == nil {
		return tickets.Ticket{}, false, invalid("preset %q does not exist", job.preset)
	}
	preset, ok := presets(job.preset)
	if !ok {
		return tickets.Ticket{}, false, invalid("preset %q does not exist", job.preset)
	}
	repository, connected, err := i.repository(ctx, job.repo)
	if err != nil {
		return tickets.Ticket{}, false, err
	}
	if !connected {
		return tickets.Ticket{}, false, invalid("vars.repo %q is not connected", job.repo)
	}
	var inputVars map[string]string
	for name, value := range job.vars {
		if name == repoVar {
			continue
		}
		if inputVars == nil {
			inputVars = make(map[string]string, len(job.vars))
		}
		inputVars[name] = value
	}
	resolvedVars, err := preset.ResolveVars(inputVars)
	if err != nil {
		return tickets.Ticket{}, false, invalid("%v", err)
	}
	prompt := strings.TrimSpace(job.prompt)
	if prompt == "" {
		return tickets.Ticket{}, false, invalid("prompt is required")
	}
	return i.Store.CreateTicket(ctx, tickets.NewTicket{
		Source:    job.source,
		Origin:    job.origin,
		SourceURL: job.link,
		Repo:      repository,
		Title:     title(prompt),
		Body:      prompt,
		Agent:     job.preset,
		Harness:   job.harness,
		Model:     job.model,
		RunsOn:    job.runsOn,
		Vars:      resolvedVars,
		CreatedBy: job.by,
		SourceKey: job.key,
	})
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
