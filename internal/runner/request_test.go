package runner

import (
	"errors"
	"testing"
)

func TestRunTokenPrefersTheRunsOwnToken(t *testing.T) {
	for _, test := range []struct{ own, fallback, want string }{
		{"ghs_run", "ghp_runner", "ghs_run"},
		{"", "ghp_runner", "ghp_runner"},
	} {
		if got, err := RunToken(RunRequest{GitHubToken: test.own}, test.fallback); err != nil || got != test.want {
			t.Fatalf("%+v: got %q, err = %v", test, got, err)
		}
	}
	if _, err := RunToken(RunRequest{}, ""); !errors.Is(err, ErrNoGitHubToken) {
		t.Fatalf("error = %v", err)
	}
}
