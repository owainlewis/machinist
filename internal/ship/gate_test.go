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
		`{"decision":"approve","risk":"low","reason":"ok","extra":true}`,
		`{"decision":"approve","risk":"low","reason":"ok"} trailing`,
	} {
		writeGate(t, dir, bad)
		if _, err := ReadGate(dir); err == nil {
			t.Fatalf("expected validation error for %s", bad)
		}
	}
}

func TestMergeWithLowRiskApprovedGateCommentsAndAllowsAutoMerge(t *testing.T) {
	directory := control(t, accepted)
	reason := "Small unit test addition with no production logic changes."
	writeGate(t, directory, `{"decision":"approve","risk":"low","reason":"`+reason+`"}`)
	gh := &stepGH{state: "OPEN"}
	if err := Merge(t.Context(), MergeOptions{Control: directory, Branch: "machinist/t_1", AutoMerge: true, RequireGate: true, GH: gh.run}); err != nil {
		t.Fatal(err)
	}
	want := protocol.Ship{
		Verdict:           Accepted,
		Head:              "sha1",
		MergeGateDecision: GateApprove,
		MergeGateRisk:     RiskLow,
		MergeGateReason:   reason,
	}
	if request := handedOver(t, directory); request == nil || *request != want {
		t.Fatalf("ship.json = %+v, want %+v", request, want)
	}
	if result := readFile(t, directory, "result.md"); result != "Merges once its checks pass: https://github.com/o/r/pull/7" {
		t.Fatalf("result.md = %q", result)
	}
	if len(gh.calls) != 3 || !strings.HasPrefix(gh.calls[1], "pr comment 7 --body ") || !strings.Contains(gh.calls[1], "Approved for auto-merge") {
		t.Fatalf("expected PR judgment comment, calls = %q", gh.calls)
	}
	if gh.calls[2] != "pr edit 7 --add-label merge-gate:auto-merge" {
		t.Fatalf("expected auto-merge label call, got %q", gh.calls[2])
	}
}

func TestMergeWithHighRiskGateCommentsAndBlocksAutoMerge(t *testing.T) {
	directory := control(t, accepted)
	reason := "Adds a destructive SQL migration dropping legacy_tokens."
	writeGate(t, directory, `{"decision":"human_review","risk":"high","reason":"`+reason+`"}`)
	gh := &stepGH{state: "OPEN"}
	if err := Merge(t.Context(), MergeOptions{Control: directory, Branch: "machinist/t_1", AutoMerge: true, RequireGate: true, GH: gh.run}); err != nil {
		t.Fatal(err)
	}
	request := handedOver(t, directory)
	wantHold := "merge gate requires human review (high risk): " + reason
	want := protocol.Ship{
		Verdict:           Accepted,
		Hold:              wantHold,
		Head:              "sha1",
		MergeGateDecision: GateHumanReview,
		MergeGateRisk:     RiskHigh,
		MergeGateReason:   reason,
	}
	if request == nil || *request != want {
		t.Fatalf("ship.json = %+v, want %+v", request, want)
	}
	if result := readFile(t, directory, "result.md"); !strings.Contains(result, "Left open: "+wantHold) {
		t.Fatalf("result.md = %q", result)
	}
	if len(gh.calls) != 3 || !strings.Contains(gh.calls[1], "Human review required") || !strings.Contains(gh.calls[1], "dropping legacy_tokens") {
		t.Fatalf("expected escalation comment on PR, calls = %q", gh.calls)
	}
	if gh.calls[2] != "pr edit 7 --add-label merge-gate:human-review" {
		t.Fatalf("expected human-review label call, got %q", gh.calls[2])
	}
	if strings.Count(gh.calls[1], "dropping legacy_tokens") != 1 || !strings.Contains(gh.calls[1], "- **Next step:** Left open for human review.") {
		t.Fatalf("comment should not duplicate reason: %q", gh.calls[1])
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
	if req := handedOver(t, dir); req == nil || !strings.Contains(req.Hold, "medium risk") || req.MergeGateDecision != GateApprove || req.MergeGateRisk != RiskMedium || req.MergeGateReason != "Changes API response schema." {
		t.Fatalf("medium risk must block auto-merge and record gate judgment: %+v", req)
	}

	// Missing merge-gate.json when RequireGate is true must block auto-merge.
	dir2 := control(t, accepted)
	gh2 := &stepGH{state: "OPEN"}
	if err := Merge(t.Context(), MergeOptions{Control: dir2, Branch: "machinist/t_1", AutoMerge: true, RequireGate: true, GH: gh2.run}); err != nil {
		t.Fatal(err)
	}
	if req := handedOver(t, dir2); req == nil || req.Hold != ErrNoGate.Error() || req.MergeGateDecision != "" || req.MergeGateRisk != "" || req.MergeGateReason != "" {
		t.Fatalf("missing required gate must hold without gate verdict: %+v", req)
	}
}

func TestBlockerEnforcesMergeGateFields(t *testing.T) {
	for _, test := range []struct {
		name    string
		request protocol.Ship
		want    string
	}{
		{
			name:    "low risk approve allows auto-merge",
			request: protocol.Ship{Verdict: Accepted, MergeGateDecision: GateApprove, MergeGateRisk: RiskLow, MergeGateReason: "Safe."},
			want:    "",
		},
		{
			name:    "low risk approve still needs accepted review",
			request: protocol.Ship{Verdict: RevisionNeeded, MergeGateDecision: GateApprove, MergeGateRisk: RiskLow, MergeGateReason: "Safe."},
			want:    "the review still asks for changes",
		},
		{
			name:    "medium risk blocks even without Hold",
			request: protocol.Ship{Verdict: Accepted, MergeGateDecision: GateApprove, MergeGateRisk: RiskMedium, MergeGateReason: "Schema change."},
			want:    "merge gate requires human review (medium risk): Schema change.",
		},
		{
			name:    "high risk human_review blocks even without Hold",
			request: protocol.Ship{Verdict: Accepted, MergeGateDecision: GateHumanReview, MergeGateRisk: RiskHigh, MergeGateReason: "Auth change."},
			want:    "merge gate requires human review (high risk): Auth change.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Blocker(true, test.request); got != test.want {
				t.Fatalf("Blocker(true, %+v) = %q, want %q", test.request, got, test.want)
			}
		})
	}
}

