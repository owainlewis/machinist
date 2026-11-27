package tickets

import (
	"context"
	"database/sql"
	"errors"
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
	Preset  string           `json:"preset"`
	Harness string           `json:"harness,omitempty"`
	Model   string           `json:"model,omitempty"`
	Agents  []protocol.Agent `json:"agents,omitempty"`
}

// Queue is the runs no runner has taken yet.
type Queue struct {
	Waiting int        `json:"waiting"`
	Oldest  *time.Time `json:"oldest_queued_at,omitempty"`
	// Blocked is the waiting runs no online runner can take, because none
	// has the harnesses their agents run. The control plane fills it in.
	Blocked []HarnessWait `json:"blocked,omitempty"`
}

// HarnessWait is how many queued runs wait for a runner with these harnesses.
type HarnessWait struct {
	Harnesses []string `json:"harnesses"`
	Waiting   int      `json:"waiting"`
}

// RecordRunner stores what a runner said about itself when it asked for work.
func (s *Store) RecordRunner(ctx context.Context, name, kind string, harnesses []string, slots int, probed bool) error {
	if slots < 1 {
		slots = 1
	}
	_, err := s.q().ExecContext(ctx, `INSERT INTO runners(name,kind,harnesses,slots,last_contact,probed) VALUES(?,?,?,?,?,?)
  ON CONFLICT(name) DO UPDATE SET kind=excluded.kind,harnesses=excluded.harnesses,slots=excluded.slots,last_contact=excluded.last_contact,probed=excluded.probed`,
		name, kind, strings.Join(harnesses, ","), slots, stamp(s.now()), boolToInt(probed))
	return err
}

// TouchRunner notes that a runner made contact. A runner with every slot busy
// stops asking for work, so its heartbeats are what keep it online.
func (s *Store) TouchRunner(ctx context.Context, name string) error {
	_, err := s.q().ExecContext(ctx, `UPDATE runners SET last_contact=? WHERE name=?`, stamp(s.now()), name)
	return err
}

var (
	// ErrRunnerBusy means a runner still holds running jobs.
	ErrRunnerBusy = errors.New("the runner has running jobs")
	// ErrRunnerOnline means a runner made contact too recently to remove.
	ErrRunnerOnline = errors.New("the runner is online")
)

// RemoveRunner forgets a runner that has not made contact since onlineSince
// and holds no running job, such as a machine that was taken out of service.
// A runner that asks for work again comes back.
func (s *Store) RemoveRunner(ctx context.Context, name string, onlineSince time.Time) error {
	return s.inTx(ctx, func(tx querier) error {
		var contact sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT last_contact FROM runners WHERE name=?`+s.forUpdate(), name).Scan(&contact); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if at := parseTime(contact); at != nil && !at.Before(onlineSince) {
			return ErrRunnerOnline
		}
		var running int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE runner=? AND status=?`, name, RunRunning).Scan(&running); err != nil {
			return err
		}
		if running > 0 {
			return ErrRunnerBusy
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM runners WHERE name=?`, name)
		return err
	})
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
	rows, err := s.q().QueryContext(ctx, `SELECT r.runner,r.id,r.ticket_id,t.title,t.agent,COALESCE(t.harness,''),COALESCE(t.model,''),r.kind,r.reviewer,r.started_at,
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
		if err := rows.Scan(&runner, &job.RunID, &job.TicketID, &job.Title, &job.Preset, &job.Harness, &job.Model, &job.Kind, &job.Reviewer, &started, &job.Step, &stepStarted, &job.Action); err != nil {
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
	if err := s.q().QueryRowContext(ctx, `SELECT COUNT(*),MIN(queued_at) FROM runs WHERE status=?`, RunQueued).Scan(&queue.Waiting, &oldest); err != nil {
		return nil, Queue{}, err
	}
	queue.Oldest = parseTime(oldest)
	return runners, queue, nil
}

// QueuedSpec counts queued agent runs sharing a preset and ticket-level
// harness/model overrides.
type QueuedSpec struct {
	Preset  string
	Harness string
	Model   string
	Waiting int
}

// QueuedSpecs counts the agent runs no runner has taken yet by preset and
// ticket-level harness/model overrides.
func (s *Store) QueuedSpecs(ctx context.Context) ([]QueuedSpec, error) {
	rows, err := s.q().QueryContext(ctx, `SELECT t.agent,COALESCE(t.harness,''),COALESCE(t.model,''),COUNT(*) FROM runs r JOIN tickets t ON t.id=r.ticket_id WHERE r.status=? AND r.kind=? GROUP BY t.agent,COALESCE(t.harness,''),COALESCE(t.model,'')`, RunQueued, RunKindAgent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	specs := []QueuedSpec{}
	for rows.Next() {
		var spec QueuedSpec
		if err := rows.Scan(&spec.Preset, &spec.Harness, &spec.Model, &spec.Waiting); err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, rows.Err()
}

// QueuedPresets counts the agent runs no runner has taken yet by the preset
// of their job.
func (s *Store) QueuedPresets(ctx context.Context) (map[string]int, error) {
	rows, err := s.q().QueryContext(ctx, `SELECT t.agent,COUNT(*) FROM runs r JOIN tickets t ON t.id=r.ticket_id WHERE r.status=? AND r.kind=? GROUP BY t.agent`, RunQueued, RunKindAgent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	presets := map[string]int{}
	for rows.Next() {
		var preset string
		var count int
		if err := rows.Scan(&preset, &count); err != nil {
			return nil, err
		}
		presets[preset] = count
	}
	return presets, rows.Err()
}

func (s *Store) listRunners(ctx context.Context) ([]Runner, error) {
	rows, err := s.q().QueryContext(ctx, `SELECT name,kind,harnesses,slots,last_contact,probed FROM runners ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runners := []Runner{}
	for rows.Next() {
		runner := Runner{Harnesses: []string{}, Jobs: []RunnerJob{}}
		var harnesses string
		var contact sql.NullString
		var probed int
		if err := rows.Scan(&runner.Name, &runner.Kind, &harnesses, &runner.Slots, &contact, &probed); err != nil {
			return nil, err
		}
		runner.Probed = probed != 0
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
