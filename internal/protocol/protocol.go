package protocol

// StepsHarness is the one kind of run: a preset's steps. A runner that does not
// offer it is an older runner that cannot run steps.
const StepsHarness = "steps"

// PollRequest offers the harnesses this runner can run. Kind and Slots say
// where it runs and how many runs it does at once, for the fleet page.
type PollRequest struct {
	Harnesses []string `json:"harnesses"`
	Kind      string   `json:"kind,omitempty"`
	Slots     int      `json:"slots,omitempty"`
}

type HeartbeatResponse struct {
	Cancel bool `json:"cancel"`
}

// Completion ends a run. Error says why a run has no report.
type Completion struct {
	State  string        `json:"state"`
	Report *Report       `json:"report,omitempty"`
	Tokens *int64        `json:"tokens,omitempty"`
	Cost   *float64      `json:"cost,omitempty"`
	Error  string        `json:"error,omitempty"`
	Stages []StageResult `json:"stages,omitempty"`
}

// GitHubToken is a new token for a run's repository, for a runner whose run
// outlasts the one it started with.
type GitHubToken struct {
	Token string `json:"token"`
}

// StageSkipped is the state of a stage that never ran, because an earlier
// stage ended the run.
const StageSkipped = "skipped"

// StageResult is what one stage of a run produced. State is a runner state or
// StageSkipped. Outcome is the report outcome of an agent stage.
type StageResult struct {
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`
	State          string   `json:"state"`
	Outcome        string   `json:"outcome,omitempty"`
	Tokens         *int64   `json:"tokens,omitempty"`
	Cost           *float64 `json:"cost,omitempty"`
	DurationMillis int64    `json:"duration_millis,omitempty"`
	Error          string   `json:"error,omitempty"`
	// Agents are the factory agents the step ran, in order.
	Agents []Agent `json:"agents,omitempty"`
}

// Agent names one of the factory's agents and what runs it. Model is empty
// when the agent uses its harness's default.
type Agent struct {
	Name    string `json:"name"`
	Harness string `json:"harness"`
	Model   string `json:"model,omitempty"`
}

// Totals adds up the tokens and cost the stages reported. It returns nil for
// each that no stage reported.
func Totals(stages []StageResult) (*int64, *float64) {
	var tokens *int64
	var cost *float64
	for _, stage := range stages {
		if stage.Tokens != nil {
			if tokens == nil {
				tokens = new(int64)
			}
			*tokens += *stage.Tokens
		}
		if stage.Cost != nil {
			if cost == nil {
				cost = new(float64)
			}
			*cost += *stage.Cost
		}
	}
	return tokens, cost
}
