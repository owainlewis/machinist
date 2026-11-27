package ship

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// GateFile is the file the merge-gate step writes in the control directory.
const GateFile = "merge-gate.json"

const (
	maxGateFileBytes   = 64 << 10
	maxGateReasonRunes = 2000
)

// Decisions the merge gate can return.
const (
	GateApprove     = "approve"
	GateHumanReview = "human_review"
)

// Risk levels the merge gate can return.
const (
	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
)

// Gate is the structured judgment written by the merge-gate agent (and
// optionally overridden by the deterministic tripwire).
type Gate struct {
	Decision string `json:"decision"`
	Risk     string `json:"risk"`
	Reason   string `json:"reason"`
	Tripwire bool   `json:"tripwire,omitempty"`
}

// ErrNoGate means the control directory has no merge-gate.json.
var ErrNoGate = errors.New("the merge-gate step wrote no merge-gate.json")

// ReadGate reads and strictly validates merge-gate.json in the control directory.
func ReadGate(control string) (Gate, error) {
	var gate Gate
	body, err := os.ReadFile(filepath.Join(control, GateFile))
	if errors.Is(err, os.ErrNotExist) {
		return gate, ErrNoGate
	}
	if err != nil {
		return gate, err
	}
	if len(body) > maxGateFileBytes {
		return gate, errors.New("merge-gate.json exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&gate); err != nil {
		return gate, fmt.Errorf("merge-gate.json is not valid JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return gate, errors.New("merge-gate.json must contain a single JSON object")
	}
	gate.Decision = strings.TrimSpace(gate.Decision)
	gate.Risk = strings.TrimSpace(gate.Risk)
	gate.Reason = strings.TrimSpace(gate.Reason)
	switch gate.Decision {
	case GateApprove, GateHumanReview:
	case "":
		return gate, errors.New("merge-gate.json has no decision")
	default:
		return gate, fmt.Errorf("merge-gate.json has an unknown decision %q", gate.Decision)
	}
	switch gate.Risk {
	case RiskLow, RiskMedium, RiskHigh:
	case "":
		return gate, errors.New("merge-gate.json has no risk level")
	default:
		return gate, fmt.Errorf("merge-gate.json has an unknown risk level %q", gate.Risk)
	}
	if gate.Reason == "" {
		return gate, errors.New("merge-gate.json has no reason")
	}
	if utf8.RuneCountInString(gate.Reason) > maxGateReasonRunes {
		return gate, errors.New("merge-gate.json reason exceeds 2000 characters")
	}
	return gate, nil
}

// gateBlocker says why the merge gate stops an automatic merge, or "" when it
// allows one. Only an explicit "approve" with "low" risk allows auto-merge.
func gateBlocker(gate Gate) string {
	if gate.Decision == GateApprove && gate.Risk == RiskLow {
		return ""
	}
	return fmt.Sprintf("merge gate requires human review (%s risk): %s", gate.Risk, gate.Reason)
}

// FormatGateComment formats a concise Markdown judgment comment for the pull
// request explaining whether Machinist will auto-merge it or hold it for a person.
func FormatGateComment(gate Gate, head string, willAutoMerge bool, hold string) string {
	shortHead := head
	if len(shortHead) > 7 {
		shortHead = shortHead[:7]
	}
	heading := "Machinist Merge Gate — Human review required"
	nextStep := "Left open for human review."
	if gate.Tripwire {
		heading = "Machinist Merge Gate — Human review required (tripwire)"
	} else if willAutoMerge {
		heading = "Machinist Merge Gate — Approved for auto-merge"
		nextStep = "Will auto-merge automatically once CI checks pass."
	} else if gate.Decision == GateApprove && gate.Risk == RiskLow {
		heading = "Machinist Merge Gate — Approved (left open)"
		if hold != "" {
			nextStep = "Left open: " + strings.TrimRight(hold, ". ") + "."
		}
	} else if hold != "" && hold != gateBlocker(gate) {
		nextStep = "Left open: " + strings.TrimRight(hold, ". ") + "."
	}
	riskLabel := map[string]string{
		RiskLow:    "Low",
		RiskMedium: "Medium",
		RiskHigh:   "High",
	}[gate.Risk]
	if riskLabel == "" {
		riskLabel = gate.Risk
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n\n", heading)
	if shortHead != "" {
		fmt.Fprintf(&b, "- **Commit:** `%s`\n", shortHead)
	}
	fmt.Fprintf(&b, "- **Risk:** %s\n", riskLabel)
	fmt.Fprintf(&b, "- **Judgment:** %s\n", strings.Join(strings.Fields(gate.Reason), " "))
	fmt.Fprintf(&b, "- **Next step:** %s", nextStep)
	return b.String()
}

// GateLabel returns the GitHub label name, hex color (without #), and short
// description for a merge-gate outcome so teams can count gate results weekly.
func GateLabel(gate Gate, willAutoMerge bool, autoMergeEnabled bool) (name, color, description string) {
	switch {
	case gate.Tripwire:
		return "merge-gate:tripwire", "b60205", "Held by deterministic merge-gate tripwire"
	case willAutoMerge:
		return "merge-gate:auto-merge", "0e8a16", "Approved by merge gate for auto-merge"
	case !autoMergeEnabled && gate.Decision == GateApprove && gate.Risk == RiskLow:
		return "merge-gate:observe", "1d76db", "Approved by merge gate in observe mode"
	default:
		return "merge-gate:human-review", "d93f0b", "Held by merge gate for human review"
	}
}
