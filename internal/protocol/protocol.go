package protocol

// PollRequest offers the harnesses this runner has a credential for. Kind and
// Slots say where it runs and how many runs it does at once, for the fleet page.
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
}
