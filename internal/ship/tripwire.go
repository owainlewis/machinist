package ship

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// skippedTestMarker pairs a substring found in an added test line with the
// short label shown in the tripwire reason.
type skippedTestMarker struct {
	needle string
	label  string
}

var skippedTestMarkers = []skippedTestMarker{
	{needle: "t.Skip(", label: "t.Skip"},
	{needle: "t.Skipf(", label: "t.Skipf"},
	{needle: "t.SkipNow(", label: "t.SkipNow"},
	{needle: "b.Skip(", label: "b.Skip"},
	{needle: "b.Skipf(", label: "b.Skipf"},
	{needle: "b.SkipNow(", label: "b.SkipNow"},
	{needle: "it.skip(", label: "it.skip"},
	{needle: "test.skip(", label: "test.skip"},
	{needle: "describe.skip(", label: "describe.skip"},
	{needle: "xit(", label: "xit"},
	{needle: "xtest(", label: "xtest"},
	{needle: "xdescribe(", label: "xdescribe"},
	{needle: "@pytest.mark.skip", label: "@pytest.mark.skip"},
	{needle: "@unittest.skip", label: "@unittest.skip"},
	{needle: "pytest.skip(", label: "pytest.skip"},
	{needle: "#[ignore]", label: "#[ignore]"},
}

// CheckTripwire runs deterministic checks on the branch diff against base
// (defaulting to origin/HEAD) that no LLM judgment can override:
//   - changes to CI/CD workflow files (.github/workflows/)
//   - changes to the merge policy (.machinist/merge-policy.md, MERGE_POLICY.md)
//   - changes to database/SQL migration files
//   - added skipped/disabled tests in test files
func CheckTripwire(ctx context.Context, git Git, base string) ([]string, error) {
	if base == "" {
		base = "origin/HEAD"
	}
	diffRange := base + "...HEAD"
	// Disable rename detection so renaming a protected file out of its folder
	// (e.g. .github/workflows/ci.yml -> ci.yml.disabled) still surfaces the
	// protected source path as a deletion.
	namesOut, err := gitOutput(ctx, git, "diff", "--no-renames", "--name-only", diffRange)
	if err != nil {
		return nil, fmt.Errorf("tripwire could not inspect changed files: %w", err)
	}
	var findings []string
	var testFiles []string
	for raw := range strings.Lines(namesOut) {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		if reason := protectedPathReason(path); reason != "" && !slices.Contains(findings, reason) {
			findings = append(findings, reason)
		}
		if isTestFile(path) && !slices.Contains(testFiles, path) {
			testFiles = append(testFiles, path)
		}
	}
	if len(testFiles) > 0 {
		args := append([]string{"diff", "--no-renames", "--unified=0", "--no-color", diffRange, "--"}, testFiles...)
		patchOut, err := gitOutput(ctx, git, args...)
		if err != nil {
			return nil, fmt.Errorf("tripwire could not inspect test diff: %w", err)
		}
		findings = append(findings, findSkippedTests(patchOut)...)
	}
	return findings, nil
}

// ApplyTripwire forces human_review with high risk when findings is non-empty,
// and rewrites merge-gate.json in control so downstream merge checks enforce it.
func ApplyTripwire(control string, gate Gate, findings []string) (Gate, error) {
	if len(findings) == 0 {
		return gate, nil
	}
	reason := "Tripwire: " + strings.Join(findings, "; ")
	runes := []rune(reason)
	if len(runes) > maxGateReasonRunes {
		reason = string(runes[:maxGateReasonRunes-1]) + "…"
	}
	overridden := Gate{
		Decision: GateHumanReview,
		Risk:     RiskHigh,
		Reason:   reason,
		Tripwire: true,
	}
	body, err := json.MarshalIndent(overridden, "", "  ")
	if err != nil {
		return gate, err
	}
	body = append(body, '\n')
	if err := os.WriteFile(filepath.Join(control, GateFile), body, 0o600); err != nil {
		return gate, err
	}
	return overridden, nil
}

func protectedPathReason(path string) string {
	clean := filepath.ToSlash(strings.TrimPrefix(path, "./"))
	lower := strings.ToLower(clean)
	switch {
	case strings.HasPrefix(lower, ".github/workflows/"):
		return "modifies workflow file " + clean
	case lower == ".machinist/merge-policy.md" || lower == "merge_policy.md":
		return "modifies merge policy " + clean
	case isMigrationPath(lower):
		return "modifies migration " + clean
	default:
		return ""
	}
}

func isMigrationPath(lower string) bool {
	if strings.HasPrefix(lower, "migrations/") || strings.Contains(lower, "/migrations/") {
		return true
	}
	if strings.HasSuffix(lower, ".sql") && (strings.HasPrefix(lower, "db/") || strings.Contains(lower, "/db/") || strings.HasPrefix(lower, "schema/") || strings.Contains(lower, "/schema/")) {
		return true
	}
	return false
}

func isTestFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.HasSuffix(base, "_test.go") ||
		strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") ||
		(strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py")) ||
		strings.HasSuffix(base, "_test.py") ||
		strings.HasSuffix(base, ".rs")
}

func findSkippedTests(patch string) []string {
	var findings []string
	var currentFile string
	for raw := range strings.Lines(patch) {
		line := strings.TrimRight(raw, "\r\n")
		if after, ok := strings.CutPrefix(line, "+++ b/"); ok {
			currentFile = after
			continue
		}
		if strings.HasPrefix(line, "+++ ") || !strings.HasPrefix(line, "+") {
			continue
		}
		added := line[1:]
		for _, marker := range skippedTestMarkers {
			if strings.Contains(added, marker.needle) {
				entry := fmt.Sprintf("skips tests in %s (%s)", currentFile, marker.label)
				if currentFile == "" {
					entry = fmt.Sprintf("skips tests (%s)", marker.label)
				}
				if !slices.Contains(findings, entry) {
					findings = append(findings, entry)
				}
			}
		}
	}
	return findings
}
