package sources

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/tickets"
)

// MaxLinearSkew is how far a webhook's timestamp may be from now. Linear sends
// each delivery at once, so an older one is a replay.
const MaxLinearSkew = time.Minute

// ErrLinearUnsigned means a request is not from Linear, or is a replay.
var ErrLinearUnsigned = errors.New("the request is not signed by Linear")

// LinearRules decide which Linear issues start jobs, and where. Assigning an
// issue to the Machinist app is the only trigger.
type LinearRules struct {
	Secret    string
	AppUserID string
	Preset    string
	// Teams maps a team key to a repository.
	Teams map[string]string
}

// LinearEvent is the part of a Linear webhook payload Machinist reads.
type LinearEvent struct {
	Action    string `json:"action"`
	Type      string `json:"type"`
	Timestamp int64  `json:"webhookTimestamp"`
	Data      struct {
		ID          string `json:"id"`
		Identifier  string `json:"identifier"`
		Title       string `json:"title"`
		Description string `json:"description"`
		URL         string `json:"url"`
		AssigneeID  string `json:"assigneeId"`
		Team        struct {
			Key string `json:"key"`
		} `json:"team"`
	} `json:"data"`
	// UpdatedFrom holds the old value of each field an update changed.
	UpdatedFrom map[string]json.RawMessage `json:"updatedFrom"`
}

// VerifyLinear checks the Linear-Signature header, an HMAC-SHA256 of the raw
// body, and that the delivery is fresh. It returns the parsed event.
func VerifyLinear(secret string, body []byte, signature string, now time.Time) (LinearEvent, error) {
	got, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil || secret == "" {
		return LinearEvent{}, ErrLinearUnsigned
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return LinearEvent{}, ErrLinearUnsigned
	}
	var event LinearEvent
	if err := json.Unmarshal(body, &event); err != nil {
		return LinearEvent{}, invalid("the payload is not JSON: %v", err)
	}
	sent := time.UnixMilli(event.Timestamp)
	if event.Timestamp == 0 || now.Sub(sent).Abs() > MaxLinearSkew {
		return LinearEvent{}, fmt.Errorf("%w: webhookTimestamp is more than %s from now", ErrLinearUnsigned, MaxLinearSkew)
	}
	return event, nil
}

// LinearJob is a job a Linear issue asked for.
type LinearJob struct {
	Key, Repo, Preset, Prompt string
}

// Decide returns the job an event starts, or the reason it starts none.
func (rules LinearRules) Decide(event LinearEvent) (LinearJob, string) {
	if event.Type != "Issue" || (event.Action != "create" && event.Action != "update") {
		return LinearJob{}, "not an issue being created or updated"
	}
	if event.Data.ID == "" {
		return LinearJob{}, "the issue has no id"
	}
	repository, ok := rules.team(event.Data.Team.Key)
	if !ok {
		return LinearJob{}, fmt.Sprintf("team %q is not connected", event.Data.Team.Key)
	}
	if !rules.assignedToApp(event) {
		return LinearJob{}, "the issue was not just assigned to the Machinist app"
	}
	return LinearJob{Key: "linear:" + event.Data.ID, Repo: repository, Preset: rules.Preset, Prompt: linearPrompt(event)}, ""
}

func (rules LinearRules) team(key string) (string, bool) {
	for team, repository := range rules.Teams {
		if key != "" && strings.EqualFold(team, key) {
			return repository, true
		}
	}
	return "", false
}

// assignedToApp reports whether this event assigned the issue to the app.
func (rules LinearRules) assignedToApp(event LinearEvent) bool {
	if rules.AppUserID == "" || event.Data.AssigneeID != rules.AppUserID {
		return false
	}
	if event.Action == "create" {
		return true
	}
	raw, changed := event.UpdatedFrom["assigneeId"]
	if !changed {
		return false
	}
	var before *string
	if json.Unmarshal(raw, &before) != nil {
		return false
	}
	return before == nil || *before != rules.AppUserID
}

func linearPrompt(event LinearEvent) string {
	issue := event.Data
	prompt := strings.TrimSpace(issue.Title)
	if description := strings.TrimSpace(issue.Description); description != "" {
		prompt += "\n\n" + description
	}
	if issue.Identifier != "" || issue.URL != "" {
		prompt += fmt.Sprintf("\n\nLinear issue %s: %s", issue.Identifier, issue.URL)
	}
	return prompt
}

// Linear starts the job a Linear issue asked for. The same issue always
// returns its first job, so Linear's retries are safe.
func (i *Intake) Linear(ctx context.Context, job LinearJob) (tickets.Ticket, bool, error) {
	return i.start(ctx, start{source: "linear", key: job.Key, preset: job.Preset, repo: job.Repo, prompt: job.Prompt, by: "linear"})
}

// LinearExisting returns the job an issue already started, if any. The webhook
// checks it first, so a retry never counts against the hourly cap.
func (i *Intake) LinearExisting(ctx context.Context, job LinearJob) (tickets.Ticket, bool, error) {
	ticket, err := i.Store.TicketBySourceKey(ctx, job.Key)
	if errors.Is(err, tickets.ErrNotFound) {
		return tickets.Ticket{}, false, nil
	}
	return ticket, err == nil, err
}
