package protocol

// PollRequest offers the harnesses this runner has a credential for.
type PollRequest struct {
	Harnesses []string `json:"harnesses"`
}

type HeartbeatResponse struct {
	Cancel bool `json:"cancel"`
}

// Completion ends a run. Error says why a run has no report.
type Completion struct {
	State  string   `json:"state"`
	Report *Report  `json:"report,omitempty"`
	Tokens *int64   `json:"tokens,omitempty"`
	Cost   *float64 `json:"cost,omitempty"`
	Error  string   `json:"error,omitempty"`
}
