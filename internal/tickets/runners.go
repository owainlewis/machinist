package tickets

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

// Runner is one machine or cluster that asks for work, as it last described
// itself, with the runs it holds now.
type Runner struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Harnesses []string `json:"harnesses"`
	// Probed is true when the runner looked up its harnesses, so a list with
	// no agent harness means it has none, not that it did not say.
	Probed      bool        `json:"probed"`
	Slots       int         `json:"slots"`
	LastContact time.Time   `json:"last_contact"`
	Jobs        []RunnerJob `json:"jobs"`
}

// RunnerJob is one run a runner is doing, with the step it is on and the
// last action that step's agent named.
type RunnerJob struct {
	RunID         string     `json:"run_id"`
	TicketID      string     `json:"ticket_id"`
	Title         string     `json:"title"`
	Kind          string     `json:"kind"`
	Reviewer      string     `json:"reviewer,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	Step          string     `json:"step,omitempty"`
	StepStartedAt *time.Time `json:"step_started_at,omitempty"`
	Action        string     `json:"action,omitempty"`
	// Preset is the job's preset. The control plane fills in Agents, the
	// agents its current step runs, from the factory.
	Preset string           `json:"preset"`
	Agents []protocol.Agent `json:"agents,omitempty"`
}

// Queue is the runs no runner has taken yet.
type Queue struct {
	Waiting int        `json:"waiting"`
	Oldest  *time.Time `json:"oldest_queued_at,omitempty"`
}

// RecordRunner stores what a runner said about itself when it asked for work.
func (s *Store) RecordRunner(ctx context.Context, name, kind string, harnesses []string, slots int, probed bool) error {
	if slots < 1 {
		slots = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO runners(name,kind,harnesses,slots,last_contact,probed) VALUES(?,?,?,?,?,?)
  ON CONFLICT(name) DO UPDATE SET kind=excluded.kind,harnesses=excluded.harnesses,slots=excluded.slots,last_contact=excluded.last_contact,probed=excluded.probed`,
		name, kind, strings.Join(harnesses, ","), slots, stamp(s.now()), probed)
	return err
}

// TouchRunner notes that a runner made contact. A runner with every slot busy
// stops asking for work, so its heartbeats are what keep it online.
func (s *Store) TouchRunner(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runners SET last_contact=? WHERE name=?`, stamp(s.now()), name)
	return err
}

// Fleet returns every runner by name with its running jobs, and the queue.
func (s *Store) Fleet(ctx context.Context) ([]Runner, Queue, error) {
	runners, err := s.listRunners(ctx)
	if err != nil {
		return nil, Queue{}, err
	}
	byName := make(map[string]*Runner, len(runners))
	for index := range runners {
		byName[runners[index].Name] = &runners[index]
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.runner,r.id,r.ticket_id,t.title,t.agent,r.kind,r.reviewer,r.started_at,
  COALESCE(p.step,''),p.step_started_at,COALESCE(p.action,'')
  FROM runs r JOIN tickets t ON t.id=r.ticket_id LEFT JOIN run_progress p ON p.run=r.id WHERE r.status=? ORDER BY r.started_at`, RunRunning)
	if err != nil {
		return nil, Queue{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var runner string
		var job RunnerJob
		var started, stepStarted sql.NullString
		if err := rows.Scan(&runner, &job.RunID, &job.TicketID, &job.Title, &job.Preset, &job.Kind, &job.Reviewer, &started, &job.Step, &stepStarted, &job.Action); err != nil {
			return nil, Queue{}, err
		}
		job.StepStartedAt = parseTime(stepStarted)
		if at := parseTime(started); at != nil {
			job.StartedAt = *at
		}
		if owner := byName[runner]; owner != nil {
			owner.Jobs = append(owner.Jobs, job)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, Queue{}, err
	}
	var queue Queue
	var oldest sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),MIN(queued_at) FROM runs WHERE status=?`, RunQueued).Scan(&queue.Waiting, &oldest); err != nil {
		return nil, Queue{}, err
	}
	queue.Oldest = parseTime(oldest)
	return runners, queue, nil
}

func (s *Store) listRunners(ctx context.Context) ([]Runner, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name,kind,harnesses,slots,last_contact,probed FROM runners ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runners := []Runner{}
	for rows.Next() {
		runner := Runner{Harnesses: []string{}, Jobs: []RunnerJob{}}
		var harnesses string
		var contact sql.NullString
		if err := rows.Scan(&runner.Name, &runner.Kind, &harnesses, &runner.Slots, &contact, &runner.Probed); err != nil {
			return nil, err
		}
		if harnesses != "" {
			runner.Harnesses = strings.Split(harnesses, ",")
		}
		if at := parseTime(contact); at != nil {
			runner.LastContact = *at
		}
		runners = append(runners, runner)
	}
	return runners, rows.Err()
}
