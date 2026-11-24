package ship

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GateFile is the file the merge-gate step writes in the control directory.
const GateFile = "merge-gate.json"

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

// Gate is the structured judgment written by the merge-gate agent.
type Gate struct {
	Decision string `json:"decision"`
	Risk     string `json:"risk"`
	Reason   string `json:"reason"`
}

// ErrNoGate means the control directory has no merge-gate.json.
var ErrNoGate = errors.New("the merge-gate step wrote no merge-gate.json")

// ReadGate reads and validates merge-gate.json in the control directory.
func ReadGate(control string) (Gate, error) {
	var gate Gate
	body, err := os.ReadFile(filepath.Join(control, GateFile))
	if errors.Is(err, os.ErrNotExist) {
		return gate, ErrNoGate
	}
	if err != nil {
		return gate, err
	}
	if err := json.Unmarshal(body, &gate); err != nil {
		return gate, fmt.Errorf("merge-gate.json is not valid JSON: %w", err)
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
	if hold != "" {
		nextStep = "Left open: " + strings.TrimRight(hold, ". ") + "."
	}
	if willAutoMerge {
		heading = "Machinist Merge Gate — Approved for auto-merge"
		nextStep = "Will auto-merge automatically once CI checks pass."
	} else if gate.Decision == GateApprove && gate.Risk == RiskLow {
		heading = "Machinist Merge Gate — Approved (left open)"
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