func TestMergeStopsCommentingAfterApproval(t *testing.T) {
	dir := control(t, accepted)
	writeGate(t, dir, `{"decision":"approve","risk":"low","reason":"Scoped hardening with unit tests."}`)
	gh := &stepGH{state: "OPEN", head: "8f5a0ab123456"}

	// First run approves and posts one comment.
	if err := Merge(t.Context(), MergeOptions{Control: dir, Branch: "machinist/t_1", AutoMerge: true, RequireGate: true, GH: gh.run}); err != nil {
		t.Fatal(err)
	}
	if len(gh.comments) != 1 || !strings.Contains(gh.comments[0].Body, "Approved for auto-merge") {
		t.Fatalf("expected 1 approval comment after first run, got %+v", gh.comments)
	}

	// Simulate an unrelated comment and a PR Steward rebase pushing a new commit.
	gh.comments = append(gh.comments, prComment{Body: "CI check passed."})
	gh.head = "e976d43567890"
	gh.calls = nil
	writeGate(t, dir, `{"decision":"approve","risk":"low","reason":"Clean merge conflict resolution with tests passing."}`)
	if err := Merge(t.Context(), MergeOptions{Control: dir, Branch: "machinist/t_1", AutoMerge: true, RequireGate: true, GH: gh.run}); err != nil {
		t.Fatal(err)
	}
	// ship.json still updates to the new head so the control plane can auto-merge it.
	if req := handedOver(t, dir); req == nil || req.Head != "e976d43567890" || req.MergeGateDecision != GateApprove {
		t.Fatalf("ship.json = %+v, want updated head e976d43567890", req)
	}
	// Only pr view and pr edit (label) should run; no second pr comment.
	if len(gh.calls) != 2 || !strings.HasPrefix(gh.calls[0], "pr view ") || gh.calls[1] != "pr edit 7 --add-label merge-gate:auto-merge" {
		t.Fatalf("expected no duplicate pr comment after approval, calls = %q", gh.calls)
	}
	if len(gh.comments) != 2 {
		t.Fatalf("expected comment count to remain 2, got %d", len(gh.comments))
	}
}

func TestMergeObserveModeStopsCommentingAfterApproval(t *testing.T) {
	dir := control(t, accepted)
	writeGate(t, dir, `{"decision":"approve","risk":"low","reason":"Safe refactor."}`)
	gh := &stepGH{state: "OPEN", head: "1111111aaa"}

	if err := Merge(t.Context(), MergeOptions{Control: dir, Branch: "machinist/t_1", AutoMerge: false, RequireGate: true, GH: gh.run}); err != nil {
		t.Fatal(err)
	}
	if len(gh.comments) != 1 || !strings.Contains(gh.comments[0].Body, "Approved (left open)") {
		t.Fatalf("expected observe-mode approval comment, got %+v", gh.comments)
	}

	gh.head = "2222222bbb"
	gh.calls = nil
	if err := Merge(t.Context(), MergeOptions{Control: dir, Branch: "machinist/t_1", AutoMerge: false, RequireGate: true, GH: gh.run}); err != nil {
		t.Fatal(err)
	}
	if len(gh.calls) != 2 || gh.calls[1] != "pr edit 7 --add-label merge-gate:observe" {
		t.Fatalf("expected no duplicate observe-mode approval comment, calls = %q", gh.calls)
	}
}

