package protocol

import (
	"errors"
	"fmt"
	"strings"
)

const (
	OutcomePRReady  = "pr_ready"
	OutcomeNoChange = "no_change"
	OutcomeFailed   = "failed"
	// OutcomeMerged is never reported by a runner. The control plane records
	// it on a run whose pull request had merged by the time the run ended.
	OutcomeMerged = "merged"
)

// Report is the one result of every run. A run never asks a person anything.
type Report struct {
	Outcome    string `json:"outcome"`
	Summary    string `json:"summary"`
	FailedStep string `json:"failed_step,omitempty"`
}

func (r Report) Validate() error {
	switch r.Outcome {
	case OutcomePRReady, OutcomeNoChange, OutcomeFailed:
	default:
		return fmt.Errorf("report outcome %q is not one of pr_ready, no_change or failed", r.Outcome)
	}
	if strings.TrimSpace(r.Summary) == "" {
		return errors.New("report summary is empty")
	}
	return nil
}
