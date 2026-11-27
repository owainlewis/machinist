package ship

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCheckTripwireCleanChangePasses(t *testing.T) {
	git := func(_ context.Context, stdout io.Writer, args ...string) (string, error) {
		switch {
		case len(args) >= 2 && args[0] == "diff" && args[1] == "--name-only":
			_, _ = io.WriteString(stdout, "pkg/math/multiply.go\npkg/math/multiply_test.go\n")
		case len(args) >= 2 && args[0] == "diff" && args[1] == "--unified=0":
			_, _ = io.WriteString(stdout, "+++ b/pkg/math/multiply_test.go\n+func TestMultiply(t *testing.T) {}\n")
		default:
			t.Fatalf("unexpected git call: %v", args)
		}
		return "", nil
	}
	findings, err := CheckTripwire(t.Context(), git, "origin/main")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected no tripwire findings, got %v", findings)
	}
}

func TestCheckTripwireCatchesProtectedPathsAndSkippedTests(t *testing.T) {
	git := func(_ context.Context, stdout io.Writer, args ...string) (string, error) {
		switch {
		case len(args) >= 2 && args[0] == "diff" && args[1] == "--name-only":
			_, _ = io.WriteString(stdout, strings.Join([]string{
				".github/workflows/ci.yml",
				".machinist/merge-policy.md",
				"db/migrations/004_drop_users.sql",
				"pkg/calc/calc_test.go",
				"web/app.test.js",
			}, "\n")+"\n")
		case len(args) >= 2 && args[0] == "diff" && args[1] == "--unified=0":
			_, _ = io.WriteString(stdout, strings.Join([]string{
				"--- a/pkg/calc/calc_test.go",
				"+++ b/pkg/calc/calc_test.go",
				"@@ -10,0 +11 @@",
				"-\t// removed",
				"+\tt.Skip(\"flaky\")",
				"--- a/web/app.test.js",
				"+++ b/web/app.test.js",
				"@@ -5,0 +6 @@",
				"+test.skip('broken', () => {});",
			}, "\n")+"\n")
		default:
			t.Fatalf("unexpected git call: %v", args)
		}
		return "", nil
	}
	findings, err := CheckTripwire(t.Context(), git, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"modifies workflow file .github/workflows/ci.yml",
		"modifies merge policy .machinist/merge-policy.md",
		"modifies migration db/migrations/004_drop_users.sql",
		"skips tests in pkg/calc/calc_test.go (t.Skip)",
		"skips tests in web/app.test.js (test.skip)",
	}
	if strings.Join(findings, "\n") != strings.Join(want, "\n") {
		t.Fatalf("findings =\n%s\nwant:\n%s", strings.Join(findings, "\n"), strings.Join(want, "\n"))
	}
}

func TestApplyTripwireOverridesApprovedGateAndPersists(t *testing.T) {
	dir := t.TempDir()
	original := Gate{Decision: GateApprove, Risk: RiskLow, Reason: "Looks tiny."}
	updated, err := ApplyTripwire(dir, original, []string{"modifies workflow file .github/workflows/ship.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Decision != GateHumanReview || updated.Risk != RiskHigh || !updated.Tripwire {
		t.Fatalf("unexpected overridden gate: %+v", updated)
	}
	reloaded, err := ReadGate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded != updated {
		t.Fatalf("persisted gate = %+v, want %+v", reloaded, updated)
	}
}

func TestGateLabelAndAutoCreateFallback(t *testing.T) {
	cases := []struct {
		name      string
		gate      Gate
		willMerge bool
		autoMerge bool
		wantLabel string
	}{
		{"tripwire", Gate{Decision: GateHumanReview, Risk: RiskHigh, Tripwire: true}, false, true, "merge-gate:tripwire"},
		{"auto-merge", Gate{Decision: GateApprove, Risk: RiskLow}, true, true, "merge-gate:auto-merge"},
		{"observe", Gate{Decision: GateApprove, Risk: RiskLow}, false, false, "merge-gate:observe"},
		{"human-review", Gate{Decision: GateHumanReview, Risk: RiskMedium}, false, true, "merge-gate:human-review"},
	}
	for _, tc := range cases {
		label, color, desc := GateLabel(tc.gate, tc.willMerge, tc.autoMerge)
		if label != tc.wantLabel || color == "" || desc == "" {
			t.Errorf("%s: GateLabel = (%q, %q, %q), want %q", tc.name, label, color, desc, tc.wantLabel)
		}
	}

	// Verify Merge creates missing label and retries pr edit.
	dir := control(t, accepted)
	writeGate(t, dir, `{"decision":"human_review","risk":"high","reason":"Tripwire: modifies workflow file .github/workflows/ci.yml","tripwire":true}`)
	var calls []string
	editAttempts := 0
	gh := func(_ context.Context, args ...string) ([]byte, string, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		if args[0] == "pr" && args[1] == "view" {
			return []byte(`{"number":7,"url":"https://github.com/o/r/pull/7","state":"OPEN","headRefOid":"sha1"}`), "", nil
		}
		if args[0] == "pr" && args[1] == "edit" {
			editAttempts++
			if editAttempts == 1 {
				return nil, "label not found", errors.New("exit status 1")
			}
		}
		return nil, "", nil
	}
	if err := Merge(t.Context(), MergeOptions{Control: dir, Branch: "machinist/t_1", AutoMerge: true, RequireGate: true, GH: gh}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 5 {
		t.Fatalf("expected 5 gh calls (view, comment, edit fail, label create, edit retry), got %q", calls)
	}
	if !strings.Contains(calls[1], "Human review required (tripwire)") {
		t.Fatalf("expected tripwire heading in comment: %q", calls[1])
	}
	if !strings.HasPrefix(calls[3], "label create merge-gate:tripwire ") {
		t.Fatalf("expected label create fallback, got %q", calls[3])
	}
	if calls[4] != "pr edit 7 --add-label merge-gate:tripwire" {
		t.Fatalf("expected retry pr edit, got %q", calls[4])
	}
}
