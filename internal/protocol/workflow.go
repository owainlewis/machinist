package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

type StepResult struct {
	ApprovalRequired bool   `json:"approval_required,omitempty"`
	Outcome          string `json:"outcome"`
	Summary          string `json:"summary"`
}

func ParseStepResult(body []byte) (*StepResult, error) {
	if len(body) > 16384 {
		return nil, errors.New("step result exceeds 16 KiB")
	}
	var result StepResult
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("step result must contain one JSON object")
	}
	if result.Outcome != "complete" && result.Outcome != "blocked" && result.Outcome != "failed" {
		return nil, errors.New("step outcome must be complete, blocked, or failed")
	}
	if strings.TrimSpace(result.Summary) == "" {
		return nil, errors.New("step summary is required")
	}
	return &result, nil
}
