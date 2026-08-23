package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestParseTargetAcceptsEveryDocumentedForm(t *testing.T) {
	for _, testCase := range []struct {
		name, work, repo, owner string
		want                    target
	}{
		{"number with repo", "412", "acme/api", "", target{"acme", "api", 412}},
		{"number with bare repo and owner", "412", "api", "acme", target{"acme", "api", 412}},
		{"issue url", "https://github.com/acme/api/issues/412", "", "", target{"acme", "api", 412}},
		{"issue url with slash", "https://github.com/acme/api/issues/412/", "", "", target{"acme", "api", 412}},
		{"short reference", "acme/api#412", "", "", target{"acme", "api", 412}},
		{"repository only", "", "acme/api", "", target{"acme", "api", 0}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseTarget(testCase.work, testCase.repo, testCase.owner)
			if err != nil {
				t.Fatal(err)
			}
			if got != testCase.want {
				t.Fatalf("target = %+v, want %+v", got, testCase.want)
			}
		})
	}
}

func TestParseTargetRejectsUnusableInput(t *testing.T) {
	for _, testCase := range []struct{ name, work, repo, owner, want string }{
		{"no repo", "412", "", "", "--repo is required"},
		{"bare repo without owner", "412", "api", "", "has no owner"},
		{"not a number", "twelve", "acme/api", "", "cannot read"},
		{"zero", "0", "acme/api", "", "cannot read"},
		{"negative", "-3", "acme/api", "", "cannot read"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := parseTarget(testCase.work, testCase.repo, testCase.owner)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

func TestTargetRendersIdentityAndReference(t *testing.T) {
	issue := target{Owner: "acme", Repo: "api", Number: 412}
	if issue.Identity() != "github.com/acme/api" {
		t.Fatalf("identity = %q", issue.Identity())
	}
	if issue.Reference() != "acme/api#412" {
		t.Fatalf("reference = %q", issue.Reference())
	}
	repository := target{Owner: "acme", Repo: "api"}
	if repository.Reference() != "acme/api" {
		t.Fatalf("repository reference = %q", repository.Reference())
	}
}

func TestSelectedPhases(t *testing.T) {
	got, err := selectedPhases("build", "")
	if err != nil || len(got) != 1 || got[0] != "build" {
		t.Fatalf("got %v, err %v", got, err)
	}
	if got, err := selectedPhases("", "verify"); err != nil || got[0] != "verify" {
		t.Fatalf("got %v, err %v", got, err)
	}

	// A multi-phase dispatch must be atomic and needs a dependency edge the
	// control plane does not have. Refusing beats starting them in parallel.
	_, err = selectedPhases("", "plan,build,verify")
	if err == nil || !strings.Contains(err.Error(), "one phase at a time") {
		t.Fatalf("error = %v", err)
	}
	if _, err := selectedPhases("build", "verify"); err == nil {
		t.Fatal("both --phase and --phases were accepted")
	}
	if _, err := selectedPhases("", ""); err == nil {
		t.Fatal("a dispatch with no phase was accepted")
	}
}

func TestResolveRuntime(t *testing.T) {
	for _, testCase := range []struct{ override, declared, want string }{
		{"", "", protocol.RuntimeCodex},
		{"", "claude", protocol.RuntimeClaudeCode},
		{"", "claude-code", protocol.RuntimeClaudeCode},
		{"codex", "claude", protocol.RuntimeCodex},
		{"pi", "", protocol.RuntimePi},
	} {
		got, err := resolveRuntime(testCase.override, testCase.declared)
		if err != nil {
			t.Fatalf("resolveRuntime(%q, %q): %v", testCase.override, testCase.declared, err)
		}
		if got != testCase.want {
			t.Fatalf("resolveRuntime(%q, %q) = %q, want %q", testCase.override, testCase.declared, got, testCase.want)
		}
	}
	if _, err := resolveRuntime("gpt", ""); err == nil {
		t.Fatal("an unsupported runtime was accepted")
	}
}

func TestSummariseKeepsEventsToOneLine(t *testing.T) {
	for _, testCase := range []struct{ name, payload, want string }{
		{"string", `"hello  world"`, "hello world"},
		{"message field", `{"message":"ran the tests"}`, "ran the tests"},
		{"error field", `{"error":"exit 1"}`, "exit 1"},
		{"multiline", `"first\nsecond"`, "first second"},
		{"opaque", `{"other":1}`, `{"other":1}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := summarise(json.RawMessage(testCase.payload))
			if got != testCase.want {
				t.Fatalf("summarise = %q, want %q", got, testCase.want)
			}
		})
	}
	if summarise(nil) != "" {
		t.Fatal("an empty payload produced output")
	}
	long := summarise(json.RawMessage(`"` + strings.Repeat("a", 400) + `"`))
	if len([]rune(long)) != 120 {
		t.Fatalf("summarise did not bound the line: %d runes", len([]rune(long)))
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncate("abcdef", 4); got != "abc…" {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncate("héllo wörld", 6); len([]rune(got)) != 6 {
		t.Fatalf("truncate broke a multi-byte string: %q", got)
	}
}

func TestCheckFormat(t *testing.T) {
	for _, format := range []string{"table", "json"} {
		if err := checkFormat(format); err != nil {
			t.Fatalf("checkFormat(%q) = %v", format, err)
		}
	}
	if err := checkFormat("yaml"); err == nil {
		t.Fatal("an unknown format was accepted")
	}
}

func TestRunRejectsAnUnknownCommand(t *testing.T) {
	if err := run([]string{"deploy"}); err == nil {
		t.Fatal("an unknown command was accepted")
	}
	if err := run(nil); err == nil {
		t.Fatal("an empty command line was accepted")
	}
	if err := run([]string{"version"}); err != nil {
		t.Fatalf("version = %v", err)
	}
}
