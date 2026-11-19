package tickets

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"strings"
	"unicode/utf8"

	"github.com/owainlewis/machinist/internal/agentlog"
	"github.com/owainlewis/machinist/internal/protocol"
)

// maxAction bounds the action a running job shows.
const maxAction = 200

// progress is where a running run has got to: the step it is on, when that
// step started, and the last action the step's agent named.
type progress struct {
	step, stepStartedAt, action string
	changed                     bool
}

// note moves progress on by one stored event. Only a step's start and its
// stdout can change it.
func (p *progress) note(event protocol.RunEvent) {
	switch {
	case event.Type == "step.started":
		p.step, _ = strings.CutPrefix(event.Message, "name=")
		p.stepStartedAt, p.action, p.changed = event.Time, "", true
	case event.Type == "process.output" && event.Encoding == "base64" && (event.Stream == "" || event.Stream == "stdout"):
		data, err := base64.StdEncoding.DecodeString(event.Data)
		if err != nil {
			return
		}
		if action := lastAction(data); action != "" {
			p.action, p.changed = action, true
		}
	}
}

// lastAction is the last whole line in data that names an action.
func lastAction(data []byte) string {
	found := ""
	for _, line := range bytes.Split(data, []byte("\n")) {
		if text, ok := strings.CutPrefix(strings.TrimRight(string(line), "\r"), agentlog.ActionPrefix); ok && utf8.ValidString(text) {
			found = strings.TrimSpace(text)
		}
	}
	if runes := []rune(found); len(runes) > maxAction {
		found = string(runes[:maxAction-1]) + "…"
	}
	return found
}

func loadProgress(ctx context.Context, tx *sql.Tx, runID string) (progress, error) {
	var p progress
	var started sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT step,step_started_at,action FROM run_progress WHERE run=?`, runID).Scan(&p.step, &started, &p.action)
	if err == sql.ErrNoRows {
		return progress{}, nil
	}
	p.stepStartedAt = started.String
	return p, err
}

func saveProgress(ctx context.Context, tx *sql.Tx, runID string, p progress) error {
	if !p.changed {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO run_progress(run,step,step_started_at,action) VALUES(?,?,?,?)
  ON CONFLICT(run) DO UPDATE SET step=excluded.step,step_started_at=excluded.step_started_at,action=excluded.action`,
		runID, p.step, nullable(p.stepStartedAt), p.action)
	return err
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
