package tickets

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/machinist/internal/harness"
	"github.com/owainlewis/machinist/internal/protocol"
)

// maxAction bounds the action a running job shows. maxPartial bounds the
// unfinished line kept between output events.
const (
	maxAction  = 200
	maxPartial = 4 << 10
)

// progress is where a running run has got to: the step it is on, when that
// step started, and the last action the step's agent named. partial is the
// step's stdout after its last newline, because an output event ends wherever
// a pipe read ended, not at the end of a line.
type progress struct {
	step, stepStartedAt, action, partial string
	changed                              bool
}

// note moves progress on by one stored event. Only a step's start and its
// stdout can change it.
func (p *progress) note(event protocol.RunEvent) {
	switch {
	case event.Type == "step.started":
		p.step, _ = strings.CutPrefix(event.Message, "name=")
		p.stepStartedAt, p.action, p.partial, p.changed = event.Time, "", "", true
	case event.Type == "process.output" && event.Encoding == "base64" && (event.Stream == "" || event.Stream == "stdout"):
		data, err := base64.StdEncoding.DecodeString(event.Data)
		if err != nil {
			return
		}
		text := p.partial + string(data)
		end := strings.LastIndexByte(text, '\n')
		if action := lastAction(text[:end+1]); action != "" {
			p.action = action
		}
		p.partial = text[end+1:]
		if len(p.partial) > maxPartial {
			p.partial = ""
		}
		p.changed = true
	}
}

// lastAction is the last line in text that names an action.
func lastAction(text string) string {
	found := ""
	for _, line := range strings.Split(text, "\n") {
		if text, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), harness.ActionPrefix); ok && utf8.ValidString(text) {
			found = strings.TrimSpace(text)
		}
	}
	if runes := []rune(found); len(runes) > maxAction {
		found = string(runes[:maxAction-1]) + "…"
	}
	return found
}

func loadProgress(ctx context.Context, tx querier, runID string) (progress, error) {
	var p progress
	var started sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT step,step_started_at,action,partial FROM run_progress WHERE run=?`, runID).Scan(&p.step, &started, &p.action, &p.partial)
	if err == sql.ErrNoRows {
		return progress{}, nil
	}
	p.stepStartedAt = started.String
	return p, err
}

func saveProgress(ctx context.Context, tx querier, runID string, p progress) error {
	if !p.changed {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO run_progress(run,step,step_started_at,action,partial) VALUES(?,?,?,?,?)
  ON CONFLICT(run) DO UPDATE SET step=excluded.step,step_started_at=excluded.step_started_at,action=excluded.action,partial=excluded.partial`,
		runID, p.step, nullable(p.stepStartedAt), p.action, p.partial)
	return err
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// RequestKey is the source key of the request that made a ticket, or empty when
// it has none.
func (s *Store) RequestKey(ctx context.Context, ticketID string) (string, error) {
	var key string
	err := s.q().QueryRowContext(ctx, `SELECT source_key FROM ticket_entries WHERE ticket_id=? AND kind=? AND source_key IS NOT NULL ORDER BY id LIMIT 1`, ticketID, EntryRequest).Scan(&key)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return key, err
}

// CurrentStep is the step a running run of the ticket is on, or empty when no
// run is running or it has not started a step.
func (s *Store) CurrentStep(ctx context.Context, ticketID string) (string, error) {
	var step string
	err := s.q().QueryRowContext(ctx, `SELECT p.step FROM runs r JOIN run_progress p ON p.run=r.id WHERE r.ticket_id=? AND r.status=? ORDER BY r.seq DESC, r.id DESC LIMIT 1`, ticketID, RunRunning).Scan(&step)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return step, err
}

// RunProgress is where a run is (or got to): the current or last step name,
// when that step started, and the latest action line its agent reported.
type RunProgress struct {
	Step          string
	StepStartedAt *time.Time
	Action        string
}

// RunProgress returns the recorded progress of a run, or the zero value when
// none has been recorded yet.
func (s *Store) RunProgress(ctx context.Context, runID string) (RunProgress, error) {
	var p RunProgress
	var started sql.NullString
	err := s.q().QueryRowContext(ctx, `SELECT step,step_started_at,action FROM run_progress WHERE run=?`, runID).Scan(&p.Step, &started, &p.Action)
	if err == sql.ErrNoRows {
		return RunProgress{}, nil
	}
	p.StepStartedAt = parseTime(started)
	return p, err
}

// EventStepName extracts the step name from a step.started or legacy
// stage.started run event.
func EventStepName(event protocol.RunEvent) (string, bool) {
	switch event.Type {
	case "step.started":
		if name, ok := strings.CutPrefix(event.Message, "name="); ok && name != "" {
			return name, true
		}
		return "step", true
	case "stage.started":
		rest, ok := strings.CutPrefix(event.Message, "name=")
		if !ok {
			return "stage", true
		}
		if idx := strings.LastIndex(rest, " kind="); idx >= 0 {
			rest = rest[:idx]
		}
		if rest == "" {
			return "stage", true
		}
		return rest, true
	default:
		return "", false
	}
}

// RunStartedSteps returns the names of the steps that have started in a run,
// in order.
func (s *Store) RunStartedSteps(ctx context.Context, runID string) ([]string, error) {
	rows, err := s.q().QueryContext(ctx, `SELECT body FROM run_activity_events WHERE run=? AND (body LIKE '%"step.started"%' OR body LIKE '%"stage.started"%') ORDER BY sequence`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var steps []string
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var event protocol.RunEvent
		if json.Unmarshal([]byte(body), &event) != nil {
			continue
		}
		if name, ok := EventStepName(event); ok {
			steps = append(steps, name)
		}
	}
	return steps, rows.Err()
}

// RunStepAt returns the step name that was active at sequence atSeq (inclusive),
// or empty when no step had started by then.
func (s *Store) RunStepAt(ctx context.Context, runID string, atSeq int64) (string, error) {
	if atSeq <= 0 {
		return "", nil
	}
	rows, err := s.q().QueryContext(ctx, `SELECT body FROM run_activity_events WHERE run=? AND sequence<=? AND (body LIKE '%"step.started"%' OR body LIKE '%"stage.started"%') ORDER BY sequence DESC`, runID, atSeq)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return "", err
		}
		var event protocol.RunEvent
		if json.Unmarshal([]byte(body), &event) != nil {
			continue
		}
		if name, ok := EventStepName(event); ok {
			return name, nil
		}
	}
	return "", rows.Err()
}
