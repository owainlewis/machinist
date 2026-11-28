package tickets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

// Automation is an automation created in the web app or API. Automations in
// factory/automations/*.md live on disk, not here.
type Automation struct {
	Name      string            `json:"name"`
	Repo      string            `json:"repo"`
	Preset    string            `json:"preset"`
	Prompt    string            `json:"prompt"`
	Schedule  string            `json:"schedule,omitempty"`
	Timezone  string            `json:"timezone,omitempty"`
	Harness   string            `json:"harness,omitempty"`
	Model     string            `json:"model,omitempty"`
	RunsOn    []string          `json:"runs_on,omitempty"`
	Vars      map[string]string `json:"vars,omitempty"`
	CreatedBy string            `json:"created_by,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

const automationColumns = `name,repo,preset,prompt,schedule,timezone,harness,model,COALESCE(runs_on,''),COALESCE(vars,''),created_by,created_at,updated_at`

func scanAutomation(row scanner) (Automation, error) {
	var a Automation
	var runsOn, rawVars, created, updated string
	if err := row.Scan(&a.Name, &a.Repo, &a.Preset, &a.Prompt, &a.Schedule, &a.Timezone, &a.Harness, &a.Model, &runsOn, &rawVars, &a.CreatedBy, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Automation{}, ErrNotFound
		}
		return Automation{}, err
	}
	a.RunsOn = splitLabels(runsOn)
	var err error
	if a.Vars, err = decodeVars(rawVars); err != nil {
		return Automation{}, err
	}
	if at, err := time.Parse(time.RFC3339Nano, created); err == nil {
		a.CreatedAt = at
	}
	if at, err := time.Parse(time.RFC3339Nano, updated); err == nil {
		a.UpdatedAt = at
	}
	return a, nil
}

// Automations lists the UI-managed automations, ordered by name.
func (s *Store) Automations(ctx context.Context) ([]Automation, error) {
	rows, err := s.q().QueryContext(ctx, `SELECT `+automationColumns+` FROM automations ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Automation{}
	for rows.Next() {
		a, err := scanAutomation(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, rows.Err()
}

// Automation returns one UI-managed automation by name.
func (s *Store) Automation(ctx context.Context, name string) (Automation, error) {
	return scanAutomation(s.q().QueryRowContext(ctx, `SELECT `+automationColumns+` FROM automations WHERE name=?`, name))
}

// CreateAutomation saves a new UI-managed automation. It returns
// ErrAutomationExists when an automation with the same name already exists.
func (s *Store) CreateAutomation(ctx context.Context, a Automation) (Automation, error) {
	runsOn, err := config.NormalizeLabels(a.RunsOn)
	if err != nil {
		return Automation{}, fmt.Errorf("runs_on: %w", err)
	}
	encodedVars, err := encodeVars(a.Vars)
	if err != nil {
		return Automation{}, err
	}
	now := stamp(s.now())
	_, err = s.q().ExecContext(ctx,
		`INSERT INTO automations(name,repo,preset,prompt,schedule,timezone,harness,model,runs_on,vars,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.Name, a.Repo, a.Preset, a.Prompt, a.Schedule, a.Timezone, a.Harness, a.Model, strings.Join(runsOn, ","), encodedVars, a.CreatedBy, now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return Automation{}, ErrAutomationExists
		}
		return Automation{}, err
	}
	return s.Automation(ctx, a.Name)
}

// UpdateAutomation updates an existing UI-managed automation by name.
func (s *Store) UpdateAutomation(ctx context.Context, a Automation) (Automation, error) {
	runsOn, err := config.NormalizeLabels(a.RunsOn)
	if err != nil {
		return Automation{}, fmt.Errorf("runs_on: %w", err)
	}
	encodedVars, err := encodeVars(a.Vars)
	if err != nil {
		return Automation{}, err
	}
	now := stamp(s.now())
	result, err := s.q().ExecContext(ctx,
		`UPDATE automations SET repo=?,preset=?,prompt=?,schedule=?,timezone=?,harness=?,model=?,runs_on=?,vars=?,updated_at=? WHERE name=?`,
		a.Repo, a.Preset, a.Prompt, a.Schedule, a.Timezone, a.Harness, a.Model, strings.Join(runsOn, ","), encodedVars, now, a.Name)
	if err != nil {
		return Automation{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Automation{}, err
	}
	if affected == 0 {
		return Automation{}, ErrNotFound
	}
	return s.Automation(ctx, a.Name)
}

// DeleteAutomation deletes a UI-managed automation by name. It reports whether
// a row was removed.
func (s *Store) DeleteAutomation(ctx context.Context, name string) (bool, error) {
	result, err := s.q().ExecContext(ctx, `DELETE FROM automations WHERE name=?`, name)
	if err != nil {
		return false, err
	}
	removed, err := result.RowsAffected()
	return removed > 0, err
}
