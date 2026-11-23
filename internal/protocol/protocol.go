package protocol

// StepsHarness is the one kind of run: a preset's steps. A runner that does not
// offer it is an older runner that cannot run steps.
const StepsHarness = "steps"

// PollRequest offers the harnesses this runner can run. Kind and Slots say
// where it runs and how many runs it does at once, for the fleet page.
type PollRequest struct {
	// Harnesses is "steps" and, when Probed, every agent harness the runner
	// found installed, such as "claude".
	Harnesses []string `json:"harnesses"`
	// Probed says the runner looked for its harnesses. The control plane then
	// gives it only jobs whose agents it can run. A runner that did not look,
	// such as a Kubernetes runner or one from before this, takes any job.
	Probed bool   `json:"probed,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Slots  int    `json:"slots,omitempty"`
}

type HeartbeatResponse struct {
	Cancel bool `json:"cancel"`
}

// Completion ends a run. Error says why a run has no report. Patch is the
// change a step saved when it could not push it, so it can be recovered
// without running the job again.
type Completion struct {
	State  string        `json:"state"`
	Report *Report       `json:"report,omitempty"`
	Tokens *int64        `json:"tokens,omitempty"`
	Cost   *float64      `json:"cost,omitempty"`
	Error  string        `json:"error,omitempty"`
	Stages []StageResult `json:"stages,omitempty"`
	Patch  []byte        `json:"patch,omitempty"`
	// Ship asks the control plane to see the job's pull request through its
	// checks, and to merge it when Ship allows. The runner is free once the
	// run ends, so no runner waits on CI.
	Ship *Ship `json:"ship,omitempty"`
}

// ShipFile is where machinist merge leaves the Ship for the control plane, in
// the control directory.
const ShipFile = "ship.json"

// Ship is what the merge step found, which decides whether the control plane
// may merge the pull request once its checks pass. The control plane also
// requires github.auto_merge, whatever a step says.
type Ship struct {
	// Verdict is the verdict in review.json, or empty when there was none.
	Verdict string `json:"verdict,omitempty"`
	// WithoutReview means the preset has no review step, so no verdict is
	// needed.
	WithoutReview bool `json:"without_review,omitempty"`
	// Hold says why the pull request must stay open, such as an invalid
	// review.json. Its checks are still watched, so a failed one fails the job.
	Hold string `json:"hold,omitempty"`
}

const (
	// PatchFile is where a step leaves the job's change, in the control
	// directory, when it could not push it. machinist push writes it.
	PatchFile = "change.patch"
	// MaxPatchBytes caps a saved patch. A larger one is not kept.
	MaxPatchBytes = 4 << 20
)

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
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	State   string   `json:"state"`
	Outcome string   `json:"outcome,omitempty"`
	Tokens  *int64   `json:"tokens,omitempty"`
	Cost    *float64 `json:"cost,omitempty"`
	// CostUnknown is true when an agent in the step reports no cost, so Cost,
	// when set, is only part of what the step spent.
	CostUnknown    bool   `json:"cost_unknown,omitempty"`
	DurationMillis int64  `json:"duration_millis,omitempty"`
	Error          string `json:"error,omitempty"`
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
