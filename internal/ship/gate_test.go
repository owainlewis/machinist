package ship

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
)

func writeGate(t *testing.T, directory, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, GateFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadGateValidation(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadGate(dir); err != ErrNoGate {
		t.Fatalf("expected ErrNoGate, got %v", err)
	}
	writeGate(t, dir, `{"decision":"approve","risk":"low","reason":"Adds unit tests only."}`)
	gate, err := ReadGate(dir)
	if err != nil || gate.Decision != GateApprove || gate.Risk != RiskLow {
		t.Fatalf("valid gate failed: %+v err=%v", gate, err)
	}
	for _, bad := range []string{
		`not json`,
		`{"decision":"maybe","risk":"low","reason":"ok"}`,
		`{"decision":"approve","risk":"critical","reason":"ok"}`,
		`{"decision":"approve","risk":"low","reason":"   "}`,
	} {
		writeGate(t, dir, bad)
		if _, err := ReadGate(dir); err == nil {
			t.Fatalf("expected validation error for %s", bad)
		}
	}
}

func TestMergeWithLowRiskApprovedGateCommentsAndAllowsAutoMerge(t *testing.T) {
	directory := control(t, accepted)
	writeGate(t, directory, `{"decision":"approve","risk":"low","reason":"Small unit test addition with no production logic changes."}`)
	gh := &stepGH{state: "OPEN"}
	if err := Merge(t.Context(), MergeOptions{Control: directory, Branch: "machinist/t_1", AutoMerge: true, RequireGate: true, GH: gh.run}); err != nil {
		t.Fatal(err)
	}
	if request := handedOver(t, directory); request == nil || *request != (protocol.Ship{Verdict: Accepted, Head: "sha1"}) {
		t.Fatalf("ship.json = %+v", request)
	}
	if result := readFile(t, directory, "result.md"); result != "Merges once its checks pass: https://github.com/o/r/pull/7" {
		t.Fatalf("result.md = %q", result)
	}
	if len(gh.calls) != 2 || !strings.HasPrefix(gh.calls[1], "pr comment 7 --body ") || !strings.Contains(gh.calls[1], "Approved for auto-merge") {
		t.Fatalf("expected PR judgment comment, calls = %q", gh.calls)
	}
}

func TestMergeWithHighRiskGateCommentsAndBlocksAutoMerge(t *testing.T) {
	directory := control(t, accepted)
	writeGate(t, directory, `{"decision":"human_review","risk":"high","reason":"Adds a destructive SQL migration dropping legacy_tokens."}`)
	gh := &stepGH{state: "OPEN"}
	if err := Merge(t.Context(), MergeOptions{Control: directory, Branch: "machinist/t_1", AutoMerge: true, RequireGate: true, GH: gh.run}); err != nil {
		t.Fatal(err)
	}
	request := handedOver(t, directory)
	wantHold := "merge gate requires human review (high risk): Adds a destructive SQL migration dropping legacy_tokens."
	if request == nil || request.Hold != wantHold {
		t.Fatalf("ship.json = %+v, want hold %q", request, wantHold)
	}
	if result := readFile(t, directory, "result.md"); !strings.Contains(result, "Left open: "+wantHold) {
		t.Fatalf("result.md = %q", result)
	}
	if len(gh.calls) != 2 || !strings.Contains(gh.calls[1], "Human review required") || !strings.Contains(gh.calls[1], "dropping legacy_tokens") {
		t.Fatalf("expected escalation comment on PR, calls = %q", gh.calls)
	}
}

func TestMergeFailsClosedOnMediumRiskOrMissingRequiredGate(t *testing.T) {
	// Even if an agent says "approve" with "medium" risk, merge gate fails closed.
	dir := control(t, accepted)
	writeGate(t, dir, `{"decision":"approve","risk":"medium","reason":"Changes API response schema."}`)
	gh := &stepGH{state: "OPEN"}
	if err := Merge(t.Context(), MergeOptions{Control: dir, Branch: "machinist/t_1", AutoMerge: true, RequireGate: true, GH: gh.run}); err != nil {
		t.Fatal(err)
	}
	if req := handedOver(t, dir); req == nil || !strings.Contains(req.Hold, "medium risk") {
		t.Fatalf("medium risk must block auto-merge: %+v", req)
	}

	// Missing merge-gate.json when RequireGate is true must block auto-merge.
	dir2 := control(t, accepted)
	gh2 := &stepGH{state: "OPEN"}
	if err := Merge(t.Context(), MergeOptions{Control: dir2, Branch: "machinist/t_1", AutoMerge: true, RequireGate: true, GH: gh2.run}); err != nil {
		t.Fatal(err)
	}
	if req := handedOver(t, dir2); req == nil || req.Hold != ErrNoGate.Error() {
		t.Fatalf("missing required gate must hold: %+v", req)
	}
}