func TestMergeCommentsWhenOutcomeChangesOrHeldPRUpdates(t *testing.T) {
	t.Run("approval downgraded to tripwire posts escalation", func(t *testing.T) {
		dir := control(t, accepted)
		gh := &stepGH{
			state: "OPEN",
			head:  "e976d43",
			comments: []prComment{{
				Body: FormatGateComment(Gate{Decision: GateApprove, Risk: RiskLow, Reason: "Safe."}, "8f5a0ab", true, ""),
			}},
		}
		writeGate(t, dir, `{"decision":"human_review","risk":"high","reason":"Tripwire: modifies workflow file .github/workflows/ci.yml","tripwire":true}`)
		if err := Merge(t.Context(), MergeOptions{Control: dir, Branch: "machinist/t_1", AutoMerge: true, RequireGate: true, GH: gh.run}); err != nil {
			t.Fatal(err)
		}
		if len(gh.comments) != 2 || !strings.Contains(gh.comments[1].Body, "Human review required (tripwire)") {
			t.Fatalf("expected tripwire escalation comment, got %+v", gh.comments)
		}
	})

	t.Run("held PR upgraded to approved posts once then stops", func(t *testing.T) {
		dir := control(t, accepted)
		gh := &stepGH{
			state: "OPEN",
			head:  "e976d43",
			comments: []prComment{{
				Body: FormatGateComment(Gate{Decision: GateHumanReview, Risk: RiskHigh, Reason: "Touches auth."}, "8f5a0ab", false, ""),
			}},
		}
		writeGate(t, dir, `{"decision":"approve","risk":"low","reason":"Reverted auth changes."}`)
		if err := Merge(t.Context(), MergeOptions{Control: dir, Branch: "machinist/t_1", AutoMerge: true, RequireGate: true, GH: gh.run}); err != nil {
			t.Fatal(err)
		}
		if len(gh.comments) != 2 || !strings.Contains(gh.comments[1].Body, "Approved for auto-merge") {
			t.Fatalf("expected new approval comment after hold, got %+v", gh.comments)
		}

		// Subsequent approved commit stops commenting.
		gh.head = "faecd2f"
		if err := Merge(t.Context(), MergeOptions{Control: dir, Branch: "machinist/t_1", AutoMerge: true, RequireGate: true, GH: gh.run}); err != nil {
			t.Fatal(err)
		}
		if len(gh.comments) != 2 {
			t.Fatalf("expected no third comment after approval, got %d", len(gh.comments))
		}
	})

	t.Run("held PR skips duplicate on same commit but updates on new commit", func(t *testing.T) {
		dir := control(t, accepted)
		writeGate(t, dir, `{"decision":"human_review","risk":"high","reason":"Touches auth."}`)
		gh := &stepGH{
			state: "OPEN",
			head:  "8f5a0ab",
			comments: []prComment{{
				Body: FormatGateComment(Gate{Decision: GateHumanReview, Risk: RiskHigh, Reason: "Touches auth."}, "8f5a0ab", false, ""),
			}},
		}
		if err := Merge(t.Context(), MergeOptions{Control: dir, Branch: "machinist/t_1", AutoMerge: true, RequireGate: true, GH: gh.run}); err != nil {
			t.Fatal(err)
		}
		if len(gh.comments) != 1 {
			t.Fatalf("expected duplicate hold comment on same commit to be skipped, got %d", len(gh.comments))
		}

		gh.head = "e976d43"
		writeGate(t, dir, `{"decision":"human_review","risk":"medium","reason":"Still modifies session parsing."}`)
		if err := Merge(t.Context(), MergeOptions{Control: dir, Branch: "machinist/t_1", AutoMerge: true, RequireGate: true, GH: gh.run}); err != nil {
			t.Fatal(err)
		}
		if len(gh.comments) != 2 || !strings.Contains(gh.comments[1].Body, "`e976d43`") {
			t.Fatalf("expected updated hold comment on new commit, got %+v", gh.comments)
		}
	})

	t.Run("minimized prior approval does not suppress new approval", func(t *testing.T) {
		dir := control(t, accepted)
		writeGate(t, dir, `{"decision":"approve","risk":"low","reason":"Safe."}`)
		gh := &stepGH{
			state: "OPEN",
			head:  "e976d43",
			comments: []prComment{{
				Body:        FormatGateComment(Gate{Decision: GateApprove, Risk: RiskLow, Reason: "Old."}, "8f5a0ab", true, ""),
				IsMinimized: true,
			}},
		}
		if err := Merge(t.Context(), MergeOptions{Control: dir, Branch: "machinist/t_1", AutoMerge: true, RequireGate: true, GH: gh.run}); err != nil {
			t.Fatal(err)
		}
		if len(gh.comments) != 2 {
			t.Fatalf("expected fresh approval comment when prior one was minimized, got %d", len(gh.comments))
		}
	})
}
