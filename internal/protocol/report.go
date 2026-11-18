package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	OutcomePRReady  = "pr_ready"
	OutcomeNoChange = "no_change"
	OutcomeFailed   = "failed"
)

// Report is the one result of every run. A run never asks a person anything.
type Report struct {
	Outcome string `json:"outcome"`
	Summary string `json:"summary"`
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
