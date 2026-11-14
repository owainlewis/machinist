package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	OutcomePRReady    = "pr_ready"
	OutcomeNeedsInput = "needs_input"
	OutcomeNoChange   = "no_change"
	OutcomeFailed     = "failed"
)

// Report is the one result an agent returns at the end of every run.
type Report struct {
	Outcome  string `json:"outcome"`
	Summary  string `json:"summary"`
	Question string `json:"question,omitempty"`
}

func (r Report) Validate() error {
	switch r.Outcome {
	case OutcomePRReady, OutcomeNeedsInput, OutcomeNoChange, OutcomeFailed:
	default:
		return fmt.Errorf("report outcome %q is not one of pr_ready, needs_input, no_change or failed", r.Outcome)
	}
	if strings.TrimSpace(r.Summary) == "" {
		return errors.New("report summary is empty")
	}
	if r.Outcome == OutcomeNeedsInput && strings.TrimSpace(r.Question) == "" {
		return errors.New("report outcome needs_input requires a question")
	}
	return nil
}

// ParseReport accepts exactly one JSON object holding a valid report.
func ParseReport(body []byte) (*Report, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var report Report
	if err := decoder.Decode(&report); err != nil {
		return nil, fmt.Errorf("invalid report: expected one JSON object: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("invalid report: expected one JSON object")
	}
	if err := report.Validate(); err != nil {
		return nil, err
	}
	return &report, nil
}
