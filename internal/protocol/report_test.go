package protocol

import (
	"strings"
	"testing"
)

func TestReportValidateAcceptsEachOutcome(t *testing.T) {
	for _, report := range []Report{
		{Outcome: OutcomePRReady, Summary: "Opened"},
		{Outcome: OutcomeNoChange, Summary: "Nothing to do"},
		{Outcome: OutcomeFailed, Summary: "Tests cannot run"},
	} {
		if err := report.Validate(); err != nil {
			t.Fatalf("%+v: %v", report, err)
		}
	}
}

func TestReportValidateRejectsInvalidReports(t *testing.T) {
	for _, test := range []struct {
		report Report
		want   string
	}{
		{Report{Outcome: "complete", Summary: "old"}, "not one of"},
		{Report{Outcome: OutcomePRReady, Summary: "  "}, "summary is empty"},
		{Report{Outcome: "needs_input", Summary: "Stuck"}, "not one of"},
		{Report{Outcome: OutcomeMerged, Summary: "Merged"}, "not one of"},
	} {
		if err := test.report.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%+v: error = %v, want %q", test.report, err, test.want)
		}
	}
}
