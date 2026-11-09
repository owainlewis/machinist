package protocol

import (
	"strings"
	"testing"
)

func TestParseReportAcceptsEachOutcome(t *testing.T) {
	for _, body := range []string{
		`{"outcome":"pr_ready","summary":"Opened"}`,
		`{"outcome":"needs_input","summary":"Stuck","question":"Which API?"}`,
		`{"outcome":"no_change","summary":"Nothing to do"}`,
		`{"outcome":"failed","summary":"Tests cannot run"}`,
	} {
		if _, err := ParseReport([]byte(body)); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
	}
}

func TestParseReportRejectsInvalidReports(t *testing.T) {
	for _, test := range []struct{ body, want string }{
		{`{"outcome":"complete","summary":"old"}`, "not one of"},
		{`{"outcome":"pr_ready","summary":"  "}`, "summary is empty"},
		{`{"outcome":"needs_input","summary":"Stuck"}`, "requires a question"},
		{`{"outcome":"pr_ready","summary":"x","pr_url":"https://example.test"}`, "expected one JSON object"},
		{`{"outcome":"pr_ready","summary":"x"} {}`, "expected one JSON object"},
		{`Done: {"outcome":"pr_ready","summary":"x"}`, "expected one JSON object"},
	} {
		if _, err := ParseReport([]byte(test.body)); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%s: error = %v, want %q", test.body, err, test.want)
		}
	}
}
